package service_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/service"
	"github.com/pantalan/pantalan/internal/store/sqlite"
)

func TestDeploySwitchesTrafficAndRetiresPrevious(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "web")

	first, err := h.deploy(app.ID)
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	second, err := h.deploy(app.ID)
	if err != nil {
		t.Fatalf("second deploy: %v", err)
	}

	if second.Status != domain.DeploymentLive {
		t.Errorf("second status = %s, want live", second.Status)
	}
	if got := h.router.routes[app.Domain]; got != second.Upstream {
		t.Errorf("route = %q, want %q", got, second.Upstream)
	}
	if got := h.get(t, first.ID).Status; got != domain.DeploymentSuperseded {
		t.Errorf("first status = %s, want superseded", got)
	}
	if !slices.Contains(h.runtime.removed, first.ContainerID) {
		t.Errorf("first container %s was not removed (removed: %v)", first.ContainerID, h.runtime.removed)
	}
}

func TestFailedHealthCheckKeepsCurrentDeploymentLive(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "web")

	first, err := h.deploy(app.ID)
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	h.health.unhealthy["127.0.0.1:9002"] = true // the second container's upstream

	second, err := h.deploy(app.ID)
	if err == nil {
		t.Fatal("second deploy succeeded, want health check failure")
	}

	if second.Status != domain.DeploymentFailed {
		t.Errorf("second status = %s, want failed", second.Status)
	}
	if got := h.router.routes[app.Domain]; got != first.Upstream {
		t.Errorf("route = %q, want it to stay on %q", got, first.Upstream)
	}
	if got := h.get(t, first.ID).Status; got != domain.DeploymentLive {
		t.Errorf("first status = %s, want live", got)
	}
	if !slices.Contains(h.runtime.removed, second.ContainerID) {
		t.Errorf("unhealthy container %s was not removed", second.ContainerID)
	}
}

func TestStartRejectsFlagLikeRef(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "web")

	_, err := h.deployer.Start(context.Background(), app.ID, "--upload-pack=evil")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// harness drives the Deployer with fakes at the Node's edge and the real
// SQLite store. The store, data dir and runtime (standing in for Docker)
// outlive a restart; the deployer, router and scheduler are rebuilt by it.
type harness struct {
	store     *sqlite.Store
	dataDir   string
	apps      *service.AppService
	deployer  *service.Deployer
	runtime   *fakeRuntime
	health    *fakeHealth
	router    *fakeRouter
	scheduler *fakeScheduler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		store:   openStore(t),
		dataDir: t.TempDir(),
		runtime: &fakeRuntime{containers: map[string]fakeContainer{}},
		health:  &fakeHealth{unhealthy: map[string]bool{}},
	}
	h.apps = service.NewAppService(h.store)
	h.wire()
	return h
}

func (h *harness) wire() {
	h.router = &fakeRouter{routes: map[string]string{}}
	h.scheduler = &fakeScheduler{}
	h.deployer = service.NewDeployer(service.DeployerDeps{
		Apps:        h.store,
		Deployments: h.store,
		Source:      fakeSource{},
		Builders:    map[domain.BuildType]service.Builder{domain.BuildDockerfile: fakeBuilder{}},
		Runtime:     h.runtime,
		Health:      h.health,
		Router:      h.router,
		Scheduler:   h.scheduler,
		DataDir:     h.dataDir,
		Log:         slog.New(slog.DiscardHandler),
	})
}

// restart simulates the service starting again on the same data dir and
// Docker: a fresh deployer, empty routes and queue, then startup recovery.
func (h *harness) restart(t *testing.T) {
	t.Helper()
	h.wire()
	if err := h.deployer.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
}

// seed stores a deployment as a previous run of the service left it.
func (h *harness) seed(t *testing.T, dep domain.Deployment) domain.Deployment {
	t.Helper()
	dep.ID = domain.NewID()
	if err := h.store.SaveDeployment(context.Background(), dep); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
	return dep
}

