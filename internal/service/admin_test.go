package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/service"
	"github.com/pantalan/pantalan/internal/store/sqlite"
)

func TestTheCreatedPasswordSignsInAndItsSessionAuthenticates(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "Alice")

	token, err := a.svc.SignIn(t.Context(), "ALICE", pw)
	if err != nil {
		t.Fatalf("sign in with the created password: %v", err)
	}
	if err := a.svc.Authenticate(t.Context(), token); err != nil {
		t.Errorf("authenticate the new session: %v", err)
	}
}

const wrongPassword = "NOTTHEPASSWORDNOTTHEPASSW"

// signIn signs in as username and returns the error alone.
func (a *adminHarness) signIn(username, pw string) error {
	_, err := a.svc.SignIn(context.Background(), username, pw)
	return err
}

// wantBadCredentials fails the test unless err is the generic refusal.
func wantBadCredentials(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, service.ErrBadCredentials) {
		t.Fatalf("%s: sign in = %v, want ErrBadCredentials", what, err)
	}
}

// wantRetryLater fails the test unless err refuses the sign-in for reason
// with a wait of wait.
func wantRetryLater(t *testing.T, err error, reason error, wait time.Duration, what string) {
	t.Helper()
	var later *service.RetryLaterError
	if !errors.As(err, &later) || !errors.Is(err, reason) {
		t.Fatalf("%s: sign in = %v, want a RetryLaterError for %v", what, err, reason)
	}
	if later.Wait != wait {
		t.Fatalf("%s: retry after %v, want %v", what, later.Wait, wait)
	}
}

// failSignIns makes n wrong-password attempts, each refused as wrong.
func (a *adminHarness) failSignIns(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		wantBadCredentials(t, a.signIn("alice", wrongPassword), fmt.Sprintf("failure %d", i+1))
	}
}

func TestTheWaitDoublesFromOneSecondAndStopsAtFifteenMinutes(t *testing.T) {
	a := newAdminHarness(t)
	a.create(t, "alice")
	a.failSignIns(t, 5)

	// The wait after the 5th, 6th, ... failure: 1 s doubling, capped at
	// 15 min from the 15th failure on (2^10 s would be 17 min).
	waits := []time.Duration{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 900, 900}
	for i, w := range waits {
		wait := w * time.Second
		what := fmt.Sprintf("after failure %d", i+5)
		wantRetryLater(t, a.signIn("alice", wrongPassword), service.ErrThrottled, wait, what+": right away")
		a.clock.advance(wait - time.Millisecond)
		wantRetryLater(t, a.signIn("alice", wrongPassword), service.ErrThrottled, time.Millisecond, what+": 1 ms before the wait ends")
		a.clock.advance(time.Millisecond)
		wantBadCredentials(t, a.signIn("alice", wrongPassword), what+": when the wait ends")
	}
	wantRetryLater(t, a.signIn("alice", wrongPassword), service.ErrThrottled, 15*time.Minute, "at the cap")
}

func TestFailuresWithUnknownUsernamesCountTowardsTheSameWait(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")

	for i, name := range []string{"mallory", "root", "admin", "alice2", "x"} {
		wantBadCredentials(t, a.signIn(name, pw), fmt.Sprintf("unknown username %d", i+1))
	}

	wantRetryLater(t, a.signIn("alice", pw), service.ErrThrottled, time.Second, "the Admin after five unknown usernames")
}

func TestASuccessfulSignInClearsTheFailureCount(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")

	a.failSignIns(t, 4)
	if err := a.signIn("alice", pw); err != nil {
		t.Fatalf("sign in after 4 failures: %v", err)
	}

	// Five more are free again; only the sixth attempt waits.
	a.failSignIns(t, 5)
	wantRetryLater(t, a.signIn("alice", pw), service.ErrThrottled, time.Second, "after five failures since the success")
}

