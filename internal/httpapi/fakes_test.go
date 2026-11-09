package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/service"
)

// The fakes below stand in for the Node's edges only: git, the builder,
// Docker, the health check, the proxy, the queue, the password hasher and
// the clock. Everything between them and the HTTP handler is real. Each
// fake is safe for concurrent use, since the handler runs on the test
// server's goroutines.

// fakeClock is the clock the services read; tests move it with advance.
// Its After channels fire only when advance reaches them.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []clockTimer
}

type clockTimer struct {
	at time.Time
	c  chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	pending := c.timers[:0]
	for _, t := range c.timers {
		if t.at.After(c.now) {
			pending = append(pending, t)
			continue
		}
		t.c <- c.now
	}
	c.timers = pending
}

// After fires once advance has moved the clock d past now.
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.timers = append(c.timers, clockTimer{at: c.now.Add(d), c: ch})
	return ch
}

// waitForTimer waits until something waits on After, so a test moves the
// clock only once the timer it means to fire is set.
func (c *fakeClock) waitForTimer(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		n := len(c.timers)
		c.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("nothing waited on the clock within 5s")
		}
		time.Sleep(time.Millisecond)
	}
}

// fakeHasher keeps the password in the "hash", so signing in costs nothing.
// The real Argon2id hasher takes 64 MiB and most of a second per call.
// hold makes every Verify wait, so a test can keep the hash slots busy.
type fakeHasher struct {
	mu      sync.Mutex
	release chan struct{} // while non-nil, Verify waits for it to close
	waiting chan struct{} // gets a value as each held Verify starts waiting
}

const fakeHashPrefix = "fake$"

func (*fakeHasher) Hash(pw string) (string, error) { return fakeHashPrefix + pw, nil }

func (h *fakeHasher) Verify(pw, hash string) (bool, error) {
	h.mu.Lock()
	release, waiting := h.release, h.waiting
	h.mu.Unlock()
	if release != nil {
		waiting <- struct{}{}
		<-release
	}
	stored, ok := strings.CutPrefix(hash, fakeHashPrefix)
	if !ok {
		return false, errors.New("not a fake hash")
	}
	return stored == pw, nil
}

func (*fakeHasher) NeedsRehash(string) bool { return false }

// hold makes every Verify from now on wait until the returned func is
// called. Each one sends on waiting as it starts to wait.
func (h *fakeHasher) hold() (waiting <-chan struct{}, release func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.release, h.waiting = make(chan struct{}), make(chan struct{}, 16)
	ch := h.release
	return h.waiting, sync.OnceFunc(func() {
		h.mu.Lock()
		h.release = nil
		h.mu.Unlock()
		close(ch)
	})
}

// fakeTimer is AdminService's wait for a hash slot. Its waits run out
// only when a test calls expire, and from then on at once.
type fakeTimer struct {
	mu      sync.Mutex
	expired chan time.Time
}

func newFakeTimer() *fakeTimer { return &fakeTimer{expired: make(chan time.Time)} }

func (t *fakeTimer) After(time.Duration) <-chan time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.expired
}

func (t *fakeTimer) expire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	close(t.expired)
}

// unfetchableRef is the one ref fakeSource can't fetch.
const unfetchableRef = "no-such-ref"

// fakeSource "checks out" any ref as the commit "commit-of-<ref>", and a
// commit ("commit-of-...") as itself.
type fakeSource struct{}

func (fakeSource) Fetch(_ context.Context, _, ref, _ string) (string, error) {
	switch {
	case ref == unfetchableRef:
		return "", errors.New("couldn't find remote ref " + ref)
	case strings.HasPrefix(ref, "commit-of-"):
		return ref, nil
	}
	return "commit-of-" + ref, nil
}

// fakeBuilder builds every image at once, into docker's images:
// successfully, until a test calls fail. Each build writes its output (see writes) to the
// Deployment Log, and while a test holds builds, waits for it mid-build.
type fakeBuilder struct {
	mu     sync.Mutex
	reason string // non-empty: every build fails with it
	output string // what every build writes to its log
	held   *heldBuilds
	docker *fakeRuntime
}

