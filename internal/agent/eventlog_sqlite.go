package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteEventLog is the SQLite/WAL-backed EventLog implementation. The
// schema matches design § Storage exactly.
//
// Single writer: callers MUST serialize Append through one logical owner
// (the per-agent goroutine, in production). We don't sprinkle locks here
// because the per-agent loop already owns the write side. Reader queries
// (Read, projection scans) are concurrent-safe because SQLite WAL allows
// concurrent readers alongside one writer.
//
// Subscribe fan-out: per-process, per-agent-id channels delivered to from
// inside Append. Best-effort delivery - a slow subscriber that lets its
// channel fill is dropped (we never block Append on a subscriber). The
// TUI is the canonical consumer; if it falls behind, it re-reads from the
// log via Read() instead of expecting Subscribe to backfill.
type SQLiteEventLog struct {
	db   *sql.DB
	once sync.Once

	subMu sync.Mutex
	subs  map[string]map[chan Event]struct{}
}

const sqliteSchemaSQL = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS events (
  seq      INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_id TEXT    NOT NULL,
  ts       INTEGER NOT NULL,
  type     TEXT    NOT NULL,
  payload  BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS events_by_agent ON events(agent_id, seq);

CREATE TABLE IF NOT EXISTS agents (
  id                TEXT PRIMARY KEY,
  parent_id         TEXT REFERENCES agents(id),
  root_id           TEXT NOT NULL,
  state             TEXT NOT NULL,
  attempt           INTEGER NOT NULL DEFAULT 1,
  title             TEXT NOT NULL,
  model             TEXT,
  tokens_in         INTEGER NOT NULL DEFAULT 0,
  tokens_out        INTEGER NOT NULL DEFAULT 0,
  cost_cents        INTEGER NOT NULL DEFAULT 0,
  tool_calls        INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  last_heartbeat_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS artifacts (
  id         TEXT PRIMARY KEY,
  agent_id   TEXT NOT NULL REFERENCES agents(id),
  path       TEXT NOT NULL,
  kind       TEXT NOT NULL,
  sha256     TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
`

// OpenSQLiteEventLog opens (creating if needed) a SQLite database at path,
// applies WAL pragmas, creates the schema, and returns an EventLog.
//
// The DSN forces:
//   - WAL journal mode
//   - synchronous=NORMAL (durable across app crash; OS/power loss is the
//     only window for data loss, per design comment)
//   - busy_timeout so concurrent readers don't see SQLITE_BUSY immediately
//
// Pure Go: uses modernc.org/sqlite, no CGO.
func OpenSQLiteEventLog(path string) (*SQLiteEventLog, error) {
	// modernc driver name is "sqlite". URI form lets us set pragmas at open
	// time so the very first statement on the connection (including those
	// run by the pool internally) sees them.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open: %w", err)
	}
	// Single writer model: cap to one to avoid SQLite "database is locked"
	// during multi-conn writes. Readers can still pile up because we use a
	// separate read path that opens the same DB read-only? -> No: we use
	// the same handle. SQLite WAL allows concurrent reads against one
	// writer connection. Cap conns at a small number; one writer is the
	// invariant the caller enforces.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	if _, err := db.Exec(sqliteSchemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("eventlog: schema: %w", err)
	}
	return &SQLiteEventLog{
		db:   db,
		subs: map[string]map[chan Event]struct{}{},
	}, nil
}

func (l *SQLiteEventLog) Append(ctx context.Context, ev Event) (int64, error) {
	res, err := l.db.ExecContext(ctx,
		`INSERT INTO events(agent_id, ts, type, payload) VALUES (?, ?, ?, ?)`,
		ev.AgentID, ev.TS.UnixMilli(), string(ev.Type), ev.Payload,
	)
	if err != nil {
		return 0, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	ev.Seq = seq
	// Best-effort fan-out to subscribers; never block Append on a slow
	// consumer. The TS we publish is whatever the caller provided,
	// already-normalized UTC by convention; we don't re-touch it here.
	l.publish(ev)
	return seq, nil
}

// publish delivers ev to every subscriber registered for ev.AgentID. A
// channel that is full (cap 64) gets the event dropped silently - the
// subscriber is expected to fall back to Read() if it cares about a gap.
//
// Holds subMu for the whole fan-out so unsub can close the channel
// safely under the same lock without racing a concurrent send. The
// per-subscriber send is non-blocking (select + default), so the lock
// is held for O(N_subs) microseconds - the table is small (the chat
// view + the manage view + apply handler in production) and a full
// channel never stalls us.
func (l *SQLiteEventLog) publish(ev Event) {
	l.subMu.Lock()
	defer l.subMu.Unlock()
	for c := range l.subs[ev.AgentID] {
		select {
		case c <- ev:
		default:
			// drop - see contract in SQLiteEventLog doc
		}
	}
}

// Read returns all events for agentID with seq > fromSeq, in seq order.
// Phase 1 preflight scope: cap at 100_000 rows per call to bound memory.
func (l *SQLiteEventLog) Read(ctx context.Context, agentID string, fromSeq int64) ([]Event, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT seq, agent_id, ts, type, payload FROM events WHERE agent_id = ? AND seq > ? ORDER BY seq ASC LIMIT 100000`,
		agentID, fromSeq,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			ev    Event
			tsMs  int64
			typeS string
		)
		if err := rows.Scan(&ev.Seq, &ev.AgentID, &tsMs, &typeS, &ev.Payload); err != nil {
			return nil, err
		}
		ev.TS = time.UnixMilli(tsMs).UTC()
		ev.Type = EventType(typeS)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Subscribe registers a per-process channel that receives every event
// appended for agentID, starting from the call. Returns the channel and an
// unsubscribe func; callers MUST call unsubscribe to free the slot.
//
// Channel lifecycle: the returned channel IS closed on unsubscribe, so
// consumers reading with `ev, ok := <-ch` see ok == false and can
// return cleanly, and consumers ranging over the channel exit the loop.
// unsub is idempotent: a second call after the first is a safe no-op
// (no double-close panic), so a defer + an explicit cancel can both
// fire without coordination.
//
// Buffer = 64. If the consumer falls behind, Append drops events to it
// (the log is the authoritative state; Subscribe is a live-update
// convenience). A consumer that needs to recover from a gap should call
// Read(fromSeq) to catch up.
func (l *SQLiteEventLog) Subscribe(agentID string) (<-chan Event, func(), error) {
	ch := make(chan Event, 64)
	l.subMu.Lock()
	if l.subs[agentID] == nil {
		l.subs[agentID] = map[chan Event]struct{}{}
	}
	l.subs[agentID][ch] = struct{}{}
	l.subMu.Unlock()
	unsub := func() {
		l.subMu.Lock()
		defer l.subMu.Unlock()
		m, ok := l.subs[agentID]
		if !ok {
			return
		}
		if _, present := m[ch]; !present {
			// Already unsubscribed - keep idempotent so a defer +
			// an explicit cancel can both fire without panicking
			// on a double close(ch).
			return
		}
		delete(m, ch)
		close(ch)
		if len(m) == 0 {
			delete(l.subs, agentID)
		}
	}
	return ch, unsub, nil
}

// InsertArtifact records a reference to an on-disk artifact (file, diff,
// plan, skill PROPOSAL, etc.). Per the design, blobs do NOT live in SQLite
// rows; this row is the reference + integrity hash.
func (l *SQLiteEventLog) InsertArtifact(ctx context.Context, a Artifact) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO artifacts(id, agent_id, path, kind, sha256, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.AgentID, a.Path, a.Kind, a.SHA256, a.CreatedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("eventlog: insert artifact: %w", err)
	}
	return nil
}

func (l *SQLiteEventLog) Close() error {
	var err error
	l.once.Do(func() {
		err = l.db.Close()
	})
	return err
}

// DB exposes the underlying *sql.DB for the projection / recovery code paths.
// Kept package-internal so external callers must go through EventLog methods.
func (l *SQLiteEventLog) DB() *sql.DB { return l.db }

// --- Slice 1h additions: projection-cache helpers used by lifecycle /
// recovery / orphan sweep. These do NOT touch the events table - they read
// or update the `agents` projection cache that the supervisor populates
// when it spawns an agent.
//
// Discipline (design § Write discipline): callers append the
// authoritative event to `events` FIRST, then call UpdateAgentState to
// keep the cache in sync. The cache is always reconstructable from
// `events` via Replay; this helper just keeps the next-read fast.

// nonTerminalStateList is the SQL-side list of states considered
// non-terminal. Kept in sync with State.IsTerminal in state.go. We hard-
// code the strings here (matching State.String()) because we want a
// single SQL query, not a per-row Go-side filter, when scanning >100
// rows.
const nonTerminalStateList = `('spawning','queued','running','awaiting-input','blocked','paused-by-user','compacting','cancelling')`

// StaleAgents returns the IDs of agents whose `state` is non-terminal AND
// whose `last_heartbeat_at` is strictly before `threshold`. Threshold is
// expected in UTC; we compare on the stored unix-ms representation.
//
// Used by:
//   - Recover (at startup, with a generous tolerance)
//   - OrphanSweeper (at runtime, with 2 x HeartbeatInterval tolerance)
func (l *SQLiteEventLog) StaleAgents(ctx context.Context, threshold time.Time) ([]string, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT id FROM agents WHERE last_heartbeat_at < ? AND state IN `+nonTerminalStateList+` ORDER BY id ASC`,
		threshold.UnixMilli(),
	)
	if err != nil {
		return nil, fmt.Errorf("eventlog: stale agents: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// NonTerminalAgents returns the IDs of all agents whose state is
// non-terminal (regardless of heartbeat freshness). Used by Recover to
// build the StillActive list.
func (l *SQLiteEventLog) NonTerminalAgents(ctx context.Context) ([]string, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT id FROM agents WHERE state IN `+nonTerminalStateList+` ORDER BY id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("eventlog: non-terminal agents: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UpdateAgentState updates the projection cache row for `agentID`:
// bumps `state` and `updated_at`. Callers MUST first Append the
// state_change event to `events` (so the SoT replay produces the same
// row); this helper just keeps the cache fresh for the next read.
//
// Returns sql.ErrNoRows-equivalent (wrapped) if no row was updated, so
// callers detect a missing-agent bug rather than silently no-oping.
func (l *SQLiteEventLog) UpdateAgentState(ctx context.Context, agentID string, state State, ts time.Time) error {
	res, err := l.db.ExecContext(ctx,
		`UPDATE agents SET state = ?, updated_at = ? WHERE id = ?`,
		state.String(), ts.UnixMilli(), agentID,
	)
	if err != nil {
		return fmt.Errorf("eventlog: update agent state %s: %w", agentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("eventlog: update agent state %s: no row", agentID)
	}
	return nil
}

// UpdateAgentModel updates the projection cache row's `model` for
// `agentID`. Called when the user runs `/model <provider>:<model>`
// mid-chat so the header pill / `/whoami` echo reflect the freshly-
// chosen model on the very next render — model swaps don't emit a
// state_change event the projection would otherwise pick up
// naturally. Returns an error when no row was updated so the caller
// (the model-swap closure in runtime_tui.go) can surface a bug
// instead of silently no-oping when the agent id is wrong.
func (l *SQLiteEventLog) UpdateAgentModel(ctx context.Context, agentID, model string) error {
	res, err := l.db.ExecContext(ctx,
		`UPDATE agents SET model = ?, updated_at = ? WHERE id = ?`,
		model, time.Now().UTC().UnixMilli(), agentID,
	)
	if err != nil {
		return fmt.Errorf("eventlog: update agent model %s: %w", agentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("eventlog: update agent model %s: no row", agentID)
	}
	return nil
}

// UpdateHeartbeat updates the projection cache row's
// `last_heartbeat_at` (and `updated_at`) for `agentID`. Called by the
// HeartbeatTicker immediately after appending an EvtHeartbeat event, for
// the same reason as UpdateAgentState: keep the cache fresh so the next
// StaleAgents scan sees the heartbeat without re-replaying events.
func (l *SQLiteEventLog) UpdateHeartbeat(ctx context.Context, agentID string, ts time.Time) error {
	res, err := l.db.ExecContext(ctx,
		`UPDATE agents SET last_heartbeat_at = ?, updated_at = ? WHERE id = ?`,
		ts.UnixMilli(), ts.UnixMilli(), agentID,
	)
	if err != nil {
		return fmt.Errorf("eventlog: update heartbeat %s: %w", agentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("eventlog: update heartbeat %s: no row", agentID)
	}
	return nil
}

// InsertAgent inserts a fresh projection-cache row for a newly-spawned
// agent. Called by Supervisor.Spawn AFTER appending the corresponding
// state_change kind=created event. Idempotent insert is NOT supported -
// double-insert is a supervisor bug (each agent ID is a fresh ULID).
func (l *SQLiteEventLog) InsertAgent(ctx context.Context, r AgentRow) error {
	var parent any
	if r.ParentID != "" {
		parent = r.ParentID
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO agents(
			id, parent_id, root_id, state, attempt, title, model,
			created_at, updated_at, last_heartbeat_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, parent, r.RootID, r.State.String(), r.Attempt, r.Title, r.Model,
		r.CreatedAt.UnixMilli(), r.UpdatedAt.UnixMilli(), r.LastHeartbeatAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("eventlog: insert agent %s: %w", r.ID, err)
	}
	return nil
}

// DefaultOrphanPruneAge is the production grace period the prune
// callers (chat startup, /resume picker, daemon boot) pass to
// DeleteEmptyOrphanedAgents. A week is long enough that a session the
// user abandoned for the weekend survives until Monday, short enough
// that crash-litter from a month ago does not pile up in the picker.
// Tests override with a tighter window (or zero) so they can assert the
// happy + bad paths without sleeping wall-clock.
const DefaultOrphanPruneAge = 7 * 24 * time.Hour

// DeleteEmptyOrphanedAgents prunes agents that died without producing
// any data the user would miss — heartbeat-lost from a prior process
// kill, no children, no artifacts. Covers both top-level chat sessions
// (orphans that the user never typed in) and sub-agents (orphans that
// never made a tool call). Both flavours accumulate across abrupt
// exits: top-level rows clutter the /resume picker, sub-agent rows
// clutter /agents with dead `[spawning]` cards.
//
// Common predicates — applied to every candidate, top-level or sub:
//
//   - state = 'orphaned'         (terminal; nothing is going to revive it)
//   - 0 child agents             (nothing depends on this row's id)
//   - 0 artifact rows            (no file output recorded)
//   - updated_at <= now - olderThan  (age-gate: a brief lunch break
//     should not nuke a session)
//
// Top-level branch (parent_id IS NULL):
//
//   - 0 EvtUserMessage events    (the user never typed)
//
// Sub-agent branch (parent_id IS NOT NULL):
//
//   - 0 EvtToolCall events       (no work was dispatched)
//   - 0 EvtToolResult events     (no work came back)
//
// Sub-agents do not receive EvtUserMessage so that gate would always
// pass; tool events are the equivalent "the row did real work" signal.
//
// Pass olderThan = 0 to disable the age gate (handy in tests). Wrapped
// in a single transaction so a partial failure can't leave dangling
// events behind. Returns the deleted ids so the caller can log them.
func (l *SQLiteEventLog) DeleteEmptyOrphanedAgents(ctx context.Context, olderThan time.Duration) ([]string, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("eventlog: prune orphans: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Compute the cutoff once. olderThan <= 0 means "no age gate" —
	// pass math.MaxInt64 so the SQL comparison is vacuously true.
	var cutoffMs int64
	if olderThan <= 0 {
		cutoffMs = 1<<62 - 1 // far-future ms; effectively disables the gate
	} else {
		cutoffMs = time.Now().UTC().Add(-olderThan).UnixMilli()
	}

	// Single SELECT covers both scopes via a UNION ALL so the caller
	// gets one transaction's worth of work. Top-level rows match the
	// EvtUserMessage predicate; sub-agent rows match the tool-event
	// pair. The shared predicates (state + children + artifacts +
	// age) sit on each branch.
	rows, err := tx.QueryContext(ctx, `
		SELECT a.id FROM agents a
		WHERE a.state = 'orphaned'
		  AND a.parent_id IS NULL
		  AND a.updated_at <= ?
		  AND NOT EXISTS (
		    SELECT 1 FROM events e
		     WHERE e.agent_id = a.id AND e.type = ?
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM agents c WHERE c.parent_id = a.id
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM artifacts r WHERE r.agent_id = a.id
		  )
		UNION ALL
		SELECT a.id FROM agents a
		WHERE a.state = 'orphaned'
		  AND a.parent_id IS NOT NULL
		  AND a.updated_at <= ?
		  AND NOT EXISTS (
		    SELECT 1 FROM events e
		     WHERE e.agent_id = a.id AND e.type = ?
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM events e
		     WHERE e.agent_id = a.id AND e.type = ?
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM agents c WHERE c.parent_id = a.id
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM artifacts r WHERE r.agent_id = a.id
		  )
	`,
		cutoffMs, string(EvtUserMessage),
		cutoffMs, string(EvtToolCall), string(EvtToolResult),
	)
	if err != nil {
		return nil, fmt.Errorf("eventlog: prune orphans: select: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(ids) == 0 {
		return nil, tx.Commit()
	}

	// Two-step delete: events first (no FK pointing in but we want
	// the cascade ordering explicit), then the projection row. We use
	// per-id Exec rather than IN (...) so we can stream-delete without
	// dynamic SQL string-building or sqlite's variadic-IN limits.
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM events WHERE agent_id = ?`, id,
		); err != nil {
			return nil, fmt.Errorf("eventlog: prune orphans: delete events %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agents WHERE id = ?`, id,
		); err != nil {
			return nil, fmt.Errorf("eventlog: prune orphans: delete agent %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("eventlog: prune orphans: commit: %w", err)
	}
	return ids, nil
}

// GetAgent returns the projection-cache row for `agentID`. Returns
// (row, true, nil) on hit, (zero, false, nil) on miss, (zero, false, err)
// on DB error. Used by the OrphanSweeper to read the current state
// before computing the next transition.
func (l *SQLiteEventLog) GetAgent(ctx context.Context, agentID string) (AgentRow, bool, error) {
	row := l.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(parent_id,''), root_id, state, attempt, title, COALESCE(model,''),
		        tokens_in, tokens_out, cost_cents, tool_calls,
		        created_at, updated_at, last_heartbeat_at
		   FROM agents WHERE id = ?`,
		agentID,
	)
	var (
		r        AgentRow
		stateS   string
		createdM int64
		updatedM int64
		hbM      int64
	)
	err := row.Scan(
		&r.ID, &r.ParentID, &r.RootID, &stateS, &r.Attempt, &r.Title, &r.Model,
		&r.TokensIn, &r.TokensOut, &r.CostCents, &r.ToolCalls,
		&createdM, &updatedM, &hbM,
	)
	if err == sql.ErrNoRows {
		return AgentRow{}, false, nil
	}
	if err != nil {
		return AgentRow{}, false, err
	}
	st, ok := parseState(stateS)
	if !ok {
		return AgentRow{}, false, fmt.Errorf("eventlog: get agent %s: unknown state %q", agentID, stateS)
	}
	r.State = st
	r.CreatedAt = time.UnixMilli(createdM).UTC()
	r.UpdatedAt = time.UnixMilli(updatedM).UTC()
	r.LastHeartbeatAt = time.UnixMilli(hbM).UTC()
	return r, true, nil
}

// LastToolCall returns the name of the most recent EvtToolCall event
// recorded for agentID. ok=false means no tool calls have been logged
// yet (sub-agent just spawned, not yet acting). Used by the inline
// child-snapshot path to surface a live "current tool" signal on the
// parent's bordered card.
//
// Cheap: covered by the events_by_agent index (agent_id, seq) with a
// LIMIT 1, degrades to a constant-time index lookup regardless of
// event-log size.
func (l *SQLiteEventLog) LastToolCall(ctx context.Context, agentID string) (string, bool, error) {
	row := l.db.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE agent_id = ? AND type = ? ORDER BY seq DESC LIMIT 1`,
		agentID, string(EvtToolCall),
	)
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	var tc ToolCall
	if err := json.Unmarshal(payload, &tc); err != nil {
		// Defensive: corrupt payload should not fail the snapshot.
		return "", false, nil
	}
	return tc.Name, true, nil
}

// RecentCommandsUsed returns the verbs of the most recent EvtCommandUsed
// events across ALL agents, newest first, capped at limit. Cross-agent
// on purpose: chat session agent IDs are fresh ULIDs, so a per-agent
// query would always come back empty on a brand-new session - the
// Ctrl+P palette wants "what I've been running lately" regardless of
// which session ran it. Duplicate verbs are NOT collapsed here; the
// palette dedupes after this call so the recency order is preserved.
// Rows with corrupt or empty payloads are skipped, never fatal.
//
// Cheap: a single index-free type scan would be O(log size), but the
// events table is keyed by seq (the PK), so ORDER BY seq DESC LIMIT N
// walks the newest rows first and stops as soon as it has N matches.
func (l *SQLiteEventLog) RecentCommandsUsed(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := l.db.QueryContext(ctx,
		`SELECT payload FROM events WHERE type = ? ORDER BY seq DESC LIMIT ?`,
		string(EvtCommandUsed), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var p CommandUsedPayload
		if err := json.Unmarshal(payload, &p); err != nil || p.Command == "" {
			continue
		}
		out = append(out, p.Command)
	}
	return out, rows.Err()
}

// parseState is the inverse of State.String(). Returns (state, true) on
// match, (0, false) on miss. Kept local to this file because it's only
// used by GetAgent's cache-row hydration.
func parseState(s string) (State, bool) {
	for _, st := range []State{
		StateSpawning, StateQueued, StateRunning, StateAwaitingInput,
		StateBlocked, StatePausedByUser, StateCompacting, StateCancelling,
		StateDone, StateFailed, StateOrphaned,
	} {
		if st.String() == s {
			return st, true
		}
	}
	return 0, false
}
