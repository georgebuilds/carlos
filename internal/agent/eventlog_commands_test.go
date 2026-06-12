package agent_test

// RecentCommandsUsed tests (slice 9k). The helper backs the Ctrl+P
// command palette's MRU ordering, so the contract under test is:
// newest first, across ALL agents, corrupt rows skipped, empty log is
// a clean empty result.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

func openCommandsTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func appendCommandUsed(t *testing.T, log *agent.SQLiteEventLog, agentID, verb string) {
	t.Helper()
	payload, err := json.Marshal(agent.CommandUsedPayload{Command: verb})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtCommandUsed,
		Payload: payload,
	}); err != nil {
		t.Fatalf("append command_used: %v", err)
	}
}

// TestRecentCommandsUsed_NewestFirstAcrossAgents proves the two core
// MRU semantics: ordering is newest-first by seq, and the query spans
// every agent in the log (session IDs are fresh ULIDs, so a per-agent
// MRU would always start empty).
func TestRecentCommandsUsed_NewestFirstAcrossAgents(t *testing.T) {
	log := openCommandsTestLog(t)
	appendCommandUsed(t, log, "agent-one", "help")
	appendCommandUsed(t, log, "agent-two", "frame")
	appendCommandUsed(t, log, "agent-one", "clear")

	got, err := log.RecentCommandsUsed(context.Background(), 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	want := []string{"clear", "frame", "help"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestRecentCommandsUsed_LimitCaps confirms the LIMIT clause: only the
// newest N rows come back.
func TestRecentCommandsUsed_LimitCaps(t *testing.T) {
	log := openCommandsTestLog(t)
	for _, v := range []string{"help", "clear", "frame", "jobs"} {
		appendCommandUsed(t, log, "a", v)
	}
	got, err := log.RecentCommandsUsed(context.Background(), 2)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 || got[0] != "jobs" || got[1] != "frame" {
		t.Errorf("limit 2 should return the two newest; got %v", got)
	}
}

// TestRecentCommandsUsed_EmptyLog is the happy empty path: no rows of
// the type means an empty result and a nil error, never a failure.
func TestRecentCommandsUsed_EmptyLog(t *testing.T) {
	log := openCommandsTestLog(t)
	got, err := log.RecentCommandsUsed(context.Background(), 10)
	if err != nil {
		t.Fatalf("empty log should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty log should return no verbs; got %v", got)
	}
}

// TestRecentCommandsUsed_SkipsCorruptAndEmptyPayloads pins the
// defensive contract: a corrupt JSON row or a row with an empty verb
// is skipped silently rather than failing the whole query.
func TestRecentCommandsUsed_SkipsCorruptAndEmptyPayloads(t *testing.T) {
	log := openCommandsTestLog(t)
	appendCommandUsed(t, log, "a", "help")
	// Corrupt payload row.
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: "a", TS: time.Now().UTC(),
		Type: agent.EvtCommandUsed, Payload: []byte("{not json"),
	}); err != nil {
		t.Fatalf("append corrupt: %v", err)
	}
	// Empty-verb row.
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: "a", TS: time.Now().UTC(),
		Type: agent.EvtCommandUsed, Payload: []byte(`{"command":""}`),
	}); err != nil {
		t.Fatalf("append empty verb: %v", err)
	}
	got, err := log.RecentCommandsUsed(context.Background(), 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0] != "help" {
		t.Errorf("only the valid row should survive; got %v", got)
	}
}

// TestRecentCommandsUsed_NonPositiveLimit returns empty without
// touching the database.
func TestRecentCommandsUsed_NonPositiveLimit(t *testing.T) {
	log := openCommandsTestLog(t)
	appendCommandUsed(t, log, "a", "help")
	for _, n := range []int{0, -3} {
		got, err := log.RecentCommandsUsed(context.Background(), n)
		if err != nil {
			t.Fatalf("limit %d: %v", n, err)
		}
		if len(got) != 0 {
			t.Errorf("limit %d should return nothing; got %v", n, got)
		}
	}
}

// TestRecentCommandsUsed_ClosedDBErrors is the bad path: a query
// against a closed handle surfaces the error to the caller (the
// palette degrades to "no MRU" and logs to diag).
func TestRecentCommandsUsed_ClosedDBErrors(t *testing.T) {
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	_ = log.Close()
	if _, err := log.RecentCommandsUsed(context.Background(), 10); err == nil {
		t.Error("closed DB should error, not return silently")
	}
}

// TestRecentCommandsUsed_IgnoresOtherEventTypes proves the type filter:
// a busy log full of other events contributes nothing to the MRU.
func TestRecentCommandsUsed_IgnoresOtherEventTypes(t *testing.T) {
	log := openCommandsTestLog(t)
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: "a", TS: time.Now().UTC(),
		Type: agent.EvtUserMessage, Payload: []byte(`{"text":"/help"}`),
	}); err != nil {
		t.Fatalf("append user msg: %v", err)
	}
	appendCommandUsed(t, log, "a", "frame")
	got, err := log.RecentCommandsUsed(context.Background(), 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0] != "frame" {
		t.Errorf("only command_used rows should count; got %v", got)
	}
}
