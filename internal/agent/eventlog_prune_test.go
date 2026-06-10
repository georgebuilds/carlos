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

// pruneSeedAgentAged is like pruneSeedAgent but lets the test caller
// pin updated_at to a specific moment so the age-gate predicate can be
// exercised without sleeping wall-clock.
func pruneSeedAgentAged(t *testing.T, log *agent.SQLiteEventLog, id, parentID string, state agent.State, updatedAt time.Time) {
	t.Helper()
	ts := updatedAt.UTC().Truncate(time.Millisecond)
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID:              id,
		ParentID:        parentID,
		RootID:          id,
		State:           state,
		Attempt:         1,
		Title:           "t",
		Model:           "m",
		CreatedAt:       ts,
		UpdatedAt:       ts,
		LastHeartbeatAt: ts,
	}); err != nil {
		t.Fatalf("insert agent %s: %v", id, err)
	}
}

func seedToolCall(t *testing.T, log *agent.SQLiteEventLog, agentID string) {
	t.Helper()
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    agent.EvtToolCall,
		Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("append tool call: %v", err)
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

	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 0)
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
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 0)
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
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 0)
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

// Sub-agent prune with no age gate: a sub-agent that orphaned without
// firing any tool events and with no children/artifacts is exactly
// the dead `[spawning]` clutter the /agents view accumulates. With
// olderThan=0 the prune fires unconditionally.
func TestDeleteEmptyOrphanedAgents_PrunesEmptySubAgent(t *testing.T) {
	log := pruneLog(t)
	pruneSeedAgent(t, log, "parent", "", agent.StateRunning)
	pruneSeedAgent(t, log, "kid", "parent", agent.StateOrphaned)
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "kid" {
		t.Fatalf("want pruned=[kid], got %v", pruned)
	}
	if agentExists(t, log, "kid") {
		t.Error("empty orphaned sub-agent should have been pruned")
	}
	if !agentExists(t, log, "parent") {
		t.Error("running parent must never be pruned")
	}
}

// Even if a top-level agent has zero user messages, refusing to drop
// it when a child row points at it protects against FK breakage and
// data loss for the descendant tree.
func TestDeleteEmptyOrphanedAgents_PreservesOrphanWithChildren(t *testing.T) {
	log := pruneLog(t)
	pruneSeedAgent(t, log, "root", "", agent.StateOrphaned)
	pruneSeedAgent(t, log, "kid", "root", agent.StateOrphaned)
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 0)
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
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 0)
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

	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 0)
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
	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("want no rows pruned, got %v", pruned)
	}
}

// Age-gate behavior --------------------------------------------------
//
// The 7-day production grace period exists so a session the user
// stepped away from over lunch (or for the weekend) is not silently
// reaped before they come back. These tests exercise the age gate
// directly by passing a tight olderThan and pinning updated_at via
// pruneSeedAgentAged.

// Even when a top-level row is well past the grace window, the
// presence of any user_message event means the user actually spoke
// there - that is data, never prune it.
func TestDeleteEmptyOrphanedAgents_TopLevelOldWithUserMessage_Preserved(t *testing.T) {
	log := pruneLog(t)
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	pruneSeedAgentAged(t, log, "spoke", "", agent.StateOrphaned, old)
	seedUserMsg(t, log, "spoke", "hello carlos")

	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("want no rows pruned, got %v", pruned)
	}
	if !agentExists(t, log, "spoke") {
		t.Fatal("orphan with user message was wrongly deleted past the grace window")
	}
}

// A fresh-but-empty top-level orphan inside the grace window must
// survive: the user might still be coming back to type. This is the
// "abandoned for lunch, not for a month" case.
func TestDeleteEmptyOrphanedAgents_TopLevelYoungEmpty_Preserved(t *testing.T) {
	log := pruneLog(t)
	fresh := time.Now().UTC().Add(-1 * time.Hour)
	pruneSeedAgentAged(t, log, "lunch", "", agent.StateOrphaned, fresh)

	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("want no rows pruned within grace window, got %v", pruned)
	}
	if !agentExists(t, log, "lunch") {
		t.Fatal("young empty orphan was wrongly pruned inside the grace window")
	}
}

// An empty top-level orphan older than the grace window is the
// canonical target: crash-litter from a prior abrupt exit. Prune.
func TestDeleteEmptyOrphanedAgents_TopLevelOldEmpty_Deleted(t *testing.T) {
	log := pruneLog(t)
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	pruneSeedAgentAged(t, log, "old-empty", "", agent.StateOrphaned, old)

	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "old-empty" {
		t.Fatalf("want pruned=[old-empty], got %v", pruned)
	}
	if agentExists(t, log, "old-empty") {
		t.Error("old empty orphan should have been pruned past the grace window")
	}
}

// A sub-agent that orphaned with no tool events and aged past the
// grace window is the dead `[spawning]` row the /agents view
// accumulates. Prune.
func TestDeleteEmptyOrphanedAgents_SubAgentOldEmpty_Deleted(t *testing.T) {
	log := pruneLog(t)
	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)
	pruneSeedAgentAged(t, log, "parent", "", agent.StateRunning, now)
	pruneSeedAgentAged(t, log, "kid", "parent", agent.StateOrphaned, old)

	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "kid" {
		t.Fatalf("want pruned=[kid], got %v", pruned)
	}
	if agentExists(t, log, "kid") {
		t.Error("old empty sub-agent should have been pruned")
	}
	if !agentExists(t, log, "parent") {
		t.Error("running parent must never be pruned")
	}
}

// A sub-agent that fired a tool call did real work even if the
// supervisor lost its heartbeat - preserve so /agents history stays
// truthful. The fresh tool-call event itself bumps updated_at, but we
// explicitly pin the row to "old" so the age gate alone cannot save it.
func TestDeleteEmptyOrphanedAgents_SubAgentWithToolCall_Preserved(t *testing.T) {
	log := pruneLog(t)
	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)
	pruneSeedAgentAged(t, log, "parent", "", agent.StateRunning, now)
	pruneSeedAgentAged(t, log, "worker", "parent", agent.StateOrphaned, old)
	seedToolCall(t, log, "worker")

	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, id := range pruned {
		if id == "worker" {
			t.Fatalf("worker with a tool_call event was pruned: %v", pruned)
		}
	}
	if !agentExists(t, log, "worker") {
		t.Fatal("sub-agent that fired a tool call must be preserved")
	}
}

// A sub-agent that itself spawned a child must never be pruned even
// past the grace window: deleting it would FK-orphan its descendant
// and lose the tree's lineage. Mirrors PreservesOrphanWithChildren but
// at the sub-agent layer to cover the lineage check explicitly for
// the new branch of the predicate.
func TestDeleteEmptyOrphanedAgents_SubAgentWithChild_Preserved(t *testing.T) {
	log := pruneLog(t)
	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)
	pruneSeedAgentAged(t, log, "parent", "", agent.StateRunning, now)
	pruneSeedAgentAged(t, log, "mid", "parent", agent.StateOrphaned, old)
	// Grandchild keeps "mid" pinned even though "mid" has no events
	// of its own and is well past the grace window.
	pruneSeedAgentAged(t, log, "grand", "mid", agent.StateOrphaned, old)

	pruned, err := log.DeleteEmptyOrphanedAgents(context.Background(), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, id := range pruned {
		if id == "mid" {
			t.Fatalf("sub-agent with a child was pruned: %v", pruned)
		}
	}
	if !agentExists(t, log, "mid") {
		t.Fatal("sub-agent with a child was wrongly deleted")
	}
}
