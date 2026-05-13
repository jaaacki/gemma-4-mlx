// Package store persists access-log records to SQLite.
//
// Uses modernc.org/sqlite (pure-Go, no CGO) so the tailer binary cross-compiles
// cleanly on macOS without a working C toolchain.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jaaacki/gemma-4-mlx/internal/accesslog"
)

const schema = `
CREATE TABLE IF NOT EXISTS requests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  time TEXT NOT NULL,
  remote_addr TEXT,
  method TEXT,
  path TEXT,
  status INTEGER,
  bytes_sent INTEGER,
  duration_ms REAL,
  upstream_ms REAL
);
CREATE INDEX IF NOT EXISTS idx_requests_time ON requests(time);
CREATE INDEX IF NOT EXISTS idx_requests_status ON requests(status);
`

const insertSQL = `INSERT INTO requests
  (time, remote_addr, method, path, status, bytes_sent, duration_ms, upstream_ms)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

// Store wraps a *sql.DB plus the prepared insert statement. Safe for concurrent
// use by sql/database semantics, but the tailer's design is single-writer.
type Store struct {
	db   *sql.DB
	ins  *sql.Stmt
}

// Open opens or creates the SQLite database at path and ensures the schema
// exists. The caller must Close the Store.
//
// Pragmas: journal_mode=WAL (concurrent reads while the tailer writes),
// synchronous=NORMAL (durable enough for telemetry, much faster than FULL).
func Open(path string) (*Store, error) {
	// Ensure the parent directory exists. modernc.org/sqlite will happily
	// create the DB file itself, but it will NOT mkdir-p the parent — and
	// callers routinely point us at state/metrics.sqlite before state/
	// exists on a freshly-cloned checkout.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: creating sqlite dir %s: %w", dir, err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: sql.Open(%q): %w", path, err)
	}

	// Reasonable pragmas for a high-write, low-concurrency telemetry sink.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: %s: %w", pragma, err)
		}
	}

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}

	ins, err := db.Prepare(insertSQL)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: prepare insert: %w", err)
	}

	return &Store{db: db, ins: ins}, nil
}

// Insert writes a single record. Prefer InsertBatch for multiple records.
func (s *Store) Insert(r *accesslog.Record) error {
	if r == nil {
		return fmt.Errorf("store: Insert: nil record")
	}
	if _, err := s.ins.Exec(
		formatTime(r.Time),
		r.RemoteAddr,
		r.Method,
		r.Path,
		r.Status,
		r.BytesSent,
		r.DurationMs,
		r.UpstreamMs,
	); err != nil {
		return fmt.Errorf("store: insert: %w", err)
	}
	return nil
}

// InsertBatch writes records inside a single transaction. Empty input is a
// no-op. On any error the transaction is rolled back.
func (s *Store) InsertBatch(rs []*accesslog.Record) error {
	if len(rs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	stmt := tx.Stmt(s.ins)
	for i, r := range rs {
		if r == nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: InsertBatch: nil record at index %d", i)
		}
		if _, err := stmt.Exec(
			formatTime(r.Time),
			r.RemoteAddr,
			r.Method,
			r.Path,
			r.Status,
			r.BytesSent,
			r.DurationMs,
			r.UpstreamMs,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: batch exec at index %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// Close releases the prepared statement and underlying DB handle. Calling
// Close more than once is safe.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.ins != nil {
		_ = s.ins.Close()
		s.ins = nil
	}
	err := s.db.Close()
	s.db = nil
	if err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	return nil
}

// formatTime emits an RFC3339Nano string for the SQLite TEXT column, or
// empty for the zero time (so the indexed `time` column doesn't get
// garbage like "0001-01-01T00:00:00Z" for malformed entries).
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
