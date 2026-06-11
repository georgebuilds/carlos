package agent

// Coverage for ApplyHandler.handle dispatch branches that the existing
// malformed-payload tests don't reach:
//
//   - lookupArtifact miss / non-plan kind → skip.
//   - no registered worktree → "no_worktree" outcome.
//   - accept → Apply success (applied + Close + ClearAgentWorktree).
//   - reject → Discard success and Discard failure → "apply_failed".
//   - accept → Apply failure → "apply_failed".
//   - Run's ctx-cancel and channel-close clean-shutdown returns, plus
//     the nil-receiver guard.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// handlerFakeWorktree is an internal-package AgentWorktree fake (the
// agent_test package has its own; this one lets the internal handle test
// drive Apply/Discard outcomes without git).
type handlerFakeWorktree struct {
	applyErr error
	discErr  error
	applied  bool
	discard  bool
	closed   bool
}

func (f *handlerFakeWorktree) Apply() error {
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied = true
	return nil
}

func (f *handlerFakeWorktree) Discard() error {
	if f.discErr != nil {
		return f.discErr
	}
	f.discard = true
	return nil
}

func (f *handlerFakeWorktree) Close() error {
	f.closed = true
	return nil
}

func newApplyTestHandler(t *testing.T) (*ApplyHandler, *Supervisor, *SQLiteEventLog) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(dir, "artifacts"))
	log, err := OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = CloseStateDB(log) })
	sup := NewSupervisor(log, nil, nil)
	t.Cleanup(sup.Shutdown)
	h := &ApplyHandler{Supervisor: sup, Log: log}
	return h, sup, log
}

