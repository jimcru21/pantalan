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
	env, err := marshalEnv(app.Env)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO apps (`+appColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		app.ID, app.Name, app.RepoURL, app.Branch, string(app.BuildType), app.Domain, app.Port, env,
		formatTime(app.CreatedAt))
	if isUniqueViolation(err) {
		return s.domainTaken(ctx, app.Domain)
	}
	return err
}

// UpdateApp writes the settings columns only, so env vars and the
// creation time stay as they are.
func (s *Store) UpdateApp(ctx context.Context, app domain.App) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE apps SET name = ?, repo_url = ?, branch = ?, build_type = ?, domain = ?, port = ? WHERE id = ?`,
		app.Name, app.RepoURL, app.Branch, string(app.BuildType), app.Domain, app.Port, app.ID)
	if isUniqueViolation(err) {
		return s.domainTaken(ctx, app.Domain)
	}
	if err != nil {
		return err
	}
	return touchedRow(res, appNotFound(app.ID))
}

// SetAppEnv replaces the env vars of the App with ID id.
func (s *Store) SetAppEnv(ctx context.Context, id string, env map[string]string) error {
	data, err := marshalEnv(env)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE apps SET env = ? WHERE id = ?`, data, id)
	if err != nil {
		return err
	}
	return touchedRow(res, appNotFound(id))
}

// DeleteApp deletes the App with ID id and its Deployments, at once.
func (s *Store) DeleteApp(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM deployments WHERE app_id = ?`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return touchedRow(res, appNotFound(id))
	})
}

func appNotFound(id string) error { return fmt.Errorf("app %s: %w", id, domain.ErrNotFound) }

// touchedRow is nil when the write whose result is res changed a row,
// none when it changed nothing (the row it was for isn't there), or the
// error reading how many rows it changed.
func touchedRow(res sql.Result, none error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return none
	}
	return nil
}

func marshalEnv(env map[string]string) (string, error) {
	if len(env) == 0 {
		return "{}", nil // json.Marshal writes null for a nil map
	}
	data, err := json.Marshal(env)
	return string(data), err
}

// domainTaken is the conflict for a domain another App already has.
func (s *Store) domainTaken(ctx context.Context, host string) error {
	var holder string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM apps WHERE domain = ?`, host).Scan(&holder); err != nil {
		return domain.ConflictingField("domain", fmt.Sprintf("domain %s is already in use", host))
	}
	return domain.ConflictingField("domain", fmt.Sprintf("domain %s is used by app %s", host, holder))
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
