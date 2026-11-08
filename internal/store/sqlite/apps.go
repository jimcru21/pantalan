package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/pantalan/pantalan/internal/domain"
)

// timeLayout is RFC 3339 in UTC with fixed-width nanoseconds, so stored
// times sort as strings.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

const appColumns = `id, name, repo_url, branch, build_type, domain, port, env, created_at`

func (s *Store) CreateApp(ctx context.Context, app domain.App) error {
	env := []byte("{}") // json.Marshal writes null for a nil map
	if len(app.Env) > 0 {
		var err error
		if env, err = json.Marshal(app.Env); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO apps (`+appColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		app.ID, app.Name, app.RepoURL, app.Branch, string(app.BuildType), app.Domain, app.Port, string(env),
		formatTime(app.CreatedAt))
	if isUniqueViolation(err) {
		var holder string
		if lookupErr := s.db.QueryRowContext(ctx, `SELECT name FROM apps WHERE domain = ?`, app.Domain).Scan(&holder); lookupErr != nil {
			return fmt.Errorf("%w: domain %s is already in use", domain.ErrConflict, app.Domain)
		}
		return fmt.Errorf("%w: domain %s is used by app %s", domain.ErrConflict, app.Domain, holder)
	}
	return err
}

func isUniqueViolation(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

func (s *Store) GetApp(ctx context.Context, id string) (domain.App, error) {
	app, err := scanApp(s.db.QueryRowContext(ctx, `SELECT `+appColumns+` FROM apps WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.App{}, fmt.Errorf("app %s: %w", id, domain.ErrNotFound)
	}
	return app, err
}

func (s *Store) ListApps(ctx context.Context) ([]domain.App, error) {
	return s.queryApps(ctx, `SELECT `+appColumns+` FROM apps ORDER BY created_at, id`)
}

// FindAppsByRepo matches the branch in SQL and the repository with
// domain.SameRepo, which no index can express.
func (s *Store) FindAppsByRepo(ctx context.Context, repoURL, branch string) ([]domain.App, error) {
	apps, err := s.queryApps(ctx, `SELECT `+appColumns+` FROM apps WHERE branch = ? ORDER BY created_at, id`, branch)
	if err != nil {
		return nil, err
	}
	var out []domain.App
	for _, app := range apps {
		if domain.SameRepo(app.RepoURL, repoURL) {
			out = append(out, app)
		}
	}
	return out, nil
}

func (s *Store) queryApps(ctx context.Context, query string, args ...any) ([]domain.App, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := []domain.App{}
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanApp(row scanner) (domain.App, error) {
	var (
		app       domain.App
		buildType string
		env       string
		createdAt string
	)
	if err := row.Scan(&app.ID, &app.Name, &app.RepoURL, &app.Branch, &buildType, &app.Domain, &app.Port, &env, &createdAt); err != nil {
		return domain.App{}, err
	}
	app.BuildType = domain.BuildType(buildType)
	if err := json.Unmarshal([]byte(env), &app.Env); err != nil {
		return domain.App{}, fmt.Errorf("app %s env: %w", app.ID, err)
	}
	if len(app.Env) == 0 {
		app.Env = nil
	}
	var err error
	if app.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.App{}, fmt.Errorf("app %s created_at: %w", app.ID, err)
	}
	return app, nil
}
