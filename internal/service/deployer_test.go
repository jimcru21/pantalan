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
	app := h.createApp(t)

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
	app := h.createApp(t)

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
	app := h.createApp(t)

	_, err := h.deployer.Start(context.Background(), app.ID, "--upload-pack=evil")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

type harness struct {
	store    *sqlite.Store
	apps     *service.AppService
	deployer *service.Deployer
	runtime  *fakeRuntime
	health   *fakeHealth
	router   *fakeRouter
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		store:   openStore(t),
		runtime: &fakeRuntime{},
		health:  &fakeHealth{unhealthy: map[string]bool{}},
		router:  &fakeRouter{routes: map[string]string{}},
	}
	h.apps = service.NewAppService(h.store)
	h.deployer = service.NewDeployer(service.DeployerDeps{
		Apps:        h.store,
		Deployments: h.store,
		Source:      fakeSource{},
		Builders:    map[domain.BuildType]service.Builder{domain.BuildDockerfile: fakeBuilder{}},
		Runtime:     h.runtime,
		Health:      h.health,
		Router:      h.router,
		Scheduler:   fakeScheduler{},
		DataDir:     t.TempDir(),
		Log:         slog.New(slog.DiscardHandler),
	})
	return h
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

func (h *harness) createApp(t *testing.T) domain.App {
	t.Helper()
	app, err := h.apps.Create(context.Background(), domain.App{
		Name:    "web",
		RepoURL: "https://example.test/acme/web.git",
		Domain:  "web.example.test",
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

type fakeRuntime struct {
	started int
	removed []string
}

func (r *fakeRuntime) Start(context.Context, service.ContainerSpec) (service.Container, error) {
	r.started++
	return service.Container{
		ID:       fmt.Sprintf("ctr-%d", r.started),
		Upstream: fmt.Sprintf("127.0.0.1:%d", 9000+r.started),
	}, nil
}

func (r *fakeRuntime) Remove(_ context.Context, id string) error {
	r.removed = append(r.removed, id)
	return nil
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

// fakeScheduler accepts jobs; tests call Deployer.Run themselves.
type fakeScheduler struct{}

func (fakeScheduler) Enqueue(string) error { return nil }