// seedPlanArtifact inserts an artifacts row of the given kind attributed
// to producingAgent and returns its id. We insert directly (the agent FK
// requires an agents row, so we seed one first).
func seedPlanArtifact(t *testing.T, log *SQLiteEventLog, producingAgent, kind string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	created, err := NewStateChangeCreated(AgentCreated{ID: producingAgent, RootID: producingAgent, Title: "plan producer", Model: "fake"})
	if err != nil {
		t.Fatalf("created: %v", err)
	}
	if _, err := log.Append(ctx, Event{AgentID: producingAgent, TS: now, Type: EvtStateChange, Payload: created}); err != nil {
		t.Fatalf("append created: %v", err)
	}
	if err := log.InsertAgent(ctx, AgentRow{
		ID: producingAgent, RootID: producingAgent, State: StateRunning, Attempt: 1,
		Title: "plan producer", Model: "fake", CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	artID := "01HPLANARTIFACT" + kind
	if err := log.InsertArtifact(ctx, Artifact{
		ID: artID, AgentID: producingAgent, Path: "/tmp/plan", Kind: kind, SHA256: "abc", CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	return artID
}

func resolutionEvent(t *testing.T, typ EventType, artifactID string) Event {
	t.Helper()
	payload, err := json.Marshal(ApprovalResolutionPayload{ArtifactID: artifactID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return Event{Seq: 1, AgentID: resolverAgentID, TS: time.Now().UTC(), Type: typ, Payload: payload}
}

// lastOutcome reads the most recent apply_outcome artifact blob written
// for producingAgent and decodes it.
func lastOutcome(t *testing.T, log *SQLiteEventLog, producingAgent string) ApplyOutcome {
	t.Helper()
	ctx := context.Background()
	var path string
	err := log.DB().QueryRowContext(ctx,
		`SELECT path FROM artifacts WHERE agent_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`,
		producingAgent, ApplyOutcomeKind,
	).Scan(&path)
	if err != nil {
		t.Fatalf("query outcome path: %v", err)
	}
	var sha string
	if err := log.DB().QueryRowContext(ctx,
		`SELECT sha256 FROM artifacts WHERE agent_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`,
		producingAgent, ApplyOutcomeKind,
	).Scan(&sha); err != nil {
		t.Fatalf("query outcome sha: %v", err)
	}
	base := ArtifactBasePath("")
	blob, err := ReadArtifact(base, sha)
	if err != nil {
		t.Fatalf("read outcome blob: %v", err)
	}
	var o ApplyOutcome
	if err := json.Unmarshal(blob, &o); err != nil {
		t.Fatalf("decode outcome: %v", err)
	}
	return o
}

func TestApplyHandler_Run_NilReceiverGuard(t *testing.T) {
	h := &ApplyHandler{} // no supervisor, no log
	if err := h.Run(context.Background()); err == nil {
		t.Fatal("Run with nil supervisor/log should error")
	}
}

func TestApplyHandler_Handle_SkipsNonResolutionEvent(t *testing.T) {
	h, _, _ := newApplyTestHandler(t)
	// A non-accept/reject event is a no-op (early return). Should not panic.
	h.handle(context.Background(), Event{Type: EvtHeartbeat, Payload: []byte("{}")})
}

func TestApplyHandler_Handle_SkipsNonPlanArtifact(t *testing.T) {
	h, _, log := newApplyTestHandler(t)
	artID := seedPlanArtifact(t, log, "01HNOTPLAN", ArtifactKindText) // not a plan
	h.handle(context.Background(), resolutionEvent(t, EvtApprovalAccepted, artID))
	// No outcome should be written for a non-plan artifact.
	var n int
	if err := log.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM artifacts WHERE kind = ?`, ApplyOutcomeKind).Scan(&n); err != nil {
		t.Fatalf("count outcomes: %v", err)
	}
	if n != 0 {
		t.Fatalf("non-plan artifact should produce no outcome, got %d", n)
	}
}

func TestApplyHandler_Handle_MissingArtifactSkips(t *testing.T) {
	h, _, log := newApplyTestHandler(t)
	// No artifact row for this id → lookupArtifact returns ok=false.
	h.handle(context.Background(), resolutionEvent(t, EvtApprovalAccepted, "01HGHOSTARTIFACT"))
	var n int
	_ = log.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM artifacts WHERE kind = ?`, ApplyOutcomeKind).Scan(&n)
	if n != 0 {
		t.Fatalf("missing artifact should produce no outcome, got %d", n)
	}
}

func TestApplyHandler_Handle_NoWorktreeOutcome(t *testing.T) {
	h, _, log := newApplyTestHandler(t)
	artID := seedPlanArtifact(t, log, "01HNOWT", ArtifactKindPlan)
	// No SetAgentWorktree → "no_worktree" outcome.
	h.handle(context.Background(), resolutionEvent(t, EvtApprovalAccepted, artID))
	o := lastOutcome(t, log, "01HNOWT")
	if o.Status != "no_worktree" {
		t.Fatalf("status = %q, want no_worktree", o.Status)
	}
	if o.PlanArtifactID != artID {
		t.Errorf("outcome plan id = %q, want %q", o.PlanArtifactID, artID)
	}
}

func TestApplyHandler_Handle_AcceptApplies(t *testing.T) {
	h, sup, log := newApplyTestHandler(t)
	artID := seedPlanArtifact(t, log, "01HACCEPT", ArtifactKindPlan)
	wt := &handlerFakeWorktree{}
	sup.SetAgentWorktree("01HACCEPT", wt)

	h.handle(context.Background(), resolutionEvent(t, EvtApprovalAccepted, artID))

	if !wt.applied {
		t.Error("worktree.Apply was not called")
	}
	if !wt.closed {
		t.Error("worktree.Close was not called after successful apply")
	}
	if _, ok := sup.AgentWorktreeFor("01HACCEPT"); ok {
		t.Error("worktree should be cleared after successful apply")
	}
	o := lastOutcome(t, log, "01HACCEPT")
	if o.Status != "applied" {
		t.Fatalf("status = %q, want applied", o.Status)
	}
}

func TestApplyHandler_Handle_AcceptApplyFailure(t *testing.T) {
	h, sup, log := newApplyTestHandler(t)
	artID := seedPlanArtifact(t, log, "01HAPPLYFAIL", ArtifactKindPlan)
	wt := &handlerFakeWorktree{applyErr: errors.New("ff-only refused: parent moved")}
	sup.SetAgentWorktree("01HAPPLYFAIL", wt)

	h.handle(context.Background(), resolutionEvent(t, EvtApprovalAccepted, artID))

	o := lastOutcome(t, log, "01HAPPLYFAIL")
	if o.Status != "apply_failed" {
		t.Fatalf("status = %q, want apply_failed", o.Status)
	}
	if o.Error == "" {
		t.Error("apply_failed outcome should carry the error string")
	}
	// On failure we keep the worktree registered so the user can retry.
	if _, ok := sup.AgentWorktreeFor("01HAPPLYFAIL"); !ok {
		t.Error("worktree should remain registered after a failed apply")
	}
}

func TestApplyHandler_Handle_RejectDiscards(t *testing.T) {
	h, sup, log := newApplyTestHandler(t)
	artID := seedPlanArtifact(t, log, "01HREJECT", ArtifactKindPlan)
	wt := &handlerFakeWorktree{}
	sup.SetAgentWorktree("01HREJECT", wt)

	h.handle(context.Background(), resolutionEvent(t, EvtApprovalRejected, artID))

	if !wt.discard {
		t.Error("worktree.Discard was not called")
	}
	o := lastOutcome(t, log, "01HREJECT")
	if o.Status != "discarded" {
		t.Fatalf("status = %q, want discarded", o.Status)
	}
	if _, ok := sup.AgentWorktreeFor("01HREJECT"); ok {
		t.Error("worktree should be cleared after a discard")
	}
}

func TestApplyHandler_Handle_RejectDiscardFailure(t *testing.T) {
	h, sup, log := newApplyTestHandler(t)
	artID := seedPlanArtifact(t, log, "01HDISCFAIL", ArtifactKindPlan)
	wt := &handlerFakeWorktree{discErr: errors.New("rm -rf failed")}
	sup.SetAgentWorktree("01HDISCFAIL", wt)

	h.handle(context.Background(), resolutionEvent(t, EvtApprovalRejected, artID))

	o := lastOutcome(t, log, "01HDISCFAIL")
	if o.Status != "apply_failed" {
		t.Fatalf("status = %q, want apply_failed", o.Status)
	}
	if o.Error == "" {
		t.Error("discard failure should carry the error string")
	}
}

// TestApplyHandler_Run_DispatchesEvent runs the full loop: Run
// subscribes, AcceptApproval publishes a resolution event for a plan
// artifact, and the handler applies the registered worktree. Covers the
// `case ev, ok := <-ch` happy branch dispatching into handle.
func TestApplyHandler_Run_DispatchesEvent(t *testing.T) {
	h, sup, log := newApplyTestHandler(t)
	artID := seedPlanArtifact(t, log, "01HRUNDISPATCH", ArtifactKindPlan)
	wt := &handlerFakeWorktree{}
	sup.SetAgentWorktree("01HRUNDISPATCH", wt)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- h.Run(ctx) }()

	// Give Run a moment to register its subscription before we publish.
	time.Sleep(50 * time.Millisecond)
	if _, err := AcceptApproval(context.Background(), log, artID, ""); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// Poll for the apply to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := sup.AgentWorktreeFor("01HRUNDISPATCH"); !ok {
			break // cleared → apply succeeded
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := sup.AgentWorktreeFor("01HRUNDISPATCH"); ok {
		t.Fatal("Run did not dispatch the accept event to handle (worktree still registered)")
	}
	if !wt.applied {
		t.Error("worktree was not applied via the Run loop")
	}
	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestApplyHandler_Run_CtxCancelCleanShutdown drives Run through the
// ctx-cancel path: it returns nil on cancellation.
func TestApplyHandler_Run_CtxCancelCleanShutdown(t *testing.T) {
	h, _, _ := newApplyTestHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run on ctx-cancel should return nil, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
