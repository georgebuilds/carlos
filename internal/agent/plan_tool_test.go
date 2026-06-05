package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// fakeWorktree is a hand-scripted PlanWorktree + AgentWorktree. The
// caller wires the Diff / ChangedFiles values to drive PlanTool tests
// + the apply / discard observable state to drive apply_handler tests.
type fakeWorktree struct {
	mu       sync.Mutex
	diff     []byte
	diffErr  error
	files    []string
	fileErr  error
	applied  bool
	applyErr error
	discard  bool
	discErr  error
	closed   bool
}

func (f *fakeWorktree) Diff() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.diff, f.diffErr
}
func (f *fakeWorktree) ChangedFiles() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files, f.fileErr
}
func (f *fakeWorktree) Apply() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied = true
	return nil
}
func (f *fakeWorktree) Discard() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.discErr != nil {
		return f.discErr
	}
	f.discard = true
	return nil
}
func (f *fakeWorktree) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func openPlanLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(tmp, "artifacts"))
	log, err := agent.OpenSQLiteEventLog(filepath.Join(tmp, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func seedPlanAgent(t *testing.T, log *agent.SQLiteEventLog, id string) {
	t.Helper()
	ctx := context.Background()
	created, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: id, RootID: id, Title: id, Model: "fake",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: created,
	}); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	now := time.Now().UTC()
	if err := log.InsertAgent(ctx, agent.AgentRow{
		ID: id, RootID: id, State: agent.StateRunning, Attempt: 1,
		Title: id, Model: "fake", CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
}

// === PlanTool ===============================================================

func TestPlanTool_HappyPath(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-plan-1"
	seedPlanAgent(t, log, id)
	wt := &fakeWorktree{
		diff:  []byte("diff --git a/foo.go b/foo.go\n@@ ...\n+new line\n"),
		files: []string{"foo.go"},
	}
	tool := agent.NewPlanTool(id, wt, log)
	in, _ := json.Marshal(map[string]any{
		"title":         "refactor foo",
		"summary":       "rename Bar to Baz; tests still pass",
		"files_changed": []string{"foo.go"},
	})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res agent.PlanResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !res.Queued || res.PlanID == "" || res.MetadataID == "" {
		t.Errorf("result missing fields: %+v", res)
	}
	pending, err := agent.ListPendingApprovals(context.Background(), log)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if pending[0].Title != "refactor foo" {
		t.Errorf("title = %q, want %q", pending[0].Title, "refactor foo")
	}
	if pending[0].Ref.Kind != agent.ArtifactKindPlan {
		t.Errorf("ref kind = %q, want plan", pending[0].Ref.Kind)
	}
}

func TestPlanTool_EmptyDiffErrors(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-plan-2"
	seedPlanAgent(t, log, id)
	tool := agent.NewPlanTool(id, &fakeWorktree{diff: nil}, log)
	in, _ := json.Marshal(map[string]any{"title": "x", "summary": "y"})
	_, err := tool.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for empty diff, got nil")
	}
}

func TestPlanTool_MissingTitleErrors(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-plan-3"
	seedPlanAgent(t, log, id)
	tool := agent.NewPlanTool(id, &fakeWorktree{diff: []byte("x")}, log)
	in, _ := json.Marshal(map[string]any{"summary": "y"})
	_, err := tool.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for missing title, got nil")
	}
}

func TestPlanTool_DiffErrorBubbles(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-plan-4"
	seedPlanAgent(t, log, id)
	tool := agent.NewPlanTool(id, &fakeWorktree{diffErr: errors.New("git wedged")}, log)
	in, _ := json.Marshal(map[string]any{"title": "x", "summary": "y"})
	_, err := tool.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected error when Diff fails, got nil")
	}
}

func TestPlanTool_NilWorktreeErrors(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-plan-5"
	seedPlanAgent(t, log, id)
	tool := agent.NewPlanTool(id, nil, log)
	in, _ := json.Marshal(map[string]any{"title": "x", "summary": "y"})
	_, err := tool.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for nil worktree, got nil")
	}
}

// === ApplyHandler ===========================================================

func newSupervisorForHandler(t *testing.T, log *agent.SQLiteEventLog) *agent.Supervisor {
	t.Helper()
	// Provider/tools are nil here — the handler doesn't spawn agents,
	// it just reads the worktrees map.
	return agent.NewSupervisor(log, nil, nil)
}

