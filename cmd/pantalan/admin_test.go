package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// adminEnv is the environment the admin commands need: just a data dir.
func adminEnv(t *testing.T) []string {
	return []string{"PANTALAN_DATA_DIR=" + t.TempDir()}
}

// generatedPassword is what crypto/rand.Text prints: 26 base32 characters.
var generatedPassword = regexp.MustCompile(`^[A-Z2-7]{26}$`)

// createAdmin runs `admin create` and returns the printed password.
func createAdmin(t *testing.T, env []string, username string) string {
	t.Helper()
	got := runCLI(t, t.Context(), env, "admin", "create", "--username", username)
	if got.code != 0 {
		t.Fatalf("admin create exit code = %d, want 0 (stderr %q)", got.code, got.stderr)
	}
	return passwordLine(t, got.stdout)
}

// passwordLine checks that stdout is exactly one generated password.
func passwordLine(t *testing.T, stdout string) string {
	t.Helper()
	pw, ok := strings.CutSuffix(stdout, "\n")
	if !ok || !generatedPassword.MatchString(pw) {
		t.Fatalf("stdout = %q, want one line holding a 26-character base32 password", stdout)
	}
	return pw
}

func TestAdminCreateOnAnEmptyDatabasePrintsAGeneratedPassword(t *testing.T) {
	env := adminEnv(t)

	createAdmin(t, env, "Alice")

	got := runCLI(t, t.Context(), env, "admin", "show")
	if got.code != 0 || got.stdout != "alice\n" {
		t.Errorf("admin show = %d %q (stderr %q), want 0 %q", got.code, got.stdout, got.stderr, "alice\n")
	}
}

func TestTheAdminPasswordIsStoredAsAnArgon2idPHCString(t *testing.T) {
	env := adminEnv(t)
	pw := createAdmin(t, env, "alice")

	// passwordVerifies fails the test unless the hash is
	// $argon2id$v=19$m=65536,t=3,p=4$<16-byte salt>$<32-byte key>.
	if !passwordVerifies(t, env, pw) {
		t.Errorf("printed password does not verify against the stored hash %q", storedHash(t, env))
	}
	if passwordVerifies(t, env, strings.ToLower(pw)) {
		t.Error("a different password verifies")
	}
	if strings.Contains(storedHash(t, env), pw) {
		t.Error("the stored hash contains the password")
	}
}

// stdinTrap fails the test if anything reads it.
type stdinTrap struct{ t *testing.T }

func (r stdinTrap) Read([]byte) (int, error) {
	r.t.Error("the command read stdin")
	return 0, io.EOF
}

func TestAdminCreateAndResetPasswordReadNothingFromStdin(t *testing.T) {
	env := adminEnv(t)
	for _, args := range [][]string{
		{"admin", "create", "--username", "alice"},
		{"admin", "reset-password"},
	} {
		var stdout, stderr strings.Builder
		code := run(t.Context(), args, env, Stdio{In: stdinTrap{t}, Out: &stdout, Err: &stderr}, testNode(t))
		if code != 0 {
			t.Fatalf("%v exit code = %d, want 0 (stderr %q)", args, code, stderr.String())
		}
		passwordLine(t, stdout.String())
	}
}