func TestAnAttemptInsideTheWaitIsRefusedUncheckedAndUncounted(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	a.failSignIns(t, 5)
	checked, _, _ := a.hasher.counts()

	// Even the right password is refused unchecked while the wait runs.
	wantRetryLater(t, a.signIn("alice", pw), service.ErrThrottled, time.Second, "right password inside the wait")
	a.clock.advance(999 * time.Millisecond)
	wantRetryLater(t, a.signIn("alice", wrongPassword), service.ErrThrottled, time.Millisecond, "wrong password inside the wait")
	if now, _, _ := a.hasher.counts(); now != checked {
		t.Errorf("%d passwords were checked inside the wait, want none", now-checked)
	}

	// Those attempts weren't counted: the wait is still 1 s after the fifth
	// failure, so the sixth failure needs only that, and waits 2 s after.
	a.clock.advance(time.Millisecond)
	wantBadCredentials(t, a.signIn("alice", wrongPassword), "first attempt after the wait")
	wantRetryLater(t, a.signIn("alice", pw), service.ErrThrottled, 2*time.Second, "start of the second wait")
	a.clock.advance(2 * time.Second)
	if err := a.signIn("alice", pw); err != nil {
		t.Fatalf("right password after the second wait: %v", err)
	}
}

func TestOfTwoAttemptsAtTheEndOfAWaitOnlyOneIsChecked(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	a.failSignIns(t, 5)
	a.clock.advance(time.Second)
	a.hasher.closeGate()

	first := make(chan error, 1)
	go func() { first <- a.signIn("alice", wrongPassword) }()
	a.hasher.waitEntered(t, 1) // counted, and being checked

	// The second sees the first counted as a failure: the sixth.
	wantRetryLater(t, a.signIn("alice", pw), service.ErrThrottled, 2*time.Second, "the attempt racing the first")
	a.hasher.letOneThrough(t)
	wantBadCredentials(t, <-first, "the first attempt")
}

func TestAQueuedAttemptIsThrottledByFailuresCountedWhileItQueued(t *testing.T) {
	a := newAdminHarness(t)
	a.create(t, "alice")
	a.failSignIns(t, 3)
	a.hasher.closeGate()
	holding := []<-chan error{a.signInAsync("alice", wrongPassword), a.signInAsync("alice", wrongPassword)}
	a.hasher.waitEntered(t, 2) // failures 4 and 5 hold both hash slots
	a.clock.advance(time.Second)

	// Both find the 1 s wait over, and queue for a hash slot.
	queued := []<-chan error{a.signInAsync("alice", wrongPassword), a.signInAsync("alice", wrongPassword)}
	a.timer.started(t)
	a.timer.started(t)
	a.hasher.letOneThrough(t)
	a.hasher.waitEntered(t, 1) // one queued attempt is counted: failure 6
	a.hasher.letOneThrough(t)  // the other then gets a slot
	// Every attempt is counted before it is checked, so letting the rest
	// finish counts nothing more.
	a.hasher.openGate()

	var throttled, refused int
	for _, c := range append(holding, queued...) {
		var later *service.RetryLaterError
		switch err := <-c; {
		case errors.Is(err, service.ErrBadCredentials):
			refused++
		case errors.As(err, &later) && errors.Is(err, service.ErrThrottled) && later.Wait == 2*time.Second:
			throttled++
		default:
			t.Errorf("sign in = %v, want ErrBadCredentials or throttled for 2s", err)
		}
	}
	if throttled != 1 || refused != 3 {
		t.Errorf("%d checked and %d throttled, want 3 and 1: the second queued attempt must see the sixth failure", refused, throttled)
	}
}

func TestTheFailureCountSurvivesARestartAndResetPasswordClearsIt(t *testing.T) {
	a := newAdminHarness(t)
	a.create(t, "alice")
	a.failSignIns(t, 5)

	a.restart()
	wantRetryLater(t, a.signIn("alice", wrongPassword), service.ErrThrottled, time.Second, "after a restart")

	pw, err := a.svc.ResetPassword(t.Context())
	if err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if err := a.signIn("alice", pw); err != nil {
		t.Fatalf("sign in right after the reset: %v", err)
	}
}

// signInAsync signs in on its own goroutine and delivers the error.
func (a *adminHarness) signInAsync(username, pw string) <-chan error {
	done := make(chan error, 1)
	go func() { done <- a.signIn(username, pw) }()
	return done
}

