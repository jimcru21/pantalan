package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pantalan/pantalan/internal/service"
)

// The fakes below stand in for the Node's edges only: git, the builder,
// Docker, the health check, the proxy, the queue, the password hasher and
// the clock. Everything between them and the HTTP handler is real. Each
// fake is safe for concurrent use, since the handler runs on the test
// server's goroutines.

// fakeClock is the clock the services read; tests move it with advance.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
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

// fakeSource "checks out" any ref as the commit "commit-of-<ref>".
type fakeSource struct{}

func (fakeSource) Fetch(_ context.Context, _, ref, _ string) (string, error) {
	return "commit-of-" + ref, nil
}

// fakeBuilder builds every image at once: successfully, until a test
// calls fail.
type fakeBuilder struct {
	mu     sync.Mutex
	reason string // non-empty: every build fails with it
}

func (b *fakeBuilder) Build(context.Context, service.BuildRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reason != "" {
		return errors.New(b.reason)
	}
	return nil
}

// fail makes every build from now on fail with reason.
func (b *fakeBuilder) fail(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reason = reason
}

// fakeRuntime stands in for Docker: it remembers the containers it runs.
type fakeRuntime struct {
	mu         sync.Mutex
	started    int
	containers map[string]fakeContainer // by ID
}

type fakeContainer struct {
	name, upstream string
	env            map[string]string
}

// envOf is the env the container with ID id was started with.
func (r *fakeRuntime) envOf(id string) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.containers[id].env
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{containers: map[string]fakeContainer{}} }

func (r *fakeRuntime) Start(_ context.Context, spec service.ContainerSpec) (service.Container, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started++
	ctr := service.Container{ID: fmt.Sprintf("ctr-%d", r.started), Upstream: fmt.Sprintf("127.0.0.1:%d", 9000+r.started)}
	r.containers[ctr.ID] = fakeContainer{name: spec.Name, upstream: ctr.Upstream, env: spec.Env}
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
