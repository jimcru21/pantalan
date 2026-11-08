// Package sqlite is the persistent store: the app and deployment
// repositories backed by one SQLite file, $PANTALAN_DATA_DIR/pantalan.db
// (ADR 0001). Every process that opens the file migrates it first.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite" // registers the "sqlite" database/sql driver
	sqlite3 "modernc.org/sqlite/lib"
)

// FileName is the database's name inside the data directory.
const FileName = "pantalan.db"

//go:embed migrations/*.sql
var embedded embed.FS

// Store implements service.AppRepository and service.DeploymentRepository.
type Store struct {
	db *sql.DB
}

// Open opens the database in dataDir, creating the directory (0750) and the
// file (0600) when missing, and applies pending migrations.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	migrations, err := fs.Sub(embedded, "migrations")
	if err != nil {
		return nil, err
	}
	return open(ctx, dataDir, migrations)
}

func open(ctx context.Context, dataDir string, migrations fs.FS) (*Store, error) {
	steps, err := loadMigrations(migrations)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, FileName)
	// SQLite creates a missing file with 0644 less the umask; create it
	// first so it starts private. The -wal and -shm files copy its mode.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	if err := retryBusy(ctx, func() error { return migrate(ctx, db, path, steps) }); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// How long, and how often, open retries a migration that met SQLITE_BUSY.
const (
	busyRetryFor   = 15 * time.Second
	busyRetryEvery = 25 * time.Millisecond
)

// retryBusy runs f until it doesn't fail with SQLITE_BUSY, for up to
// busyRetryFor. Two processes opening a brand-new file at once (the CLI's
// `admin create` and serve's first start) race to switch it to WAL, which
// needs an exclusive lock, and the loser gets SQLITE_BUSY at once: SQLite
// skips the busy handler there to avoid a deadlock. Once the winner has
// migrated, the retry finds the schema current.
func retryBusy(ctx context.Context, f func() error) error {
	deadline := time.Now().Add(busyRetryFor)
	for {
		err := f()
		if !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-time.After(busyRetryEvery):
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
	}
}

func isBusy(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&0xff == sqlite3.SQLITE_BUSY
}

// dsn sets the pragmas on every connection the pool opens. _txlock=immediate
// makes each transaction take the write lock when it begins, so two
// processes writing at once wait out busy_timeout instead of failing with
// SQLITE_BUSY on a read-to-write upgrade.
func dsn(path string) string {
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
		"foreign_keys(ON)",
	} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