func TestAtMostTwoPasswordHashesRunAtOnceAndTheRestQueue(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	a.hasher.closeGate()

	first := a.signInAsync("alice", wrongPassword)
	second := a.signInAsync("mallory", pw) // unknown: checks the dummy hash
	a.hasher.waitEntered(t, 2)
	third := a.signInAsync("alice", pw)

	// The third finds both slots taken and starts its 5 s wait for one.
	if d := a.timer.started(t); d != 5*time.Second {
		t.Errorf("a queued sign-in waits %v for a hash slot, want 5s", d)
	}
	a.hasher.letOneThrough(t)
	a.hasher.waitEntered(t, 1) // the third gets the freed slot
	a.hasher.letOneThrough(t)
	a.hasher.letOneThrough(t)

	wantBadCredentials(t, <-first, "first")
	wantBadCredentials(t, <-second, "second")
	if err := <-third; err != nil {
		t.Errorf("queued sign-in with the right password: %v", err)
	}
	if _, _, peak := a.hasher.counts(); peak != 2 {
		t.Errorf("at most %d hashes ran at once, want 2", peak)
	}
}

func TestASignInThatWaitsFiveSecondsForAHashSlotIsRefusedBusyAndUncounted(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	a.hasher.closeGate()

	first := a.signInAsync("alice", wrongPassword)
	second := a.signInAsync("alice", wrongPassword)
	a.hasher.waitEntered(t, 2)
	third := a.signInAsync("alice", pw)
	a.timer.started(t)
	a.timer.expire(t)

	wantRetryLater(t, <-third, service.ErrBusy, 5*time.Second, "after 5 s without a hash slot")

	a.hasher.openGate()
	<-first
	<-second
	// The refused sign-in wasn't counted: after two more failures there
	// are four, so the next attempt is still checked, not throttled.
	a.failSignIns(t, 2)
	if err := a.signIn("alice", pw); err != nil {
		t.Errorf("right password after 4 failures: %v", err)
	}
}

func TestASignInCheckedAgainstTheOldPasswordGetsNoSessionWhenAResetLandsMeanwhile(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	a.hasher.closeGate()

	signIn := a.signInAsync("alice", pw)
	a.hasher.waitEntered(t, 1) // checking pw against the hash read before the reset
	if _, err := a.svc.ResetPassword(t.Context()); err != nil {
		t.Fatalf("reset password: %v", err)
	}
	a.hasher.openGate() // it checks again against the reset's hash

	wantBadCredentials(t, <-signIn, "the password reset while it was checked")
	if n := a.sessions(t); n != 0 {
		t.Errorf("%d sessions were started after the reset, want none", n)
	}
}

// sessions is how many sessions are stored: it lets every session expire
// and counts what pruning ends. It moves the clock a day forward.
func (a *adminHarness) sessions(t *testing.T) int {
	t.Helper()
	a.clock.advance(domain.SessionMaxAge)
	n, err := a.svc.PruneSessions(t.Context())
	if err != nil {
		t.Fatalf("prune sessions: %v", err)
	}
	return n
}

// plantHash stores hash as the Admin's password hash, as an older
// release might have left it.
func (a *adminHarness) plantHash(t *testing.T, hash string) {
	t.Helper()
	if err := a.store.ResetPassword(t.Context(), hash, a.clock.Now()); err != nil {
		t.Fatal(err)
	}
}

func (a *adminHarness) storedHash(t *testing.T) string {
	t.Helper()
	admin, err := a.store.GetAdmin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return admin.PasswordHash
}

func TestAHashWithOutdatedParametersIsReplacedAtSignIn(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	a.plantHash(t, oldParams+pw)

	if err := a.signIn("alice", pw); err != nil {
		t.Fatalf("sign in against the outdated hash: %v", err)
	}

	if got := a.storedHash(t); got != currentParams+pw {
		t.Errorf("stored hash after sign-in = %q, want it rehashed to %q", got, currentParams+pw)
	}
	if err := a.signIn("alice", pw); err != nil {
		t.Errorf("sign in after the rehash: %v", err)
	}
}

func TestAHashWithCurrentParametersIsKeptAtSignIn(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	_, hashes, _ := a.hasher.counts()

	if err := a.signIn("alice", pw); err != nil {
		t.Fatalf("sign in: %v", err)
	}

	if _, now, _ := a.hasher.counts(); now != hashes {
		t.Errorf("a sign-in against a current hash hashed %d times, want 0", now-hashes)
	}
	if got := a.storedHash(t); got != currentParams+pw {
		t.Errorf("stored hash = %q, want it kept as %q", got, currentParams+pw)
	}
}

