package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"

	"golang.org/x/crypto/argon2"
)

// oldHash is pw hashed with OWASP's minimum Argon2id parameters (19 MiB,
// t=2, p=1) instead of the spec's, as an older release might have stored.
func oldHash(pw string) string {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(pw), salt, 2, 19*1024, 1, 32)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=19456,t=2,p=1$%s$%s", b64.EncodeToString(salt), b64.EncodeToString(key))
}

func TestAHashWithOutdatedParametersIsReplacedAtLogin(t *testing.T) {
	s := startServe(t, serveEnv(t))
	pw := createServedAdmin(t, s, "alice")
	if _, err := adminDB(t, s.env).Exec(`UPDATE admin SET password_hash = ?`, oldHash(pw)); err != nil {
		t.Fatal(err)
	}

	if got := newBrowser(t, s).login("alice", pw); got.StatusCode != http.StatusSeeOther {
		t.Fatalf("login against the outdated hash = %d, want 303; body:\n%s", got.StatusCode, got.body)
	}

	if !passwordVerifies(t, s.env, pw) { // also requires m=65536,t=3,p=4
		t.Error("the rehashed password doesn't verify")
	}
	if got := newBrowser(t, s).login("alice", pw); got.StatusCode != http.StatusSeeOther {
		t.Errorf("login after the rehash = %d, want 303", got.StatusCode)
	}
}

func TestAHashWithCurrentParametersIsKeptAtLogin(t *testing.T) {
	s := startServe(t, serveEnv(t))
	pw := createServedAdmin(t, s, "alice")
	before := storedHash(t, s.env)

	if got := newBrowser(t, s).login("alice", pw); got.StatusCode != http.StatusSeeOther {
		t.Fatalf("login = %d, want 303", got.StatusCode)
	}

	if after := storedHash(t, s.env); after != before {
		t.Errorf("a current hash was replaced at login: %q -> %q", before, after)
	}
}
