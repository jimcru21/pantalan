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
	withDefaults(&app)
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

// Update replaces the settings of the App with ID id (name, repository,
// branch, build type, domain, port; the same defaults as Create apply)
// and returns the App. Its ID, creation time and env vars stay as they
// are, and no Deployment starts: the settings take effect on the next one.
func (s *AppService) Update(ctx context.Context, id string, settings domain.App) (domain.App, error) {
	withDefaults(&settings)
	settings.ID, settings.Env = id, nil
	if err := settings.Validate(); err != nil {
		return domain.App{}, err
	}
	if err := s.apps.UpdateApp(ctx, settings); err != nil {
		return domain.App{}, fmt.Errorf("update app: %w", err)
	}
	return s.apps.GetApp(ctx, id)
}

// SetEnv replaces the env vars of the App with ID id. Like an Update, it
// starts no Deployment: the env vars take effect on the next one.
func (s *AppService) SetEnv(ctx context.Context, id string, env map[string]string) error {
	if err := domain.ValidateEnv(env); err != nil {
		return err
	}
	if err := s.apps.SetAppEnv(ctx, id, env); err != nil {
		return fmt.Errorf("set env of app: %w", err)
	}
	return nil
}

func withDefaults(app *domain.App) {
	if app.Branch == "" {
		app.Branch = "main"
	}
	if app.BuildType == "" {
		app.BuildType = domain.BuildDockerfile
	}
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