func TestTwoSignInsThatBothRehashTheSamePasswordBothGetASession(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	a.plantHash(t, oldParams+pw)
	a.hasher.closeGate()

	first, second := a.signInAsync("alice", pw), a.signInAsync("alice", pw)
	a.hasher.waitEntered(t, 2) // both checking pw against the outdated hash
	a.hasher.openGate()

	// Whichever rehashes second finds the first's rehash, not a reset.
	for name, c := range map[string]<-chan error{"first": first, "second": second} {
		if err := <-c; err != nil {
			t.Errorf("%s sign-in with the right password: %v", name, err)
		}
	}
	if got := a.storedHash(t); got != currentParams+pw {
		t.Errorf("stored hash = %q, want it rehashed to %q", got, currentParams+pw)
	}
}

func TestARehashDoesNotUndoAResetThatLandsMeanwhile(t *testing.T) {
	a := newAdminHarness(t)
	old := a.create(t, "alice")
	a.plantHash(t, oldParams+old)
	a.hasher.closeGate()

	signIn := a.signInAsync("alice", old)
	a.hasher.waitEntered(t, 1)
	pw, err := a.svc.ResetPassword(t.Context())
	if err != nil {
		t.Fatalf("reset password: %v", err)
	}
	a.hasher.openGate() // it checks again against the reset's hash

	wantBadCredentials(t, <-signIn, "the outdated password reset while it was checked")
	if got := a.storedHash(t); got != currentParams+pw {
		t.Errorf("stored hash = %q, want the reset's %q", got, currentParams+pw)
	}
}

// signedIn signs in as alice with pw and returns the session token.
func (a *adminHarness) signedIn(t *testing.T, pw string) string {
	t.Helper()
	token, err := a.svc.SignIn(t.Context(), "alice", pw)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	return token
}

// live reports whether token still names a live session.
func (a *adminHarness) live(t *testing.T, token string) bool {
	t.Helper()
	err := a.svc.Authenticate(t.Context(), token)
	if err != nil && !errors.Is(err, service.ErrNoSession) {
		t.Fatalf("authenticate: %v", err)
	}
	return err == nil
}

func TestASessionEndsAfterAnHourIdle(t *testing.T) {
	a := newAdminHarness(t)
	token := a.signedIn(t, a.create(t, "alice"))

	a.clock.advance(59 * time.Minute)
	if !a.live(t, token) {
		t.Fatal("signed out after 59 idle minutes, want still signed in")
	}
	a.clock.advance(time.Hour)
	if a.live(t, token) {
		t.Error("still signed in after an idle hour")
	}
}

func TestRequestsExtendTheIdleTimer(t *testing.T) {
	a := newAdminHarness(t)
	token := a.signedIn(t, a.create(t, "alice"))

	// Three requests, each 50 minutes after the last: 2.5 h in all.
	for i := range 3 {
		a.clock.advance(50 * time.Minute)
		if !a.live(t, token) {
			t.Fatalf("signed out at request %d, 50 minutes after the previous one", i+1)
		}
	}
}

func TestASessionEndsAfter24HoursHoweverActive(t *testing.T) {
	a := newAdminHarness(t)
	token := a.signedIn(t, a.create(t, "alice"))

	elapsed := time.Duration(0)
	for elapsed+50*time.Minute < 24*time.Hour {
		a.clock.advance(50 * time.Minute)
		elapsed += 50 * time.Minute
		if !a.live(t, token) {
			t.Fatalf("signed out after %v of activity, want signed in until 24h", elapsed)
		}
	}
	a.clock.advance(24*time.Hour - elapsed)
	if a.live(t, token) {
		t.Error("still signed in 24 hours after signing in")
	}
}

func TestPruningEndsTheExpiredSessionsOnly(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	a.signedIn(t, pw)
	a.clock.advance(90 * time.Minute)
	live := a.signedIn(t, pw)
	a.clock.advance(30 * time.Minute) // the first is past its idle hour

	n, err := a.svc.PruneSessions(t.Context())
	if err != nil || n != 1 {
		t.Errorf("prune = %d, %v; want 1 (the idle one)", n, err)
	}
	if !a.live(t, live) {
		t.Error("pruning ended the live session")
	}
}

func TestTheTwentyFirstSignInEndsTheOldestSession(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")

	tokens := make([]string, 21)
	for i := range tokens {
		a.clock.advance(time.Second)
		tokens[i] = a.signedIn(t, pw)
	}

	if a.live(t, tokens[0]) {
		t.Error("the oldest session survived the 21st sign-in")
	}
	for i, token := range tokens[1:] {
		if !a.live(t, token) {
			t.Errorf("session %d of the newest 20 was ended", i+2)
		}
	}
}

