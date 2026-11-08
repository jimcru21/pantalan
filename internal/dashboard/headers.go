package dashboard

import (
	"io"
	"io/fs"
	"net/http"
	"time"
)

// contentSecurityPolicy allows only same-origin scripts, styles and
// requests: no inline script or style, no eval (htmx runs with
// allowEval=false), no framing.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// noStore keeps pages, fragments and redirects out of every cache: they
// depend on the session.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// refuseAdminToken answers 401 to any request carrying an Authorization
// header: the Admin Token drives /api/v1 only, never the Dashboard.
func refuseAdminToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "the Admin Token is accepted only on /api/v1; sign in to use the Dashboard", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// assetBase is the URL prefix of the static files.
func (d *Dashboard) assetBase() string { return "/static/" + d.deps.AssetVersion + "/" }

// static serves web/static under /static/<version>/. The version changes
// whenever the files can, so they are cached for a year as immutable; a
// request for another version is a 404 rather than stale content.
func (d *Dashboard) static() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if r.PathValue("version") != d.deps.AssetVersion || !fs.ValidPath(name) {
			notFound(w, r)
			return
		}
		f, err := d.deps.Static.Open(name)
		if err != nil {
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
	})
}

func notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.NotFound(w, r)
}
