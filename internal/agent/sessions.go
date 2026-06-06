// User-facing chat sessions.
//
// In carlos's schema, every conversation is a row in the `agents`
// projection table. Top-level agents (parent_id IS NULL) are the
// surface a user actually interacts with — what CC and most agent
// CLIs call a "session". Spawned sub-agents have parent_id set and
// are out of scope for the session picker.
//
// This file owns the read side: ListUserSessions for the picker,
// MostRecentUserSession for `carlos -c`. Writes go through the
// existing InsertAgent + UpdateAgentState path (an agent IS a
// session — there's no separate session table).
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
// browsable list without re-querying. Last-message preview comes
// from the most recent EvtUserMessage payload, truncated.
type Session struct {
	ID         string
	Title      string
	Model      string
	State      State
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Preview    string // last user message, truncated; "" if no user messages yet
	UserMsgs   int    // count of user messages — handy for filtering empty drafts
}

// ErrNoSessions is returned by MostRecentUserSession when the agents
// table holds no top-level rows. Callers (carlos -c) surface this as
// a friendly "no sessions yet — start one with `carlos`" hint.
var ErrNoSessions = errors.New("agent: no user sessions found")

// ListUserSessions returns every top-level agent (parent_id IS NULL),
// sorted by updated_at descending so the most recently active session
// is first. Excludes excluded if non-empty (lets the in-chat /resume
// picker hide the session the user is currently in).
//
// Each Session is enriched with a last-user-message preview by
// scanning the events table — bounded to one extra query per
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
			// Skip rows with an unknown state value — projection
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
		preview, count, err := lastUserMessage(ctx, log, out[i].ID)
		if err != nil {
			// A bad preview shouldn't break the picker — leave
			// blank and move on.
			continue
		}
		out[i].Preview = preview
		out[i].UserMsgs = count
	}
	return out, nil
}

// MostRecentUserSession returns the single most-recently-active
// top-level session — used by `carlos -c` / `--continue`. Returns
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

// lastUserMessage scans the events table for the most recent
// EvtUserMessage on agentID, returning (truncated preview, total
// user-message count). A nil payload or a malformed JSON row
// doesn't error — preview stays empty for that session.
func lastUserMessage(ctx context.Context, log *SQLiteEventLog, agentID string) (string, int, error) {
	// Count first — cheap aggregate.
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

	// Then preview — last user message body.
	var payload []byte
	row = log.DB().QueryRowContext(ctx,
		`SELECT payload FROM events
		 WHERE agent_id = ? AND type = ?
		 ORDER BY seq DESC LIMIT 1`,
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
// spaces. A decode error returns "" — preview is a nice-to-have,
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
// max <= 0 returns the empty string — "don't render any preview" is
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
