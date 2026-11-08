package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const wrongPassword = "NOTTHEPASSWORDNOTTHEPASSW"

// throttleHint is the spec's throttled message around its wait.
func throttleHint(wait string) string {
	return "Too many attempts, try again in " + wait + ", or run sudo pantalan admin reset-password on the server."
}

// wantFailed fails the test unless the attempt was checked and refused
// with the generic error.
func wantFailed(t *testing.T, got page, what string) {
	t.Helper()
	if got.StatusCode != http.StatusUnauthorized || !strings.Contains(got.body, "Incorrect username or password.") {
		t.Fatalf("%s: POST /login = %d, want 401 with the generic error; body:\n%s", what, got.StatusCode, got.body)
	}
}

// wantThrottled fails the test unless the attempt was refused by the
// back-off with a wait of wait.
func wantThrottled(t *testing.T, got page, wait, what string) {
	t.Helper()
	if got.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("%s: POST /login = %d, want 429; body:\n%s", what, got.StatusCode, got.body)
	}
	if hint := throttleHint(wait); !strings.Contains(text(got.body), hint) {
		t.Fatalf("%s: login page does not say %q:\n%s", what, hint, text(got.body))
	}
	if len(got.Cookies()) != 0 {
		t.Errorf("%s: a throttled attempt sets cookies %q", what, got.Header.Values("Set-Cookie"))
	}
}

// failLogins makes n wrong-password attempts, each refused as wrong.
func failLogins(t *testing.T, b *browser, n int) {
	t.Helper()
	for i := range n {
		wantFailed(t, b.login("alice", wrongPassword), fmt.Sprintf("failure %d", i+1))
	}
}

func TestTheWaitDoublesFromOneSecondAndStopsAtFifteenMinutes(t *testing.T) {
	clock := newFakeClock()
	s := startServeAt(t, serveEnv(t), clock)
	createServedAdmin(t, s, "alice")
	b := newBrowser(t, s)
	failLogins(t, b, 5)

	// The wait after the 5th, 6th, ... failure: 1 s doubling, capped at
	// 15 min from the 15th failure on (2^10 s would be 17 min).
	waits := []time.Duration{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 900, 900}
	for i, w := range waits {
		wait := w * time.Second
		what := fmt.Sprintf("after failure %d", i+5)
		clock.advance(wait - time.Millisecond)
		got := b.login("alice", wrongPassword)
		if got.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("%s: attempt 1 ms before the %v wait ends = %d, want 429", what, wait, got.StatusCode)
		}
		clock.advance(time.Millisecond)
		wantFailed(t, b.login("alice", wrongPassword), what+": attempt when the wait ends")
	}

	got := b.login("alice", wrongPassword)
	wantThrottled(t, got, "15 min", "at the cap")
	if ra := got.Header.Get("Retry-After"); ra != "900" {
		t.Errorf("Retry-After at the cap = %q, want 900", ra)
	}
}

func TestFailuresWithUnknownUsernamesCountTowardsTheSameWait(t *testing.T) {
	clock := newFakeClock()
	s := startServeAt(t, serveEnv(t), clock)
	pw := createServedAdmin(t, s, "alice")
	b := newBrowser(t, s)

	for i, name := range []string{"mallory", "root", "admin", "alice2", "x"} {
		wantFailed(t, b.login(name, pw), fmt.Sprintf("unknown username %d", i+1))
	}

	wantThrottled(t, b.login("alice", pw), "1 s", "the Admin after five unknown usernames")
}

func TestTheFailureCountSurvivesARestartAndResetPasswordClearsIt(t *testing.T) {
	clock := newFakeClock()
	env := serveEnv(t)
	s := startServeAt(t, env, clock)
	pw := createServedAdmin(t, s, "alice")
	failLogins(t, newBrowser(t, s), 5)
	s.stop()
	if code := s.wait(t); code != 0 {
		t.Fatalf("serve exited with %d; output:\n%s", code, s.stdout)
	}

	s = startServeAt(t, env, clock)
	b := newBrowser(t, s)
	wantThrottled(t, b.login("alice", pw), "1 s", "after a restart")

	reset := runCLI(t, t.Context(), env, "admin", "reset-password")
	if reset.code != 0 {
		t.Fatalf("admin reset-password exit code = %d (stderr %q)", reset.code, reset.stderr)
	}
	if got := b.login("alice", passwordLine(t, reset.stdout)); got.StatusCode != http.StatusSeeOther {
		t.Fatalf("login right after reset-password = %d, want 303; body:\n%s", got.StatusCode, got.body)
	}
}

func TestASuccessfulLoginClearsTheFailureCount(t *testing.T) {
	clock := newFakeClock()
	s := startServeAt(t, serveEnv(t), clock)
	pw := createServedAdmin(t, s, "alice")
	b := newBrowser(t, s)

	failLogins(t, b, 4)
	if got := b.login("alice", pw); got.StatusCode != http.StatusSeeOther {
		t.Fatalf("login after 4 failures = %d, want 303", got.StatusCode)
	}

	// Five more are free again; only the sixth attempt waits.
	failLogins(t, newBrowser(t, s), 5)
	wantThrottled(t, b.login("alice", pw), "1 s", "after five failures since the success")
}

func TestAfterFiveFailuresAnAttemptInsideTheWaitIsRefusedUncheckedAndUncounted(t *testing.T) {
	clock := newFakeClock()
	s := startServeAt(t, serveEnv(t), clock)
	pw := createServedAdmin(t, s, "alice")
	b := newBrowser(t, s)

	failLogins(t, b, 5)

	// Even the right password is refused unchecked while the wait runs.
	wantThrottled(t, b.login("alice", pw), "1 s", "right password inside the wait")
	clock.advance(999 * time.Millisecond)
	wantThrottled(t, b.login("alice", wrongPassword), "1 s", "wrong password inside the wait")

	// Those attempts weren't counted: the wait is still 1 s after the fifth
	// failure, so the sixth failure needs only that, and waits 2 s after.
	clock.advance(time.Millisecond)
	wantFailed(t, b.login("alice", wrongPassword), "first attempt after the wait")
	wantThrottled(t, b.login("alice", pw), "2 s", "start of the second wait")
	clock.advance(1999 * time.Millisecond)
	wantThrottled(t, b.login("alice", pw), "1 s", "end of the second wait")
	clock.advance(time.Millisecond)
	if got := b.login("alice", pw); got.StatusCode != http.StatusSeeOther {
		t.Fatalf("right password after the second wait = %d, want 303; body:\n%s", got.StatusCode, got.body)
	}
}
