package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

// The admin table holds at most one row, pinned to id 1. Each method here
// that writes more than one statement runs in one transaction that takes
// the write lock when it begins (BEGIN IMMEDIATE, see dsn), so the CLI and
// a running service sharing the file serialise on it.

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

func (s *Store) GetAdmin(ctx context.Context) (domain.Admin, error) {
	return getAdmin(ctx, s.db)
}

// ResetPassword runs in one transaction, so a running service sees either
// the old password and sessions or the new password and none.
func (s *Store) ResetPassword(ctx context.Context, hash string, at time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
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
		_, err = tx.ExecContext(ctx, `DELETE FROM sessions`)
		return err
	})
}

// ReserveAttempt reads the Admin and counts the failure under one write
// lock, so no other attempt can be counted in between.
func (s *Store) ReserveAttempt(ctx context.Context, at time.Time, admit func(domain.Admin) error) (domain.Admin, error) {
	var admin domain.Admin
	err := s.inTx(ctx, func(tx *sql.Tx) (err error) {
		if admin, err = getAdmin(ctx, tx); err != nil {
			return err
		}
		if err := admit(admin); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE admin SET failed_logins = failed_logins + 1, last_failed_at = ?
			WHERE id = 1`, formatTime(at))
		return err
	})
	if err != nil {
		return domain.Admin{}, err
	}
	return admin, nil
}

// StartSession checks the hash and inserts the session under one write
// lock, so a password reset commits either before (and the session is
// refused with domain.ErrConflict) or after (and ends it). Sessions
// created at the same time are ordered by insertion. A rehash leaves
// password_changed_at alone: the password is the same, only its hash
// changes.
func (s *Store) StartSession(ctx context.Context, checked, rehashed string, sess domain.Session, keep int) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		hash := checked
		if rehashed != "" {
			hash = rehashed
		}
		res, err := tx.ExecContext(ctx, `UPDATE admin SET password_hash = ?, failed_logins = 0, last_failed_at = NULL
			WHERE id = 1 AND password_hash = ?`, hash, checked)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("%w: the password changed", domain.ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (token_hash, created_at, last_seen_at) VALUES (?, ?, ?)`,
			sess.TokenHash, formatTime(sess.CreatedAt), formatTime(sess.LastSeenAt)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE rowid NOT IN
			(SELECT rowid FROM sessions ORDER BY created_at DESC, rowid DESC LIMIT ?)`, keep)
		return err
	})
}

// inTx runs f in a transaction and commits it unless f fails.
func (s *Store) inTx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil) // BEGIN IMMEDIATE (see dsn)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// queryer is what reading the Admin needs: a database or a transaction.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getAdmin(ctx context.Context, q queryer) (domain.Admin, error) {
	var (
		a                            domain.Admin
		lastFailed                   sql.NullString
		createdAt, passwordChangedAt string
	)
	err := q.QueryRowContext(ctx, `SELECT username, password_hash, failed_logins, last_failed_at, created_at, password_changed_at
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
