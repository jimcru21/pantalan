// Package password hashes the Admin's password with Argon2id and encodes
// the result as a PHC string:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>
//
// with the salt and key in unpadded standard base64. No pepper is used.
package password

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// Parameters every new hash is made with.
const (
	Memory  = 64 * 1024 // KiB, so 64 MiB
	Time    = 3
	Threads = 4
	SaltLen = 16
	KeyLen  = 32
)

var b64 = base64.RawStdEncoding

// Hasher implements service.PasswordHasher.
type Hasher struct{}

// Hash returns the PHC string of password under a fresh random salt.
func (Hasher) Hash(password string) (string, error) {
	salt := make([]byte, SaltLen)
	rand.Read(salt) // never returns an error; crashes the program if it can't read
	key := argon2.IDKey([]byte(password), salt, Time, Memory, Threads, KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, Memory, Time, Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}