func TestSignOutEndsTheSession(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	token, other := a.signedIn(t, pw), a.signedIn(t, pw)

	if err := a.svc.SignOut(t.Context(), token); err != nil {
		t.Fatalf("sign out: %v", err)
	}

	if a.live(t, token) {
		t.Error("the signed-out session is still live")
	}
	if !a.live(t, other) {
		t.Error("signing one session out ended another")
	}
	if err := a.svc.SignOut(t.Context(), token); err != nil {
		t.Errorf("signing out an ended session again: %v", err)
	}
	if a.live(t, "") {
		t.Error("an empty token authenticates")
	}
}

func TestResetPasswordEndsEverySessionAndOnlyTheNewPasswordSignsIn(t *testing.T) {
	a := newAdminHarness(t)
	old := a.create(t, "alice")
	token := a.signedIn(t, old)

	pw, err := a.svc.ResetPassword(t.Context())
	if err != nil {
		t.Fatalf("reset password: %v", err)
	}

	if pw == old {
		t.Fatal("the reset gave the old password")
	}
	if a.live(t, token) {
		t.Error("a session survived the reset")
	}
	wantBadCredentials(t, a.signIn("alice", old), "the old password")
	a.signedIn(t, pw)
}

func TestResetPasswordWithoutAnAdminIsNotFound(t *testing.T) {
	a := newAdminHarness(t)
	if _, err := a.svc.ResetPassword(t.Context()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("reset password with no Admin = %v, want ErrNotFound", err)
	}
}

func TestWithNoAdminNobodyCanSignIn(t *testing.T) {
	a := newAdminHarness(t)

	if exists, err := a.svc.AdminExists(t.Context()); err != nil || exists {
		t.Errorf("AdminExists before create = %v, %v; want false", exists, err)
	}
	if err := a.signIn("alice", "ANYTHINGANYTHINGANYTHING12"); !errors.Is(err, service.ErrNoAdmin) {
		t.Errorf("sign in with no Admin = %v, want ErrNoAdmin", err)
	}
	a.create(t, "alice")
	if exists, err := a.svc.AdminExists(t.Context()); err != nil || !exists {
		t.Errorf("AdminExists after create = %v, %v; want true", exists, err)
	}
}

func TestASecondCreateConflictsAndKeepsTheAdmin(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")

	if _, _, err := a.svc.Create(t.Context(), "bob"); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second create = %v, want ErrConflict", err)
	}
	if _, _, err := a.svc.Create(t.Context(), "1bob"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("create with a bad username = %v, want ErrInvalid", err)
	}
	a.signedIn(t, pw)
}

func TestAnUnknownUsernameIsCheckedLikeAWrongPassword(t *testing.T) {
	a := newAdminHarness(t)
	pw := a.create(t, "alice")
	before, _, _ := a.hasher.counts()

	wantBadCredentials(t, a.signIn("bob", pw), "unknown username with the Admin's password")
	wantBadCredentials(t, a.signIn("alice", wrongPassword), "wrong password")

	if checks, _, _ := a.hasher.counts(); checks-before != 2 {
		t.Errorf("%d password checks for two refused sign-ins, want 2 (one each)", checks-before)
	}
}

// adminHarness drives the AdminService over the real SQLite store with a
// clock, a hasher and a hash-slot timer the test controls.
type adminHarness struct {
	store  *sqlite.Store
	clock  *fakeClock
	hasher *fakeHasher
	timer  *fakeTimer
	svc    *service.AdminService
}

func newAdminHarness(t *testing.T) *adminHarness {
	t.Helper()
	a := &adminHarness{store: openStore(t), clock: newFakeClock(), hasher: newFakeHasher(), timer: newFakeTimer()}
	// Registered after the store's cleanup, so it runs first: a test that
	// fails mid-way doesn't leave checks stuck at the gate.
	t.Cleanup(a.hasher.openGate)
	a.restart()
	return a
}

// restart builds a fresh AdminService on the same store, as a restarted
// service (or a CLI run next to it) would.
func (a *adminHarness) restart() {
	a.svc = service.NewAdminService(service.AdminDeps{
		Credentials: a.store,
		Hasher:      a.hasher,
		Now:         a.clock.Now,
		After:       a.timer.after,
	})
}