func TestAdminShowWithoutAnAdminExits3(t *testing.T) {
	got := runCLI(t, t.Context(), adminEnv(t), "admin", "show")

	if got.code != 3 {
		t.Errorf("admin show exit code = %d, want 3 (stderr %q)", got.code, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty", got.stdout)
	}
	if !strings.Contains(got.stderr, "admin create") {
		t.Errorf("stderr = %q, want the admin create hint", got.stderr)
	}
}

// The installer asks `admin show` before it changes the host, so show must
// leave the database as it found it: no migration, no snapshot.
func TestAdminShowReadsAnOlderSchemaWithoutMigratingIt(t *testing.T) {
	env := adminEnv(t)
	createAdmin(t, env, "alice")
	db := filepath.Join(dataDir(t, env), "pantalan.db")
	older := newestMigration(t) - 1
	setUserVersion(t, db, older) // as if a release behind wrote it

	got := runCLI(t, t.Context(), env, "admin", "show")

	if got.code != 0 || got.stdout != "alice\n" {
		t.Errorf("admin show = %d %q (stderr %q), want 0 %q", got.code, got.stdout, got.stderr, "alice\n")
	}
	if v := userVersion(t, db); v != older {
		t.Errorf("user_version after admin show = %d, want it left at %d", v, older)
	}
	if snaps, _ := filepath.Glob(db + ".bak-*"); len(snaps) != 0 {
		t.Errorf("admin show snapshotted the database: %v", snaps)
	}
	if left, _ := filepath.Glob(filepath.Join(dataDir(t, env), "*")); len(left) != 1 {
		t.Errorf("after admin show the data dir holds %q, want only pantalan.db", left)
	}
}

func TestAdminShowWithoutADatabaseExits3AndCreatesNothing(t *testing.T) {
	env := adminEnv(t)

	got := runCLI(t, t.Context(), env, "admin", "show")

	if got.code != 3 {
		t.Errorf("admin show exit code = %d, want 3 (stderr %q)", got.code, got.stderr)
	}
	if entries, _ := os.ReadDir(dataDir(t, env)); len(entries) != 0 {
		t.Errorf("admin show created %d files in the data dir", len(entries))
	}
}

func TestAdminShowRefusesADatabaseNewerThanItKnows(t *testing.T) {
	env := adminEnv(t)
	createAdmin(t, env, "alice")
	setUserVersion(t, filepath.Join(dataDir(t, env), "pantalan.db"), newestMigration(t)+1)

	got := runCLI(t, t.Context(), env, "admin", "show")

	if got.code != 1 || !strings.Contains(got.stderr, "is newer than this pantalan knows") {
		t.Errorf("admin show = %d (stderr %q), want 1 and the newer-schema refusal", got.code, got.stderr)
	}
}

func TestASecondAdminCreateFailsWithoutChangingTheAdmin(t *testing.T) {
	env := adminEnv(t)
	pw := createAdmin(t, env, "alice")

	got := runCLI(t, t.Context(), env, "admin", "create", "--username", "bob")

	if got.code != 1 {
		t.Errorf("second admin create exit code = %d, want 1", got.code)
	}
	if got.stdout != "" {
		t.Errorf("second admin create stdout = %q, want no password", got.stdout)
	}
	if !strings.Contains(got.stderr, "already exists") {
		t.Errorf("stderr = %q, want it to say an Admin already exists", got.stderr)
	}
	if show := runCLI(t, t.Context(), env, "admin", "show"); show.stdout != "alice\n" {
		t.Errorf("admin show after the refusal = %q, want %q", show.stdout, "alice\n")
	}
	if !passwordVerifies(t, env, pw) {
		t.Error("the first password no longer verifies after the refused create")
	}
}

// adminDB opens env's database directly. Until the Dashboard can sign in,
// it is the only place to see the stored hash, sessions and failure count.
func adminDB(t *testing.T, env []string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir(t, env), "pantalan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// storedHash is the Admin's password_hash column.
func storedHash(t *testing.T, env []string) string {
	t.Helper()
	var hash string
	if err := adminDB(t, env).QueryRow(`SELECT password_hash FROM admin`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return hash
}

// phc is the spec's PHC string for Argon2id with 64 MiB, t=3, p=4, and
// unpadded base64 salt and key.
var phc = regexp.MustCompile(`^\$argon2id\$v=19\$m=65536,t=3,p=4\$([A-Za-z0-9+/]+)\$([A-Za-z0-9+/]+)$`)

// passwordVerifies recomputes the stored Argon2id hash for pw with
// x/crypto's argon2 directly, independent of pantalan's hasher.
func passwordVerifies(t *testing.T, env []string, pw string) bool {
	t.Helper()
	hash := storedHash(t, env)
	m := phc.FindStringSubmatch(hash)
	if m == nil {
		t.Fatalf("stored hash %q is not an Argon2id PHC string with m=65536,t=3,p=4", hash)
	}
	salt, err := base64.RawStdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatalf("salt in %q: %v", hash, err)
	}
	key, err := base64.RawStdEncoding.DecodeString(m[2])
	if err != nil {
		t.Fatalf("key in %q: %v", hash, err)
	}
	if len(salt) != 16 || len(key) != 32 {
		t.Fatalf("stored hash has a %d-byte salt and %d-byte key, want 16 and 32", len(salt), len(key))
	}
	return subtle.ConstantTimeCompare(argon2.IDKey([]byte(pw), salt, 3, 64*1024, 4, 32), key) == 1
}

func TestAdminCreateCheckValidatesWithoutTouchingTheDataDir(t *testing.T) {
	// A data dir that doesn't exist: as root, opening it would be refused,
	// and a non-root open would create it.
	missing := filepath.Join(t.TempDir(), "absent")
	env := []string{"PANTALAN_DATA_DIR=" + missing}

	for _, name := range []string{"a", "alice", "Alice", "ops-1", "j.doe_2", strings.Repeat("x", 32)} {
		got := runCLI(t, t.Context(), env, "admin", "create", "--username", name, "--check")
		if got.code != 0 || got.stdout != "" {
			t.Errorf("--check %q = %d %q (stderr %q), want 0 and no output", name, got.code, got.stdout, got.stderr)
		}
	}
	for name, reason := range map[string]string{
		"":                      "required",
		strings.Repeat("x", 33): "at most 32",
		"1alice":                "start with a letter",
		"-alice":                "start with a letter",
		"al ice":                `' '`,
		"alice@example":         `'@'`,
		"émile":                 "U+00E9",
		"Kelvin":                "U+212A", // the Kelvin sign, which Unicode lowercases to k
	} {
		got := runCLI(t, t.Context(), env, "admin", "create", "--username", name, "--check")
		if got.code != 1 {
			t.Errorf("--check %q exit code = %d, want 1", name, got.code)
		}
		if !strings.Contains(got.stderr, reason) {
			t.Errorf("--check %q stderr = %q, want a reason mentioning %s", name, got.stderr, reason)
		}
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("admin create --check created the data directory")
	}
}

// signInState plants what Dashboard sign-ins would leave behind: two
// sessions and a run of failed attempts.
func signInState(t *testing.T, env []string) {
	t.Helper()
	db := adminDB(t, env)
	for _, stmt := range []string{
		`INSERT INTO sessions (token_hash, created_at, last_seen_at) VALUES
			('aa', '2026-11-08T10:00:00.000000000Z', '2026-11-08T10:00:00.000000000Z'),
			('bb', '2026-11-08T11:00:00.000000000Z', '2026-11-08T11:00:00.000000000Z')`,
		`UPDATE admin SET failed_logins = 7, last_failed_at = '2026-11-08T12:00:00.000000000Z'`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

// signInCounts returns the number of sessions and the failure count.
func signInCounts(t *testing.T, env []string) (sessions, failures int) {
	t.Helper()
	db := adminDB(t, env)
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT failed_logins FROM admin`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	return sessions, failures
}

func TestAdminResetPasswordReplacesThePasswordAndSignsEverythingOut(t *testing.T) {
	env := adminEnv(t)
	old := createAdmin(t, env, "alice")
	signInState(t, env)

	got := runCLI(t, t.Context(), env, "admin", "reset-password")

	if got.code != 0 {
		t.Fatalf("reset-password exit code = %d, want 0 (stderr %q)", got.code, got.stderr)
	}
	pw := passwordLine(t, got.stdout)
	if pw == old {
		t.Fatal("reset-password printed the old password")
	}
	if passwordVerifies(t, env, old) {
		t.Error("the old password still verifies")
	}
	if !passwordVerifies(t, env, pw) {
		t.Error("the printed password does not verify")
	}
	if sessions, failures := signInCounts(t, env); sessions != 0 || failures != 0 {
		t.Errorf("after reset: %d sessions and %d failures, want 0 and 0", sessions, failures)
	}
	var lastFailed sql.NullString
	if err := adminDB(t, env).QueryRow(`SELECT last_failed_at FROM admin`).Scan(&lastFailed); err != nil || lastFailed.Valid {
		t.Errorf("last_failed_at after reset = %v (%v), want NULL", lastFailed, err)
	}
	if show := runCLI(t, t.Context(), env, "admin", "show"); show.stdout != "alice\n" {
		t.Errorf("admin show after reset = %q, want the same Admin", show.stdout)
	}
}

func TestAdminResetPasswordChangesNothingWhenAnyPartFails(t *testing.T) {
	env := adminEnv(t)
	old := createAdmin(t, env, "alice")
	signInState(t, env)
	// Make signing the sessions out fail, after the hash could have changed.
	if _, err := adminDB(t, env).Exec(`CREATE TRIGGER keep_sessions BEFORE DELETE ON sessions
		BEGIN SELECT RAISE(ABORT, 'sessions are pinned'); END`); err != nil {
		t.Fatal(err)
	}

	got := runCLI(t, t.Context(), env, "admin", "reset-password")

	if got.code != 1 || got.stdout != "" {
		t.Errorf("reset-password = %d %q, want 1 and no password", got.code, got.stdout)
	}
	if !passwordVerifies(t, env, old) {
		t.Error("the old password stopped verifying although the reset failed")
	}
	if sessions, failures := signInCounts(t, env); sessions != 2 || failures != 7 {
		t.Errorf("after a failed reset: %d sessions and %d failures, want them untouched at 2 and 7", sessions, failures)
	}
}

func TestAdminResetPasswordWithoutAnAdminExits3(t *testing.T) {
	got := runCLI(t, t.Context(), adminEnv(t), "admin", "reset-password")
	if got.code != 3 || got.stdout != "" {
		t.Errorf("reset-password = %d %q (stderr %q), want 3 and no password", got.code, got.stdout, got.stderr)
	}
}

func TestAdminCreateWithoutAUsernamePrintsUsage(t *testing.T) {
	got := runCLI(t, t.Context(), adminEnv(t), "admin", "create")
	if got.code != 2 || !strings.Contains(got.stderr, "Usage: pantalan admin") {
		t.Errorf("admin create = %d (stderr %q), want 2 and the admin usage", got.code, got.stderr)
	}
}
