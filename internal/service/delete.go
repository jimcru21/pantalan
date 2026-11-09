package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/pantalan/pantalan/internal/domain"
)

// DeleteApp removes the App with ID appID and everything its Deployments
// left on the Node: its route, its containers, its images and its
// Deployment Logs, then the App and its Deployments.
//
// It refuses with domain.ErrConflict, and changes nothing, while a
// Deployment of the App is queued, building or starting, so a running
// Deployment never finds its App half removed. No Deployment can start
// while it runs: Start waits for it, then finds no App.
//
// Each removal succeeds when there is nothing left to remove, so after a
// failure (Docker not answering, say) deleting again finishes the job.
func (d *Deployer) DeleteApp(ctx context.Context, appID string) error {
	d.starting.Lock()
	defer d.starting.Unlock()

	app, err := d.deps.Apps.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	deps, err := d.deps.Deployments.ListDeployments(ctx, appID)
	if err != nil {
		return fmt.Errorf("deployments of %s: %w", appID, err)
	}
	for _, dep := range deps {
		switch dep.Status {
		case domain.DeploymentQueued, domain.DeploymentBuilding, domain.DeploymentStarting:
			return fmt.Errorf("%w: Deployment %s of %s is %s; delete the App once it has finished",
				domain.ErrConflict, dep.ID, app.Name, dep.Status)
		}
	}

	// Stop serving first: the current domain's route, and any other
	// route to the live container (a domain edit moves the route, but
	// one whose move failed would be left under the old domain).
	d.deps.Router.RemoveRoute(app.Domain)
	for _, dep := range deps {
		if dep.Status == domain.DeploymentLive && dep.Upstream != "" {
			d.deps.Router.RemoveRoutesTo(dep.Upstream)
		}
	}
	if err := d.deps.Runtime.RemoveLabelled(ctx, domain.AppLabel, app.ID); err != nil {
		return fmt.Errorf("remove containers of %s: %w", app.ID, err)
	}
	if err := d.deps.Runtime.RemoveImages(ctx, imageRepository(app.ID)); err != nil {
		return fmt.Errorf("remove images of %s: %w", app.ID, err)
	}
	for _, dep := range deps {
		if err := os.Remove(d.logPath(dep.ID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove log of %s: %w", dep.ID, err)
		}
	}
	if err := d.deps.Apps.DeleteApp(ctx, app.ID); err != nil {
		return fmt.Errorf("delete app: %w", err)
	}
	d.deps.Log.Info("app deleted", "app", app.ID, "deployments", len(deps))
	return nil
}
