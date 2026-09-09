// Package store is the only database access layer for Companion. All table
// access goes through it — feature code must not issue ad-hoc SQL.
//
// Absolute paths (workspaces.root_path) live only
// in this local database; the record types are written so that JSON
// serialization can never leak them.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("store: not found")

// Store wraps the local SQLite database. Safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies
// any pending migrations. Migrations are versioned and forward-only; an
// unknown version already applied in the database is an error, not a rollback.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	// A single connection serializes writers; WAL keeps readers non-blocking.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("applying %s: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	sort.Strings(entries)

	var current int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("reading migration version: %w", err)
	}

	var latest int
	for _, name := range entries {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if version <= latest {
			return fmt.Errorf("migration %s: version %d out of order", name, version)
		}
		latest = version
		if version <= current {
			continue
		}
		body, err := migrationsFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("beginning migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, formatTime(time.Now())); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %s: %w", name, err)
		}
	}
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than this build (max %d)", current, latest)
	}
	return nil
}

func migrationVersion(name string) (int, error) {
	base := strings.TrimPrefix(name, "migrations/")
	prefix, _, ok := strings.Cut(base, "_")
	if !ok {
		return 0, fmt.Errorf("migration %s: name must be <version>_<description>.sql", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("migration %s: invalid version prefix %q", name, prefix)
	}
	return version, nil
}

// NewID returns an opaque identifier with the given prefix, e.g. "ws" →
// "ws_1a2b3c4d5e6f7890". Randomness comes from crypto/rand so IDs are not
// guessable across devices.
func NewID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing means the OS entropy source is broken; there
		// is no meaningful fallback for identifier generation.
		panic(fmt.Sprintf("store: reading random bytes: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// Timestamps are stored as RFC 3339 UTC strings; NULL maps to the zero time.

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func formatNullableTime(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(t), Valid: true}
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing stored timestamp %q: %w", s, err)
	}
	return t, nil
}

func parseNullableTime(s sql.NullString) (time.Time, error) {
	if !s.Valid {
		return time.Time{}, nil
	}
	return parseTime(s.String)
}
