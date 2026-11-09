package httpapi

import "net/http"

// The session cookie belongs to the API that issues it: POST
// /api/v1/sessions sets it, /api/v1 accepts it, DELETE
// /api/v1/sessions/current clears it, and the Dashboard's page gate reads
// it through SessionToken.

// CookieName is the session cookie. The __Host- prefix makes browsers
// insist on Secure, Path=/ and no Domain, so no other host or path can
// plant or read it.
const CookieName = "__Host-pantalan_session"

// SessionToken is the session cookie's value, or "" without one.
func SessionToken(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// sessionCookie is the session cookie with every attribute the spec
// fixes: __Host- prefix, Secure, HttpOnly, SameSite=Strict, Path=/ and no
// Domain. Without MaxAge or Expires it lasts the browser session.
func sessionCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, sessionCookie(token))
}

// ClearSessionCookie tells the browser to drop the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	c := sessionCookie("")
	c.MaxAge = -1
	http.SetCookie(w, c)
}
