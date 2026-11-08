package domain

import "time"

// How long a Dashboard session lasts: it ends after SessionIdle without a
// request, and SessionMaxAge after sign-in whatever happens.
const (
	SessionIdle   = time.Hour
	SessionMaxAge = 24 * time.Hour
)

// Session is a signed-in Dashboard browser. Only a hash of its token is
// kept; the token itself lives in the browser's cookie.
type Session struct {
	TokenHash  string // hex SHA-256 of the token
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// ExpiredAt reports whether the session has ended by now.
func (s Session) ExpiredAt(now time.Time) bool {
	return !now.Before(s.LastSeenAt.Add(SessionIdle)) || !now.Before(s.CreatedAt.Add(SessionMaxAge))
}
