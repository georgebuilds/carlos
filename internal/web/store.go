package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// Group is a named, manually ordered container of threads in the roster
// (plan §4). One level, no nesting. Threads is the live member count.
type Group struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Pos     int    `json:"pos"`
	Threads int    `json:"threads"`
}

// ErrGroupNotFound is returned when a group id does not exist.
var ErrGroupNotFound = errors.New("web: group not found")

// GroupStore owns the web-only thread-grouping tables. It opens its OWN
// *sql.DB handle to the shared state.db (WAL allows multiple handles in a
// process, exactly as the TUI + daemon coexist). The agents table lives
// in the same file, so the member-count join sees live top-level threads
// without coupling to internal/agent's schema. The web layer never
// touches the agent schema; it adds its own tables (plan §4.2).
type GroupStore struct {
	db *sql.DB
}

const groupSchema = `
CREATE TABLE IF NOT EXISTS web_groups (
  id   TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  pos  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS web_thread_groups (
  thread_id TEXT PRIMARY KEY,
  group_id  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS web_thread_groups_by_group ON web_thread_groups(group_id);
-- web_hidden blacklists threads from the roster WITHOUT deleting them. Used
-- mainly for foreign (Claude Code) threads carlos does not own: hiding is a
-- carlos-web-local view filter, never a touch on the agent's data. Backend
-- agnostic (no agents join), since cc:<uuid> ids are not agent rows.
CREATE TABLE IF NOT EXISTS web_hidden (
  thread_id TEXT PRIMARY KEY
);
-- web_cc_origin records the Claude Code threads carlos web itself owns
-- (created or explicitly imported). The roster scopes the CC backend's
-- contribution to this set instead of mirroring every on-disk session, so
-- the console is not flooded by sessions carlos never started. Backend
-- agnostic: it only ever holds cc:<uuid> ids, but carries no agents join.
CREATE TABLE IF NOT EXISTS web_cc_origin (
  thread_id TEXT PRIMARY KEY
);
`

// OpenGroupStore opens (or creates) the grouping tables in the state.db at
// path. Pragmas mirror the event log's so the handle plays nice under WAL.
func OpenGroupStore(path string) (*GroupStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open group store: %w", err)
	}
	// Conservative pool: this handle does light, bursty work next to the
	// event log's writer. WAL + busy_timeout keeps it from tripping over
	// the log's writes.
	db.SetMaxOpenConns(4)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("group store pragma %q: %w", pragma, err)
		}
	}
	if _, err := db.Exec(groupSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("group store migrate: %w", err)
	}
	if _, err := db.Exec(repoSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("group store repo migrate: %w", err)
	}
	return &GroupStore{db: db}, nil
}

func (s *GroupStore) Close() error { return s.db.Close() }

// List returns every group ordered by (pos, id) with its live member
// count. Backend agnostic: membership is NOT joined to the agents table (a
// cc:<uuid> has no agent row but is still groupable, plan WB-3). Instead the
// count is gated on `live` - the set of thread ids the roster handler is
// currently showing - so memberships for janitor-pruned threads contribute
// zero without an eager sweep (lazy hiding, plan §4.6). A nil `live` applies
// no filter (callers with no roster context, e.g. a post-patch read).
func (s *GroupStore) List(ctx context.Context, live map[string]bool) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.id, g.name, g.pos
		  FROM web_groups g
		 ORDER BY g.pos ASC, g.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Pos); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts, err := s.memberCounts(ctx, live)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Threads = counts[out[i].ID]
	}
	return out, nil
}

// memberCounts tallies live members per group id. A membership counts only
// when its thread is in `live` (or unconditionally when live is nil), so the
// agents join is gone yet pruned threads still drop out (plan WB-3).
func (s *GroupStore) memberCounts(ctx context.Context, live map[string]bool) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id, group_id FROM web_thread_groups`)
	if err != nil {
		return nil, fmt.Errorf("member counts: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var tid, gid string
		if err := rows.Scan(&tid, &gid); err != nil {
			return nil, err
		}
		if live != nil && !live[tid] {
			continue
		}
		out[gid]++
	}
	return out, rows.Err()
}

// Create mints a group at the end of the manual order (max pos + 1).
func (s *GroupStore) Create(ctx context.Context, name string) (Group, error) {
	if name == "" {
		return Group{}, errors.New("web: group name is required")
	}
	id, err := newID()
	if err != nil {
		return Group{}, err
	}
	var pos int
	// COALESCE(MAX(pos), -1) + 1 puts the first group at 0.
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(pos), -1) + 1 FROM web_groups`).Scan(&pos); err != nil {
		return Group{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO web_groups(id, name, pos) VALUES(?, ?, ?)`, id, name, pos); err != nil {
		return Group{}, fmt.Errorf("create group: %w", err)
	}
	return Group{ID: id, Name: name, Pos: pos, Threads: 0}, nil
}

// Patch renames and/or repositions a group. Nil fields are left
// untouched. Idempotent. Returns ErrGroupNotFound for an unknown id.
func (s *GroupStore) Patch(ctx context.Context, id string, name *string, pos *int) (Group, error) {
	if name == nil && pos == nil {
		return s.get(ctx, id)
	}
	if name != nil && *name == "" {
		return Group{}, errors.New("web: group name cannot be empty")
	}
	// Build the SET clause from whichever fields were provided.
	set, args := "", []any{}
	if name != nil {
		set += "name = ?"
		args = append(args, *name)
	}
	if pos != nil {
		if set != "" {
			set += ", "
		}
		set += "pos = ?"
		args = append(args, *pos)
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, `UPDATE web_groups SET `+set+` WHERE id = ?`, args...)
	if err != nil {
		return Group{}, fmt.Errorf("patch group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Group{}, ErrGroupNotFound
	}
	return s.get(ctx, id)
}

// Delete removes a group and reverts its members to ungrouped in one
// transaction. Members are never deleted, only un-grouped (plan §4.6). We
// do NOT lean on ON DELETE CASCADE - carlos does not promise the FK
// pragma stays on, and the two tables are intentionally FK-free.
func (s *GroupStore) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM web_thread_groups WHERE group_id = ?`, id); err != nil {
		return fmt.Errorf("delete memberships: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM web_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGroupNotFound
	}
	return tx.Commit()
}

// SetThreadGroup moves a thread into a group, or removes it from any group
// when groupID is nil. Upsert semantics; idempotent. Validates the target
// group exists (the membership table has no FK to enforce it).
func (s *GroupStore) SetThreadGroup(ctx context.Context, threadID string, groupID *string) error {
	if threadID == "" {
		return errors.New("web: thread id is required")
	}
	if groupID == nil || *groupID == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM web_thread_groups WHERE thread_id = ?`, threadID)
		return err
	}
	if _, err := s.get(ctx, *groupID); err != nil {
		return err // ErrGroupNotFound surfaces as 404
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO web_thread_groups(thread_id, group_id) VALUES(?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET group_id = excluded.group_id`,
		threadID, *groupID)
	return err
}

