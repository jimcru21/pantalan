package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/dashboard"
)

// signIn is the sign-in screen's POST /api/v1/sessions.
func (s *seam) signIn(username, password string, opts ...option) response {
	s.t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		s.t.Fatal(err)
	}
	return s.do(http.MethodPost, "/api/v1/sessions", string(body), opts...)
}

// sessionCookie is the session cookie got sets, failing the test unless
// there is exactly one.
func sessionCookie(t *testing.T, got response) *http.Cookie {
	t.Helper()
	var found []*http.Cookie
	for _, line := range got.header.Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("parse Set-Cookie %q: %v", line, err)
		}
		if c.Name == dashboard.CookieName {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("Set-Cookie = %q, want one %s cookie", got.header.Values("Set-Cookie"), dashboard.CookieName)
	}
	return found[0]
}

func TestSigningInSetsTheSessionCookie(t *testing.T) {
	s := newSeam(t)
	password := s.adminPassword()

	got := s.signIn("alice", password)
	if got.status != http.StatusNoContent {
		t.Fatalf("POST /api/v1/sessions with the right password = %d %q, want 204", got.status, got.body)
	}
	c := sessionCookie(t, got)
	if c.Name != "__Host-pantalan_session" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" {
		t.Errorf("session cookie = %q, want __Host- prefix, Secure, HttpOnly, SameSite=Strict, Path=/ and no Domain", c.String())
	}
	if c.Value == "" || c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Errorf("session cookie = %q, want a token that lasts the browser session", c.String())
	}
	if listed := s.get("/api/v1/apps", withSession(c.Value)); listed.status != http.StatusOK {
		t.Errorf("GET /api/v1/apps with the new cookie = %d %q, want 200", listed.status, listed.body)
	}
}

func TestSigningInAgainEndsTheSessionTheBrowserHad(t *testing.T) {
	s := newSeam(t)
	password := s.adminPassword()
	old := sessionCookie(t, s.signIn("alice", password)).Value

	got := s.signIn("alice", password, withSession(old))
	if got.status != http.StatusNoContent {
		t.Fatalf("signing in again = %d %q, want 204", got.status, got.body)
	}
	if listed := s.get("/api/v1/apps", withSession(old)); listed.status != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/apps with the replaced cookie = %d, want 401", listed.status)
	}
	if listed := s.get("/api/v1/apps", withSession(sessionCookie(t, got).Value)); listed.status != http.StatusOK {
		t.Errorf("GET /api/v1/apps with the new cookie = %d, want 200", listed.status)
	}
}

func TestAWrongUsernameAndAWrongPasswordGetTheSame401(t *testing.T) {
	s := newSeam(t)
	password := s.adminPassword()

	wrongPassword := s.signIn("alice", "not-the-password")
	wrongUsername := s.signIn("bob", password)
	for name, got := range map[string]response{"wrong password": wrongPassword, "wrong username": wrongUsername} {
		if got.status != http.StatusUnauthorized {
			t.Errorf("%s: POST /api/v1/sessions = %d %q, want 401", name, got.status, got.body)
		}
		if got.header.Get("Set-Cookie") != "" {
			t.Errorf("%s: Set-Cookie = %q, want none", name, got.header.Get("Set-Cookie"))
		}
	}
	if wrongPassword.body != wrongUsername.body || wrongPassword.header.Get("Content-Type") != wrongUsername.header.Get("Content-Type") {
		t.Errorf("wrong password body %q, wrong username body %q, want them identical", wrongPassword.body, wrongUsername.body)
	}
	if want := "{\"error\":\"incorrect username or password\"}\n"; wrongPassword.body != want {
		t.Errorf("401 body = %q, want %q", wrongPassword.body, want)
	}
}

func TestAMalformedSignInIsA400(t *testing.T) {
	s := newSeam(t)
	s.adminPassword()

	for _, body := range []string{`not json`, `{"username":"alice","password":"x","extra":1}`} {
		if got := s.do(http.MethodPost, "/api/v1/sessions", body); got.status != http.StatusBadRequest {
			t.Errorf("POST /api/v1/sessions %s = %d %q, want 400", body, got.status, got.body)
		}
	}
}

func TestSigningInBeforeAnAdminExistsIsA403(t *testing.T) {
	s := newSeam(t)

	got := s.signIn("alice", "anything")
	if got.status != http.StatusForbidden || !strings.Contains(got.body, "no Admin") {
		t.Errorf("POST /api/v1/sessions with no Admin = %d %q, want 403 saying there is no Admin", got.status, got.body)
	}
}

func TestAThrottledSignInIsA429WithRetryAfterBeforeThePasswordIsChecked(t *testing.T) {
	s := newSeam(t)
	password := s.adminPassword()
	for range 5 { // the free failures
		if got := s.signIn("alice", "not-the-password"); got.status != http.StatusUnauthorized {
			t.Fatalf("failed sign-in = %d %q, want 401", got.status, got.body)
		}
	}

	// Even the right password is refused unchecked while the wait runs.
	got := s.signIn("alice", password)
	if got.status != http.StatusTooManyRequests || got.header.Get("Retry-After") != "1" {
		t.Fatalf("sign-in after 5 failures = %d Retry-After %q %q, want 429 Retry-After 1", got.status, got.header.Get("Retry-After"), got.body)
	}
	if got.header.Get("Set-Cookie") != "" {
		t.Errorf("throttled sign-in Set-Cookie = %q, want none", got.header.Get("Set-Cookie"))
	}

	// Once the wait is over, the next failure doubles it.
	s.clock.advance(time.Second)
	if got := s.signIn("alice", "not-the-password"); got.status != http.StatusUnauthorized {
		t.Fatalf("failed sign-in once the wait is over = %d %q, want 401", got.status, got.body)
	}
	s.clock.advance(500 * time.Millisecond)
	if got := s.signIn("alice", password); got.status != http.StatusTooManyRequests || got.header.Get("Retry-After") != "2" {
		t.Errorf("sign-in 0.5 s into a 2 s wait = %d Retry-After %q, want 429 Retry-After 2 (rounded up)", got.status, got.header.Get("Retry-After"))
	}

	s.clock.advance(1500 * time.Millisecond)
	if got := s.signIn("alice", password); got.status != http.StatusNoContent {
		t.Errorf("the right password once the wait is over = %d %q, want 204", got.status, got.body)
	}
}