// waitForOutcome polls the artifacts table until an apply_outcome row
// for planArtifactID appears. The handler is async; tests need a tight
// poll loop with a deadline to stay deterministic without sleeps in the
// production code.
func waitForOutcome(t *testing.T, log *agent.SQLiteEventLog, planArtifactID string) agent.ApplyOutcome {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := log.DB().Query(
			`SELECT sha256 FROM artifacts WHERE kind = ?`, agent.ApplyOutcomeKind,
		)
		if err == nil {
			for rows.Next() {
				var sha string
				_ = rows.Scan(&sha)
				blob, err := agent.ReadArtifact(agent.ArtifactBasePath(""), sha)
				if err != nil {
					continue
				}
				var o agent.ApplyOutcome
				if err := json.Unmarshal(blob, &o); err != nil {
					continue
				}
				if o.PlanArtifactID == planArtifactID {
					rows.Close()
					return o
				}
			}
			rows.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no apply_outcome surfaced within deadline for plan %s", planArtifactID)
	return agent.ApplyOutcome{}
}

func TestApplyHandler_AcceptCallsApply(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-handler-1"
	seedPlanAgent(t, log, id)
	wt := &fakeWorktree{
		diff:  []byte("d"),
		files: []string{"a.go"},
	}
	sup := newSupervisorForHandler(t, log)
	sup.SetAgentWorktree(id, wt)

	// Spin the handler.
	h := &agent.ApplyHandler{Supervisor: sup, Log: log}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()
	time.Sleep(50 * time.Millisecond) // let Subscribe register

	// Queue + accept a plan.
	tool := agent.NewPlanTool(id, wt, log)
	in, _ := json.Marshal(map[string]any{"title": "do thing", "summary": "...."})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	var res agent.PlanResult
	_ = json.Unmarshal(out, &res)
	if _, err := agent.AcceptApproval(context.Background(), log, res.PlanID, ""); err != nil {
		t.Fatal(err)
	}

	o := waitForOutcome(t, log, res.PlanID)
	if o.Status != "applied" {
		t.Errorf("status = %q, want applied (err=%q)", o.Status, o.Error)
	}
	wt.mu.Lock()
	if !wt.applied {
		t.Error("worktree.Apply not called")
	}
	if !wt.closed {
		t.Error("worktree.Close not called after apply")
	}
	wt.mu.Unlock()
}

func TestApplyHandler_RejectCallsDiscard(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-handler-2"
	seedPlanAgent(t, log, id)
	wt := &fakeWorktree{diff: []byte("d"), files: []string{"a.go"}}
	sup := newSupervisorForHandler(t, log)
	sup.SetAgentWorktree(id, wt)

	h := &agent.ApplyHandler{Supervisor: sup, Log: log}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	tool := agent.NewPlanTool(id, wt, log)
	in, _ := json.Marshal(map[string]any{"title": "nope", "summary": "..."})
	out, _ := tool.Execute(context.Background(), in)
	var res agent.PlanResult
	_ = json.Unmarshal(out, &res)
	if _, err := agent.RejectApproval(context.Background(), log, res.PlanID, "not now"); err != nil {
		t.Fatal(err)
	}

	o := waitForOutcome(t, log, res.PlanID)
	if o.Status != "discarded" {
		t.Errorf("status = %q, want discarded", o.Status)
	}
	wt.mu.Lock()
	if !wt.discard {
		t.Error("worktree.Discard not called")
	}
	if !wt.closed {
		t.Error("worktree.Close not called after discard")
	}
	wt.mu.Unlock()
}

func TestApplyHandler_NoWorktreeRecordsAnnotation(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-handler-3"
	seedPlanAgent(t, log, id)
	wt := &fakeWorktree{diff: []byte("d"), files: []string{"a.go"}}
	sup := newSupervisorForHandler(t, log)
	// Deliberately DO NOT register the worktree.

	h := &agent.ApplyHandler{Supervisor: sup, Log: log}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	tool := agent.NewPlanTool(id, wt, log)
	in, _ := json.Marshal(map[string]any{"title": "x", "summary": "y"})
	out, _ := tool.Execute(context.Background(), in)
	var res agent.PlanResult
	_ = json.Unmarshal(out, &res)
	_, _ = agent.AcceptApproval(context.Background(), log, res.PlanID, "")

	o := waitForOutcome(t, log, res.PlanID)
	if o.Status != "no_worktree" {
		t.Errorf("status = %q, want no_worktree", o.Status)
	}
}

func TestApplyHandler_ApplyErrorSurfacedNotPanicked(t *testing.T) {
	log := openPlanLog(t)
	const id = "agent-handler-4"
	seedPlanAgent(t, log, id)
	wt := &fakeWorktree{
		diff:     []byte("d"),
		files:    []string{"a.go"},
		applyErr: errors.New("non-ff merge"),
	}
	sup := newSupervisorForHandler(t, log)
	sup.SetAgentWorktree(id, wt)

	h := &agent.ApplyHandler{Supervisor: sup, Log: log}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	tool := agent.NewPlanTool(id, wt, log)
	in, _ := json.Marshal(map[string]any{"title": "x", "summary": "y"})
	out, _ := tool.Execute(context.Background(), in)
	var res agent.PlanResult
	_ = json.Unmarshal(out, &res)
	_, _ = agent.AcceptApproval(context.Background(), log, res.PlanID, "")

	o := waitForOutcome(t, log, res.PlanID)
	if o.Status != "apply_failed" {
		t.Errorf("status = %q, want apply_failed", o.Status)
	}
	if o.Error == "" {
		t.Error("expected non-empty Error string")
	}
}

func TestSupervisor_AgentWorktreeMap(t *testing.T) {
	log := openPlanLog(t)
	sup := newSupervisorForHandler(t, log)
	wt := &fakeWorktree{}
	sup.SetAgentWorktree("a", wt)
	got, ok := sup.AgentWorktreeFor("a")
	if !ok || got == nil {
		t.Fatal("AgentWorktreeFor(a) returned nothing after Set")
	}
	sup.ClearAgentWorktree("a")
	if _, ok := sup.AgentWorktreeFor("a"); ok {
		t.Error("AgentWorktreeFor(a) returned a value after Clear")
	}
}
