package main

import (
	"net/http"
	"strings"
	"testing"
)

// The back-off's rules are tested at the service seam
// (internal/service/admin_test.go); this checks the Dashboard's answer.
func TestAThrottledLoginGets429WithRetryAfterAndTheRecoveryHint(t *testing.T) {
	s := startServeAt(t, serveEnv(t), newFakeClock())
	pw := createServedAdmin(t, s, "alice")
	b := newBrowser(t, s)
	for i := range 5 {
		if got := b.login("alice", "NOTTHEPASSWORDNOTTHEPASSW"); got.StatusCode != http.StatusUnauthorized {
			t.Fatalf("failure %d: POST /login = %d, want 401", i+1, got.StatusCode)
		}
	}

	got := b.login("alice", pw)

	if got.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("POST /login after five failures = %d, want 429; body:\n%s", got.StatusCode, got.body)
	}
	if ra := got.Header.Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, want 1", ra)
	}
	hint := "Too many attempts, try again in 1 s, or run sudo pantalan admin reset-password on the server."
	if !strings.Contains(text(got.body), hint) {
		t.Errorf("login page does not say %q:\n%s", hint, text(got.body))
	}
	if len(got.Cookies()) != 0 {
		t.Errorf("a throttled attempt sets cookies %q", got.Header.Values("Set-Cookie"))
	}
	if !strings.Contains(got.body, `value="alice"`) {
		t.Errorf("the throttled form forgot the username:\n%s", got.body)
	}
}