// create creates the Admin and returns the generated password.
func (a *adminHarness) create(t *testing.T, username string) string {
	t.Helper()
	_, pw, err := a.svc.Create(t.Context(), username)
	if err != nil {
		t.Fatalf("create admin %q: %v", username, err)
	}
	return pw
}

// fakeClock moves only when the test says so.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// currentParams and oldParams prefix the fake hasher's hashes: it makes
// current ones and still verifies old ones, which need a rehash.
const (
	currentParams = "fake-v2$"
	oldParams     = "fake-v1$"
)

// fakeHasher stands in for Argon2id: a hash is the parameters followed by
// the password. Once closeGate is called, every Verify waits at a gate
// until the test lets it through. It counts checks and how many hashes
// ran at once.
type fakeHasher struct {
	entered  chan struct{} // a Verify reached the gate
	pass     chan struct{} // lets one gated Verify through
	opened   chan struct{} // closed: Verify doesn't wait
	openOnce sync.Once

	mu                   sync.Mutex
	gated                bool
	verifies, hashes     int
	running, peakRunning int
}

func newFakeHasher() *fakeHasher {
	return &fakeHasher{entered: make(chan struct{}, 100), pass: make(chan struct{}), opened: make(chan struct{})}
}

func (h *fakeHasher) begin(count *int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	*count++
	h.running++
	h.peakRunning = max(h.peakRunning, h.running)
}

func (h *fakeHasher) end() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running--
}

func (h *fakeHasher) Hash(pw string) (string, error) {
	h.begin(&h.hashes)
	defer h.end()
	return currentParams + pw, nil
}

func (h *fakeHasher) Verify(pw, hash string) (bool, error) {
	h.begin(&h.verifies)
	defer h.end()
	h.mu.Lock()
	gated := h.gated
	h.mu.Unlock()
	if gated {
		h.entered <- struct{}{}
		select {
		case <-h.pass:
		case <-h.opened:
		}
	}
	got, ok := fakeHashed(hash)
	if !ok {
		return false, errors.New("not a fake hash: " + hash)
	}
	return got == pw, nil
}

func (h *fakeHasher) NeedsRehash(hash string) bool { return !strings.HasPrefix(hash, currentParams) }

// fakeHashed returns the password a fake hash was made of.
func fakeHashed(hash string) (pw string, ok bool) {
	for _, params := range []string{currentParams, oldParams} {
		if pw, ok := strings.CutPrefix(hash, params); ok {
			return pw, true
		}
	}
	return "", false
}

// closeGate makes every later Verify wait for letOneThrough.
func (h *fakeHasher) closeGate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gated = true
}

// openGate lets every check through from now on.
func (h *fakeHasher) openGate() { h.openOnce.Do(func() { close(h.opened) }) }

// waitEntered waits for n checks to reach the gate.
func (h *fakeHasher) waitEntered(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-h.entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d password checks started", i, n)
		}
	}
}

// letOneThrough lets one waiting check finish.
func (h *fakeHasher) letOneThrough(t *testing.T) {
	t.Helper()
	select {
	case h.pass <- struct{}{}:
	case <-time.After(10 * time.Second):
		t.Fatal("no password check was waiting at the gate")
	}
}

// counts returns how many Verify and Hash calls ran, and the most that
// ran at once.
func (h *fakeHasher) counts() (verifies, hashes, peak int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.verifies, h.hashes, h.peakRunning
}

// fakeTimer stands in for the hash-slot timeout: the test sees each wait
// a queued sign-in starts and fires it when it's ready.
type fakeTimer struct {
	waits chan time.Duration
	fire  chan time.Time
}

func newFakeTimer() *fakeTimer {
	return &fakeTimer{waits: make(chan time.Duration, 100), fire: make(chan time.Time)}
}

func (f *fakeTimer) after(d time.Duration) <-chan time.Time {
	f.waits <- d
	return f.fire
}

// started waits for a sign-in to queue for a hash slot and returns how
// long it means to wait.
func (f *fakeTimer) started(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-f.waits:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("no sign-in queued for a hash slot")
		return 0
	}
}

// expire ends the wait of the sign-in that queued.
func (f *fakeTimer) expire(t *testing.T) {
	t.Helper()
	select {
	case f.fire <- time.Time{}:
	case <-time.After(10 * time.Second):
		t.Fatal("the queued sign-in stopped waiting before its timeout fired")
	}
}
