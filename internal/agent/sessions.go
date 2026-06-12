// User-facing chat sessions.
//
// In carlos's schema, every conversation is a row in the `agents`
// projection table. Top-level agents (parent_id IS NULL) are the
// surface a user actually interacts with - what CC and most agent
// CLIs call a "session". Spawned sub-agents have parent_id set and
// are out of scope for the session picker.
//
// This file owns the read side: ListUserSessions for the picker,
// MostRecentUserSession for `carlos -c`. Writes go through the
// existing InsertAgent + UpdateAgentState path (an agent IS a
// session - there's no separate session table).
package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Session is the picker-row shape: enough for a TUI to render a
// browsable list without re-querying. Preview comes from the FIRST
// EvtUserMessage payload (the topic), truncated — that's what
// identifies a thread when scrolling a long list, whereas the most
// recent message tends to be incremental follow-up the picker can't
// disambiguate on.
type Session struct {
	ID        string
	Title     string
	Model     string
	State     State
	CreatedAt time.Time
	UpdatedAt time.Time
	Preview   string // first user message, truncated; "" if no user messages yet
	UserMsgs  int    // count of user messages - handy for filtering empty drafts
}

// ErrNoSessions is returned by MostRecentUserSession when the agents
// table holds no top-level rows. Callers (carlos -c) surface this as
// a friendly "no sessions yet - start one with `carlos`" hint.
var ErrNoSessions = errors.New("agent: no user sessions found")

// Errors returned by DeleteSession.
var (
	// ErrSessionNotFound: no agent with that id exists.
	ErrSessionNotFound = errors.New("agent: session not found")
	// ErrSessionLive: the session's heartbeat is fresh, so a process is
	// actively driving it. Deleting would pull the log out from under a
	// running loop; detach or close that surface first.
	ErrSessionLive = errors.New("agent: session is live")
	// ErrNotTopLevel: the id refers to a sub-agent. Deletion operates on
	// whole threads; sub-agents go with their parent thread's lineage.
	ErrNotTopLevel = errors.New("agent: not a top-level session")
)

// ListUserSessions returns every top-level agent (parent_id IS NULL),
// sorted by updated_at descending so the most recently active session
// is first. Excludes excluded if non-empty (lets the in-chat /resume
// picker hide the session the user is currently in).
//
// Each Session is enriched with a last-user-message preview by
// scanning the events table - bounded to one extra query per
// session, ms-scale at the dozens-of-sessions level we expect from a
// single user.
func ListUserSessions(ctx context.Context, log *SQLiteEventLog, excluded string) ([]Session, error) {
	rows, err := log.DB().QueryContext(ctx, `
		SELECT id, COALESCE(title,''), COALESCE(model,''), state,
		       created_at, updated_at
		FROM agents
		WHERE parent_id IS NULL
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("agent: list sessions: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var (
			s        Session
			stateS   string
			createdM int64
			updatedM int64
		)
		if err := rows.Scan(&s.ID, &s.Title, &s.Model, &stateS, &createdM, &updatedM); err != nil {
			return nil, err
		}
		if s.ID == excluded {
			continue
		}
		st, ok := parseState(stateS)
		if !ok {
			// Skip rows with an unknown state value - projection
			// inconsistency; better to drop one row than fail the
			// whole picker.
			continue
		}
		s.State = st
		s.CreatedAt = time.UnixMilli(createdM).UTC()
		s.UpdatedAt = time.UnixMilli(updatedM).UTC()
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Per-session enrichment: preview + count. Each scan is one
	// indexed query (events_by_agent index covers it) so even at
	// 100 sessions this is ms.
	for i := range out {
		preview, count, err := firstUserMessage(ctx, log, out[i].ID)
		if err != nil {
			// A bad preview shouldn't break the picker - leave
			// blank and move on.
			continue
		}
		out[i].Preview = preview
		out[i].UserMsgs = count
	}
	return out, nil
}

// MostRecentUserSession returns the single most-recently-active
// top-level session - used by `carlos -c` / `--continue`. Returns
// ErrNoSessions when none exist.
func MostRecentUserSession(ctx context.Context, log *SQLiteEventLog) (Session, error) {
	sessions, err := ListUserSessions(ctx, log, "")
	if err != nil {
		return Session{}, err
	}
	if len(sessions) == 0 {
		return Session{}, ErrNoSessions
	}
	return sessions[0], nil
}

// DeleteSession permanently removes a top-level thread and its entire
// lineage - every sub-agent sharing the thread's root_id, plus all of
// their events and artifacts - in one transaction. It is the
// user-initiated counterpart to DeleteEmptyOrphanedAgents (the janitor):
// unlike that path, it deletes non-empty conversations on request.
//
// Guards (checked before any deletion):
//   - the id must exist, else ErrSessionNotFound;
//   - it must be a top-level session (parent_id IS NULL), else
//     ErrNotTopLevel - sub-agents are only removed via their parent;
//   - unless force is set, its heartbeat must be stale, else
//     ErrSessionLive, so a thread some OTHER live process is driving is
//     never deleted out from under it. A caller that owns the thread (a
//     TUI deleting its own session after confirmation, or a web backend
//     that has just detached the loop) passes force=true: it knows no
//     foreign process holds the thread, so the fresh heartbeat is its own.
//
// Returns the number of agent rows removed (the thread plus its
// sub-agents). The whole tree shares root_id = the top-level id, which is
// how the cascade finds the lineage.
func DeleteSession(ctx context.Context, log *SQLiteEventLog, id string, force bool) (int, error) {
	db := log.DB()

	var parentID sql.NullString
	var lastHeartbeatMs int64
	err := db.QueryRowContext(ctx,
		`SELECT parent_id, last_heartbeat_at FROM agents WHERE id = ?`, id).
		Scan(&parentID, &lastHeartbeatMs)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrSessionNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("agent: delete session: lookup: %w", err)
	}
	if parentID.Valid && parentID.String != "" {
		return 0, ErrNotTopLevel
	}
	if !force {
		if last := time.UnixMilli(lastHeartbeatMs).UTC(); !last.IsZero() && time.Since(last) < StalenessTolerance {
			return 0, ErrSessionLive
		}
	}

	// Best-effort lineage size for the caller's confirmation copy ("deleted
	// the thread + N sub-agents"). Ignored on error - it is cosmetic, never
	// load-bearing, so it adds no error branch to the delete path.
	n := 0
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE root_id = ?`, id).Scan(&n)

	if err := deleteLineageTx(ctx, db, id); err != nil {
		return 0, err
	}
	return n, nil
}

