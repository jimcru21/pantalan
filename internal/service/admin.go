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
	// ErrThrottled is the reason of a RetryLaterError that refuses a
	// sign-in too soon after too many failures.
	ErrThrottled = errors.New("too many failed sign-ins")
	// ErrBusy is the reason of a RetryLaterError that refuses a sign-in
	// that waited too long without getting to hash its password.
	ErrBusy = errors.New("too many sign-ins at once")
)

// RetryLaterError refuses a sign-in without checking its password or
// counting it as a failure. Reason is ErrThrottled or ErrBusy, which
// errors.Is sees through it.
type RetryLaterError struct {
	Reason error
	Wait   time.Duration // until another attempt may get through
}

func (e *RetryLaterError) Error() string {
	return fmt.Sprintf("%v: try again in %v", e.Reason, e.Wait)
}

func (e *RetryLaterError) Unwrap() error { return e.Reason }

// At most hashSlots password hashes run at once (each takes 64 MiB); a
// sign-in that can't start one within hashSlotWait is refused with ErrBusy.
const (
	hashSlots    = 2
	hashSlotWait = 5 * time.Second
)

// touchEvery is how stale last_seen_at may get before a request rewrites
// it, so a burst of requests (polls) costs one write a minute at most.
const touchEvery = time.Minute

// AdminService owns every rule about the Admin: creating them and
// resetting their password (the `pantalan admin` CLI), and signing them
// in and out of the Dashboard and checking their sessions. Passwords are
// always generated (crypto/rand.Text, 26 base32 characters, 130 bits),
// never chosen.
//
// It keeps nothing about the Admin in memory: every check reads the
// store, so a password reset by the CLI takes effect on the very next
// request (ADR 0001).
type AdminService struct {
	creds  Credentials
	hasher PasswordHasher
	now    func() time.Time
	after  func(time.Duration) <-chan time.Time
	// dummy is a hash checked for unknown usernames, so they take as long
	// to refuse as a wrong password.
	dummy func() (string, error)
	// hashSlots holds a token for every sign-in hashing now.
	hashSlots chan struct{}
}

// AdminDeps is what an AdminService works with.
type AdminDeps struct {
	Credentials Credentials
	Hasher      PasswordHasher
	// Now is the clock sessions and the back-off are timed with; nil
	// means time.Now.
	Now func() time.Time
	// After times the wait for a hash slot; nil means time.After.
	After func(time.Duration) <-chan time.Time
}

