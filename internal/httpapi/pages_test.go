package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

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

func TestDashboardPagesOpenWithASessionAndSendEveryoneElseToLogin(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()

	if got := s.get("/", withSession(session)); got.status != http.StatusOK || !strings.Contains(got.body, "Node overview") {
		t.Errorf("GET / signed in = %d, want 200 with the Node overview; body:\n%s", got.status, got.body)
	}
	got := s.get("/")
	if got.status != http.StatusSeeOther || got.header.Get("Location") != "/login" {
		t.Errorf("GET / signed out = %d Location %q, want 303 to /login", got.status, got.header.Get("Location"))
	}
	if got := s.get("/login"); got.status != http.StatusOK {
		t.Errorf("GET /login signed out = %d, want 200", got.status)
	}
}
