// Package store is the SQLite persistence layer (modernc.org/sqlite, no cgo)
// with goose migrations embedded from migrations/*.sql.
//
// Ownership and conventions for the agents working in parallel:
//
//   - The full initial schema lives in migrations/00001_init.sql. If a feature
//     needs a schema change, add a NEW file named with a UTC timestamp,
//     e.g. migrations/20261124153000_quota_reason.sql (out-of-order
//     migrations are allowed, so parallel branches do not collide). Never
//     edit an applied migration.
//   - Each feature adds its own repository file in this package:
//     downloads_repo.go, packages_repo.go, segments_repo.go,
//     accounts_repo.go, egresses_repo.go, quota_repo.go, history_repo.go,
//     linkgrabber_repo.go. Methods hang off *Store and take a context.
//   - Timestamps are INTEGER unix milliseconds: use TimeToDB / TimeFromDB and
//     NullTimeToDB / NullTimeFromDB.
//   - Multi-statement writes go through Store.Tx. Never store secrets.
package store

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

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DriverName is the database/sql driver registered by modernc.org/sqlite.
const DriverName = "sqlite"

// FileName is the database file name inside config.DataDir.
const FileName = "adesc.db"

// ErrNotFound is returned (wrapped) by repository methods when a row does
// not exist.
var ErrNotFound = errors.New("store: not found")

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path, enables WAL,
// foreign keys and a busy timeout, and applies pending migrations. Use
// ":memory:" for a throwaway database (single connection).
func Open(ctx context.Context, path string) (*Store, error) {
	memory := path == ":memory:"
	if !memory {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("store: mkdir: %w", err)
		}
	}
	db, err := sql.Open(DriverName, dsn(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if memory {
		// Every connection to ":memory:" is a different database.
		db.SetMaxOpenConns(1)
	}
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// dsn builds the modernc.org/sqlite DSN. Pragmas are applied on every new
// connection; _txlock=immediate makes BEGIN take the write lock up front,
// which avoids SQLITE_BUSY upgrades in read-then-write transactions.
// The path is deliberately NOT a "file:" URI: modernc then strips the query
// and opens the path verbatim, so Windows paths and names containing '#' or
// '%' work.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if path != ":memory:" {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	q.Set("_txlock", "immediate")
	return path + "?" + q.Encode()
}

func (s *Store) migrate(ctx context.Context) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("store: migrations fs: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.db, sub,
		goose.WithDisableGlobalRegistry(true),
		goose.WithAllowOutofOrder(true),
	)
	if err != nil {
		return fmt.Errorf("store: migrations: %w", err)
	}
	// Do not call p.Close: it would close s.db.
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

// DB exposes the handle for repository files of this package and for tests.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Tx runs fn inside a transaction, committing if fn returns nil and rolling
// back otherwise (also on panic).
func (s *Store) Tx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// TimeToDB converts t to unix milliseconds (UTC).
func TimeToDB(t time.Time) int64 { return t.UnixMilli() }

// TimeFromDB converts unix milliseconds to a UTC time.
func TimeFromDB(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// NullTimeToDB maps the zero time to NULL.
func NullTimeToDB(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

// NullTimeFromDB maps NULL to the zero time.
func NullTimeFromDB(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return TimeFromDB(n.Int64)
}
