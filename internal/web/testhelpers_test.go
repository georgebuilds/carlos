package web

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// newTestLog opens a fresh SQLiteEventLog under a temp dir. The group
// store (when a test needs one) opens the SAME file so the agents-table
// join works.
func newTestLog(t *testing.T) (*agent.SQLiteEventLog, string) {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/state.db"
	log, err := agent.OpenSQLiteEventLog(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log, path
}

func newTestGroups(t *testing.T, path string) *GroupStore {
	t.Helper()
	gs, err := OpenGroupStore(path)
	if err != nil {
		t.Fatalf("open groups: %v", err)
	}
	t.Cleanup(func() { _ = gs.Close() })
	return gs
}

// seedThread inserts a top-level agent row and appends one user message so
// ListUserSessions returns it with a preview + count of 1.
func seedThread(t *testing.T, log *agent.SQLiteEventLog, id, title, firstMsg string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := log.InsertAgent(ctx, agent.AgentRow{
		ID: id, RootID: id, State: agent.StateRunning, Attempt: 1,
		Title: title, Model: "claude-fable-5",
		CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert agent %s: %v", id, err)
	}
	appendEvent(t, log, id, agent.EvtUserMessage, agent.MessagePayload{Text: firstMsg})
}

// seedNonConversationRoot inserts a top-level agent row that has produced
// CONTENT (an assistant message) but NO user message - the shape a
// research root or headless `please` run takes (parent_id NULL, self
// root_id, zero EvtUserMessage, but real activity). The web roster must
// hide these.
func seedNonConversationRoot(t *testing.T, log *agent.SQLiteEventLog, id, title string) {
	t.Helper()
	seedBlankThread(t, log, id, title)
	// Content without a user message = a programmatic run, not a chat.
	appendEvent(t, log, id, agent.EvtAssistantMessage, agent.MessagePayload{Text: "spawned output"})
}

// seedBlankThread inserts a live (non-terminal) top-level agent row with NO
// events at all - the shape of a freshly minted conversation the user has
// not typed into yet. The roster MUST keep these visible (create, switch
// away, switch back).
func seedBlankThread(t *testing.T, log *agent.SQLiteEventLog, id, title string) {
	t.Helper()
	seedEmptyWithState(t, log, id, title, agent.StateRunning)
}

// seedTerminalEmpty inserts a terminal (orphaned) top-level agent row with
// NO events - an abandoned, empty session. The "app closed" case the roster
// is allowed to drop.
func seedTerminalEmpty(t *testing.T, log *agent.SQLiteEventLog, id, title string) {
	t.Helper()
	seedEmptyWithState(t, log, id, title, agent.StateOrphaned)
}

func seedEmptyWithState(t *testing.T, log *agent.SQLiteEventLog, id, title string, state agent.State) {
	t.Helper()
	now := time.Now().UTC()
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID: id, RootID: id, State: state, Attempt: 1,
		Title: title, Model: "claude-fable-5",
		CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert empty thread %s: %v", id, err)
	}
}

func appendEvent(t *testing.T, log *agent.SQLiteEventLog, id string, typ agent.EventType, payload any) int64 {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	seq, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: typ, Payload: b,
	})
	if err != nil {
		t.Fatalf("append %s: %v", typ, err)
	}
	return seq
}

func strptr(s string) *string { return &s }
