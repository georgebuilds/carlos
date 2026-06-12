package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/tools"
)

// newLineageSupervisor is newTestSupervisor's sibling that also returns
// the event log, so lineage tests can seed the parent thread row and
// inspect the child's projection row afterwards.
func newLineageSupervisor(t *testing.T, script fake.Script) (*agent.Supervisor, *agent.SQLiteEventLog) {
	t.Helper()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open eventlog: %v", err)
	}
	sup := agent.NewSupervisor(log, fake.New("test", script), tools.NewRegistry())
	t.Cleanup(func() {
		sup.Shutdown()
		_ = log.Close()
	})
	return sup, log
}

func seedThreadRow(t *testing.T, log *agent.SQLiteEventLog, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID: id, RootID: id, State: agent.StateRunning, Attempt: 1,
		Title: "chat thread", CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("seed thread row: %v", err)
	}
}

func childScript() fake.Script {
	return fake.Script{
		{Kind: providers.EventTextDelta, Text: "done."},
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	}
}

func execAgentTool(t *testing.T, ctx context.Context, sup *agent.Supervisor) string {
	t.Helper()
	tool := agent.NewAgentTool(sup)
	in, _ := json.Marshal(map[string]any{
		"objective":      "do the thing",
		"output_format":  "one line",
		"tool_allowlist": []string{},
	})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := tool.Execute(ctx, in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result struct {
		AgentID string `json:"agent_id"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, out)
	}
	if result.Error != "" {
		t.Fatalf("tool returned error: %s", result.Error)
	}
	if result.AgentID == "" {
		t.Fatal("agent_id empty")
	}
	return result.AgentID
}

// TestSpawnParentContext_RoundTrip pins the ctx helpers themselves:
// installed value reads back; absent value reads ""; empty id is a no-op.
func TestSpawnParentContext_RoundTrip(t *testing.T) {
	base := context.Background()
	if got := agent.SpawnParentFromContext(base); got != "" {
		t.Errorf("bare ctx spawn parent = %q, want \"\"", got)
	}
	ctx := agent.WithSpawnParent(base, "thread-1")
	if got := agent.SpawnParentFromContext(ctx); got != "thread-1" {
		t.Errorf("spawn parent = %q, want thread-1", got)
	}
	// Empty id installs nothing (returns the ctx unchanged).
	if got := agent.SpawnParentFromContext(agent.WithSpawnParent(base, "")); got != "" {
		t.Errorf("empty-id install leaked %q", got)
	}
}

// TestAgentTool_SpawnParentFromContext is the roster bug's root-cause
// regression: when ctx carries a spawn parent (the chat loop's thread
// id), the spawned child's projection row must record parent_id AND
// root_id = that thread - which is exactly what keeps it out of
// ListUserSessions (the web roster / TUI picker) and inside the
// thread's children listing.
func TestAgentTool_SpawnParentFromContext(t *testing.T) {
	sup, log := newLineageSupervisor(t, childScript())
	const threadID = "thread-roster-1"
	seedThreadRow(t, log, threadID)

	childID := execAgentTool(t, agent.WithSpawnParent(context.Background(), threadID), sup)

	row, ok, err := log.GetAgent(context.Background(), childID)
	if err != nil || !ok {
		t.Fatalf("GetAgent(%s): ok=%v err=%v", childID, ok, err)
	}
	if row.ParentID != threadID {
		t.Errorf("child parent_id = %q, want %q", row.ParentID, threadID)
	}
	if row.RootID != threadID {
		t.Errorf("child root_id = %q, want %q (thread lineage)", row.RootID, threadID)
	}

	// The roster must list the thread only - never the finished child.
	sessions, err := agent.ListUserSessions(context.Background(), log, "")
	if err != nil {
		t.Fatalf("ListUserSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != threadID {
		ids := make([]string, 0, len(sessions))
		for _, s := range sessions {
			ids = append(ids, s.ID)
		}
		t.Errorf("roster = %v, want [%s] only", ids, threadID)
	}

	// And the thread's children listing must surface the finished child.
	kids, err := agent.ListChildSnapshots(context.Background(), log, threadID)
	if err != nil {
		t.Fatalf("ListChildSnapshots: %v", err)
	}
	if len(kids) != 1 || kids[0].AgentID != childID {
		t.Fatalf("children = %+v, want the one finished child %s", kids, childID)
	}
	if kids[0].State != agent.StateDone {
		t.Errorf("finished child state = %s, want done", kids[0].State)
	}
}

// TestAgentTool_NoSpawnParentStaysTopLevel is the bad-path companion: a
// ctx without a spawn parent (headless `please`, legacy callers) keeps
// the old top-level spawn shape - parent_id NULL, self root_id.
func TestAgentTool_NoSpawnParentStaysTopLevel(t *testing.T) {
	sup, log := newLineageSupervisor(t, childScript())

	childID := execAgentTool(t, context.Background(), sup)

	row, ok, err := log.GetAgent(context.Background(), childID)
	if err != nil || !ok {
		t.Fatalf("GetAgent(%s): ok=%v err=%v", childID, ok, err)
	}
	if row.ParentID != "" {
		t.Errorf("legacy spawn parent_id = %q, want \"\"", row.ParentID)
	}
	if row.RootID != childID {
		t.Errorf("legacy spawn root_id = %q, want self %s", row.RootID, childID)
	}
}

// notifierRecorder collects child-notifier invocations.
type notifierRecorder struct {
	mu  sync.Mutex
	got []string
}

func (n *notifierRecorder) record(parentID string) {
	n.mu.Lock()
	n.got = append(n.got, parentID)
	n.mu.Unlock()
}

func (n *notifierRecorder) calls() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.got...)
}

// TestSupervisor_ChildNotifierFiresOnLifecycleEdges drives a full spawn
// round-trip under a real parent and asserts the notifier fired - at
// minimum for the spawn registration and the terminal classify - always
// with the parent's id. This is the live half of the web crew column:
// each notification re-publishes a children snapshot over SSE.
func TestSupervisor_ChildNotifierFiresOnLifecycleEdges(t *testing.T) {
	sup, log := newLineageSupervisor(t, childScript())
	const threadID = "thread-notify-1"
	seedThreadRow(t, log, threadID)

	rec := &notifierRecorder{}
	sup.SetChildNotifier(rec.record)

	_, resultCh, err := sup.Spawn(context.Background(), threadID, agent.SpawnContract{
		Objective: "notify me", OutputFormat: "n/a",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	select {
	case <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for child result")
	}

	calls := rec.calls()
	if len(calls) < 2 {
		t.Fatalf("notifier calls = %d (%v), want >= 2 (spawned + terminal)", len(calls), calls)
	}
	for i, got := range calls {
		if got != threadID {
			t.Errorf("call %d notified %q, want %q", i, got, threadID)
		}
	}
}

// TestSupervisor_ChildNotifierSkipsTopLevelSpawns: a parentless spawn
// has no thread to update; the notifier must stay silent.
func TestSupervisor_ChildNotifierSkipsTopLevelSpawns(t *testing.T) {
	sup, _ := newLineageSupervisor(t, childScript())

	rec := &notifierRecorder{}
	sup.SetChildNotifier(rec.record)

	_, resultCh, err := sup.Spawn(context.Background(), "", agent.SpawnContract{Objective: "loner"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	select {
	case <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for child result")
	}
	if calls := rec.calls(); len(calls) != 0 {
		t.Errorf("notifier fired for a top-level spawn: %v", calls)
	}
}
