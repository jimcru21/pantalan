package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/pantalan/pantalan/internal/domain"
)

const deploymentColumns = `id, app_id, ref, commit_sha, image, container_id, upstream, status, error, created_at, finished_at`

// SaveDeployment inserts or replaces a deployment by ID. It updates in place
// rather than using INSERT OR REPLACE, which deletes the old row first.
func (s *Store) SaveDeployment(ctx context.Context, dep domain.Deployment) error {
	var finishedAt *string
	if dep.FinishedAt != nil {
		f := formatTime(*dep.FinishedAt)
		finishedAt = &f
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployments (`+deploymentColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			app_id = excluded.app_id, ref = excluded.ref, commit_sha = excluded.commit_sha,
			image = excluded.image, container_id = excluded.container_id, upstream = excluded.upstream,
			status = excluded.status, error = excluded.error, created_at = excluded.created_at,
			finished_at = excluded.finished_at`,
		dep.ID, dep.AppID, dep.Ref, dep.Commit, dep.Image, dep.ContainerID, dep.Upstream,
		string(dep.Status), dep.Error, formatTime(dep.CreatedAt), finishedAt)
	return err
}

func (s *Store) GetDeployment(ctx context.Context, id string) (domain.Deployment, error) {
	dep, err := scanDeployment(s.db.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM deployments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Deployment{}, fmt.Errorf("deployment %s: %w", id, domain.ErrNotFound)
	}
	return dep, err
}

// ListDeployments returns an app's deployments, newest first.
func (s *Store) ListDeployments(ctx context.Context, appID string) ([]domain.Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentColumns+` FROM deployments
		WHERE app_id = ? ORDER BY created_at DESC, id DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deps := []domain.Deployment{}
	for rows.Next() {
		dep, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

// LatestDeployments returns the newest deployment of every app that has
// one, in one query, ordered as ListDeployments would put them first.
func (s *Store) LatestDeployments(ctx context.Context) ([]domain.Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentColumns+` FROM (
		SELECT `+deploymentColumns+`, ROW_NUMBER() OVER (
			PARTITION BY app_id ORDER BY created_at DESC, id DESC) AS newest
		FROM deployments)
		WHERE newest = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deps := []domain.Deployment{}
	for rows.Next() {
		dep, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

func (s *Store) LiveDeployment(ctx context.Context, appID string) (domain.Deployment, error) {
	dep, err := scanDeployment(s.db.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM deployments
		WHERE app_id = ? AND status = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		appID, string(domain.DeploymentLive)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Deployment{}, fmt.Errorf("live deployment for %s: %w", appID, domain.ErrNotFound)
	}
	return dep, err
}

func scanDeployment(row scanner) (domain.Deployment, error) {
	var (
		dep        domain.Deployment
		status     string
		createdAt  string
		finishedAt sql.NullString
	)
	if err := row.Scan(&dep.ID, &dep.AppID, &dep.Ref, &dep.Commit, &dep.Image, &dep.ContainerID, &dep.Upstream,
		&status, &dep.Error, &createdAt, &finishedAt); err != nil {
		return domain.Deployment{}, err
	}
	dep.Status = domain.DeploymentStatus(status)
	var err error
	if dep.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Deployment{}, fmt.Errorf("deployment %s created_at: %w", dep.ID, err)
	}
	if finishedAt.Valid {
		t, err := parseTime(finishedAt.String)
		if err != nil {
			return domain.Deployment{}, fmt.Errorf("deployment %s finished_at: %w", dep.ID, err)
		}
		dep.FinishedAt = &t
	}
	return dep, nil
}
