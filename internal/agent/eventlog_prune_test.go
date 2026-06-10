package agent_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Helpers --------------------------------------------------------------

func pruneLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = agent.CloseStateDB(log) })
	return log
}

// pruneSeedAgent inserts a top-level (or child, when parentID != "")
// agent row in the given state. last_heartbeat_at is set to now - the
// prune path doesn't care about heartbeat freshness (it filters by
// state), but a sane value keeps the row from being picked up by other
// tests' stale sweepers if they share the helper later.
func pruneSeedAgent(t *testing.T, log *agent.SQLiteEventLog, id, parentID string, state agent.State) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID:              id,
		ParentID:        parentID,
		RootID:          id,
		State:           state,
		Attempt:         1,
		Title:           "t",
		Model:           "m",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert agent %s: %v", id, err)
	}
}

func seedUserMsg(t *testing.T, log *agent.SQLiteEventLog, agentID, text string) {
	t.Helper()
	payload, _ := json.Marshal(agent.MessagePayload{Text: text})
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    agent.EvtUserMessage,
		Payload: payload,
	}); err != nil {
		t.Fatalf("append user msg: %v", err)
	}
}

func seedStateChangeEvent(t *testing.T, log *agent.SQLiteEventLog, agentID string) {
	t.Helper()
	// Empty-but-tracked event so we can prove the prune path clears
	// the events table even when the only rows are non-user state
	// changes (the actual shape of an orphaned-empty session).
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    agent.EvtStateChange,
		Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("append state change: %v", err)
	}
}

