package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

type DeployerDeps struct {
	Apps        AppRepository
	Deployments DeploymentRepository
	Source      SourceFetcher
	Builders    map[domain.BuildType]Builder
	Runtime     Runtime
	Health      HealthChecker
	Router      Router
	Scheduler   Scheduler
	DataDir     string // holds builds/ (scratch checkouts) and logs/
	Log         *slog.Logger
}

// Deployer runs the deploy pipeline: fetch → build → start → health check →
// switch traffic → retire the previous container. The old container keeps
// serving until the new one is healthy, so a bad deploy never takes an app down.
type Deployer struct {
	deps DeployerDeps
	now  func() time.Time

	mu       sync.Mutex
	appLocks map[string]*sync.Mutex
}

func NewDeployer(deps DeployerDeps) *Deployer {
	return &Deployer{deps: deps, now: time.Now, appLocks: map[string]*sync.Mutex{}}
}

// Start records a queued deployment of ref (the app's branch when empty) and
// hands it to the scheduler.
func (d *Deployer) Start(ctx context.Context, appID, ref string) (domain.Deployment, error) {
	app, err := d.deps.Apps.GetApp(ctx, appID)
	if err != nil {
		return domain.Deployment{}, err
	}
	if ref == "" {
		ref = app.Branch
	}
	if err := domain.ValidateRef(ref); err != nil {
		return domain.Deployment{}, err
	}
	dep := domain.Deployment{
		ID:        domain.NewID(),
		AppID:     app.ID,
		Ref:       ref,
		Status:    domain.DeploymentQueued,
		CreatedAt: d.now().UTC(),
	}
	if err := d.deps.Deployments.SaveDeployment(ctx, dep); err != nil {
		return domain.Deployment{}, fmt.Errorf("save deployment: %w", err)
	}
	if err := d.deps.Scheduler.Enqueue(dep.ID); err != nil {
		return domain.Deployment{}, d.finish(ctx, &dep, fmt.Errorf("enqueue: %w", err))
	}
	return dep, nil
}

// Run executes a queued deployment end to end. It is the queue's job handler.
func (d *Deployer) Run(ctx context.Context, deploymentID string) error {
	dep, err := d.deps.Deployments.GetDeployment(ctx, deploymentID)
	if err != nil {
		return err
	}
	app, err := d.deps.Apps.GetApp(ctx, dep.AppID)
	if err != nil {
		return d.finish(ctx, &dep, err)
	}

	unlock := d.lockApp(app.ID)
	defer unlock()

	logs, err := d.createLog(dep.ID)
	if err != nil {
		return d.finish(ctx, &dep, err)
	}
	runErr := d.run(ctx, app, &dep, logs)
	if runErr != nil {
		fmt.Fprintf(logs, "==> deployment failed: %v\n", runErr)
	}
	if err := logs.Close(); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("close log: %w", err))
	}
	return d.finish(ctx, &dep, runErr)
}

func (d *Deployer) run(ctx context.Context, app domain.App, dep *domain.Deployment, logs io.Writer) error {
	builder, ok := d.deps.Builders[app.BuildType]
	if !ok {
		return fmt.Errorf("no builder for build type %q", app.BuildType)
	}

	dir := d.buildDir(dep.ID)
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			d.deps.Log.Warn("remove build dir", "dir", dir, "err", err)
		}
	}()

	if err := d.transition(ctx, dep, domain.DeploymentBuilding); err != nil {
		return err
	}
	fmt.Fprintf(logs, "==> fetching %s at %s\n", app.RepoURL, dep.Ref)
	commit, err := d.deps.Source.Fetch(ctx, app.RepoURL, dep.Ref, dir)
	if err != nil {
		return fmt.Errorf("fetch source: %w", err)
	}
	dep.Commit = commit
	dep.Image = fmt.Sprintf("pantalan/%s:%s", app.ID, dep.ID)

	fmt.Fprintf(logs, "==> building %s (%s) with %s\n", dep.Image, commit, app.BuildType)
	if err := builder.Build(ctx, BuildRequest{Dir: dir, Image: dep.Image, Logs: logs}); err != nil {
		return fmt.Errorf("build: %w", err)
	}

	if err := d.transition(ctx, dep, domain.DeploymentStarting); err != nil {
		return err
	}
	ctr, err := d.deps.Runtime.Start(ctx, ContainerSpec{
		Name:   containerName(app.ID, dep.ID),
		Image:  dep.Image,
		Port:   app.Port,
		Env:    app.Env,
		Labels: map[string]string{domain.AppLabel: app.ID, domain.DeploymentLabel: dep.ID},
	})
	if err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	dep.ContainerID, dep.Upstream = ctr.ID, ctr.Upstream

	fmt.Fprintf(logs, "==> waiting for %s to become healthy\n", ctr.Upstream)
	if err := d.deps.Health.Wait(ctx, ctr.Upstream); err != nil {
		d.removeContainer(ctx, ctr.ID)
		return fmt.Errorf("health check: %w", err)
	}

	previous, err := d.deps.Deployments.LiveDeployment(ctx, app.ID)
	hasPrevious := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		d.removeContainer(ctx, ctr.ID)
		return fmt.Errorf("find live deployment: %w", err)
	}

	d.deps.Router.SetRoute(app.Domain, ctr.Upstream)
	dep.Status = domain.DeploymentLive
	fmt.Fprintf(logs, "==> live at %s\n", app.Domain)

	if hasPrevious {
		d.retire(ctx, previous)
	}
	return nil
}

