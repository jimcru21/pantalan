package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// Every path that isn't /api/, /webhooks/, /healthz or a hashed asset is a
// page of the Dashboard's SPA: index.html behind the session gate, with
// /login open to anyone.

// The spec's policy, written out independently of the code under test.
const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

const immutable = "public, max-age=31536000, immutable"

func TestASignedOutPageVisitIsSentToSignInCarryingTheRequestedPath(t *testing.T) {
	s := newSeam(t)

	for path, want := range map[string]string{
		"/":                       "/login",
		"/apps":                   "/login?next=%2Fapps",
		"/apps/3/deployments/7":   "/login?next=%2Fapps%2F3%2Fdeployments%2F7",
		"/apps/3?tab=env&x=a%20b": "/login?next=%2Fapps%2F3%3Ftab%3Denv%26x%3Da%2520b",
		"/no-such-page":           "/login?next=%2Fno-such-page",
	} {
		got := s.get(path)
		if got.status != http.StatusSeeOther || got.header.Get("Location") != want {
			t.Errorf("GET %s signed out = %d Location %q, want 303 to %s", path, got.status, got.header.Get("Location"), want)
		}
		if cc := got.header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("GET %s signed out: Cache-Control = %q, want no-store", path, cc)
		}
		if strings.Contains(got.body, "/assets/") {
			t.Errorf("GET %s signed out loads the app:\n%s", path, got.body)
		}
	}
}

func TestAnExpiredSessionIsSentToSignInAndItsCookieCleared(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	s.clock.advance(2 * time.Hour) // past the idle hour

	got := s.get("/apps", withSession(session))

	if got.status != http.StatusSeeOther || got.header.Get("Location") != "/login?next=%2Fapps" {
		t.Errorf("GET /apps with an expired session = %d Location %q, want 303 to /login?next=%%2Fapps", got.status, got.header.Get("Location"))
	}
	if c := sessionCookie(t, got); c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("the expired session's cookie = %q, want it cleared", c.String())
	}
}

func TestTheSignInPageIsServedToAnyoneSignedOut(t *testing.T) {
	s := newSeam(t)

	for _, path := range []string{"/login", "/login?next=%2Fapps"} {
		got := s.get(path)
		if got.status != http.StatusOK || got.body != indexHTML {
			t.Errorf("GET %s signed out = %d, want 200 with index.html; body:\n%s", path, got.status, got.body)
		}
		if ct := got.header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("GET %s: Content-Type = %q, want text/html; charset=utf-8", path, ct)
		}
		if cc := got.header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", path, cc)
		}
	}
}

func TestASignedInVisitToSignInIsSentHome(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()

	for _, path := range []string{"/login", "/login?next=%2Fapps"} {
		got := s.get(path, withSession(session))
		if got.status != http.StatusSeeOther || got.header.Get("Location") != "/" {
			t.Errorf("GET %s signed in = %d Location %q, want 303 to /", path, got.status, got.header.Get("Location"))
		}
		if cc := got.header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("GET %s signed in: Cache-Control = %q, want no-store", path, cc)
		}
	}
}

func TestEveryPageWithASessionIsIndexHTMLAndNoStore(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()

	for _, path := range []string{"/", "/apps", "/apps/3", "/apps/3/deployments/7?follow=1", "/no-such-page"} {
		got := s.get(path, withSession(session))
		if got.status != http.StatusOK || got.body != indexHTML {
			t.Errorf("GET %s signed in = %d, want 200 with index.html; body:\n%s", path, got.status, got.body)
		}
		if ct := got.header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("GET %s: Content-Type = %q, want text/html; charset=utf-8", path, ct)
		}
		if cc := got.header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", path, cc)
		}
	}
}

func TestHashedAssetsAreServedToAnyoneAndCachedForAYear(t *testing.T) {
	s := newSeam(t)

	for path, want := range map[string]struct{ body, contentType string }{
		"/assets/index-B1x2y3z4.js":  {bundleJS, "text/javascript; charset=utf-8"},
		"/assets/index-C5d6e7f8.css": {bundleCSS, "text/css; charset=utf-8"},
	} {
		got := s.get(path) // signed out: the sign-in screen loads them too
		if got.status != http.StatusOK || got.body != want.body {
			t.Errorf("GET %s = %d %q, want 200 %q", path, got.status, got.body, want.body)
		}
		if ct := got.header.Get("Content-Type"); ct != want.contentType {
			t.Errorf("GET %s: Content-Type = %q, want %q", path, ct, want.contentType)
		}
		if cc := got.header.Get("Cache-Control"); cc != immutable {
			t.Errorf("GET %s: Cache-Control = %q, want %q", path, cc, immutable)
		}
	}
}

