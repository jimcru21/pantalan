// Package sessioncookie is the Dashboard's session cookie: its name and
// attributes, and reading, setting and clearing it. Both HTTP adapters
// use it: internal/httpapi issues, accepts and clears it (POST, /api/v1,
// DELETE /api/v1/sessions...), and internal/dashboard's page gate reads
// it. It sits apart so neither adapter imports the other; like execx, it
// is a stdlib-only helper and imports nothing internal.
package sessioncookie

import "net/http"

// Name is the session cookie's. The __Host- prefix makes browsers insist
// on Secure, Path=/ and no Domain, so no other host or path can plant or
// read it.
const Name = "__Host-pantalan_session"

// Token is the session cookie's value, or "" without one.
func Token(r *http.Request) string {
	c, err := r.Cookie(Name)
	if err != nil {
		return ""
	}
	return c.Value
}

// Set gives the browser the session cookie for token.
func Set(w http.ResponseWriter, token string) {
	http.SetCookie(w, cookie(token))
}

// Clear tells the browser to drop the session cookie.
func Clear(w http.ResponseWriter) {
	c := cookie("")
	c.MaxAge = -1
	http.SetCookie(w, c)
}

// cookie is the session cookie with every attribute the spec fixes:
// __Host- prefix, Secure, HttpOnly, SameSite=Strict, Path=/ and no
// Domain. Without MaxAge or Expires it lasts the browser session.
func cookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     Name,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}
