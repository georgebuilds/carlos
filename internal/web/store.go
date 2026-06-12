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
	return &GroupStore{db: db}, nil
}

func (s *GroupStore) Close() error { return s.db.Close() }

// List returns every group ordered by (pos, id) with its live member
// count. The count joins web_thread_groups against the agents table for
// top-level threads only, so memberships for janitor-pruned threads
// contribute zero without an eager sweep (lazy hiding, plan §4.6).
func (s *GroupStore) List(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.id, g.name, g.pos,
		       (SELECT COUNT(*)
		          FROM web_thread_groups wtg
		          JOIN agents a ON a.id = wtg.thread_id
		         WHERE wtg.group_id = g.id AND a.parent_id IS NULL) AS n
		  FROM web_groups g
		 ORDER BY g.pos ASC, g.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Pos, &g.Threads); err != nil {
			return nil, err
		}
		out = append(out, g)
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
// GET /threads. Only memberships whose thread is a live top-level agent
// are returned (the join drops vanished threads, plan §4.6).
func (s *GroupStore) MembershipMap(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT wtg.thread_id, wtg.group_id
		  FROM web_thread_groups wtg
		  JOIN agents a ON a.id = wtg.thread_id
		 WHERE a.parent_id IS NULL`)
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

func (s *GroupStore) get(ctx context.Context, id string) (Group, error) {
	var g Group
	err := s.db.QueryRowContext(ctx, `
		SELECT g.id, g.name, g.pos,
		       (SELECT COUNT(*)
		          FROM web_thread_groups wtg
		          JOIN agents a ON a.id = wtg.thread_id
		         WHERE wtg.group_id = g.id AND a.parent_id IS NULL) AS n
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
