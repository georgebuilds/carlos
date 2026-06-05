// Package memory ships the carlos memory subsystem (SPEC § Memory model):
//
//   - Episodic — full conversation transcripts, reconstructable from the
//     event log (no rows in this package).
//   - Searchable — `summaries` table + `summaries_fts` FTS5 virtual table.
//     One row per closed conversation; the LLM (or NaiveSummarizer) writes
//     a short paragraph; an AFTER INSERT trigger keeps the FTS index in
//     sync.
//   - User model — `user_model` key/value table. Hand-curated + agent-
//     proposed, NEVER silent-write: agent edits flow through the same
//     approval queue as any other PROPOSAL artifact.
//
// All tables live alongside the supervisor's `events` / `agents` /
// `artifacts` tables in the shared ~/.carlos/state.db. Sharing the
// database is intentional (DESIGN § Memory): one *sql.DB handle, one
// WAL, one fsync recipe. Opening this package against an existing
// SQLiteEventLog database is a no-op (CREATE TABLE IF NOT EXISTS).
package memory

// schemaSQL applies the three memory tables (summaries + user_model)
// plus the FTS5 virtual table and trigger. Idempotent — safe to run on
// every open.
//
// The summaries_fts contentless-style indirection (`content='summaries',
// content_rowid='id'`) means the FTS5 table stores tokenized text only;
// SELECTs still join back to summaries for the typed columns. The
// AFTER INSERT trigger forwards new rows into the index. We deliberately
// do NOT add update / delete triggers: summaries are append-only at v0
// (mirrors the events table). Phase 7 follow-up adds compaction.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS summaries (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_id    TEXT NOT NULL,
  closed_at   INTEGER NOT NULL,
  text        TEXT NOT NULL,
  tokens      INTEGER NOT NULL DEFAULT 0,
  source_seq  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS summaries_by_closed_at ON summaries(closed_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS summaries_fts USING fts5(
  text,
  content='summaries',
  content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS summaries_ai AFTER INSERT ON summaries BEGIN
  INSERT INTO summaries_fts(rowid, text) VALUES (new.id, new.text);
END;

CREATE TABLE IF NOT EXISTS user_model (
  key         TEXT PRIMARY KEY,
  value       TEXT NOT NULL,
  updated_at  INTEGER NOT NULL,
  source      TEXT
);
`

// walPragmasSQL is run at open-time by OpenStore when it owns the DB
// handle (i.e. not handed one by an event log). The supervisor already
// applies the same pragmas via the DSN (see eventlog_sqlite.go), so
// re-running them is a no-op — but when the memory package opens its
// own connection (CLI path) we need to set them explicitly.
const walPragmasSQL = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
`