func agentExists(t *testing.T, log *agent.SQLiteEventLog, id string) bool {
	t.Helper()
	_, ok, err := log.GetAgent(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return ok
}

func eventCountFor(t *testing.T, log *agent.SQLiteEventLog, id string) int {
	t.Helper()
	var n int
	row := log.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM events WHERE agent_id = ?`, id)
	if err := row.Scan(&n); err != nil {
		if err == sql.ErrNoRows {
			return 0
		}
		t.Fatalf("scan event count: %v", err)
	}
	return n
}

// Tests ----------------------------------------------------------------

// The target case: a top-level chat that orphaned before the user
// typed anything. State changes from the boot transition still sit in
// the events table; prune must also clear those.
func TestDeleteEmptyOrphanedAgents_PrunesTopLevelEmptyOrphan(t *testing.T) {
	log := pruneLog(t)
	pruneSeedAgent(t, log, "A", "", agent.StateOrphaned)
	seedStateChangeEvent(t, log, "A")
	if eventCountFor(t, log, "A") == 0 {
		t.Fatal("test setup: expected at least one event for A")
	}

	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "A" {
		t.Fatalf("want pruned=[A], got %v", pruned)
	}
	if agentExists(t, log, "A") {
		t.Error("orphaned-empty agent still present after prune")
	}
	if got := eventCountFor(t, log, "A"); got != 0 {
		t.Errorf("events for pruned agent not cleared: got %d", got)
	}
}

// Non-orphaned states are off-limits even when otherwise empty - a
// running/awaiting session is the in-process chat the user is about
// to type into.
func TestDeleteEmptyOrphanedAgents_PreservesNonOrphanedStates(t *testing.T) {
	log := pruneLog(t)
	keepStates := []struct {
		id    string
		state agent.State
	}{
		{"run", agent.StateRunning},
		{"wait", agent.StateAwaitingInput},
		{"done", agent.StateDone},
		{"fail", agent.StateFailed},
	}
	for _, ks := range keepStates {
		pruneSeedAgent(t, log, ks.id, "", ks.state)
	}
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("want no rows pruned, got %v", pruned)
	}
	for _, ks := range keepStates {
		if !agentExists(t, log, ks.id) {
			t.Errorf("state %s was incorrectly pruned", ks.state)
		}
	}
}

// A session the user actually used is data; never delete it even when
// it ends up orphaned later.
func TestDeleteEmptyOrphanedAgents_PreservesOrphanWithMessages(t *testing.T) {
	log := pruneLog(t)
	pruneSeedAgent(t, log, "B", "", agent.StateOrphaned)
	seedUserMsg(t, log, "B", "what's the weather")
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("want no rows pruned, got %v", pruned)
	}
	if !agentExists(t, log, "B") {
		t.Fatal("orphaned-with-msgs agent was wrongly deleted")
	}
}

// Sub-agents are out of scope - the prune target is top-level chat
// clutter only. A sub-agent that orphaned with no messages is still
// part of its parent's tree.
func TestDeleteEmptyOrphanedAgents_PreservesSubAgent(t *testing.T) {
	log := pruneLog(t)
	pruneSeedAgent(t, log, "parent", "", agent.StateRunning)
	pruneSeedAgent(t, log, "kid", "parent", agent.StateOrphaned)
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("want no rows pruned, got %v", pruned)
	}
	if !agentExists(t, log, "kid") {
		t.Error("sub-agent was wrongly deleted")
	}
}

// Even if a top-level agent has zero user messages, refusing to drop
// it when a child row points at it protects against FK breakage and
// data loss for the descendant tree.
func TestDeleteEmptyOrphanedAgents_PreservesOrphanWithChildren(t *testing.T) {
	log := pruneLog(t)
	pruneSeedAgent(t, log, "root", "", agent.StateOrphaned)
	pruneSeedAgent(t, log, "kid", "root", agent.StateOrphaned)
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, id := range pruned {
		if id == "root" {
			t.Fatalf("root with child agent was pruned: %v", pruned)
		}
	}
	if !agentExists(t, log, "root") {
		t.Fatal("root with child agent was wrongly deleted")
	}
}

// Artifact rows pin an agent: deleting it would orphan the artifact
// reference and risk the file going unreachable. Prune must skip.
func TestDeleteEmptyOrphanedAgents_PreservesOrphanWithArtifacts(t *testing.T) {
	log := pruneLog(t)
	pruneSeedAgent(t, log, "C", "", agent.StateOrphaned)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := log.InsertArtifact(context.Background(), agent.Artifact{
		ID:        "art-1",
		AgentID:   "C",
		Path:      "/tmp/blob",
		Kind:      agent.ArtifactKindFile,
		SHA256:    "deadbeef",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("want no rows pruned, got %v", pruned)
	}
	if !agentExists(t, log, "C") {
		t.Fatal("agent with artifact was wrongly deleted")
	}
}

// Multi-row happy path with mixed candidates: only the qualifying
// orphans should disappear, the rest must stay intact.
func TestDeleteEmptyOrphanedAgents_MixedFleet(t *testing.T) {
	log := pruneLog(t)
	// Three pruneable: top-level, orphaned, no msgs, no kids, no artifacts.
	pruneSeedAgent(t, log, "prune-1", "", agent.StateOrphaned)
	pruneSeedAgent(t, log, "prune-2", "", agent.StateOrphaned)
	pruneSeedAgent(t, log, "prune-3", "", agent.StateOrphaned)
	// One running session — keep.
	pruneSeedAgent(t, log, "live", "", agent.StateRunning)
	// One orphaned with content — keep.
	pruneSeedAgent(t, log, "talky", "", agent.StateOrphaned)
	seedUserMsg(t, log, "talky", "let's chat")

	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 3 {
		t.Fatalf("want 3 pruned, got %d: %v", len(pruned), pruned)
	}
	for _, id := range []string{"prune-1", "prune-2", "prune-3"} {
		if agentExists(t, log, id) {
			t.Errorf("%s should have been pruned", id)
		}
	}
	for _, id := range []string{"live", "talky"} {
		if !agentExists(t, log, id) {
			t.Errorf("%s should have been preserved", id)
		}
	}
}

// Empty DB - no-op, no error. Otherwise startup would surface a
// confusing prune-fail warning on first run.
func TestDeleteEmptyOrphanedAgents_EmptyDB(t *testing.T) {
	log := pruneLog(t)
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("want no rows pruned, got %v", pruned)
	}
}
