package service_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

func TestRecoverRequeuesQueuedDeploymentsOldestFirst(t *testing.T) {
	h := newHarness(t)
	web := h.createApp(t, "web")
	api := h.createApp(t, "api")
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	third := h.seed(t, domain.Deployment{AppID: web.ID, Ref: "main", Status: domain.DeploymentQueued, CreatedAt: base.Add(2 * time.Minute)})
	first := h.seed(t, domain.Deployment{AppID: web.ID, Ref: "main", Status: domain.DeploymentQueued, CreatedAt: base})
	second := h.seed(t, domain.Deployment{AppID: api.ID, Ref: "main", Status: domain.DeploymentQueued, CreatedAt: base.Add(time.Minute)})

	h.restart(t)

	want := []string{first.ID, second.ID, third.ID}
	if !slices.Equal(h.scheduler.enqueued, want) {
		t.Fatalf("enqueued = %v, want %v", h.scheduler.enqueued, want)
	}
	for _, id := range h.scheduler.enqueued {
		if err := h.deployer.Run(context.Background(), id); err != nil {
			t.Fatalf("run %s: %v", id, err)
		}
	}
	for _, dep := range []domain.Deployment{second, third} {
		if got := h.get(t, dep.ID).Status; got != domain.DeploymentLive {
			t.Errorf("deployment %s status = %s, want live", dep.ID, got)
		}
	}
	if got := h.get(t, first.ID).Status; got != domain.DeploymentSuperseded {
		t.Errorf("first status = %s, want superseded by the later deploy of the same app", got)
	}
}

func TestRecoverFailsDeploymentsInterruptedMidBuild(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "web")
	created := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	building := h.seed(t, domain.Deployment{AppID: app.ID, Ref: "main", Status: domain.DeploymentBuilding, CreatedAt: created})
	starting := h.seed(t, domain.Deployment{AppID: app.ID, Ref: "main", Status: domain.DeploymentStarting, CreatedAt: created})
	// A deployment is saved as starting before its container is run, so the
	// stored row has no container ID; the container is known only by name.
	startingName := "pantalan-" + app.ID + "-" + starting.ID
	h.runtime.run("ctr-orphan", startingName, "127.0.0.1:9100")
	for _, dep := range []domain.Deployment{building, starting} {
		writeFile(t, filepath.Join(h.dataDir, "builds", dep.ID, "Dockerfile"))
	}

	h.restart(t)

	for _, dep := range []domain.Deployment{building, starting} {
		got := h.get(t, dep.ID)
		if got.Status != domain.DeploymentFailed || got.Error != "interrupted by restart" {
			t.Errorf("deployment %s = %s %q, want failed %q", dep.ID, got.Status, got.Error, "interrupted by restart")
		}
		if got.FinishedAt == nil {
			t.Errorf("deployment %s has no finished_at", dep.ID)
		}
		if _, err := os.Stat(filepath.Join(h.dataDir, "builds", dep.ID)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("build dir of %s still there (stat err: %v)", dep.ID, err)
		}
	}
	if h.runtime.running(startingName) {
		t.Errorf("container %s of the interrupted deployment is still running", startingName)
	}
	if len(h.scheduler.enqueued) != 0 {
		t.Errorf("enqueued = %v, want nothing", h.scheduler.enqueued)
	}
}

func TestRecoverRoutesLiveAppToItsContainersNewPort(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "web")
	live, err := h.deploy(app.ID)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	// Docker came back from a reboot and published the port elsewhere.
	h.runtime.run(live.ContainerID, "pantalan-"+app.ID+"-"+live.ID, "127.0.0.1:9555")

	h.restart(t)

	if got := h.router.routes[app.Domain]; got != "127.0.0.1:9555" {
		t.Errorf("route = %q, want the new upstream 127.0.0.1:9555", got)
	}
	if got := h.get(t, live.ID).Upstream; got != "127.0.0.1:9555" {
		t.Errorf("stored upstream = %q, want 127.0.0.1:9555", got)
	}
}

func TestRecoverKeepsStoredUpstreamWhenDockerCannotSay(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "web")
	live, err := h.deploy(app.ID)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	delete(h.runtime.containers, live.ContainerID) // removed behind Pantalan's back

	h.restart(t)

	if got := h.router.routes[app.Domain]; got != live.Upstream {
		t.Errorf("route = %q, want the stored upstream %q", got, live.Upstream)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
