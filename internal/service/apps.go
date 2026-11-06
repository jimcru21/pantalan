package service

import (
	"context"
	"fmt"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

type AppService struct {
	apps AppRepository
	now  func() time.Time
}

func NewAppService(apps AppRepository) *AppService {
	return &AppService{apps: apps, now: time.Now}
}

func (s *AppService) Create(ctx context.Context, app domain.App) (domain.App, error) {
	if app.Branch == "" {
		app.Branch = "main"
	}
	if app.BuildType == "" {
		app.BuildType = domain.BuildDockerfile
	}
	if err := app.Validate(); err != nil {
		return domain.App{}, err
	}
	app.ID = domain.NewID()
	app.CreatedAt = s.now().UTC()
	if err := s.apps.CreateApp(ctx, app); err != nil {
		return domain.App{}, fmt.Errorf("create app: %w", err)
	}
	return app, nil
}

func (s *AppService) Get(ctx context.Context, id string) (domain.App, error) {
	return s.apps.GetApp(ctx, id)
}

func (s *AppService) List(ctx context.Context) ([]domain.App, error) {
	return s.apps.ListApps(ctx)
}

// FindByRepo returns the apps that track branch of repoURL; webhooks use it
// to decide what a push should deploy.
func (s *AppService) FindByRepo(ctx context.Context, repoURL, branch string) ([]domain.App, error) {
	return s.apps.FindAppsByRepo(ctx, repoURL, branch)
}
