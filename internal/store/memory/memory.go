// Package memory is an in-memory store for development and tests. State is
// lost on restart; a SQLite adapter with the same methods replaces it in
// production.
package memory

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/pantalan/pantalan/internal/domain"
)

type Store struct {
	mu          sync.RWMutex
	apps        map[string]domain.App
	deployments map[string]domain.Deployment
}

func New() *Store {
	return &Store{apps: map[string]domain.App{}, deployments: map[string]domain.Deployment{}}
}

func (s *Store) CreateApp(_ context.Context, app domain.App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.apps {
		if strings.EqualFold(existing.Domain, app.Domain) {
			return fmt.Errorf("%w: domain %s is used by app %s", domain.ErrConflict, app.Domain, existing.Name)
		}
	}
	app.Env = maps.Clone(app.Env)
	s.apps[app.ID] = app
	return nil
}

func (s *Store) GetApp(_ context.Context, id string) (domain.App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	app, ok := s.apps[id]
	if !ok {
		return domain.App{}, fmt.Errorf("app %s: %w", id, domain.ErrNotFound)
	}
	app.Env = maps.Clone(app.Env)
	return app, nil
}

func (s *Store) ListApps(_ context.Context) ([]domain.App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	apps := slices.Collect(maps.Values(s.apps))
	slices.SortFunc(apps, func(a, b domain.App) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return apps, nil
}

func (s *Store) FindAppsByRepo(_ context.Context, repoURL, branch string) ([]domain.App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.App
	for _, app := range s.apps {
		if app.Branch == branch && domain.SameRepo(app.RepoURL, repoURL) {
			out = append(out, app)
		}
	}
	return out, nil
}

func (s *Store) SaveDeployment(_ context.Context, dep domain.Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deployments[dep.ID] = dep
	return nil
}

func (s *Store) GetDeployment(_ context.Context, id string) (domain.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dep, ok := s.deployments[id]
	if !ok {
		return domain.Deployment{}, fmt.Errorf("deployment %s: %w", id, domain.ErrNotFound)
	}
	return dep, nil
}

// ListDeployments returns an app's deployments, newest first.
func (s *Store) ListDeployments(_ context.Context, appID string) ([]domain.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Deployment
	for _, dep := range s.deployments {
		if dep.AppID == appID {
			out = append(out, dep)
		}
	}
	slices.SortFunc(out, func(a, b domain.Deployment) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.ID, a.ID))
	})
	return out, nil
}

func (s *Store) LiveDeployment(ctx context.Context, appID string) (domain.Deployment, error) {
	deps, err := s.ListDeployments(ctx, appID)
	if err != nil {
		return domain.Deployment{}, err
	}
	for _, dep := range deps {
		if dep.Status == domain.DeploymentLive {
			return dep, nil
		}
	}
	return domain.Deployment{}, fmt.Errorf("live deployment for %s: %w", appID, domain.ErrNotFound)
}
