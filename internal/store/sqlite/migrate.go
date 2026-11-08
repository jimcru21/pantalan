package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// A migration moves the schema from version-1 to version. Migrations are
// files named NNNN_description.sql, numbered from 1 with no gaps, and are
// never edited once released: a schema change is always a new file.
type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	var steps []migration
	for _, name := range names { // fs.Glob returns names sorted
		num, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %s: name must start with its number and an underscore", name)
		}
		if want := len(steps) + 1; version != want {
			return nil, fmt.Errorf("migration %s: want number %d next", name, want)
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		steps = append(steps, migration{version: version, name: path.Base(name), sql: string(body)})
	}
	return steps, nil
}

// migrate applies every migration newer than the database's user_version in
// one transaction.
func migrate(ctx context.Context, db *sql.DB, dbPath string, steps []migration) error {
	latest := len(steps)
	current, err := userVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if err := checkKnown(current, latest); err != nil || current == latest {
		return err
	}
	// A brand-new database has nothing to roll back to.
	if current > 0 {
		if err := snapshot(ctx, db, dbPath, current); err != nil {
			return fmt.Errorf("snapshot before migrating: %w", err)
		}
	}

	tx, err := db.BeginTx(ctx, nil) // BEGIN IMMEDIATE (see dsn)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	// Another process may have migrated since we looked.
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if err := checkKnown(current, latest); err != nil {
		return err
	}
	for _, step := range steps[current:] {
		if _, err := tx.ExecContext(ctx, step.sql); err != nil {
			return fmt.Errorf("apply %s: %w", step.name, err)
		}
	}
	// PRAGMA takes no bound parameters; latest is an int we computed.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", latest)); err != nil {
		return err
	}
	return tx.Commit()
}

// keepSnapshots is how many pre-migration snapshots survive pruning.
const keepSnapshots = 2

// snapshot copies the database at schema version to <dbPath>.bak-<version>
// with VACUUM INTO, then prunes all but the newest keepSnapshots. Rolling a
// release back means restoring one of these: there are no down migrations.
func snapshot(ctx context.Context, db *sql.DB, dbPath string, version int) error {
	target := fmt.Sprintf("%s.bak-%d", dbPath, version)
	// Left over from an earlier attempt that failed to migrate. VACUUM INTO
	// needs an absent or empty target; an empty one created here keeps the
	// copy as private as the database.
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		return err
	}
	return pruneSnapshots(dbPath)
}

func pruneSnapshots(dbPath string) error {
	paths, err := filepath.Glob(dbPath + ".bak-*")
	if err != nil {
		return err
	}
	type snap struct {
		path    string
		version int
	}
	var snaps []snap
	for _, p := range paths {
		v, err := strconv.Atoi(strings.TrimPrefix(p, dbPath+".bak-"))
		if err != nil {
			continue // not ours
		}
		snaps = append(snaps, snap{p, v})
	}
	slices.SortFunc(snaps, func(a, b snap) int { return cmp.Compare(b.version, a.version) })
	var errs []error
	for _, s := range snaps[min(keepSnapshots, len(snaps)):] {
		errs = append(errs, os.Remove(s.path))
	}
	return errors.Join(errs...)
}

// checkKnown refuses a schema from a newer binary: this one would not know
// what its tables mean, and migrations only run forward.
func checkKnown(current, latest int) error {
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than this pantalan knows (%d); "+
			"run a pantalan at least as new as the one that wrote it, or restore a %s.bak-* snapshot",
			current, latest, FileName)
	}
	return nil
}

func userVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}
