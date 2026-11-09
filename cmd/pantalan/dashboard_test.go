package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
)

// The HTTP seam (internal/httpapi) covers every sign-in, credential and
// page rule; these check that serve wires them up end to end, with the
// real AdminService, cookie and listener.

// sessionCookie is the Dashboard's session cookie.
const sessionCookie = "__Host-pantalan_session"

// browser is an Admin's browser: an HTTPS client pinned to the Node's
// certificate, with a cookie jar, that never follows redirects.
type browser struct {
	t      *testing.T
	client *http.Client
	base   string // https://host:port
}

func newBrowser(t *testing.T, s *running) *browser {
	t.Helper()
	base := "https://" + s.addr(t, "dashboard") // also waits for the certificate
	client := pinnedClient(t, s.env)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.Jar = jar
	return &browser{t: t, client: client, base: base}
}

// page is a response with its body read.
type page struct {
	*http.Response
	body string
}

func (b *browser) do(req *http.Request) page {
	b.t.Helper()
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return page{Response: resp, body: string(body)}
}

func (b *browser) get(path string) page {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodGet, b.base+path, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.do(req)
}

// fetch makes a same-origin request with a JSON body (when it isn't
// empty) the way the SPA's fetch does.
func (b *browser) fetch(method, path, body string) page {
	b.t.Helper()
	req, err := http.NewRequest(method, b.base+path, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", b.base)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return b.do(req)
}

// cookie is the value the jar would send to the Dashboard for name.
func (b *browser) cookie(name string) string {
	b.t.Helper()
	u, _ := url.Parse(b.base)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	b.t.Fatalf("browser holds no %s cookie", name)
	return ""
}

func (b *browser) setCookie(name, value string) {
	u, _ := url.Parse(b.base)
	b.client.Jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value, Path: "/", Secure: true}})
}

// login signs in the way the sign-in screen does; 204 is success.
func (b *browser) login(username, password string) page {
	b.t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		b.t.Fatal(err)
	}
	return b.fetch(http.MethodPost, "/api/v1/sessions", string(body))
}

// createServedAdmin runs `admin create` against a running serve's data dir
// once serve is listening, and returns the password.
func createServedAdmin(t *testing.T, s *running, username string) string {
	t.Helper()
	s.addr(t, "dashboard")
	return createAdmin(t, s.env, username)
}

// signedIn signs in as a fresh Admin and returns the browser.
func signedIn(t *testing.T, s *running) *browser {
	t.Helper()
	pw := createServedAdmin(t, s, "alice")
	b := newBrowser(t, s)
	if got := b.login("alice", pw); got.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /api/v1/sessions = %d, want 204; body:\n%s", got.StatusCode, got.body)
	}
	return b
}

// signedInStill reports whether the browser's session still opens the
// Dashboard; a signed-out browser is sent to the sign-in screen.
func (b *browser) signedInStill() bool {
	b.t.Helper()
	got := b.get("/")
	switch {
	case got.StatusCode == http.StatusOK:
		return true
	case got.StatusCode == http.StatusSeeOther && got.Header.Get("Location") == "/login":
		return false
	}
	b.t.Fatalf("GET / = %d Location %q, want 200 or 303 to /login", got.StatusCode, got.Header.Get("Location"))
	return false
}

func TestSigningInWithTheCreatedPasswordOpensTheDashboardAndTheAPI(t *testing.T) {
	s := startServe(t, serveEnv(t))
	b := newBrowser(t, s)
	if b.signedInStill() {
		t.Fatal("a fresh browser opens the Dashboard")
	}
	pw := createServedAdmin(t, s, "alice")

	if got := b.login("Alice", pw); got.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /api/v1/sessions = %d, want 204; body:\n%s", got.StatusCode, got.body)
	}

	if !b.signedInStill() {
		t.Error("the new session doesn't open the Dashboard")
	}
	if got := b.get("/api/v1/apps"); got.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/apps with the session cookie = %d %q, want 200", got.StatusCode, got.body)
	}
}