func (b *fakeBuilder) Build(ctx context.Context, req service.BuildRequest) error {
	b.mu.Lock()
	reason, output, held := b.reason, b.output, b.held
	b.mu.Unlock()
	if _, err := io.WriteString(req.Logs, output); err != nil {
		return err
	}
	if held != nil {
		held.logs <- req.Logs
		select {
		case <-held.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if reason != "" {
		return errors.New(reason)
	}
	b.docker.addImage(req.Image)
	return nil
}

// writes makes every build from now on write output to its log.
func (b *fakeBuilder) writes(output string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.output = output
}

// hold makes every build from now on wait, mid-build, until the test
// calls finish on what it returns, so its Deployment stays building.
func (b *fakeBuilder) hold() *heldBuilds {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held = &heldBuilds{logs: make(chan io.Writer, 16), release: make(chan struct{})}
	return b.held
}

// heldBuilds are the builds a test holds.
type heldBuilds struct {
	logs    chan io.Writer // each held build's log, as it starts to wait
	release chan struct{}
	once    sync.Once
}

// started waits for the next held build and returns its Deployment Log,
// for the test to write to as the build would.
func (h *heldBuilds) started(t *testing.T) io.Writer {
	t.Helper()
	select {
	case w := <-h.logs:
		return w
	case <-time.After(5 * time.Second):
		t.Fatal("no build started within 5s")
		return nil
	}
}

// finish lets every held build end.
func (h *heldBuilds) finish() { h.once.Do(func() { close(h.release) }) }

// fail makes every build from now on fail with reason.
func (b *fakeBuilder) fail(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reason = reason
}

// fakeRuntime stands in for Docker: it remembers the containers it runs
// and the images fakeBuilder builds.
type fakeRuntime struct {
	mu         sync.Mutex
	started    int
	containers map[string]fakeContainer // by ID
	imageSet   map[string]bool          // by "repository:tag"
}

type fakeContainer struct {
	name, upstream string
	env            map[string]string
	labels         map[string]string
}

func (r *fakeRuntime) addImage(image string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.imageSet[image] = true
}

// images lists the images Docker has, sorted.
func (r *fakeRuntime) images() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(maps.Keys(r.imageSet))
}

// containersOf lists the IDs of the containers labelled with the App.
func (r *fakeRuntime) containersOf(appID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id, c := range r.containers {
		if c.labels[domain.AppLabel] == appID {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func (r *fakeRuntime) RemoveLabelled(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.containers {
		if v, ok := c.labels[key]; ok && v == value {
			delete(r.containers, id)
		}
	}
	return nil
}

func (r *fakeRuntime) RemoveImages(_ context.Context, repository string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for image := range r.imageSet {
		if strings.HasPrefix(image, repository+":") {
			delete(r.imageSet, image)
		}
	}
	return nil
}

// envOf is the env the container with ID id was started with.
func (r *fakeRuntime) envOf(id string) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.containers[id].env
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{containers: map[string]fakeContainer{}, imageSet: map[string]bool{}}
}

func (r *fakeRuntime) Start(_ context.Context, spec service.ContainerSpec) (service.Container, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started++
	ctr := service.Container{ID: fmt.Sprintf("ctr-%d", r.started), Upstream: fmt.Sprintf("127.0.0.1:%d", 9000+r.started)}
	r.containers[ctr.ID] = fakeContainer{name: spec.Name, upstream: ctr.Upstream, env: spec.Env, labels: spec.Labels}
	return ctr, nil
}

// Remove accepts an ID or a name, as docker rm does.
func (r *fakeRuntime) Remove(_ context.Context, idOrName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.containers {
		if id == idOrName || c.name == idOrName {
			delete(r.containers, id)
			return nil
		}
	}
	return fmt.Errorf("no such container: %s", idOrName)
}

func (r *fakeRuntime) Upstream(_ context.Context, id string, _ int) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return "", fmt.Errorf("no such container: %s", id)
	}
	return c.upstream, nil
}

// fakeHealth finds every upstream healthy.
type fakeHealth struct{}

func (fakeHealth) Wait(context.Context, string) error { return nil }

// fakeRouter is the proxy's route table.
type fakeRouter struct {
	mu     sync.Mutex
	routes map[string]string
}

func newFakeRouter() *fakeRouter { return &fakeRouter{routes: map[string]string{}} }

func (r *fakeRouter) SetRoute(host, upstream string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[host] = upstream
}

func (r *fakeRouter) RemoveRoute(host string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, host)
}

func (r *fakeRouter) RemoveRoutesTo(upstream string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for host, u := range r.routes {
		if u == upstream {
			delete(r.routes, host)
		}
	}
}

// table is a copy of the routes, host to upstream.
func (r *fakeRouter) table() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.routes)
}

// fakeScheduler queues Deployments without running them; a test runs one
// with seam.runQueued, so it controls when a Deployment moves on.
type fakeScheduler struct {
	mu     sync.Mutex
	queued []string
}

func (s *fakeScheduler) Enqueue(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = append(s.queued, id)
	return nil
}

func (s *fakeScheduler) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.queued
	s.queued = nil
	return ids
}
