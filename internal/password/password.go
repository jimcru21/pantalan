// Package password hashes the Admin's password with Argon2id and encodes
// the result as a PHC string:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>
//
// with the salt and key in unpadded standard base64. No pepper is used.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

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

// Verify reports whether password matches phc, a hash made by Hash. It uses
// the parameters recorded in phc, so hashes made with older parameters
// still verify. A hash it can't parse is an error, not a mismatch.
func (Hasher) Verify(password, phc string) (bool, error) {
	h, err := parse(phc)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), h.salt, h.iterations, h.memory, h.threads, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(got, h.key) == 1, nil
}

// NeedsRehash reports whether phc was made with other parameters than
// Hash uses now (including one it can't parse), so a password that
// verifies against it should be hashed again.
func (Hasher) NeedsRehash(phc string) bool {
	h, err := parse(phc)
	return err != nil || h.memory != Memory || h.iterations != Time || h.threads != Threads ||
		len(h.salt) != SaltLen || len(h.key) != KeyLen
}

// hash is a parsed PHC string.
type hash struct {
	memory     uint32
	iterations uint32
	threads    uint8
	salt, key  []byte
}

func parse(phc string) (hash, error) {
	var (
		h       hash
		version int
	)
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return hash{}, errors.New("password hash is not an argon2id PHC string")
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return hash{}, fmt.Errorf("password hash has unsupported argon2 version %q", parts[2])
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &h.memory, &h.iterations, &h.threads); err != nil || h.iterations == 0 || h.threads == 0 {
		return hash{}, fmt.Errorf("password hash has malformed parameters %q", parts[3])
	}
	var err error
	if h.salt, err = b64.DecodeString(parts[4]); err != nil {
		return hash{}, fmt.Errorf("password hash salt: %w", err)
	}
	if h.key, err = b64.DecodeString(parts[5]); err != nil || len(h.key) == 0 {
		return hash{}, errors.New("password hash key is malformed")
	}
	return h, nil
}
