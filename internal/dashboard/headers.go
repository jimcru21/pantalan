package dashboard

import "net/http"

// contentSecurityPolicy allows only same-origin scripts, styles and
// requests: no inline script or style, no eval, no framing. Vite's build
// loads everything from files, and React's style prop writes through the
// CSSOM, which the policy allows.
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

// refuseAdminToken answers 401 to any request carrying an Authorization
// header: the Admin Token drives /api/v1 only, never the Dashboard's pages.
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
