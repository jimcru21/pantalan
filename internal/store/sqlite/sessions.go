package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

// CreateSession stores sess, only while the Admin's password hash is
// still passwordHash, and then deletes the oldest sessions beyond the
// newest keep, in one transaction. Sessions created at the same time are
// ordered by insertion. The check and the insert share the transaction's
// write lock, so a password reset commits either before (and the session
// is refused with domain.ErrConflict) or after (and deletes it).
func (s *Store) CreateSession(ctx context.Context, sess domain.Session, keep int, passwordHash string) error {
	tx, err := s.db.BeginTx(ctx, nil) // BEGIN IMMEDIATE (see dsn)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO sessions (token_hash, created_at, last_seen_at)
		SELECT ?, ?, ? WHERE EXISTS (SELECT 1 FROM admin WHERE id = 1 AND password_hash = ?)`,
		sess.TokenHash, formatTime(sess.CreatedAt), formatTime(sess.LastSeenAt), passwordHash)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%w: the password changed", domain.ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE rowid NOT IN
		(SELECT rowid FROM sessions ORDER BY created_at DESC, rowid DESC LIMIT ?)`, keep); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteExpiredSessions deletes the sessions last seen at or before seenBy
// or created at or before createdBy, and returns how many it deleted.
func (s *Store) DeleteExpiredSessions(ctx context.Context, seenBy, createdBy time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE last_seen_at <= ? OR created_at <= ?`,
		formatTime(seenBy), formatTime(createdBy))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (s *Store) GetSession(ctx context.Context, tokenHash string) (domain.Session, error) {
	var created, lastSeen string
	err := s.db.QueryRowContext(ctx, `SELECT created_at, last_seen_at FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&created, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, fmt.Errorf("session: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Session{}, err
	}
	sess := domain.Session{TokenHash: tokenHash}
	if sess.CreatedAt, err = parseTime(created); err != nil {
		return domain.Session{}, err
	}
	if sess.LastSeenAt, err = parseTime(lastSeen); err != nil {
		return domain.Session{}, err
	}
	return sess, nil
}

func (s *Store) TouchSession(ctx context.Context, tokenHash string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, formatTime(at), tokenHash)
	return err
}

// DeleteSession removes the session; deleting one that is already gone is
// not an error.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}