// MembershipMap returns thread_id -> group_id for the overlay on
// GET /threads. Backend agnostic (no agents join, plan WB-3): a cc:<uuid>
// membership is returned exactly like a carlos one. The overlay only stamps
// ids that are already in the roster, so rows for vanished threads match
// nothing and drop out without an eager sweep (lazy hiding, plan §4.6).
func (s *GroupStore) MembershipMap(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id, group_id FROM web_thread_groups`)
	if err != nil {
		return nil, fmt.Errorf("membership map: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var tid, gid string
		if err := rows.Scan(&tid, &gid); err != nil {
			return nil, err
		}
		out[tid] = gid
	}
	return out, rows.Err()
}

// SweepOrphans deletes membership rows whose thread no longer exists as a
// top-level agent. Lazy hygiene the server can call on a coarse interval;
// the overlay already hides these, so this is bookkeeping, not correctness
// (plan §4.6). Returns the number of rows swept.
func (s *GroupStore) SweepOrphans(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM web_thread_groups
		 WHERE thread_id NOT IN (SELECT id FROM agents WHERE parent_id IS NULL)`)
	if err != nil {
		return 0, fmt.Errorf("sweep orphan memberships: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Hide adds a thread to the roster blacklist (idempotent). It does not
// delete anything; ListThreads stamps `hidden` on these and the SPA folds
// them out of the default view.
func (s *GroupStore) Hide(ctx context.Context, threadID string) error {
	if threadID == "" {
		return errors.New("web: thread id is required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO web_hidden(thread_id) VALUES(?) ON CONFLICT(thread_id) DO NOTHING`, threadID)
	return err
}

// Unhide removes a thread from the blacklist (idempotent).
func (s *GroupStore) Unhide(ctx context.Context, threadID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM web_hidden WHERE thread_id = ?`, threadID)
	return err
}

// HiddenSet returns the set of blacklisted thread ids for the roster
// overlay. Backend-agnostic (no agents join): a cc:<uuid> hidden row is just
// an id. Rows for threads that vanished do no harm (they match nothing).
func (s *GroupStore) HiddenSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id FROM web_hidden`)
	if err != nil {
		return nil, fmt.Errorf("hidden set: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// MarkCCOrigin records a Claude Code thread as web-owned (idempotent). The
// roster lists a CC session only when it is in this set, so an old on-disk
// session carlos never started stays out of the console until imported.
func (s *GroupStore) MarkCCOrigin(ctx context.Context, threadID string) error {
	if threadID == "" {
		return errors.New("web: thread id is required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO web_cc_origin(thread_id) VALUES(?) ON CONFLICT(thread_id) DO NOTHING`, threadID)
	return err
}

// CCOriginSet returns the set of web-owned CC thread ids for the roster
// scope. Backend agnostic (no agents join): a cc:<uuid> origin row is just
// an id.
func (s *GroupStore) CCOriginSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id FROM web_cc_origin`)
	if err != nil {
		return nil, fmt.Errorf("cc origin set: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// get returns one group with its (unfiltered) member count. Backend
// agnostic: the count is a plain tally of membership rows, no agents join
// (plan WB-3). It is used to echo a group after a patch/assignment; the SPA
// refetches GET /api/groups (roster-gated) for the authoritative live count.
func (s *GroupStore) get(ctx context.Context, id string) (Group, error) {
	var g Group
	err := s.db.QueryRowContext(ctx, `
		SELECT g.id, g.name, g.pos,
		       (SELECT COUNT(*) FROM web_thread_groups wtg WHERE wtg.group_id = g.id) AS n
		  FROM web_groups g WHERE g.id = ?`, id).
		Scan(&g.ID, &g.Name, &g.Pos, &g.Threads)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrGroupNotFound
	}
	if err != nil {
		return Group{}, err
	}
	return g, nil
}
