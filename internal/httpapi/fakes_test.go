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
type fakeHasher struct{}

const fakeHashPrefix = "fake$"

func (fakeHasher) Hash(pw string) (string, error) { return fakeHashPrefix + pw, nil }

func (fakeHasher) Verify(pw, hash string) (bool, error) {
	stored, ok := strings.CutPrefix(hash, fakeHashPrefix)
	if !ok {
		return false, errors.New("not a fake hash")
	}
	return stored == pw, nil
}

func (fakeHasher) NeedsRehash(string) bool { return false }

// fakeSource "checks out" any ref as the commit "commit-of-<ref>".
type fakeSource struct{}

func (fakeSource) Fetch(_ context.Context, _, ref, _ string) (string, error) {
	return "commit-of-" + ref, nil
}

// fakeBuilder builds every image at once, successfully.
type fakeBuilder struct{}

func (fakeBuilder) Build(context.Context, service.BuildRequest) error { return nil }

// fakeRuntime stands in for Docker: it remembers the containers it runs.
type fakeRuntime struct {
	mu         sync.Mutex
	started    int
	containers map[string]fakeContainer // by ID
}

type fakeContainer struct{ name, upstream string }

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{containers: map[string]fakeContainer{}} }

func (r *fakeRuntime) Start(_ context.Context, spec service.ContainerSpec) (service.Container, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started++
	ctr := service.Container{ID: fmt.Sprintf("ctr-%d", r.started), Upstream: fmt.Sprintf("127.0.0.1:%d", 9000+r.started)}
	r.containers[ctr.ID] = fakeContainer{name: spec.Name, upstream: ctr.Upstream}
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
