package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

// EndExpiredSessions deletes the sessions last seen at or before seenBy or
// created at or before createdBy, and returns how many it deleted.
func (s *Store) EndExpiredSessions(ctx context.Context, seenBy, createdBy time.Time) (int, error) {
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

// EndSession deletes the session; ending one that is already gone is not
// an error.
func (s *Store) EndSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}
