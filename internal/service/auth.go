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
	// ErrBusy refuses a sign-in that waited HashSlotWait without getting
	// to hash its password.
	ErrBusy = errors.New("too many sign-ins at once")
)

// At most HashSlots password hashes run at once; a sign-in that can't
// start one within HashSlotWait is refused with ErrBusy.
const (
	HashSlots    = 2
	HashSlotWait = 5 * time.Second
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
	after    func(time.Duration) <-chan time.Time
	// dummy is a hash checked for unknown usernames, so they take as long
	// to refuse as a wrong password.
	dummy func() (string, error)
	// hashSlots holds a token for every hash running, so at most
	// HashSlots run at once: each one takes 64 MiB.
	hashSlots chan struct{}
}

// AuthDeps is what an AuthService works with.
type AuthDeps struct {
	Admins   AdminRepository
	Sessions SessionRepository
	Hasher   PasswordHasher
	// Now is the clock sessions and the back-off are timed with; nil
	// means time.Now.
	Now func() time.Time
	// After times the wait for a hash slot; nil means time.After.
	After func(time.Duration) <-chan time.Time
}

// NewAuthService builds the service.
func NewAuthService(deps AuthDeps) *AuthService {
	s := &AuthService{
		admins:    deps.Admins,
		sessions:  deps.Sessions,
		hasher:    deps.Hasher,
		now:       deps.Now,
		after:     deps.After,
		dummy:     sync.OnceValues(func() (string, error) { return deps.Hasher.Hash(rand.Text()) }),
		hashSlots: make(chan struct{}, HashSlots),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.after == nil {
		s.after = time.After
	}
	return s
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
// Admin exists, ErrBadCredentials for a wrong username or password, a
// *ThrottledError while the back-off after repeated failures runs, and
// ErrBusy when it can't start a hash within HashSlotWait. Neither of the
// last two is counted as a failure.
//
// Every attempt that gets checked counts as a failure before its password
// is checked, and a success clears the count again. So two attempts
// racing at the end of a wait can't both be checked: the second sees the
// first's failure and waits.
func (s *AuthService) SignIn(ctx context.Context, username, password string) (string, error) {
	// Refuse throttled attempts before they queue for a hash slot.
	if _, err := s.admitted(ctx); err != nil {
		return "", err
	}
	release, err := s.acquireHashSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()
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
	if s.hasher.NeedsRehash(hash) {
		// Still holding the hash slot.
		rehashed, err := s.hasher.Hash(password)
		if err != nil {
			return "", fmt.Errorf("rehash password: %w", err)
		}
		if err := s.admins.RehashPassword(ctx, hash, rehashed); err != nil {
			return "", fmt.Errorf("rehash password: %w", err)
		}
	}

	token := rand.Text()
	now := s.now().UTC()
	if err := s.sessions.CreateSession(ctx, domain.Session{TokenHash: tokenHash(token), CreatedAt: now, LastSeenAt: now}, domain.MaxSessions); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// admitted returns the Admin, or the reason a sign-in can't be checked
// now: ErrNoAdmin or a *ThrottledError.
func (s *AuthService) admitted(ctx context.Context) (domain.Admin, error) {
	admin, err := s.admins.GetAdmin(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Admin{}, ErrNoAdmin
	}
	if err != nil {
		return domain.Admin{}, err
	}
	if wait := admin.NextAttemptAt().Sub(s.now().UTC()); wait > 0 {
		return domain.Admin{}, &ThrottledError{Wait: wait}
	}
	return admin, nil
}

// acquireHashSlot waits up to HashSlotWait for one of the HashSlots and
// returns the func that frees it again.
func (s *AuthService) acquireHashSlot(ctx context.Context) (release func(), err error) {
	release = func() { <-s.hashSlots }
	select {
	case s.hashSlots <- struct{}{}:
		return release, nil
	default:
	}
	// Only a sign-in that has to queue starts the timer.
	select {
	case s.hashSlots <- struct{}{}:
		return release, nil
	case <-s.after(HashSlotWait):
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// reserveAttempt counts a sign-in attempt as a failure, unless the
// back-off refuses it, and returns the Admin it is checked against.
func (s *AuthService) reserveAttempt(ctx context.Context) (domain.Admin, error) {
	for {
		admin, err := s.admitted(ctx)
		if err != nil {
			return domain.Admin{}, err
		}
		counted, err := s.admins.RecordFailedLogin(ctx, admin.FailedLogins, s.now().UTC())
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
