package httpapi

import (
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/pantalan/pantalan/internal/service"
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
	var later *service.RetryLaterError
	if errors.As(err, &later) {
		w.Header().Set("Retry-After", retryAfter(later.Wait))
	}
	switch {
	case err == nil:
		// A browser that signs in again leaves its old session behind.
		if old := SessionToken(r); old != "" {
			if err := h.deps.Sessions.SignOut(r.Context(), old); err != nil {
				h.deps.Log.Warn("sign out replaced session", "err", err)
			}
		}
		setSessionCookie(w, token)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, service.ErrBadCredentials):
		h.writeJSON(w, http.StatusUnauthorized, errorBody(service.ErrBadCredentials))
	case errors.Is(err, service.ErrThrottled):
		h.writeJSON(w, http.StatusTooManyRequests, errorBody(service.ErrThrottled))
	case errors.Is(err, service.ErrBusy):
		h.writeJSON(w, http.StatusServiceUnavailable, errorBody(service.ErrBusy))
	case errors.Is(err, service.ErrNoAdmin):
		h.writeJSON(w, http.StatusForbidden, errorBody(service.ErrNoAdmin))
	default:
		h.writeError(w, err)
	}
}

// signOut ends the browser's session, if it has one, and clears its
// cookie.
func (h *handler) signOut(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.Sessions.SignOut(r.Context(), SessionToken(r)); err != nil {
		h.writeError(w, err)
		return
	}
	ClearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// errorBody is the {"error": ...} body every API error has, with err's
// message alone (not any wrapping around it).
func errorBody(err error) map[string]string { return map[string]string{"error": err.Error()} }

// retryAfter is a Retry-After value: whole seconds, rounded up.
func retryAfter(wait time.Duration) string {
	return strconv.Itoa(int(math.Ceil(wait.Seconds())))
}
