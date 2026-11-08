package main

import (
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

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

// post submits a form the way a browser does for a same-origin form.
func (b *browser) post(path string, form url.Values) page {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, b.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

func (b *browser) login(username, password string) page {
	b.t.Helper()
	return b.post("/login", url.Values{"username": {username}, "password": {password}})
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
	if got := b.login("alice", pw); got.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /login = %d, want 303; body:\n%s", got.StatusCode, got.body)
	}
	return b
}

// setCookie returns the named cookie a response sets, or fails the test.
func setCookie(t *testing.T, p page, name string) *http.Cookie {
	t.Helper()
	for _, c := range p.Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("response sets no %s cookie; Set-Cookie: %q", name, p.Header.Values("Set-Cookie"))
	return nil
}

func TestLoggingInWithTheCreatedPasswordSetsASessionCookieAndRedirectsToTheDashboard(t *testing.T) {
	s := startServe(t, serveEnv(t))
	pw := createServedAdmin(t, s, "alice")
	b := newBrowser(t, s)

	got := b.login("Alice", pw)

	if got.StatusCode != http.StatusSeeOther || got.Header.Get("Location") != "/" {
		t.Fatalf("POST /login = %d Location %q, want 303 to /; body:\n%s", got.StatusCode, got.Header.Get("Location"), got.body)
	}
	c := setCookie(t, got, sessionCookie)
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" {
		t.Errorf("cookie = %q, want Secure; HttpOnly; SameSite=Strict; Path=/ and no Domain", c.String())
	}
	if c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Errorf("cookie = %q, want a browser-session cookie (no Max-Age or Expires)", c.String())
	}

	dash := b.get("/")
	if dash.StatusCode != http.StatusOK || !strings.Contains(dash.body, "Node overview") {
		t.Errorf("GET / after login = %d, want 200 with the Node overview; body:\n%s", dash.StatusCode, dash.body)
	}
}

// signedInStill reports whether the browser's session still opens the
// Dashboard; a signed-out browser is sent to the login page.
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

func TestASessionEndsAfterAnHourIdle(t *testing.T) {
	clock := newFakeClock()
	b := signedIn(t, startServeAt(t, serveEnv(t), clock))

	clock.advance(59 * time.Minute)
	if !b.signedInStill() {
		t.Fatal("signed out after 59 idle minutes, want still signed in")
	}
	clock.advance(time.Hour)
	if b.signedInStill() {
		t.Error("still signed in after an idle hour")
	}
}

func TestRequestsExtendTheIdleTimer(t *testing.T) {
	clock := newFakeClock()
	b := signedIn(t, startServeAt(t, serveEnv(t), clock))

	// Three requests, each 50 minutes after the last: 2.5 h in all.
	for i := range 3 {
		clock.advance(50 * time.Minute)
		if !b.signedInStill() {
			t.Fatalf("signed out at request %d, 50 minutes after the previous one", i+1)
		}
	}
}

func TestASessionEndsAfter24HoursHoweverActive(t *testing.T) {
	clock := newFakeClock()
	b := signedIn(t, startServeAt(t, serveEnv(t), clock))

	elapsed := time.Duration(0)
	for elapsed+50*time.Minute < 24*time.Hour {
		clock.advance(50 * time.Minute)
		elapsed += 50 * time.Minute
		if !b.signedInStill() {
			t.Fatalf("signed out after %v of activity, want signed in until 24h", elapsed)
		}
	}
	clock.advance(24*time.Hour - elapsed)
	if b.signedInStill() {
		t.Error("still signed in 24 hours after signing in")
	}
}

func TestSignOutEndsTheSessionClearsTheCookieAndReturnsToLogin(t *testing.T) {
	s := startServe(t, serveEnv(t))
	b := signedIn(t, s)
	token := b.cookie(sessionCookie)

	got := b.post("/logout", nil)

	if got.StatusCode != http.StatusSeeOther || got.Header.Get("Location") != "/login" {
		t.Errorf("POST /logout = %d Location %q, want 303 to /login", got.StatusCode, got.Header.Get("Location"))
	}
	if c := setCookie(t, got, sessionCookie); c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("logout cookie = %q, want it cleared (empty, Max-Age=0)", c.String())
	}
	// The old token is dead on the server, not just forgotten by this browser.
	replay := newBrowser(t, s)
	replay.setCookie(sessionCookie, token)
	if replay.signedInStill() {
		t.Error("the signed-out session token still opens the Dashboard")
	}
}

