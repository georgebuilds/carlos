package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
//
// Order matters: pragmas first (so WAL is on), then migrate any
// existing summaries table to the nullable-frame shape, then run the
// CREATE-IF-NOT-EXISTS schema (which creates summaries fresh if it
// did not already exist), then create the frame index.
func applySchema(db *sql.DB) error {
	// Pragmas first so the schema CREATEs run under WAL. The DSN already
	// applies these on connections we own; for caller-supplied handles
	// this is the only time we touch them. Either way, idempotent.
	if _, err := db.Exec(walPragmasSQL); err != nil {
		return fmt.Errorf("memory: apply pragmas: %w", err)
	}
	// Migrate an existing summaries table to the nullable-frame shape
	// BEFORE schemaSQL runs. The migration is a no-op on fresh DBs
	// (the table does not exist yet) and on already-migrated DBs (the
	// frame column is already nullable).
	if err := migrateSummariesFrame(db); err != nil {
		return fmt.Errorf("memory: migrate summaries.frame: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("memory: apply schema: %w", err)
	}
	if err := ensureFrameIndex(db); err != nil {
		return fmt.Errorf("memory: ensure frame index: %w", err)
	}
	return nil
}

// summariesFrameState describes the on-disk shape of summaries.frame
// that the migration must reconcile. Three legitimate states:
//
//   - frameStateAbsent: the summaries table exists but has no `frame`
//     column at all. The simplest legacy shape, predates Phase F-13.
//   - frameStateNotNull: the summaries table has `frame TEXT NOT NULL
//     DEFAULT ”` (current production shape). Legacy unframed rows
//     stamped with "" must be mapped to NULL.
//   - frameStateNullable: already migrated; no-op.
//
// A fourth state (table does not exist yet) is also a no-op since
// schemaSQL will CREATE it with the nullable shape directly.
type summariesFrameState int

const (
	frameStateNoTable summariesFrameState = iota
	frameStateAbsent
	frameStateNotNull
	frameStateNullable
)

// inspectSummariesFrame probes `PRAGMA table_info(summaries)` to decide
// which migration branch (if any) applies. The PRAGMA returns one row
// per column with cid/name/type/notnull/dflt_value/pk; we only care
// about the `name == "frame"` row and its `notnull` flag.
func inspectSummariesFrame(db *sql.DB) (summariesFrameState, error) {
	rows, err := db.Query(`PRAGMA table_info(summaries)`)
	if err != nil {
		return frameStateNoTable, err
	}
	defer rows.Close()
	sawAny := false
	for rows.Next() {
		sawAny = true
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return frameStateNoTable, err
		}
		if name == "frame" {
			if notnull == 1 {
				return frameStateNotNull, rows.Err()
			}
			return frameStateNullable, rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return frameStateNoTable, err
	}
	if !sawAny {
		// PRAGMA table_info on a non-existent table returns zero rows;
		// schemaSQL will create the table next.
		return frameStateNoTable, nil
	}
	return frameStateAbsent, nil
}

// migrateSummariesFrame brings any pre-existing `summaries` table to
// the nullable-frame shape. Idempotent in every state:
//
//   - frameStateNoTable: nothing to do. schemaSQL will create the
//     table with the right shape.
//   - frameStateAbsent: `ALTER TABLE summaries ADD COLUMN frame TEXT`.
//     Nullable, no default, so existing rows get NULL on read.
//   - frameStateNotNull: table-recreate dance inside a transaction.
//     Maps legacy "" frame values to NULL via NULLIF, drops the old
//     table + trigger, renames the new one in place, recreates the
//     trigger + indexes, and rebuilds the FTS5 index from the
//     reconstituted summaries table.
//   - frameStateNullable: no-op; already migrated.
//
// All paths leave the database in a state where summaries.frame is
// `TEXT` (nullable) with the empty-string-rejecting CHECK constraint
// (the CHECK is added by the recreate path; ALTER ADD COLUMN cannot
// add a table-level CHECK, but the storage-side empty value is only
// produced by callers we control, so the integrity guarantee comes
// from the typed FrameFilter API on top).
func migrateSummariesFrame(db *sql.DB) error {
	state, err := inspectSummariesFrame(db)
	if err != nil {
		return err
	}
	switch state {
	case frameStateNoTable, frameStateNullable:
		return nil
	case frameStateAbsent:
		if _, err := db.Exec(`ALTER TABLE summaries ADD COLUMN frame TEXT`); err != nil {
			return err
		}
		return nil
	case frameStateNotNull:
		return recreateSummariesNullable(db)
	}
	return fmt.Errorf("memory: unknown summaries.frame state %d", state)
}

// recreateSummariesNullable swaps a NOT-NULL `frame` column for a
// nullable one via the canonical SQLite "create new table, copy,
// drop, rename" dance. Wraps the work in a transaction so a crash
// mid-migration leaves the original table untouched. The FTS5 index
// is rebuilt at the end (the new table has the same id space, but
// dropping summaries cleared the FTS5 backing rows).
func recreateSummariesNullable(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	steps := []string{
		`CREATE TABLE summaries_new (
		  id          INTEGER PRIMARY KEY AUTOINCREMENT,
		  agent_id    TEXT NOT NULL,
		  closed_at   INTEGER NOT NULL,
		  text        TEXT NOT NULL,
		  tokens      INTEGER NOT NULL DEFAULT 0,
		  source_seq  INTEGER NOT NULL DEFAULT 0,
		  frame       TEXT,
		  CHECK (frame IS NULL OR length(frame) > 0)
		)`,
		`INSERT INTO summaries_new (id, agent_id, closed_at, text, tokens, source_seq, frame)
		 SELECT id, agent_id, closed_at, text, tokens, source_seq, NULLIF(frame, '') FROM summaries`,
		`DROP TRIGGER IF EXISTS summaries_ai`,
		`DROP INDEX IF EXISTS summaries_by_closed_at`,
		`DROP INDEX IF EXISTS summaries_by_frame`,
		`DROP TABLE summaries`,
		`ALTER TABLE summaries_new RENAME TO summaries`,
		`CREATE INDEX IF NOT EXISTS summaries_by_closed_at ON summaries(closed_at DESC)`,
		`CREATE INDEX IF NOT EXISTS summaries_by_frame ON summaries(frame, closed_at DESC)`,
		`CREATE TRIGGER IF NOT EXISTS summaries_ai AFTER INSERT ON summaries BEGIN
		  INSERT INTO summaries_fts(rowid, text) VALUES (new.id, new.text);
		END`,
		`INSERT INTO summaries_fts(summaries_fts) VALUES('rebuild')`,
	}
	for _, stmt := range steps {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("memory: recreate summaries (step %q): %w", migrationStepLabel(stmt), err)
		}
	}
	return tx.Commit()
}

// migrationStepLabel returns a short label for stmt to embed in error
// messages. Keeps the wrap short instead of dumping a multi-line SQL
// blob into the caller's terminal. The label is the first non-empty
// line of stmt, trimmed and capped at 64 chars.
func migrationStepLabel(stmt string) string {
	for _, line := range strings.Split(stmt, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > 64 {
			return trimmed[:64]
		}
		return trimmed
	}
	return stmt
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