// deleteLineageTx removes every row of the thread tree (root_id == id) -
// artifacts, events, then the agent rows - in one transaction. Split from
// DeleteSession so the guard logic and the cascade can be tested
// independently. SQLite checks foreign keys at statement end, not per row,
// so a single `DELETE FROM agents WHERE root_id = ?` removes a parent and
// its children together without a deferral pragma (verified for depth >1).
func deleteLineageTx(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("agent: delete session: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// artifacts + events reference agents, so delete them while the agent
	// rows still exist, then the agents themselves.
	for _, stmt := range []string{
		`DELETE FROM artifacts WHERE agent_id IN (SELECT id FROM agents WHERE root_id = ?)`,
		`DELETE FROM events    WHERE agent_id IN (SELECT id FROM agents WHERE root_id = ?)`,
		`DELETE FROM agents    WHERE root_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
			return fmt.Errorf("agent: delete session: %w", err)
		}
	}
	return tx.Commit()
}

// firstUserMessage scans the events table for the EARLIEST
// EvtUserMessage on agentID, returning (truncated preview of that
// message, total user-message count). The first user message reads
// as the thread's topic in the picker — far more identifying than
// the latest follow-up. A nil payload or a malformed JSON row
// doesn't error - preview stays empty for that session.
func firstUserMessage(ctx context.Context, log *SQLiteEventLog, agentID string) (string, int, error) {
	// Count first - cheap aggregate.
	var count int
	row := log.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE agent_id = ? AND type = ?`,
		agentID, string(EvtUserMessage),
	)
	if err := row.Scan(&count); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, nil
		}
		return "", 0, err
	}
	if count == 0 {
		return "", 0, nil
	}

	// Then preview - first user message body (seq ASC).
	var payload []byte
	row = log.DB().QueryRowContext(ctx,
		`SELECT payload FROM events
		 WHERE agent_id = ? AND type = ?
		 ORDER BY seq ASC LIMIT 1`,
		agentID, string(EvtUserMessage),
	)
	if err := row.Scan(&payload); err != nil {
		return "", count, err
	}
	preview := decodeUserMessagePreview(payload)
	return preview, count, nil
}

// decodeUserMessagePreview unmarshals a MessagePayload and returns
// the first ~120 chars of the text, with newlines collapsed to
// spaces. A decode error returns "" - preview is a nice-to-have,
// not load-bearing.
func decodeUserMessagePreview(payload []byte) string {
	var msg MessagePayload
	if len(payload) == 0 {
		return ""
	}
	if err := tryUnmarshalMessage(payload, &msg); err != nil {
		return ""
	}
	return truncatePreview(msg.Text, 120)
}

// tryUnmarshalMessage is a thin wrapper around json.Unmarshal kept
// local to this file so the preview helpers are self-contained.
func tryUnmarshalMessage(raw []byte, out *MessagePayload) error {
	return json.Unmarshal(raw, out)
}

// truncatePreview clips s to at most max runes (newlines collapsed
// to spaces), appending an ellipsis when trimmed. Lives here so the
// picker's preview rendering stays consistent regardless of caller.
//
// max <= 0 returns the empty string - "don't render any preview" is
// a sensible interpretation of "give me zero characters".
func truncatePreview(s string, max int) string {
	if max <= 0 {
		return ""
	}
	// Inline collapse + truncate to avoid two passes on long text.
	// Walk runes, replacing newlines, and stop at max+1 to know if
	// we have to ellide.
	out := make([]rune, 0, max+1)
	count := 0
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		out = append(out, r)
		count++
		if count > max {
			break
		}
	}
	if count <= max {
		return string(out)
	}
	if max == 1 {
		return "…"
	}
	return string(out[:max-1]) + "…"
}
