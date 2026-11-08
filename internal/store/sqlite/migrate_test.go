package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
)

// These tests open the store with stand-in migrations: the snapshot rules
// only show once a database is behind by at least one real release.

// migrationsUpTo returns migrations 1..n, each creating table tN.
func migrationsUpTo(n int) fstest.MapFS {
	fsys := fstest.MapFS{}
	for v := 1; v <= n; v++ {
		fsys[fmt.Sprintf("%04d_t%d.sql", v, v)] = &fstest.MapFile{Data: fmt.Appendf(nil, "CREATE TABLE t%d (x TEXT);", v)}
	}
	return fsys
}

func openWith(t *testing.T, dir string, n int) {
	t.Helper()
	s, err := open(t.Context(), dir, migrationsUpTo(n))
	if err != nil {
		t.Fatalf("open with %d migrations: %v", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func schemaVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func snapshots(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, FileName+".bak-*"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	slices.Sort(names)
	return names
}

func TestPendingMigrationSnapshotsTheDatabaseAtItsOldVersionFirst(t *testing.T) {
	dir := t.TempDir()
	openWith(t, dir, 1)

	openWith(t, dir, 2)

	snap := filepath.Join(dir, FileName+".bak-1")
	if _, err := os.Stat(snap); err != nil {
		t.Fatalf("no snapshot before migrating: %v (have %v)", err, snapshots(t, dir))
	}
	if info, err := os.Stat(snap); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode = %o, want 600 like the database", info.Mode().Perm())
	}
	if v := schemaVersion(t, snap); v != 1 {
		t.Errorf("snapshot user_version = %d, want 1", v)
	}
	if v := schemaVersion(t, filepath.Join(dir, FileName)); v != 2 {
		t.Errorf("database user_version = %d, want 2", v)
	}
}

func TestOnlyTheTwoNewestSnapshotsAreKept(t *testing.T) {
	dir := t.TempDir()
	// Eleven releases, so snapshot versions cross into two digits.
	for n := 1; n <= 11; n++ {
		openWith(t, dir, n)
	}

	want := []string{FileName + ".bak-10", FileName + ".bak-9"}
	if got := snapshots(t, dir); !slices.Equal(got, want) {
		t.Errorf("snapshots = %v, want %v", got, want)
	}
}

func TestAFailingMigrationLeavesTheDatabaseAsItWas(t *testing.T) {
	dir := t.TempDir()
	broken := migrationsUpTo(1)
	broken["0002_broken.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE t2 (x TEXT); NOT SQL;")}

	if _, err := open(t.Context(), dir, broken); err == nil {
		t.Fatal("open with a broken migration succeeded")
	}

	path := filepath.Join(dir, FileName)
	if v := schemaVersion(t, path); v != 0 {
		t.Errorf("user_version = %d, want 0", v)
	}
	// Migration 1 ran in the same transaction, so a fixed release starts
	// from scratch rather than tripping over a half-built schema.
	openWith(t, dir, 2)
	if v := schemaVersion(t, path); v != 2 {
		t.Errorf("user_version after the fixed release = %d, want 2", v)
	}
}

func TestUpToDateDatabaseIsNotSnapshotted(t *testing.T) {
	dir := t.TempDir()
	openWith(t, dir, 1)
	openWith(t, dir, 2)

	openWith(t, dir, 2)

	if got, want := snapshots(t, dir), []string{FileName + ".bak-1"}; !slices.Equal(got, want) {
		t.Errorf("snapshots = %v, want %v", got, want)
	}
}