// NewAdminService builds the service.
func NewAdminService(deps AdminDeps) *AdminService {
	s := &AdminService{
		creds:     deps.Credentials,
		hasher:    deps.Hasher,
		now:       deps.Now,
		after:     deps.After,
		dummy:     sync.OnceValues(func() (string, error) { return deps.Hasher.Hash(rand.Text()) }),
		hashSlots: make(chan struct{}, hashSlots),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.after == nil {
		s.after = time.After
	}
	return s
}

// Create creates the Admin and returns its generated password. It fails
// with domain.ErrInvalid for a bad username and domain.ErrConflict when an
// Admin already exists.
func (s *AdminService) Create(ctx context.Context, username string) (domain.Admin, string, error) {
	username, err := domain.NormalizeUsername(username)
	if err != nil {
		return domain.Admin{}, "", err
	}
	password, hash, err := s.generate()
	if err != nil {
		return domain.Admin{}, "", err
	}
	now := s.now().UTC()
	admin := domain.Admin{Username: username, PasswordHash: hash, CreatedAt: now, PasswordChangedAt: now}
	if err := s.creds.CreateAdmin(ctx, admin); err != nil {
		return domain.Admin{}, "", fmt.Errorf("create admin: %w", err)
	}
	return admin, password, nil
}

// ResetPassword gives the Admin a new generated password, signs out every
// session and clears the failure count, all at once, and returns the
// password. It fails with domain.ErrNotFound when there is no Admin.
func (s *AdminService) ResetPassword(ctx context.Context) (string, error) {
	password, hash, err := s.generate()
	if err != nil {
		return "", err
	}
	if err := s.creds.ResetPassword(ctx, hash, s.now().UTC()); err != nil {
		return "", fmt.Errorf("reset admin password: %w", err)
	}
	return password, nil
}

func (s *AdminService) generate() (password, hash string, err error) {
	password = rand.Text()
	hash, err = s.hasher.Hash(password)
	if err != nil {
		return "", "", fmt.Errorf("hash password: %w", err)
	}
	return password, hash, nil
}

// AdminExists reports whether there is an Admin to sign in as.
func (s *AdminService) AdminExists(ctx context.Context) (bool, error) {
	_, err := s.creds.GetAdmin(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// SignIn checks the credentials and starts a new session, returning its
// token for the browser's cookie. It fails with ErrNoAdmin before any
// Admin exists, ErrBadCredentials for a wrong username or password, and a
// *RetryLaterError, not counted as a failure, while the back-off after
// repeated failures runs (ErrThrottled) or when it can't start a hash
// within hashSlotWait (ErrBusy).
//
// Every attempt that gets checked counts as a failure before its password
// is checked, and a success clears the count again. So two attempts
// racing at the end of a wait can't both be checked: the second sees the
// first's failure and waits.
func (s *AdminService) SignIn(ctx context.Context, username, password string) (string, error) {
	// Refuse throttled attempts before they queue for a hash slot.
	admin, err := s.creds.GetAdmin(ctx)
	if err == nil {
		err = s.admit(admin)
	}
	if err != nil {
		return "", s.signInError(err)
	}
	release, err := s.acquireHashSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	admin, err = s.creds.ReserveAttempt(ctx, s.now().UTC(), s.admit)
	if err != nil {
		return "", s.signInError(err)
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
	var rehashed string
	if s.hasher.NeedsRehash(hash) {
		// Still holding the hash slot.
		if rehashed, err = s.hasher.Hash(password); err != nil {
			return "", fmt.Errorf("rehash password: %w", err)
		}
	}

	return s.startSession(ctx, password, hash, rehashed)
}

// startSession starts a session for a sign-in whose password verified
// against hash, still holding its hash slot. It does so only while the
// password is unchanged: a reset that committed after the hash was read
// takes effect on this sign-in too (ADR 0001).
func (s *AdminService) startSession(ctx context.Context, password, hash, rehashed string) (string, error) {
	token := rand.Text()
	now := s.now().UTC()
	sess := domain.Session{TokenHash: tokenHash(token), CreatedAt: now, LastSeenAt: now}
	err := s.creds.StartSession(ctx, hash, rehashed, sess, domain.MaxSessions)
	if errors.Is(err, domain.ErrConflict) {
		// The hash changed since it was read: a reset, or another
		// sign-in's rehash of the same password.
		if hash, err = s.stillVerifies(ctx, password); err == nil {
			err = s.creds.StartSession(ctx, hash, "", sess, domain.MaxSessions)
		}
	}
	if errors.Is(err, domain.ErrConflict) {
		return "", ErrBadCredentials // changed again: give up
	}
	if err != nil {
		return "", fmt.Errorf("start session: %w", err)
	}
	return token, nil
}

// stillVerifies checks password against the Admin's hash stored now and
// returns that hash. Only after a rehash does it still verify: a reset
// generates a new password. It fails with ErrBadCredentials otherwise.
func (s *AdminService) stillVerifies(ctx context.Context, password string) (string, error) {
	admin, err := s.creds.GetAdmin(ctx)
	if err != nil {
		return "", s.signInError(err)
	}
	ok, err := s.hasher.Verify(password, admin.PasswordHash)
	if err != nil {
		return "", fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return "", ErrBadCredentials
	}
	return admin.PasswordHash, nil
}

// admit refuses a sign-in against admin while the back-off runs.
func (s *AdminService) admit(admin domain.Admin) error {
	if wait := admin.NextAttemptAt().Sub(s.now().UTC()); wait > 0 {
		return &RetryLaterError{Reason: ErrThrottled, Wait: wait}
	}
	return nil
}

// signInError turns a missing Admin into ErrNoAdmin.
func (s *AdminService) signInError(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return ErrNoAdmin
	}
	return err
}

// acquireHashSlot waits up to hashSlotWait for one of the hashSlots and
// returns the func that frees it again.
func (s *AdminService) acquireHashSlot(ctx context.Context) (release func(), err error) {
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
	case <-s.after(hashSlotWait):
		return nil, &RetryLaterError{Reason: ErrBusy, Wait: hashSlotWait}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Authenticate checks that token names a live session and counts the
// request as activity. It fails with ErrNoSession otherwise.
func (s *AdminService) Authenticate(ctx context.Context, token string) error {
	sess, now, err := s.liveSession(ctx, token)
	if err != nil {
		return err
	}
	if now.Sub(sess.LastSeenAt) >= touchEvery {
		if err := s.creds.TouchSession(ctx, sess.TokenHash, now); err != nil {
			return err
		}
	}
	return nil
}

// CheckSession is Authenticate without counting as activity: an open
// stream re-checks its session with it, and a stream must not keep its
// session alive. It fails with ErrNoSession once the session has ended.
func (s *AdminService) CheckSession(ctx context.Context, token string) error {
	_, _, err := s.liveSession(ctx, token)
	return err
}

// liveSession returns the live session token names and the time it was
// checked at, ending it if it has just expired.
func (s *AdminService) liveSession(ctx context.Context, token string) (domain.Session, time.Time, error) {
	if token == "" {
		return domain.Session{}, time.Time{}, ErrNoSession
	}
	hash := tokenHash(token)
	sess, err := s.creds.GetSession(ctx, hash)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Session{}, time.Time{}, ErrNoSession
	}
	if err != nil {
		return domain.Session{}, time.Time{}, err
	}
	now := s.now().UTC()
	if sess.ExpiredAt(now) {
		if err := s.creds.EndSession(ctx, hash); err != nil {
			return domain.Session{}, time.Time{}, err
		}
		return domain.Session{}, time.Time{}, ErrNoSession
	}
	sess.TokenHash = hash
	return sess, now, nil
}

// PruneSessions ends every session that has expired by now and returns
// how many there were.
func (s *AdminService) PruneSessions(ctx context.Context) (int, error) {
	now := s.now().UTC()
	return s.creds.EndExpiredSessions(ctx, now.Add(-domain.SessionIdle), now.Add(-domain.SessionMaxAge))
}

// SignOut ends the session token names, if there is one.
func (s *AdminService) SignOut(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.creds.EndSession(ctx, tokenHash(token))
}

// tokenHash is what the store keeps of a session token: hex SHA-256.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
