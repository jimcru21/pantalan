package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

var (
	// ErrBadCredentials is the one answer to a wrong username or a wrong
	// password, so a sign-in never reveals which was wrong.
	ErrBadCredentials = errors.New("incorrect username or password")
	// ErrNoAdmin means nobody can sign in because no Admin exists yet.
	ErrNoAdmin = errors.New("no Admin has been created yet")
	// ErrNoSession means a token names no live session: it never existed,
	// expired, was signed out, or was ended by a password reset.
	ErrNoSession = errors.New("not signed in")
)

// ThrottledError refuses a sign-in that came too soon after too many
// failures. Its password was not checked and the attempt isn't counted.
type ThrottledError struct {
	Wait time.Duration // until the next attempt will be checked
}

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("too many failed sign-ins: try again in %v", e.Wait)
}

// touchEvery is how stale last_seen_at may get before a request rewrites
// it, so a burst of requests (polls) costs one write a minute at most.
const touchEvery = time.Minute

// AuthService signs the Admin in and out of the Dashboard and checks
// sessions. It keeps nothing in memory: every check reads the store, so a
// password reset by the CLI takes effect on the very next request.
type AuthService struct {
	admins   AdminRepository
	sessions SessionRepository
	hasher   PasswordHasher
	now      func() time.Time
	// dummy is a hash checked for unknown usernames, so they take as long
	// to refuse as a wrong password.
	dummy func() (string, error)
}

// NewAuthService builds the service; now is the clock sessions are timed
// with (time.Now in production).
func NewAuthService(admins AdminRepository, sessions SessionRepository, hasher PasswordHasher, now func() time.Time) *AuthService {
	return &AuthService{
		admins:   admins,
		sessions: sessions,
		hasher:   hasher,
		now:      now,
		dummy:    sync.OnceValues(func() (string, error) { return hasher.Hash(rand.Text()) }),
	}
}

// AdminExists reports whether there is an Admin to sign in as.
func (s *AuthService) AdminExists(ctx context.Context) (bool, error) {
	_, err := s.admins.GetAdmin(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// SignIn checks the credentials and starts a new session, returning its
// token for the browser's cookie. It fails with ErrNoAdmin before any
// Admin exists, ErrBadCredentials for a wrong username or password, and a
// *ThrottledError while the back-off after repeated failures runs.
//
// Every attempt that gets checked counts as a failure before its password
// is checked, and a success clears the count again. So two attempts
// racing at the end of a wait can't both be checked: the second sees the
// first's failure and waits.
func (s *AuthService) SignIn(ctx context.Context, username, password string) (string, error) {
	admin, err := s.reserveAttempt(ctx)
	if err != nil {
		return "", err
	}
	hash := admin.PasswordHash
	known := domain.SameUsername(username, admin.Username)
	if !known {
		if hash, err = s.dummy(); err != nil {
			return "", err
		}
	}
	ok, err := s.hasher.Verify(password, hash)
	if err != nil {
		return "", fmt.Errorf("verify password: %w", err)
	}
	if !ok || !known {
		return "", ErrBadCredentials
	}
	if err := s.admins.ResetFailedLogins(ctx); err != nil {
		return "", fmt.Errorf("reset failed logins: %w", err)
	}

	token := rand.Text()
	now := s.now().UTC()
	if err := s.sessions.CreateSession(ctx, domain.Session{TokenHash: tokenHash(token), CreatedAt: now, LastSeenAt: now}, domain.MaxSessions); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// reserveAttempt counts a sign-in attempt as a failure, unless the
// back-off refuses it, and returns the Admin it is checked against.
func (s *AuthService) reserveAttempt(ctx context.Context) (domain.Admin, error) {
	for {
		admin, err := s.admins.GetAdmin(ctx)
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Admin{}, ErrNoAdmin
		}
		if err != nil {
			return domain.Admin{}, err
		}
		now := s.now().UTC()
		if wait := admin.NextAttemptAt().Sub(now); wait > 0 {
			return domain.Admin{}, &ThrottledError{Wait: wait}
		}
		counted, err := s.admins.RecordFailedLogin(ctx, admin.FailedLogins, now)
		if err != nil {
			return domain.Admin{}, fmt.Errorf("record failed login: %w", err)
		}
		if counted {
			return admin, nil
		}
		// Another attempt was counted first: look again.
	}
}

// Authenticate checks that token names a live session and counts the
// request as activity. It fails with ErrNoSession otherwise.
func (s *AuthService) Authenticate(ctx context.Context, token string) error {
	if token == "" {
		return ErrNoSession
	}
	hash := tokenHash(token)
	sess, err := s.sessions.GetSession(ctx, hash)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrNoSession
	}
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if sess.ExpiredAt(now) {
		if err := s.sessions.DeleteSession(ctx, hash); err != nil {
			return err
		}
		return ErrNoSession
	}
	if now.Sub(sess.LastSeenAt) >= touchEvery {
		if err := s.sessions.TouchSession(ctx, hash, now); err != nil {
			return err
		}
	}
	return nil
}

// PruneSessions deletes every session that has expired by now and
// returns how many there were.
func (s *AuthService) PruneSessions(ctx context.Context) (int, error) {
	now := s.now().UTC()
	return s.sessions.DeleteExpiredSessions(ctx, now.Add(-domain.SessionIdle), now.Add(-domain.SessionMaxAge))
}

// SignOut ends the session token names, if there is one.
func (s *AuthService) SignOut(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.sessions.DeleteSession(ctx, tokenHash(token))
}

// tokenHash is what the store keeps of a session token: hex SHA-256.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
