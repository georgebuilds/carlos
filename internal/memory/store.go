package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store is the memory subsystem's database handle. It holds a *sql.DB
// (either opened by this package or handed in by the supervisor, which
// shares its event-log DB) plus an `owned` flag that controls whether
// Close shuts the DB down.
//
// Concurrency: all methods are safe for concurrent use because *sql.DB
// is. The summaries / user_model schema has no foreign keys to the
// supervisor's tables, so writes here don't need to coordinate with
// the supervisor's single-writer discipline on `events`.
type Store struct {
	db    *sql.DB
	owned bool // true iff Close should close db (we opened it ourselves)
}

// OpenStore opens (creating if needed) a SQLite database at dbPath and
// initializes the memory schema. The parent directory is created at
// mode 0700 to match the rest of ~/.carlos.
//
// Use this from the CLI (`carlos memory search`). In-process callers
// that already hold a *sql.DB (the supervisor's event log) should use
// NewStore to share the handle.
func OpenStore(dbPath string) (*Store, error) {
	if dbPath == "" {
		return nil, errors.New("memory: empty db path")
	}
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("memory: mkdir %s: %w", dir, err)
		}
		_ = os.Chmod(dir, 0o700)
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
		dbPath,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("memory: open: %w", err)
	}
	// Conservative pool: same shape as eventlog_sqlite.go.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	if err := applySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, owned: true}, nil
}

// NewStore wraps an already-open *sql.DB (typically the supervisor's
// event-log handle) and applies the memory schema. The caller retains
// ownership of the DB; Close on the returned Store is a no-op for the
// underlying handle.
//
// Schema apply is idempotent - calling NewStore on a DB where the
// memory tables already exist is fine.
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("memory: nil db")
	}
	if err := applySchema(db); err != nil {
		return nil, err
	}
	return &Store{db: db, owned: false}, nil
}

// Close releases the underlying *sql.DB if (and only if) this Store
// opened it. Stores constructed via NewStore leave the handle alone -
// the caller (event log) owns its lifecycle.
func (s *Store) Close() error {
	if s == nil || !s.owned || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB exposes the underlying *sql.DB so the CLI / tests can run ad-hoc
// queries (e.g. counting rows) without going through the typed API.
// Kept package-internal-flavored: external callers should prefer the
// typed methods.
func (s *Store) DB() *sql.DB { return s.db }

// applySchema runs the memory-schema CREATE statements + WAL pragmas
// against db. Idempotent. Errors are wrapped with the offending
// statement type for diagnostics.
func applySchema(db *sql.DB) error {
	// Pragmas first so the schema CREATEs run under WAL. The DSN already
	// applies these on connections we own; for caller-supplied handles
	// this is the only time we touch them. Either way, idempotent.
	if _, err := db.Exec(walPragmasSQL); err != nil {
		return fmt.Errorf("memory: apply pragmas: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("memory: apply schema: %w", err)
	}
	// Phase F-13 migration: add summaries.frame to legacy databases.
	// CREATE TABLE IF NOT EXISTS won't touch an existing summaries table
	// that predates the frame column, so we probe + ALTER explicitly.
	if err := migrateSummariesFrame(db); err != nil {
		return fmt.Errorf("memory: migrate summaries.frame: %w", err)
	}
	if err := ensureFrameIndex(db); err != nil {
		return fmt.Errorf("memory: ensure frame index: %w", err)
	}
	return nil
}

// migrateSummariesFrame adds the `frame` column to an existing
// summaries table when it's missing. Idempotent: a second run on the
// already-migrated database is a no-op. The matching frame index is
// covered by schemaSQL's CREATE INDEX IF NOT EXISTS, so we only need
// the column add here.
func migrateSummariesFrame(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(summaries)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "frame" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE summaries ADD COLUMN frame TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS summaries_by_frame ON summaries(frame, closed_at DESC)`)
	return err
}

// ensureFrameIndex creates summaries_by_frame on fresh databases (where
// the frame column was created by schemaSQL, not the migration ALTER).
// Called from applySchema after migrateSummariesFrame so both the
// fresh-create and legacy-migrate paths end with the index present.
func ensureFrameIndex(db *sql.DB) error {
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS summaries_by_frame ON summaries(frame, closed_at DESC)`)
	return err
}

// withTx is a small helper for the few methods that mutate more than
// one row. Begins, runs fn, commits on nil error or rolls back. Not
// used by the FTS5 trigger path (single INSERT) but kept here for
// ApplyFact and any future multi-statement writes.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
