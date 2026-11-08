package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

// The admin table holds at most one row, pinned to id 1.

func (s *Store) CreateAdmin(ctx context.Context, admin domain.Admin) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO admin (id, username, password_hash, created_at, password_changed_at)
		VALUES (1, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		admin.Username, admin.PasswordHash, formatTime(admin.CreatedAt), formatTime(admin.PasswordChangedAt))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%w: an Admin already exists", domain.ErrConflict)
	}
	return nil
}

// ResetAdminPassword runs in one transaction, so a running service sees
// either the old password and sessions or the new password and none.
func (s *Store) ResetAdminPassword(ctx context.Context, hash string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil) // BEGIN IMMEDIATE (see dsn)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE admin SET password_hash = ?, password_changed_at = ?,
		failed_logins = 0, last_failed_at = NULL WHERE id = 1`, hash, formatTime(at))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("admin: %w", domain.ErrNotFound)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetAdmin(ctx context.Context) (domain.Admin, error) {
	var (
		a                            domain.Admin
		lastFailed                   sql.NullString
		createdAt, passwordChangedAt string
	)
	err := s.db.QueryRowContext(ctx, `SELECT username, password_hash, failed_logins, last_failed_at, created_at, password_changed_at
		FROM admin WHERE id = 1`).Scan(&a.Username, &a.PasswordHash, &a.FailedLogins, &lastFailed, &createdAt, &passwordChangedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Admin{}, fmt.Errorf("admin: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Admin{}, err
	}
	if a.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Admin{}, err
	}
	if a.PasswordChangedAt, err = parseTime(passwordChangedAt); err != nil {
		return domain.Admin{}, err
	}
	if lastFailed.Valid {
		if a.LastFailedAt, err = parseTime(lastFailed.String); err != nil {
			return domain.Admin{}, err
		}
	}
	return a, nil
}

// RecordFailedLogin counts one more failed sign-in at at, but only while
// the count is still seen: a sign-in racing another sees false and must
// look again.
func (s *Store) RecordFailedLogin(ctx context.Context, seen int, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE admin SET failed_logins = failed_logins + 1, last_failed_at = ?
		WHERE id = 1 AND failed_logins = ?`, formatTime(at), seen)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) ResetFailedLogins(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin SET failed_logins = 0, last_failed_at = NULL WHERE id = 1`)
	return err
}

// RehashPassword leaves password_changed_at alone: the password is the
// same, only its hash changes.
func (s *Store) RehashPassword(ctx context.Context, old, rehashed string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin SET password_hash = ? WHERE id = 1 AND password_hash = ?`, rehashed, old)
	return err
}
