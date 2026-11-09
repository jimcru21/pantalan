package httpapi

import (
	"net"
	"net/http"

	"github.com/pantalan/pantalan/internal/sessioncookie"
)

// The sign-in contract the Dashboard's sign-in screen talks to. These
// routes take no credential, so they sit outside requireCredential; every
// sign-in rule is AdminService's, and only the HTTP side is here.

// LoginInfo is GET /api/v1/login-info's body: which Node the sign-in screen
// is on, and whether anyone can sign in yet.
type LoginInfo struct {
	Hostname    string `json:"hostname"`
	Address     string `json:"address"` // ip:port the browser reached
	AdminExists bool   `json:"admin_exists"`
}

// loginInfo serves LoginInfo; it needs no credential.
func (h *handler) loginInfo(w http.ResponseWriter, r *http.Request) {
	exists, err := h.deps.Sessions.AdminExists(r.Context())
	if err != nil {
		h.writeError(w, err)
		return
	}
	info := LoginInfo{Hostname: h.deps.Hostname, AdminExists: exists}
	if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		info.Address = addr.String()
	}
	h.writeJSON(w, http.StatusOK, info)
}

// SignInRequest is POST /api/v1/sessions' body.
type SignInRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// signIn starts a session and sets its cookie: 204, or why not, as
// 401 (any wrong credential, one message), 429 (throttled), 403 (no
// Admin) or 503 (no hash slot). 429 and 503 carry Retry-After.
func (h *handler) signIn(w http.ResponseWriter, r *http.Request) {
	var req SignInRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, err)
		return
	}
	token, err := h.deps.Sessions.SignIn(r.Context(), req.Username, req.Password)
	if err != nil {
		h.writeError(w, err)
		return
	}
	// Signing in again ends the session the browser's cookie named, as
	// the form login before the SPA did: the new cookie replaces it, so
	// nothing could use it again.
	if old := sessioncookie.Token(r); old != "" {
		if err := h.deps.Sessions.SignOut(r.Context(), old); err != nil {
			h.deps.Log.Warn("sign out replaced session", "err", err)
		}
	}
	sessioncookie.Set(w, token)
	w.WriteHeader(http.StatusNoContent)
}

// checkSession answers 204 while the browser's session cookie names a
// live session, and 401 once it doesn't. It is Sessions.CheckSession, so
// it doesn't count as activity: a page checking every 30 s never keeps
// its session alive, but notices within 30 s that it has ended. It reads
// the cookie alone; the Admin Token has no session.
func (h *handler) checkSession(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.Sessions.CheckSession(r.Context(), sessioncookie.Token(r)); err != nil {
		h.writeError(w, err) // a 401 once the session has ended
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// signOut ends the browser's session, if it has one, and clears its
// cookie.
func (h *handler) signOut(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.Sessions.SignOut(r.Context(), sessioncookie.Token(r)); err != nil {
		h.writeError(w, err)
		return
	}
	sessioncookie.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}
