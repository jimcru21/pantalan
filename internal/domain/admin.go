package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Admin is the single person account that signs in to the Dashboard. Its
// username is fixed once created.
type Admin struct {
	Username     string
	PasswordHash string // PHC string
	// FailedLogins counts sign-in failures since the last success or
	// password reset; LastFailedAt is zero when there are none.
	FailedLogins      int
	LastFailedAt      time.Time
	CreatedAt         time.Time
	PasswordChangedAt time.Time
}

// MaxUsernameLen is the longest username allowed.
const MaxUsernameLen = 32

// NormalizeUsername lowercases a username and checks it: 1 to 32 characters
// of a-z, 0-9, '.', '_' and '-', starting with a letter. The error says
// what is wrong.
//
// Characters are checked before lowercasing, so a non-ASCII letter that
// Unicode lowercases to an ASCII one (such as the Kelvin sign) is refused.
func NormalizeUsername(name string) (string, error) {
	for _, r := range name {
		if !isUsernameRune(r) {
			// Name a non-ASCII rune by code point: a lookalike printed
			// alone would read as the letter it imitates.
			bad := fmt.Sprintf("%q", r)
			if r > unicode.MaxASCII {
				bad = fmt.Sprintf("%#U", r)
			}
			return "", invalid("username may contain only a-z, 0-9, '.', '_' and '-', not " + bad)
		}
	}
	name = strings.ToLower(name)
	switch {
	case name == "":
		return "", invalid("username is required")
	case len(name) > MaxUsernameLen:
		return "", invalid(fmt.Sprintf("username must be at most %d characters", MaxUsernameLen))
	case name[0] < 'a' || name[0] > 'z':
		return "", invalid("username must start with a letter")
	}
	return name, nil
}

func isUsernameRune(r rune) bool {
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '.' || r == '_' || r == '-'
}