// openStore opens the real SQLite store in a temp data dir.
func openStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func (h *harness) createApp(t *testing.T, name string) domain.App {
	t.Helper()
	app, err := h.apps.Create(context.Background(), domain.App{
		Name:    name,
		RepoURL: "https://example.test/acme/" + name + ".git",
		Domain:  name + ".example.test",
		Port:    8080,
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	return app
}

// deploy queues and runs a deployment synchronously, returning its stored
// state and the error from running it.
func (h *harness) deploy(appID string) (domain.Deployment, error) {
	ctx := context.Background()
	dep, err := h.deployer.Start(ctx, appID, "")
	if err != nil {
		return domain.Deployment{}, err
	}
	runErr := h.deployer.Run(ctx, dep.ID)
	stored, err := h.store.GetDeployment(ctx, dep.ID)
	if err != nil {
		return domain.Deployment{}, err
	}
	return stored, runErr
}

func (h *harness) get(t *testing.T, id string) domain.Deployment {
	t.Helper()
	dep, err := h.store.GetDeployment(context.Background(), id)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	return dep
}

type fakeSource struct{}

func (fakeSource) Fetch(_ context.Context, _, ref, _ string) (string, error) {
	return "commit-of-" + ref, nil
}

type fakeBuilder struct{}

func (fakeBuilder) Build(context.Context, service.BuildRequest) error { return nil }

// fakeRuntime stands in for Docker: it remembers the containers it runs, so
// a test can change a published port or check what is still running.
type fakeRuntime struct {
	started    int
	containers map[string]fakeContainer // by ID
	removed    []string                 // IDs or names passed to Remove
}

type fakeContainer struct {
	name     string
	upstream string
}

func (r *fakeRuntime) Start(_ context.Context, spec service.ContainerSpec) (service.Container, error) {
	r.started++
	ctr := service.Container{
		ID:       fmt.Sprintf("ctr-%d", r.started),
		Upstream: fmt.Sprintf("127.0.0.1:%d", 9000+r.started),
	}
	r.containers[ctr.ID] = fakeContainer{name: spec.Name, upstream: ctr.Upstream}
	return ctr, nil
}

// Remove accepts an ID or a name, as docker rm does.
func (r *fakeRuntime) Remove(_ context.Context, idOrName string) error {
	r.removed = append(r.removed, idOrName)
	for id, c := range r.containers {
		if id == idOrName || c.name == idOrName {
			delete(r.containers, id)
			return nil
		}
	}
	return fmt.Errorf("no such container: %s", idOrName)
}

func (r *fakeRuntime) Upstream(_ context.Context, id string, _ int) (string, error) {
	c, ok := r.containers[id]
	if !ok {
		return "", fmt.Errorf("no such container: %s", id)
	}
	return c.upstream, nil
}

// Deleting an App is tested at the HTTP seam (internal/httpapi).
func (*fakeRuntime) RemoveLabelled(context.Context, string, string) error { return nil }
func (*fakeRuntime) RemoveImages(context.Context, string) error           { return nil }

// run plants a container as an earlier run of the service left it.
func (r *fakeRuntime) run(id, name, upstream string) {
	r.containers[id] = fakeContainer{name: name, upstream: upstream}
}

// running reports whether a container with this name exists.
func (r *fakeRuntime) running(name string) bool {
	for _, c := range r.containers {
		if c.name == name {
			return true
		}
	}
	return false
}

type fakeHealth struct{ unhealthy map[string]bool }

func (h *fakeHealth) Wait(_ context.Context, upstream string) error {
	if h.unhealthy[upstream] {
		return errors.New("connection refused")
	}
	return nil
}

type fakeRouter struct{ routes map[string]string }

func (r *fakeRouter) SetRoute(host, upstream string) { r.routes[host] = upstream }
func (r *fakeRouter) RemoveRoute(host string)        { delete(r.routes, host) }
func (*fakeRouter) RemoveRoutesTo(string)            {}

// fakeScheduler records jobs; tests call Deployer.Run themselves.
type fakeScheduler struct{ enqueued []string }

func (s *fakeScheduler) Enqueue(id string) error {
	s.enqueued = append(s.enqueued, id)
	return nil
}
