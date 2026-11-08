package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/pantalan/pantalan/internal/domain"
)

// errInterrupted is recorded on deployments the service stopped in the
// middle of: a crash, a reboot or a kill outlasting the shutdown grace.
var errInterrupted = errors.New("interrupted by restart")

// Recover brings the Node back to where the previous run of the service left
// it. Call it once at startup, after the store is open and before the queue
// runs. It fails deployments that were mid-build and cleans up after them,
// re-queues the ones still waiting, oldest first, and restores routes to
// live deployments at the port Docker publishes them on now.
func (d *Deployer) Recover(ctx context.Context) error {
	apps, err := d.deps.Apps.ListApps(ctx)
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}
	var queued []domain.Deployment
	for _, app := range apps {
		deps, err := d.deps.Deployments.ListDeployments(ctx, app.ID)
		if err != nil {
			return fmt.Errorf("deployments of %s: %w", app.ID, err)
		}
		for _, dep := range deps {
			switch dep.Status {
			case domain.DeploymentQueued:
				queued = append(queued, dep)
			case domain.DeploymentBuilding, domain.DeploymentStarting:
				if err := d.failInterrupted(ctx, dep); err != nil {
					return err
				}
			}
		}
	}

	slices.SortStableFunc(queued, func(a, b domain.Deployment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	for _, dep := range queued {
		if err := d.deps.Scheduler.Enqueue(dep.ID); err != nil {
			// The queue is full: fail it as Start would, and carry on.
			err = d.finish(ctx, &dep, fmt.Errorf("enqueue: %w", err))
			d.deps.Log.Error("re-queue deployment", "deployment", dep.ID, "err", err)
		}
	}

	for _, app := range apps {
		if err := d.restoreRoute(ctx, app); err != nil {
			return err
		}
	}
	return nil
}

// restoreRoute points the router at app's live deployment, if it has one,
// first refreshing the stored upstream from Docker: routes live only in
// memory, and a reboot or Docker upgrade gives containers new ports.
func (d *Deployer) restoreRoute(ctx context.Context, app domain.App) error {
	dep, err := d.deps.Deployments.LiveDeployment(ctx, app.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("live deployment for %s: %w", app.ID, err)
	}
	upstream, err := d.deps.Runtime.Upstream(ctx, dep.ContainerID, app.Port)
	switch {
	case err != nil:
		// Route to the last known upstream: the proxy answers 502 until the
		// container is back, which says more than a missing route's 404.
		d.deps.Log.Warn("look up upstream, keeping the stored one", "deployment", dep.ID, "upstream", dep.Upstream, "err", err)
	case upstream != dep.Upstream:
		d.deps.Log.Info("upstream changed", "deployment", dep.ID, "from", dep.Upstream, "to", upstream)
		dep.Upstream = upstream
		if err := d.deps.Deployments.SaveDeployment(ctx, dep); err != nil {
			return fmt.Errorf("save deployment: %w", err)
		}
	}
	d.deps.Router.SetRoute(app.Domain, dep.Upstream)
	return nil
}

// failInterrupted marks a deployment that was building or starting as
// failed, then removes what its run would have removed on failure.
func (d *Deployer) failInterrupted(ctx context.Context, dep domain.Deployment) error {
	wasStarting := dep.Status == domain.DeploymentStarting
	// finish hands back errInterrupted itself unless saving failed too.
	if err := d.finish(ctx, &dep, errInterrupted); err != errInterrupted {
		return fmt.Errorf("fail interrupted deployment %s: %w", dep.ID, err)
	}
	d.deps.Log.Warn("deployment interrupted by restart", "deployment", dep.ID, "app", dep.AppID)
	// Only a starting deployment can have a container. Its row is saved
	// before the container runs, so the ID is usually missing; use the name.
	if wasStarting {
		id := dep.ContainerID
		if id == "" {
			id = containerName(dep.AppID, dep.ID)
		}
		d.removeContainer(ctx, id)
	}
	if err := os.RemoveAll(d.buildDir(dep.ID)); err != nil {
		d.deps.Log.Warn("remove build dir", "deployment", dep.ID, "err", err)
	}
	return nil
}
