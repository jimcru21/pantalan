package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pantalan/pantalan/internal/service"
)

// Sessions signs the Admin in and out and checks their sessions;
// service.AdminService provides it and keeps every rule.
type Sessions interface {
	AdminExists(ctx context.Context) (bool, error)
	// SignIn returns a new session token, or why it refused one:
	// service.ErrNoAdmin, service.ErrBadCredentials, or a
	// *service.RetryLaterError carrying its own wait whose reason is
	// service.ErrThrottled or service.ErrBusy. Any other error is the
	// server's own failure.
	SignIn(ctx context.Context, username, password string) (token string, err error)
	// Authenticate returns service.ErrNoSession unless token names a live
	// session, and extends it.
	Authenticate(ctx context.Context, token string) error
	// CheckSession is Authenticate without extending the session: an
	// open stream re-checks its session with it.
	CheckSession(ctx context.Context, token string) error
	// SignOut ends the session token names, if there is one.
	SignOut(ctx context.Context, token string) error
}

// requireCredential lets a request through with either credential:
//
//   - the Admin Token, as "Authorization: Bearer <token>", which skips the
//     cross-origin check so scripts keep working from anywhere; or
//   - a live session cookie, which only a same-origin request may carry:
//     http.CrossOriginProtection checks it before the session is looked
//     at, so a cross-site request can't even extend the session.
//
// A request with an Authorization header is judged by the token alone,
// whatever cookie it carries. Anything else gets 401.
func requireCredential(token string, sessions Sessions, log *slog.Logger, next http.Handler) http.Handler {
	sameOrigin := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			got, ok := strings.CutPrefix(auth, "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				unauthorized(w)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(CookieName)
		if err != nil || cookie.Value == "" {
			unauthorized(w)
			return
		}
		if err := sameOrigin.Check(r); err != nil {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		switch err := sessions.Authenticate(r.Context(), cookie.Value); {
		case err == nil:
			next.ServeHTTP(w, r)
		case errors.Is(err, service.ErrNoSession):
			unauthorized(w)
		default:
			log.Error("authenticate session", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	})
}

func unauthorized(w http.ResponseWriter) {
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// noStoreAPI keeps every /api/ response out of caches: each depends on
// the credential and on the Node's state now.
func noStoreAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration", time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer (Flush etc.).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