func TestSignOutEndsTheSessionOnTheServer(t *testing.T) {
	s := startServe(t, serveEnv(t))
	b := signedIn(t, s)
	token := b.cookie(sessionCookie)

	if got := b.fetch(http.MethodDelete, "/api/v1/sessions/current", ""); got.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /api/v1/sessions/current = %d, want 204", got.StatusCode)
	}

	// The old token is dead on the server, not just forgotten by this browser.
	replay := newBrowser(t, s)
	replay.setCookie(sessionCookie, token)
	if replay.signedInStill() {
		t.Error("the signed-out session token still opens the Dashboard")
	}
}

func TestTheAPIAcceptsTheSessionCookieAndTheAdminTokenAndTheDashboardRefusesTheToken(t *testing.T) {
	s := startServe(t, serveEnv(t))
	b := signedIn(t, s)

	if got := newBrowser(t, s).get("/api/v1/apps"); got.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/apps with no credential = %d, want 401", got.StatusCode)
	}
	withToken := func(path string) page {
		req, _ := http.NewRequest(http.MethodGet, b.base+path, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		return newBrowser(t, s).do(req)
	}
	if got := withToken("/api/v1/apps"); got.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/apps with the Admin Token = %d %q, want 200", got.StatusCode, got.body)
	}
	for _, path := range []string{"/", "/login"} {
		if got := withToken(path); got.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s with the Admin Token = %d, want 401", path, got.StatusCode)
		}
	}
	if got := newBrowser(t, s).get("/healthz"); got.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz with no credentials = %d, want 200", got.StatusCode)
	}
}

func TestResetPasswordWhileServingSignsOutOnTheNextRequest(t *testing.T) {
	s := startServe(t, serveEnv(t))
	b := signedIn(t, s)

	reset := runCLI(t, t.Context(), s.env, "admin", "reset-password")
	if reset.code != 0 {
		t.Fatalf("admin reset-password exit code = %d (stderr %q)", reset.code, reset.stderr)
	}

	if b.signedInStill() {
		t.Error("the session survived admin reset-password")
	}
	if got := b.login("alice", passwordLine(t, reset.stdout)); got.StatusCode != http.StatusNoContent {
		t.Errorf("sign-in with the new password = %d, want 204", got.StatusCode)
	}
}

// The spec's policy, written out independently of the code under test.
const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func TestServedPagesAreNoStoreUnderTheCSP(t *testing.T) {
	s := startServe(t, serveEnv(t))
	b := signedIn(t, s)
	anon := newBrowser(t, s)

	for name, p := range map[string]page{
		"a page":            b.get("/apps"),
		"the sign-in page":  anon.get("/login"),
		"the page redirect": anon.get("/apps"),
	} {
		if got := p.Header.Get("Content-Security-Policy"); got != wantCSP {
			t.Errorf("%s: CSP = %q, want %q", name, got, wantCSP)
		}
		if got := p.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, got)
		}
	}
}

func TestTheSignInScreenNamesTheNodeItIsOn(t *testing.T) {
	s := startServe(t, serveEnv(t))
	host, _ := os.Hostname()

	got := newBrowser(t, s).get("/api/v1/login-info")

	var info struct {
		Hostname    string `json:"hostname"`
		Address     string `json:"address"`
		AdminExists bool   `json:"admin_exists"`
	}
	if err := json.Unmarshal([]byte(got.body), &info); err != nil {
		t.Fatalf("GET /api/v1/login-info = %d %q: %v", got.StatusCode, got.body, err)
	}
	if info.Hostname != host || info.Address != s.addr(t, "dashboard") || info.AdminExists {
		t.Errorf("GET /api/v1/login-info = %+v, want hostname %q, address %q and no Admin yet", info, host, s.addr(t, "dashboard"))
	}
}
