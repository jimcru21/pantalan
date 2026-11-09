// Package dashboard serves the Dashboard: the React SPA that package web
// embeds (ADR 0003). It serves the SPA's index.html for every page behind
// the session gate, the content-hashed assets, and the security and cache
// headers on all of it. Everything the SPA shows and does goes through
// /api/v1 (internal/httpapi), which also issues the session cookie.
package dashboard

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pantalan/pantalan/internal/httpapi"
	"github.com/pantalan/pantalan/internal/service"
)

// Sessions checks the session cookie; service.AdminService provides it.
type Sessions interface {
	// Authenticate returns service.ErrNoSession unless token names a live
	// session, and extends it.
	Authenticate(ctx context.Context, token string) error
}

type Deps struct {
	Sessions Sessions
	// Files is the built SPA, web.Dist(): index.html and assets/. Without
	// index.html every page is the "frontend not built" page.
	Files fs.FS
	Log   *slog.Logger
}

// Dashboard is the http.Handler for every path outside /api/, /webhooks/
// and /healthz. Mount it at "/" behind them.
type Dashboard struct {
	sessions Sessions
	files    fs.FS
	log      *slog.Logger
	index    []byte // index.html, or the "frontend not built" page
	handler  http.Handler
}

func New(deps Deps) *Dashboard {
	d := &Dashboard{sessions: deps.Sessions, files: deps.Files, log: deps.Log}
	if d.log == nil {
		d.log = slog.New(slog.DiscardHandler)
	}
	var err error
	if d.index, err = fs.ReadFile(deps.Files, "index.html"); err != nil {
		d.index = []byte(notBuiltPage)
	}

	mux := http.NewServeMux()
	// The sign-in screen is open to anyone; the SPA routes it.
	mux.HandleFunc("GET /login", d.loginPage)
	// Every other path is a page, but the hashed assets.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if name, ok := strings.CutPrefix(r.URL.Path, "/assets/"); ok {
			d.asset(w, r, "assets/"+name)
			return
		}
		d.page(w, r)
	})
	d.handler = securityHeaders(refuseAdminToken(mux))
	return d
}

func (d *Dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) { d.handler.ServeHTTP(w, r) }

// page serves index.html to a live session, and sends anyone else to the
// sign-in screen before any app code loads, carrying the path asked for
// in ?next= so sign-in can return there (the SPA's 401 redirect uses the
// same parameter).
func (d *Dashboard) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ok, err := d.signedIn(w, r)
	if err != nil {
		d.serverError(w, err)
		return
	}
	if !ok {
		http.Redirect(w, r, signInPath(r.URL), http.StatusSeeOther)
		return
	}
	d.serveIndex(w)
}

// loginPage serves index.html, the SPA's sign-in screen, or sends a
// signed-in Admin home.
func (d *Dashboard) loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ok, err := d.signedIn(w, r)
	if err != nil {
		d.serverError(w, err)
		return
	}
	if ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	d.serveIndex(w)
}

// signedIn reports whether r carries a live session, and clears a cookie
// that names a dead one.
func (d *Dashboard) signedIn(w http.ResponseWriter, r *http.Request) (bool, error) {
	token := httpapi.SessionToken(r)
	if token == "" {
		return false, nil
	}
	err := d.sessions.Authenticate(r.Context(), token)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, service.ErrNoSession):
		httpapi.ClearSessionCookie(w)
		return false, nil
	}
	return false, err
}

// signInPath is the sign-in screen's address, carrying u's path and query
// to return to; home needs no carrying.
func signInPath(u *url.URL) string {
	next := u.EscapedPath()
	if u.RawQuery != "" {
		next += "?" + u.RawQuery
	}
	if next == "/" {
		return "/login"
	}
	return "/login?" + url.Values{"next": {next}}.Encode()
}

func (d *Dashboard) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(d.index)
}

// asset serves one of the SPA's content-hashed files. Its name changes
// whenever its content does, so it is cached for a year as immutable. A
// missing one is a 404, not a page: a stale index.html must not get HTML
// for its script.
func (d *Dashboard) asset(w http.ResponseWriter, r *http.Request, name string) {
	f, err := d.files.Open(name)
	if err != nil || !fs.ValidPath(name) {
		notFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	content, seekable := f.(io.ReadSeeker)
	if err != nil || info.IsDir() || !seekable {
		notFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, time.Time{}, content)
}

func notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.NotFound(w, r)
}

func (d *Dashboard) serverError(w http.ResponseWriter, err error) {
	d.log.Error("dashboard: authenticate", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// notBuiltPage stands in for index.html in a binary built without the
// frontend, as `go build` without Node makes. The CSP allows no inline
// style, so it is plain HTML.
const notBuiltPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pantalan: frontend not built</title>
</head>
<body>
<h1>Dashboard frontend not built</h1>
<p>This pantalan binary was built without the Dashboard's frontend
(frontend not built). Run <code>make web</code>, then rebuild and restart
pantalan; <code>make build</code> does both.</p>
</body>
</html>
`