func TestASignInThatGetsNoHashSlotIsA503WithRetryAfter5(t *testing.T) {
	s := newSeam(t)
	password := s.adminPassword()

	// Two sign-ins take both hash slots and keep them.
	waiting, release := s.hasher.hold()
	defer release()
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, _ = s.admins.SignIn(t.Context(), "alice", password) })
	}
	defer wg.Wait()
	<-waiting
	<-waiting
	s.hashWait.expire() // the 5 s wait for a slot runs out

	got := s.signIn("alice", password)
	if got.status != http.StatusServiceUnavailable || got.header.Get("Retry-After") != "5" {
		t.Errorf("sign-in with both hash slots busy = %d Retry-After %q %q, want 503 Retry-After 5", got.status, got.header.Get("Retry-After"), got.body)
	}
	if got.header.Get("Set-Cookie") != "" {
		t.Errorf("busy sign-in Set-Cookie = %q, want none", got.header.Get("Set-Cookie"))
	}
	release()
	wg.Wait()
	if got.body == s.signIn("alice", "not-the-password").body {
		t.Errorf("busy sign-in body %q is the wrong-credential body, want a different message", got.body)
	}
}

func TestLoginInfoNeedsNoCredential(t *testing.T) {
	for name, adminExists := range map[string]bool{"before the Admin is created": false, "once the Admin exists": true} {
		t.Run(name, func(t *testing.T) {
			s := newSeam(t)
			if adminExists {
				s.adminPassword()
			}

			got := s.get("/api/v1/login-info")
			if got.status != http.StatusOK || got.header.Get("Content-Type") != "application/json" {
				t.Fatalf("GET /api/v1/login-info = %d %q %q, want 200 JSON", got.status, got.header.Get("Content-Type"), got.body)
			}
			// The address is the ip:port the client dialled.
			addr := strings.TrimPrefix(s.srv.URL, "https://")
			want := fmt.Sprintf(`{"hostname":"node","address":%q,"admin_exists":%t}`+"\n", addr, adminExists)
			if got.body != want {
				t.Errorf("GET /api/v1/login-info = %q, want %q", got.body, want)
			}
		})
	}
}

func TestSigningOutEndsTheSessionAndClearsTheCookie(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()

	got := s.do(http.MethodDelete, "/api/v1/sessions/current", "", withSession(session))
	if got.status != http.StatusNoContent {
		t.Fatalf("DELETE /api/v1/sessions/current = %d %q, want 204", got.status, got.body)
	}
	if c := sessionCookie(t, got); c.Value != "" || c.MaxAge >= 0 || !c.Secure || c.Path != "/" {
		t.Errorf("Set-Cookie = %q, want the session cookie cleared", c.String())
	}
	if listed := s.get("/api/v1/apps", withSession(session)); listed.status != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/apps with the signed-out cookie = %d, want 401", listed.status)
	}
}

func TestSigningOutWithoutALiveSessionStillClearsTheCookie(t *testing.T) {
	s := newSeam(t)

	for name, creds := range map[string][]option{
		"no cookie":       nil,
		"unknown session": {withSession("NOSUCHSESSION")},
	} {
		got := s.do(http.MethodDelete, "/api/v1/sessions/current", "", creds...)
		if got.status != http.StatusNoContent {
			t.Errorf("%s: DELETE /api/v1/sessions/current = %d %q, want 204", name, got.status, got.body)
			continue
		}
		if c := sessionCookie(t, got); c.MaxAge >= 0 {
			t.Errorf("%s: Set-Cookie = %q, want the session cookie cleared", name, c.String())
		}
	}
}

func TestTheSessionEndpointsRefuseCrossOriginRequests(t *testing.T) {
	for name, from := range crossOrigin {
		t.Run(name, func(t *testing.T) {
			s := newSeam(t)
			password := s.adminPassword()
			session := sessionCookie(t, s.signIn("alice", password)).Value

			if got := s.signIn("alice", password, from); got.status != http.StatusForbidden || got.header.Get("Set-Cookie") != "" {
				t.Errorf("cross-origin POST /api/v1/sessions = %d Set-Cookie %q, want 403 and no cookie", got.status, got.header.Get("Set-Cookie"))
			}
			if got := s.do(http.MethodDelete, "/api/v1/sessions/current", "", withSession(session), from); got.status != http.StatusForbidden {
				t.Errorf("cross-origin DELETE /api/v1/sessions/current = %d %q, want 403", got.status, got.body)
			}
			if listed := s.get("/api/v1/apps", withSession(session)); listed.status != http.StatusOK {
				t.Errorf("GET /api/v1/apps after a refused cross-origin sign-out = %d, want 200: the session must live on", listed.status)
			}
		})
	}
}