func TestAMissingAssetIsANoStore404RatherThanAPage(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()

	for _, path := range []string{"/assets/index-gone.js", "/assets/", "/assets", "/index.html", "/.gitkeep"} {
		got := s.get(path, withSession(session))
		if path == "/assets" || path == "/index.html" || path == "/.gitkeep" {
			// Not under /assets/: just a page, like any other path.
			if got.status != http.StatusOK || got.body != indexHTML {
				t.Errorf("GET %s = %d, want the page; body:\n%s", path, got.status, got.body)
			}
			continue
		}
		if got.status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404; body:\n%s", path, got.status, got.body)
		}
		if cc := got.header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", path, cc)
		}
	}
}

func TestEveryDashboardResponseCarriesTheCSP(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()

	for name, got := range map[string]response{
		"a page":                  s.get("/apps", withSession(session)),
		"the sign-in page":        s.get("/login"),
		"the redirect to sign in": s.get("/apps"),
		"the redirect home":       s.get("/login", withSession(session)),
		"a hashed asset":          s.get("/assets/index-B1x2y3z4.js"),
		"a missing asset":         s.get("/assets/index-gone.js"),
		"a page with the token":   s.get("/", withToken(adminToken)),
		"a page POST":             s.do(http.MethodPost, "/apps", "", withSession(session)),
	} {
		if csp := got.header.Get("Content-Security-Policy"); csp != wantCSP {
			t.Errorf("%s: CSP = %q, want %q", name, csp, wantCSP)
		}
		if got.header.Get("X-Content-Type-Options") != "nosniff" || got.header.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s: X-Content-Type-Options %q, Referrer-Policy %q, want nosniff and same-origin",
				name, got.header.Get("X-Content-Type-Options"), got.header.Get("Referrer-Policy"))
		}
	}
}

func TestDashboardPagesRefuseTheAdminToken(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()

	for _, path := range []string{"/", "/login", "/no-such-page"} {
		for name, creds := range map[string][]option{
			"the token alone":         {withToken(adminToken)},
			"the token and a session": {withToken(adminToken), withSession(session)},
		} {
			if got := s.get(path, creds...); got.status != http.StatusUnauthorized {
				t.Errorf("GET %s with %s = %d, want 401", path, name, got.status)
			}
		}
	}
}

func TestWithoutABuiltFrontendEveryPageSaysHowToBuildIt(t *testing.T) {
	// What a checkout holds before `make web`: web/dist's placeholder.
	s := newSeam(t, withFrontend(fstest.MapFS{".gitkeep": {Data: []byte("placeholder\n")}}))
	session := s.signedIn()

	for name, got := range map[string]response{
		"the sign-in page": s.get("/login"),
		"a signed-in page": s.get("/apps", withSession(session)),
		"the home page":    s.get("/", withSession(session)),
	} {
		if got.status != http.StatusOK || !strings.Contains(got.body, "frontend not built") || !strings.Contains(got.body, "make web") {
			t.Errorf("%s = %d, want 200 saying the frontend is not built and to run make web; body:\n%s", name, got.status, got.body)
		}
		if ct := got.header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q, want text/html; charset=utf-8", name, ct)
		}
		if got.header.Get("Cache-Control") != "no-store" || got.header.Get("Content-Security-Policy") != wantCSP {
			t.Errorf("%s: Cache-Control %q, CSP %q, want no-store under the CSP", name, got.header.Get("Cache-Control"), got.header.Get("Content-Security-Policy"))
		}
		if strings.Contains(got.body, "<script") || strings.Contains(got.body, "<style") || strings.Contains(got.body, "style=") {
			t.Errorf("%s: the page has inline script or style, which the CSP blocks:\n%s", name, got.body)
		}
	}
	// The gate still stands: signed out, a page is still a redirect.
	if got := s.get("/apps"); got.status != http.StatusSeeOther {
		t.Errorf("GET /apps signed out without a frontend = %d, want 303", got.status)
	}
}

func TestAPIResponsesAreNoStore(t *testing.T) {
	s := newSeam(t)

	for name, got := range map[string]response{
		"a token request":     s.get("/api/v1/apps", withToken(adminToken)),
		"a refused request":   s.get("/api/v1/apps"),
		"an unknown API path": s.get("/api/v1/no-such-thing", withToken(adminToken)),
		"the sign-in info":    s.get("/api/v1/login-info"),
		"a refused sign-in":   s.do(http.MethodPost, "/api/v1/sessions", `{"username":"alice","password":"x"}`),
		"a sign-out":          s.do(http.MethodDelete, "/api/v1/sessions/current", ""),
		"the Node overview":   s.get("/api/v1/node", withToken(adminToken)),
	} {
		if cc := got.header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, cc)
		}
	}
}

func TestTheAPIWebhooksAndHealthzAreNeverPages(t *testing.T) {
	s := newSeam(t)

	for _, path := range []string{"/api/v1/no-such-thing", "/api/", "/webhooks/github", "/webhooks/other", "/healthz"} {
		got := s.get(path)
		if got.status == http.StatusSeeOther || strings.Contains(got.body, indexHTML) {
			t.Errorf("GET %s signed out = %d Location %q, want no page and no redirect to sign in", path, got.status, got.header.Get("Location"))
		}
	}
}