func (d *Deployer) List(ctx context.Context, appID string) ([]domain.Deployment, error) {
	if _, err := d.deps.Apps.GetApp(ctx, appID); err != nil {
		return nil, err
	}
	return d.deps.Deployments.ListDeployments(ctx, appID)
}

// Get returns one of an App's Deployments. A Deployment of another App is
// not found, so a Deployment is only ever addressed under its own App.
func (d *Deployer) Get(ctx context.Context, appID, deploymentID string) (domain.Deployment, error) {
	if _, err := d.deps.Apps.GetApp(ctx, appID); err != nil {
		return domain.Deployment{}, err
	}
	dep, err := d.deps.Deployments.GetDeployment(ctx, deploymentID)
	if err != nil {
		return domain.Deployment{}, err
	}
	if dep.AppID != appID {
		return domain.Deployment{}, fmt.Errorf("deployment %s of app %s: %w", deploymentID, appID, domain.ErrNotFound)
	}
	return dep, nil
}

// Latest returns every App's newest Deployment, by App ID. An App with no
// Deployments has no entry.
func (d *Deployer) Latest(ctx context.Context) (map[string]domain.Deployment, error) {
	deps, err := d.deps.Deployments.LatestDeployments(ctx)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]domain.Deployment, len(deps))
	for _, dep := range deps {
		latest[dep.AppID] = dep
	}
	return latest, nil
}

// OpenLog opens a deployment's build log. Looking the deployment up first
// means only IDs from the store, never raw user input, reach the filesystem.
func (d *Deployer) OpenLog(ctx context.Context, deploymentID string) (*os.File, error) {
	if _, err := d.deps.Deployments.GetDeployment(ctx, deploymentID); err != nil {
		return nil, err
	}
	f, err := os.Open(d.logPath(deploymentID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("logs for %s: %w", deploymentID, domain.ErrNotFound)
	}
	return f, err
}

func (d *Deployer) transition(ctx context.Context, dep *domain.Deployment, status domain.DeploymentStatus) error {
	dep.Status = status
	if err := d.deps.Deployments.SaveDeployment(ctx, *dep); err != nil {
		return fmt.Errorf("save deployment: %w", err)
	}
	return nil
}

// finish records dep's final state and returns runErr joined with any error
// from saving it. It ignores ctx cancellation so a deploy interrupted by
// shutdown is still recorded as failed.
func (d *Deployer) finish(ctx context.Context, dep *domain.Deployment, runErr error) error {
	now := d.now().UTC()
	dep.FinishedAt = &now
	if runErr != nil {
		dep.Status = domain.DeploymentFailed
		dep.Error = runErr.Error()
	}
	if err := d.deps.Deployments.SaveDeployment(context.WithoutCancel(ctx), *dep); err != nil {
		return errors.Join(runErr, fmt.Errorf("save deployment: %w", err))
	}
	return runErr
}

func (d *Deployer) retire(ctx context.Context, dep domain.Deployment) {
	dep.Status = domain.DeploymentSuperseded
	if err := d.deps.Deployments.SaveDeployment(context.WithoutCancel(ctx), dep); err != nil {
		d.deps.Log.Error("mark deployment superseded", "deployment", dep.ID, "err", err)
	}
	d.removeContainer(ctx, dep.ContainerID)
}

func (d *Deployer) removeContainer(ctx context.Context, id string) {
	if err := d.deps.Runtime.Remove(context.WithoutCancel(ctx), id); err != nil {
		d.deps.Log.Warn("remove container", "container", id, "err", err)
	}
}

// lockApp serialises deployments of one app, so two concurrent deploys cannot
// both believe they replaced the same live container.
func (d *Deployer) lockApp(appID string) (unlock func()) {
	d.mu.Lock()
	l, ok := d.appLocks[appID]
	if !ok {
		l = &sync.Mutex{}
		d.appLocks[appID] = l
	}
	d.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// buildDir is the scratch checkout of a deployment, removed when its run ends.
func (d *Deployer) buildDir(deploymentID string) string {
	return filepath.Join(d.deps.DataDir, "builds", deploymentID)
}

// containerName is the Docker name of a deployment's container. It is known
// before the container exists, so cleanup can find a container whose ID was
// never recorded.
func containerName(appID, deploymentID string) string {
	return fmt.Sprintf("pantalan-%s-%s", appID, deploymentID)
}

func (d *Deployer) logPath(deploymentID string) string {
	return filepath.Join(d.deps.DataDir, "logs", deploymentID+".log")
}

func (d *Deployer) createLog(deploymentID string) (*os.File, error) {
	path := d.logPath(deploymentID)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create log: %w", err)
	}
	return f, nil
}
