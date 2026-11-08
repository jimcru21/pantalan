package dashboard

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/pantalan/pantalan/internal/service"
)

// CookieName is the session cookie. The __Host- prefix makes browsers
// insist on Secure, Path=/ and no Domain, so no other host or path can
// plant or read it.
const CookieName = "__Host-pantalan_session"

// requireSession lets a request through only with a live session cookie.
// A page request without one is sent to the login page; an htmx fragment
// request gets 401 with HX-Redirect, which htmx turns into a full
// navigation to the login page.
func (d *Dashboard) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := d.deps.Auth.Authenticate(r.Context(), sessionToken(r))
		switch {
		case err == nil:
			next.ServeHTTP(w, r)
		case errors.Is(err, service.ErrNoSession):
			if sessionToken(r) != "" {
				clearSessionCookie(w)
			}
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				http.Error(w, "signed out", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		default:
			d.serverError(w, "authenticate", err)
		}
	})
}

// loginData is what login.html renders.
type loginData struct {
	Title    string
	Hostname string
	Address  string // ip:port the browser reached
	NoAdmin  bool   // replace the form with the `admin create` hint
	Error    string
	// Throttled is how long the back-off still runs, as the page says it
	// ("8 s", "15 min"); empty when not throttled.
	Throttled string
	Username  string // kept in the form after a failed attempt
}

// formatWait rounds a back-off wait up, to seconds under a minute and to
// minutes from there, so the page never promises less than the wait.
func formatWait(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(math.Ceil(d.Seconds())))
	}
	return fmt.Sprintf("%d min", int(math.Ceil(d.Minutes())))
}

func (d *Dashboard) loginData(r *http.Request) loginData {
	data := loginData{Title: "Sign in", Hostname: d.deps.Hostname}
	if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		data.Address = addr.String()
	}
	return data
}

// loginPage shows the login card, or sends a signed-in Admin on to the
// Dashboard.
func (d *Dashboard) loginPage(w http.ResponseWriter, r *http.Request) {
	if token := sessionToken(r); token != "" {
		if err := d.deps.Auth.Authenticate(r.Context(), token); err == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	exists, err := d.deps.Auth.AdminExists(r.Context())
	if err != nil {
		d.serverError(w, "check admin", err)
		return
	}
	data := d.loginData(r)
	data.NoAdmin = !exists
	d.render(w, http.StatusOK, d.views.login, "base", data)
}

// maxLoginForm bounds the login form body; it holds two short fields.
const maxLoginForm = 8 << 10

// login is the login form's POST: a 303 to the Dashboard with a new
// session cookie, or the login card again with one generic error.
func (d *Dashboard) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	token, err := d.deps.Auth.SignIn(r.Context(), username, password)
	data := d.loginData(r)
	var throttled *service.ThrottledError
	switch {
	case err == nil:
		// A browser that signs in again leaves its old session behind.
		if old := sessionToken(r); old != "" {
			if err := d.deps.Auth.SignOut(r.Context(), old); err != nil {
				d.log.Warn("sign out replaced session", "err", err)
			}
		}
		http.SetCookie(w, &http.Cookie{
			Name:     CookieName,
			Value:    token,
			Path:     "/",
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case errors.Is(err, service.ErrBadCredentials):
		data.Error = "Incorrect username or password."
		data.Username = username
		d.render(w, http.StatusUnauthorized, d.views.login, "base", data)
	case errors.As(err, &throttled):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(throttled.Wait.Seconds()))))
		data.Throttled = formatWait(throttled.Wait)
		data.Username = username
		d.render(w, http.StatusTooManyRequests, d.views.login, "base", data)
	case errors.Is(err, service.ErrNoAdmin):
		data.NoAdmin = true
		d.render(w, http.StatusForbidden, d.views.login, "base", data)
	default:
		d.serverError(w, "sign in", err)
	}
}

// logout ends the session, clears the cookie and returns to the login page.
func (d *Dashboard) logout(w http.ResponseWriter, r *http.Request) {
	if err := d.deps.Auth.SignOut(r.Context(), sessionToken(r)); err != nil {
		d.serverError(w, "sign out", err)
		return
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (d *Dashboard) serverError(w http.ResponseWriter, what string, err error) {
	d.log.Error("dashboard: "+what, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
