package dashboard_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/dashboard"
	"github.com/pantalan/pantalan/internal/service"
	"github.com/pantalan/pantalan/web"
)

// refusingAuth refuses every sign-in with err.
type refusingAuth struct{ err error }

func (refusingAuth) AdminExists(context.Context) (bool, error) { return true, nil }
func (a refusingAuth) SignIn(context.Context, string, string) (string, error) {
	return "", a.err
}
func (refusingAuth) Authenticate(context.Context, string) error { return service.ErrNoSession }
func (refusingAuth) SignOut(context.Context, string) error      { return nil }

func TestASignInRefusedAsBusyGets503WithItsOwnRetryAfter(t *testing.T) {
	d, err := dashboard.New(dashboard.Deps{
		Auth:         refusingAuth{&service.RetryLaterError{Reason: service.ErrBusy, Wait: 7 * time.Second}},
		Templates:    web.Templates(),
		Static:       web.Static(),
		AssetVersion: "test",
		Hostname:     "node",
		Log:          slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"username": {"alice"}, "password": {"x"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	d.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /login refused as busy = %d, want 503", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "7" {
		t.Errorf("Retry-After = %q, want 7, the refusal's own wait", ra)
	}
	if c := rec.Header().Values("Set-Cookie"); len(c) != 0 {
		t.Errorf("the 503 sets cookies %q", c)
	}
	if !strings.Contains(rec.Body.String(), "Too many sign-ins at once.") {
		t.Errorf("the login page doesn't say why:\n%s", rec.Body.String())
	}
}