func TestCrossOriginLoginAndLogoutAreRejected(t *testing.T) {
	s := startServe(t, serveEnv(t))
	b := signedIn(t, s)

	for _, path := range []string{"/login", "/logout"} {
		for name, set := range map[string]func(*http.Request){
			"Sec-Fetch-Site": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
			"Origin":         func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		} {
			form := url.Values{"username": {"alice"}, "password": {"x"}}
			req, _ := http.NewRequest(http.MethodPost, b.base+path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			set(req)
			if got := b.do(req); got.StatusCode != http.StatusForbidden {
				t.Errorf("cross-origin (%s) POST %s = %d, want 403", name, path, got.StatusCode)
			}
		}
	}
	if !b.signedInStill() {
		t.Error("a rejected cross-origin logout signed the Admin out")
	}
}

func TestTheAPIAcceptsOnlyTheAdminTokenAndTheDashboardRefusesIt(t *testing.T) {
	s := startServe(t, serveEnv(t))
	b := signedIn(t, s)

	// A signed-in browser's cookie is no credential on /api/v1.
	if got := b.get("/api/v1/apps"); got.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/apps with only a session cookie = %d, want 401", got.StatusCode)
	}

	withToken := func(path string) page {
		req, _ := http.NewRequest(http.MethodGet, b.base+path, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		return b.do(req)
	}
	if got := withToken("/api/v1/apps"); got.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/apps with the Admin Token = %d %q, want 200", got.StatusCode, got.body)
	}
	tokenOnly := newBrowser(t, s)
	for _, path := range []string{"/", "/login"} {
		req, _ := http.NewRequest(http.MethodGet, tokenOnly.base+path, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		if got := tokenOnly.do(req); got.StatusCode != http.StatusUnauthorized {
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
	if got := b.login("alice", passwordLine(t, reset.stdout)); got.StatusCode != http.StatusSeeOther {
		t.Errorf("login with the new password = %d, want 303", got.StatusCode)
	}
}

// The spec's policy, written out independently of the code under test.
const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

var assetRef = regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`)

func TestPagesAreNoStoreUnderTheCSPAndLoadImmutableAssetsFromAVersionedPath(t *testing.T) {
	s := startServe(t, serveEnv(t))
	b := signedIn(t, s)
	anon := newBrowser(t, s)

	pages := map[string]page{
		"dashboard":    b.get("/"),
		"login page":   anon.get("/login"),
		"redirect":     anon.get("/"),
		"failed login": anon.login("alice", "NOTTHEPASSWORDNOTTHEPASSW"),
	}
	for name, p := range pages {
		if got := p.Header.Get("Content-Security-Policy"); got != wantCSP {
			t.Errorf("%s: CSP = %q, want %q", name, got, wantCSP)
		}
		if got := p.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, got)
		}
		// The CSP forbids inline styles and scripts, so templates carry none.
		if strings.Contains(p.body, "style=") || strings.Contains(p.body, "<style") || regexp.MustCompile(`<script(?:\s[^>]*)?>[^<]`).MatchString(p.body) {
			t.Errorf("%s: page has inline style or script:\n%s", name, p.body)
		}
	}

	dash := pages["dashboard"].body
	if !strings.Contains(dash, `"allowEval":false`) || !strings.Contains(dash, `"includeIndicatorStyles":false`) {
		t.Errorf("dashboard does not configure htmx with allowEval and includeIndicatorStyles off:\n%s", dash)
	}
	refs := assetRef.FindAllStringSubmatch(dash, -1)
	var assets []string
	for _, m := range refs {
		assets = append(assets, m[1])
	}
	for _, want := range []string{"app.css", "app.js", "htmx-2.0.11.min.js"} {
		if !slices.ContainsFunc(assets, func(a string) bool { return strings.HasSuffix(a, "/"+want) }) {
			t.Errorf("dashboard loads no %s from /static/<version>/; it loads %q", want, assets)
		}
	}
	for _, a := range assets {
		got := anon.get(a)
		if got.StatusCode != http.StatusOK || got.body == "" {
			t.Errorf("GET %s = %d with %d bytes, want 200 with content", a, got.StatusCode, len(got.body))
			continue
		}
		if cc := got.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Errorf("GET %s Cache-Control = %q, want immutable for a year", a, cc)
		}
		if got.Header.Get("Content-Security-Policy") != wantCSP {
			t.Errorf("GET %s carries no CSP", a)
		}
	}

	// Another version's path is not served stale content.
	if len(assets) > 0 {
		parts := strings.SplitN(strings.TrimPrefix(assets[0], "/static/"), "/", 2)
		if got := anon.get("/static/v0.0.0-other/" + parts[1]); got.StatusCode != http.StatusNotFound {
			t.Errorf("GET an asset under another version = %d, want 404", got.StatusCode)
		}
		if got := anon.get("/static/" + parts[0] + "/"); got.StatusCode != http.StatusNotFound {
			t.Errorf("GET the static directory = %d, want 404 (no listing)", got.StatusCode)
		}
	}
}

func TestTheDashboardShellHasTheSidebarWithNodeOverviewAndSignOut(t *testing.T) {
	b := signedIn(t, startServe(t, serveEnv(t)))

	got := b.get("/")

	for _, want := range []string{
		`aria-current="page"`, // Node overview is the active page
		"Node overview",
		`<form method="post" action="/logout">`,
		"Sign out",
		"data-collapse",
	} {
		if !strings.Contains(got.body, want) {
			t.Errorf("dashboard lacks %q:\n%s", want, got.body)
		}
	}
}

func TestWrongUsernameAndWrongPasswordGetTheSameGenericError(t *testing.T) {
	s := startServe(t, serveEnv(t))
	pw := createServedAdmin(t, s, "alice")
	b := newBrowser(t, s)

	wrongUser := b.login("bob", pw)
	wrongPassword := b.login("alice", "NOTTHEPASSWORDNOTTHEPASSW")

	const generic = "Incorrect username or password."
	for name, got := range map[string]page{"wrong username": wrongUser, "wrong password": wrongPassword} {
		if got.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: POST /login = %d, want 401", name, got.StatusCode)
		}
		if !strings.Contains(got.body, generic) {
			t.Errorf("%s: body does not show %q:\n%s", name, generic, got.body)
		}
		if len(got.Cookies()) != 0 {
			t.Errorf("%s: response sets cookies %q", name, got.Header.Values("Set-Cookie"))
		}
	}
	if got := b.get("/"); got.StatusCode != http.StatusSeeOther || got.Header.Get("Location") != "/login" {
		t.Errorf("GET / after failed logins = %d Location %q, want 303 to /login", got.StatusCode, got.Header.Get("Location"))
	}
}

func TestWithNoAdminTheLoginPageShowsTheCreateHintAndLoginFails(t *testing.T) {
	s := startServe(t, serveEnv(t))
	b := newBrowser(t, s)

	got := b.get("/login")
	if got.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", got.StatusCode)
	}
	if !strings.Contains(got.body, "sudo pantalan admin create --username NAME") {
		t.Errorf("login page does not give the admin create command:\n%s", got.body)
	}
	if strings.Contains(got.body, `name="password"`) {
		t.Errorf("login page still shows the form:\n%s", got.body)
	}

	post := b.login("alice", "ANYTHINGANYTHINGANYTHING12")
	if post.StatusCode < 400 {
		t.Errorf("POST /login with no Admin = %d, want a failure", post.StatusCode)
	}
	if len(post.Cookies()) != 0 {
		t.Errorf("POST /login with no Admin sets cookies %q", post.Header.Values("Set-Cookie"))
	}
	if !strings.Contains(post.body, "sudo pantalan admin create") {
		t.Errorf("failed POST /login does not give the admin create command:\n%s", post.body)
	}
}

func TestTheLoginPageNamesTheNodeAndPointsAtTheFingerprintAndRecovery(t *testing.T) {
	s := startServe(t, serveEnv(t))
	createServedAdmin(t, s, "alice")
	b := newBrowser(t, s)
	host, _ := os.Hostname()

	got := b.get("/login")

	for _, want := range []string{
		"Sign in to the Dashboard",
		html.EscapeString(host),
		s.addr(t, "dashboard"),
		"sudo pantalan tls fingerprint",
		"sudo pantalan admin reset-password",
		`<form class="login-form" method="post" action="/login">`,
	} {
		if !strings.Contains(got.body, want) {
			t.Errorf("login page lacks %q:\n%s", want, got.body)
		}
	}
}
