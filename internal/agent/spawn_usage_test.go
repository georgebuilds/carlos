package agent_test

// Regression coverage for the crew-column stats bug: a finished
// sub-agent reported "0 tok / $0.00 / no last tool" everywhere (web
// crew rail, TUI inline panel, manage roster) because runChild never
// persisted the child's tool activity or its Tracker spend - the
// events table held only lifecycle rows and the agents projection
// columns stayed at their zero defaults. These tests drive a real
// Spawn round-trip and assert the durable read paths
// (ListChildSnapshots + the agents row + the event log) now carry the
// child's terminal state, last tool, and non-zero token/cost spend.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/tools"
)

// spawnToolChild runs one child to completion under a tool-using
// scripted provider (turn 1: tool_use "echo", turn 2: end_turn) and
// returns its id plus the log.
func spawnToolChild(t *testing.T, parentID string) (string, *agent.SQLiteEventLog) {
	t.Helper()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open eventlog: %v", err)
	}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	sup := agent.NewSupervisor(log, &toolUseProvider{toolName: "echo"}, reg)
	t.Cleanup(func() {
		sup.Shutdown()
		_ = log.Close()
	})
	if parentID != "" {
		seedThreadRow(t, log, parentID)
	}

	sub, ch, err := sup.Spawn(context.Background(), parentID, agent.SpawnContract{
		Objective:     "run the echo tool once and summarize",
		OutputFormat:  "one line",
		ToolAllowlist: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	res := drainResult(t, ch, 5*time.Second)
	if res.Err != nil {
		t.Fatalf("child run err: %v", res.Err)
	}
	return sub.ID, log
}

// TestRunChild_PersistsToolEventsAndUsage is the happy path: after a
// tool-using child finishes, the durable children read returns the
// terminal state, the last tool name, and non-zero token/cost spend.
func TestRunChild_PersistsToolEventsAndUsage(t *testing.T) {
	const threadID = "thread-usage-1"
	childID, log := spawnToolChild(t, threadID)
	ctx := context.Background()

	kids, err := agent.ListChildSnapshots(ctx, log, threadID)
	if err != nil {
		t.Fatalf("ListChildSnapshots: %v", err)
	}
	if len(kids) != 1 || kids[0].AgentID != childID {
		t.Fatalf("children = %+v, want exactly the finished child %s", kids, childID)
	}
	kid := kids[0]
	if kid.State != agent.StateDone {
		t.Errorf("state = %s, want done", kid.State)
	}
	if kid.LastTool != "echo" {
		t.Errorf("last_tool = %q, want echo (tool_call event not persisted?)", kid.LastTool)
	}
	if kid.Tokens <= 0 {
		t.Errorf("tokens = %d, want > 0 (tracker spend not flushed?)", kid.Tokens)
	}
	if kid.CostCents <= 0 {
		t.Errorf("cost = %d cents, want > 0", kid.CostCents)
	}

	// The child's own event namespace must hold the tool round-trip and
	// the usage flush - that's what makes its transcript inspectable.
	evs, err := log.Read(ctx, childID, 0)
	if err != nil {
		t.Fatalf("read child events: %v", err)
	}
	var sawCall, sawResult bool
	var usage agent.TokenUsage
	var usageEvents int
	for _, ev := range evs {
		switch ev.Type {
		case agent.EvtToolCall:
			sawCall = true
			var tc agent.ToolCall
			if err := json.Unmarshal(ev.Payload, &tc); err != nil || tc.Name != "echo" {
				t.Errorf("tool_call payload = %s (err %v), want name echo", ev.Payload, err)
			}
		case agent.EvtToolResult:
			sawResult = true
			var tr agent.ToolResult
			if err := json.Unmarshal(ev.Payload, &tr); err != nil || tr.IsError {
				t.Errorf("tool_result payload = %s (err %v), want non-error echo result", ev.Payload, err)
			}
		case agent.EvtTokenUsage:
			usageEvents++
			if err := json.Unmarshal(ev.Payload, &usage); err != nil {
				t.Errorf("token_usage payload unmarshal: %v", err)
			}
		}
	}
	if !sawCall || !sawResult {
		t.Errorf("child events: tool_call=%v tool_result=%v, want both persisted", sawCall, sawResult)
	}
	if usageEvents != 1 {
		t.Fatalf("token_usage events = %d, want exactly 1 (single flush at termination)", usageEvents)
	}

	// Two-step write consistency: the agents projection row must carry
	// exactly the deltas the token_usage event recorded, so a SoT replay
	// reproduces the same columns.
	row, ok, err := log.GetAgent(ctx, childID)
	if err != nil || !ok {
		t.Fatalf("GetAgent: ok=%v err=%v", ok, err)
	}
	if row.TokensIn != usage.DeltaIn || row.TokensOut != usage.DeltaOut || row.CostCents != usage.DeltaCost {
		t.Errorf("agents row spend (in=%d out=%d cost=%d) != token_usage event (in=%d out=%d cost=%d)",
			row.TokensIn, row.TokensOut, row.CostCents, usage.DeltaIn, usage.DeltaOut, usage.DeltaCost)
	}
}

// TestRunChild_PureReasoningChildRecordsSpend: a child with NO tools
// still burns provider calls; its spend must land while last_tool stays
// honestly empty (and ListChildSnapshots must not drop the row for it).
func TestRunChild_PureReasoningChildRecordsSpend(t *testing.T) {
	const threadID = "thread-usage-2"
	sup, log := newLineageSupervisor(t, childScript())
	seedThreadRow(t, log, threadID)

	childID := execAgentTool(t, agent.WithSpawnParent(context.Background(), threadID), sup)

	kids, err := agent.ListChildSnapshots(context.Background(), log, threadID)
	if err != nil {
		t.Fatalf("ListChildSnapshots: %v", err)
	}
	if len(kids) != 1 || kids[0].AgentID != childID {
		t.Fatalf("children = %+v, want the finished child %s", kids, childID)
	}
	if kids[0].LastTool != "" {
		t.Errorf("last_tool = %q, want empty (child ran no tools)", kids[0].LastTool)
	}
	if kids[0].Tokens <= 0 {
		t.Errorf("tokens = %d, want > 0 even for a pure-reasoning child", kids[0].Tokens)
	}
	if kids[0].State != agent.StateDone {
		t.Errorf("state = %s, want done", kids[0].State)
	}
}

// TestAddAgentUsage covers the projection-column accumulator directly:
// happy (accumulates across calls, clamps negative deltas) and bad
// (unknown id errors, closed DB errors).
func TestAddAgentUsage(t *testing.T) {
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open eventlog: %v", err)
	}
	defer log.Close()
	ctx := context.Background()
	seedThreadRow(t, log, "a1")

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := log.AddAgentUsage(ctx, "a1", 100, 40, 3, now); err != nil {
		t.Fatalf("first add: %v", err)
	}
	// Accumulate, with negatives clamped to zero (bad provider reports
	// must never shrink the meter) - per field, all three dimensions.
	if err := log.AddAgentUsage(ctx, "a1", 10, -5, 1, now.Add(time.Second)); err != nil {
		t.Fatalf("second add: %v", err)
	}
	if err := log.AddAgentUsage(ctx, "a1", -100, 0, -100, now.Add(2*time.Second)); err != nil {
		t.Fatalf("all-negative add: %v", err)
	}
	row, ok, err := log.GetAgent(ctx, "a1")
	if err != nil || !ok {
		t.Fatalf("GetAgent: ok=%v err=%v", ok, err)
	}
	if row.TokensIn != 110 || row.TokensOut != 40 || row.CostCents != 4 {
		t.Errorf("row spend = in=%d out=%d cost=%d, want 110/40/4", row.TokensIn, row.TokensOut, row.CostCents)
	}
	if got := row.UpdatedAt.UTC(); got.Before(now) {
		t.Errorf("updated_at = %s, want bumped to >= %s", got, now)
	}

	// Unknown agent: explicit error, not a silent no-op.
	if err := log.AddAgentUsage(ctx, "ghost", 1, 1, 1, now); err == nil {
		t.Error("unknown agent: want error, got nil")
	}
	// Closed DB: storage errors surface.
	_ = log.Close()
	if err := log.AddAgentUsage(ctx, "a1", 1, 1, 1, now); err == nil {
		t.Error("closed DB: want error, got nil")
	}
}

// TestTracker_InOutSplit pins the Tracker's new request/response split:
// Snapshot must expose the per-side shares (what the usage flush
// persists) alongside the combined total the budget gate uses.
func TestTracker_InOutSplit(t *testing.T) {
	tr := agent.NewTracker(nil)
	tr.Add(100, 40, 3)
	tr.Add(-7, 10, -2) // negatives clamp per-field, the rest still lands
	snap := tr.Snapshot()
	if snap.TokensIn != 100 || snap.TokensOut != 50 {
		t.Errorf("split = in=%d out=%d, want 100/50", snap.TokensIn, snap.TokensOut)
	}
	if snap.Tokens != 150 {
		t.Errorf("total = %d, want 150 (sum of the split)", snap.Tokens)
	}
	if snap.CostCents != 3 {
		t.Errorf("cost = %d, want 3", snap.CostCents)
	}

	// Parent roll-up carries the split too.
	parent := agent.NewTracker(nil)
	child := agent.NewTracker(parent)
	child.Add(5, 7, 1)
	psnap := parent.Snapshot()
	if psnap.TokensIn != 5 || psnap.TokensOut != 7 || psnap.CostCents != 1 {
		t.Errorf("parent split = %+v, want in=5 out=7 cost=1", psnap)
	}
}
