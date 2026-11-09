package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const newApp = `{"name":"web","repo_url":"https://example.test/acme/web.git","domain":"web.example.test","port":8080}`

func TestTheAPIAcceptsTheAdminToken(t *testing.T) {
	s := newSeam(t)

	created := s.do(http.MethodPost, "/api/v1/apps", newApp, withToken(adminToken))
	if created.status != http.StatusCreated {
		t.Fatalf("POST /api/v1/apps with the Admin Token = %d %q, want 201", created.status, created.body)
	}
	listed := s.get("/api/v1/apps", withToken(adminToken))
	if listed.status != http.StatusOK || !strings.Contains(listed.body, `"name":"web"`) {
		t.Errorf("GET /api/v1/apps with the Admin Token = %d %q, want 200 listing the App", listed.status, listed.body)
	}
}

func TestTheAPIAcceptsALiveSessionCookie(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()

	created := s.do(http.MethodPost, "/api/v1/apps", newApp, withSession(session))
	if created.status != http.StatusCreated {
		t.Fatalf("POST /api/v1/apps with a live session cookie = %d %q, want 201", created.status, created.body)
	}
	listed := s.get("/api/v1/apps", withSession(session))
	if listed.status != http.StatusOK || !strings.Contains(listed.body, `"name":"web"`) {
		t.Errorf("GET /api/v1/apps with a live session cookie = %d %q, want 200 listing the App", listed.status, listed.body)
	}
}

func TestTheAPIRefusesAMissingOrWrongCredentialWith401(t *testing.T) {
	for name, scene := range map[string]func(s *seam) []option{
		"no credential": func(*seam) []option { return nil },
		"wrong token":   func(*seam) []option { return []option{withToken("not-the-token")} },
		"token not as a bearer": func(*seam) []option {
			return []option{withHeader("Authorization", "Basic "+adminToken)}
		},
		"unknown session": func(*seam) []option { return []option{withSession("NOSUCHSESSION")} },
		"empty session":   func(*seam) []option { return []option{withSession("")} },
		"signed-out session": func(s *seam) []option {
			session := s.signedIn()
			if err := s.admins.SignOut(s.t.Context(), session); err != nil {
				s.t.Fatal(err)
			}
			return []option{withSession(session)}
		},
		"session idle for an hour": func(s *seam) []option {
			session := s.signedIn()
			s.clock.advance(time.Hour)
			return []option{withSession(session)}
		},
		"session ended by a password reset": func(s *seam) []option {
			session := s.signedIn()
			if _, err := s.admins.ResetPassword(s.t.Context()); err != nil {
				s.t.Fatal(err)
			}
			return []option{withSession(session)}
		},
		// The token decides alone; a live cookie doesn't rescue it.
		"wrong token beside a live session": func(s *seam) []option {
			return []option{withToken("not-the-token"), withSession(s.signedIn())}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSeam(t)
			creds := scene(s)

			for _, got := range []response{
				s.get("/api/v1/apps", creds...),
				s.do(http.MethodPost, "/api/v1/apps", newApp, creds...),
			} {
				if got.status != http.StatusUnauthorized {
					t.Errorf("/api/v1/apps = %d %q, want 401", got.status, got.body)
				}
			}
			if listed := s.get("/api/v1/apps", withToken(adminToken)); strings.Contains(listed.body, `"name":"web"`) {
				t.Errorf("a refused request created the App: GET /api/v1/apps = %q", listed.body)
			}
		})
	}
}

// crossOrigin is each way a browser marks a request as coming from
// another site.
var crossOrigin = map[string]option{
	"Sec-Fetch-Site": withHeader("Sec-Fetch-Site", "cross-site"),
	"Origin":         withHeader("Origin", "https://evil.example"),
}

func TestACrossOriginCookieRequestIsRefused(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()

	for name, from := range crossOrigin {
		t.Run(name, func(t *testing.T) {
			got := s.do(http.MethodPost, "/api/v1/apps", newApp, withSession(session), from)
			if got.status != http.StatusForbidden {
				t.Errorf("cross-origin POST /api/v1/apps with a session cookie = %d %q, want 403", got.status, got.body)
			}
		})
	}
	if listed := s.get("/api/v1/apps", withSession(session)); strings.Contains(listed.body, `"name":"web"`) {
		t.Errorf("a refused cross-origin request created the App: GET /api/v1/apps = %q", listed.body)
	}
}

func TestACrossOriginTokenRequestSucceeds(t *testing.T) {
	for name, from := range crossOrigin {
		t.Run(name, func(t *testing.T) {
			s := newSeam(t)
			got := s.do(http.MethodPost, "/api/v1/apps", newApp, withToken(adminToken), from)
			if got.status != http.StatusCreated {
				t.Errorf("cross-origin POST /api/v1/apps with the Admin Token = %d %q, want 201", got.status, got.body)
			}
		})
	}
}
