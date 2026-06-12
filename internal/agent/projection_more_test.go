package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Covers projection.Apply error paths + State.String / IsTerminal for
// states the existing suite didn't reach. These are critical to the SoT
// guarantee; schema drift must be loud at replay time, not silently
// drop rows.

// helperEvent creates an Event for the given id and payload.
func helperEvent(t *testing.T, agentID string, typ agent.EventType, payload []byte) agent.Event {
	t.Helper()
	return agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    typ,
		Payload: payload,
	}
}

func TestProjection_Apply_StateChangeUnknownKind(t *testing.T) {
	p := agent.NewProjection()
	bad := []byte(`{"kind":"weird"}`)
	err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, bad))
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("want unknown-kind err, got %v", err)
	}
}

func TestProjection_Apply_StateChangeMissingKind(t *testing.T) {
	p := agent.NewProjection()
	bad := []byte(`{}`)
	err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, bad))
	if err == nil || !strings.Contains(err.Error(), "missing required `kind`") {
		t.Fatalf("want missing-kind err, got %v", err)
	}
}

func TestProjection_Apply_StateChangeCreatedMissingPayload(t *testing.T) {
	p := agent.NewProjection()
	bad := []byte(`{"kind":"created"}`) // no Created sub-object
	err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, bad))
	if err == nil || !strings.Contains(err.Error(), "without created payload") {
		t.Fatalf("want missing created err, got %v", err)
	}
}

func TestProjection_Apply_StateChangeCreatedDuplicate(t *testing.T) {
	p := agent.NewProjection()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "fake",
	})
	if err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, created)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, created))
	if err == nil || !strings.Contains(err.Error(), "already-known agent") {
		t.Fatalf("want duplicate-known err, got %v", err)
	}
}

func TestProjection_Apply_StateChangeTransitionUnknownAgent(t *testing.T) {
	p := agent.NewProjection()
	transition, _ := agent.NewStateChangeTransition(agent.StateRunning)
	err := p.Apply(helperEvent(t, "ghost", agent.EvtStateChange, transition))
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("want unknown-agent err, got %v", err)
	}
}

func TestProjection_Apply_StateChangeTransitionMissingTo(t *testing.T) {
	p := agent.NewProjection()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "fake",
	})
	if err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, created)); err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"kind":"transition"}`)
	err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, bad))
	if err == nil || !strings.Contains(err.Error(), "without `to`") {
		t.Fatalf("want missing-to err, got %v", err)
	}
}

func TestProjection_Apply_StateChangeMalformedJSON(t *testing.T) {
	p := agent.NewProjection()
	err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, []byte("not-json")))
	if err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("want unmarshal err, got %v", err)
	}
}

func TestProjection_Apply_TokenUsageUnknownAgent(t *testing.T) {
	p := agent.NewProjection()
	pl, _ := json.Marshal(agent.TokenUsage{DeltaIn: 1, DeltaOut: 2, DeltaCost: 3})
	err := p.Apply(helperEvent(t, "ghost", agent.EvtTokenUsage, pl))
	if err == nil || !strings.Contains(err.Error(), "token_usage for unknown") {
		t.Fatalf("want unknown-agent err, got %v", err)
	}
}

func TestProjection_Apply_TokenUsageMalformedPayload(t *testing.T) {
	p := agent.NewProjection()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "fake",
	})
	if err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, created)); err != nil {
		t.Fatal(err)
	}
	err := p.Apply(helperEvent(t, "a", agent.EvtTokenUsage, []byte("not-json")))
	if err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("want unmarshal err, got %v", err)
	}
}

func TestProjection_Apply_TokenUsageAccumulates(t *testing.T) {
	p := agent.NewProjection()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "fake",
	})
	if err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, created)); err != nil {
		t.Fatal(err)
	}
	pl, _ := json.Marshal(agent.TokenUsage{DeltaIn: 10, DeltaOut: 20, DeltaCost: 3})
	if err := p.Apply(helperEvent(t, "a", agent.EvtTokenUsage, pl)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := p.Apply(helperEvent(t, "a", agent.EvtTokenUsage, pl)); err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	r, ok := p.Get("a")
	if !ok {
		t.Fatal("agent missing")
	}
	if r.TokensIn != 20 || r.TokensOut != 40 || r.CostCents != 6 {
		t.Errorf("usage mis-applied: %+v", r)
	}
}

func TestProjection_Apply_ToolCallIncrements(t *testing.T) {
	p := agent.NewProjection()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "fake",
	})
	_ = p.Apply(helperEvent(t, "a", agent.EvtStateChange, created))
	for i := 0; i < 3; i++ {
		if err := p.Apply(helperEvent(t, "a", agent.EvtToolCall, []byte(`{}`))); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	r, _ := p.Get("a")
	if r.ToolCalls != 3 {
		t.Errorf("ToolCalls = %d, want 3", r.ToolCalls)
	}
}

func TestProjection_Apply_ToolCallUnknownAgent(t *testing.T) {
	p := agent.NewProjection()
	err := p.Apply(helperEvent(t, "ghost", agent.EvtToolCall, []byte(`{}`)))
	if err == nil || !strings.Contains(err.Error(), "tool_call for unknown") {
		t.Fatalf("want unknown-agent err, got %v", err)
	}
}

func TestProjection_Apply_ToolResultUpdatesTimestamp(t *testing.T) {
	p := agent.NewProjection()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "fake",
	})
	_ = p.Apply(helperEvent(t, "a", agent.EvtStateChange, created))
	if err := p.Apply(helperEvent(t, "a", agent.EvtToolResult, []byte(`{}`))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// ToolResult to an unknown agent is silently allowed (per the code).
	if err := p.Apply(helperEvent(t, "ghost", agent.EvtToolResult, []byte(`{}`))); err != nil {
		t.Fatalf("unknown-agent tool_result should be silently ignored, got %v", err)
	}
}

func TestProjection_Apply_HeartbeatUnknownAgent(t *testing.T) {
	p := agent.NewProjection()
	err := p.Apply(helperEvent(t, "ghost", agent.EvtHeartbeat, []byte(`{}`)))
	if err == nil || !strings.Contains(err.Error(), "heartbeat for unknown") {
		t.Fatalf("want unknown-agent err, got %v", err)
	}
}

func TestProjection_Apply_HeartbeatUpdatesLastHeartbeat(t *testing.T) {
	p := agent.NewProjection()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "fake",
	})
	t0 := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	_ = p.Apply(agent.Event{
		AgentID: "a", TS: t0, Type: agent.EvtStateChange, Payload: created,
	})
	t1 := t0.Add(5 * time.Second)
	if err := p.Apply(agent.Event{
		AgentID: "a", TS: t1, Type: agent.EvtHeartbeat, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	r, _ := p.Get("a")
	if !r.LastHeartbeatAt.Equal(t1) {
		t.Errorf("LastHeartbeatAt = %v, want %v", r.LastHeartbeatAt, t1)
	}
}

func TestProjection_Apply_UnknownEventTypeErrors(t *testing.T) {
	p := agent.NewProjection()
	err := p.Apply(helperEvent(t, "a", agent.EventType("totally-not-a-real-type"), []byte(`{}`)))
	if err == nil || !strings.Contains(err.Error(), "unknown event type") {
		t.Fatalf("want unknown-event err, got %v", err)
	}
}

// Touch every passive-passthrough EventType to cover the case label.
func TestProjection_Apply_PassiveEventTypes(t *testing.T) {
	p := agent.NewProjection()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "fake",
	})
	_ = p.Apply(helperEvent(t, "a", agent.EvtStateChange, created))
	for _, typ := range []agent.EventType{
		agent.EvtProviderCall,
		agent.EvtUserMessage,
		agent.EvtAssistantMessage,
		agent.EvtSteering,
		agent.EvtArtifactRef,
		agent.EvtSessionReset,
		agent.EvtResearchPhase,
		agent.EvtUserShellStart,
		agent.EvtUserShellEnd,
		agent.EvtGatewayInbound,
		agent.EvtGatewayOutbound,
		agent.EvtApprovalProposed,
		agent.EvtApprovalAccepted,
		agent.EvtApprovalRejected,
	} {
		if err := p.Apply(helperEvent(t, "a", typ, []byte(`{}`))); err != nil {
			t.Errorf("apply %s: %v", typ, err)
		}
		// And again for an unknown agent (the code silently no-ops).
		if err := p.Apply(helperEvent(t, "ghost", typ, []byte(`{}`))); err != nil {
			t.Errorf("apply %s for ghost should silently no-op, got %v", typ, err)
		}
	}
}

// Regression for the shipped bug where Apply rejected seven production
// event types (user_shell_*, gateway_*, approval_*) as "unknown event
// type": every /shell use painted a projection-error system note in chat
// and Replay/ReplayAll failed on logs containing them. They must be
// accepted as passive no-ops, while a genuinely unknown type still errors.
func TestProjection_Apply_ShippedEventTypesNotUnknown(t *testing.T) {
	p := agent.NewProjection()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "fake",
	})
	if err := p.Apply(helperEvent(t, "a", agent.EvtStateChange, created)); err != nil {
		t.Fatalf("seed created: %v", err)
	}
	before, _ := p.Get("a")
	for _, typ := range []agent.EventType{
		agent.EvtUserShellStart,
		agent.EvtUserShellEnd,
		agent.EvtGatewayInbound,
		agent.EvtGatewayOutbound,
		agent.EvtApprovalProposed,
		agent.EvtApprovalAccepted,
		agent.EvtApprovalRejected,
	} {
		// Happy path: known agent.
		ev := helperEvent(t, "a", typ, []byte(`{}`))
		ev.TS = before.UpdatedAt.Add(time.Second)
		if err := p.Apply(ev); err != nil {
			t.Errorf("apply %s: %v", typ, err)
		}
		r, _ := p.Get("a")
		if !r.UpdatedAt.Equal(ev.TS) {
			t.Errorf("apply %s did not bump UpdatedAt: got %v, want %v", typ, r.UpdatedAt, ev.TS)
		}
		// Happy path: unknown agent silently no-ops, matching the other
		// passive types.
		if err := p.Apply(helperEvent(t, "ghost", typ, []byte(`{}`))); err != nil {
			t.Errorf("apply %s for ghost should silently no-op, got %v", typ, err)
		}
	}
	// Bad path: a genuinely unknown type must still be rejected loudly.
	err := p.Apply(helperEvent(t, "a", agent.EventType("user_shell_bogus"), []byte(`{}`)))
	if err == nil || !strings.Contains(err.Error(), "unknown event type") {
		t.Fatalf("want unknown-event err for bogus type, got %v", err)
	}
}

// Regression: Replay over a log containing user_shell events must succeed.
// Before the fix it failed with `replay: apply seq=N: projection: unknown
// event type "user_shell_start"`.
func TestReplay_LogWithUserShellEventsSucceeds(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	ctx := context.Background()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "x", Model: "fake",
	})
	for _, ev := range []agent.Event{
		helperEvent(t, "a", agent.EvtStateChange, created),
		helperEvent(t, "a", agent.EvtUserShellStart, []byte(`{"command":"ls","cwd":"/tmp"}`)),
		helperEvent(t, "a", agent.EvtUserShellEnd, []byte(`{"exit_code":0}`)),
	} {
		if _, err := log.Append(ctx, ev); err != nil {
			t.Fatalf("append %s: %v", ev.Type, err)
		}
	}

	p, err := agent.Replay(ctx, log, "a")
	if err != nil {
		t.Fatalf("replay over user_shell events: %v", err)
	}
	if _, ok := p.Get("a"); !ok {
		t.Fatalf("replayed projection missing agent row")
	}

	// ReplayAll must survive the same log.
	if _, err := agent.ReplayAll(ctx, log); err != nil {
		t.Fatalf("replayAll over user_shell events: %v", err)
	}
}

func TestProjection_Get_MissingReturnsFalse(t *testing.T) {
	p := agent.NewProjection()
	if _, ok := p.Get("nothing"); ok {
		t.Errorf("Get on empty projection should be !ok")
	}
}

func TestProjection_SnapshotEmpty(t *testing.T) {
	p := agent.NewProjection()
	if got := p.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot of empty = %v, want empty", got)
	}
}

func TestReplay_NonExistentAgentReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	p, err := agent.Replay(context.Background(), log, "no-such")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if _, ok := p.Get("no-such"); ok {
		t.Errorf("replay should produce empty projection for missing agent")
	}
}

func TestReplayAll_EmptyLogReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	p, err := agent.ReplayAll(context.Background(), log)
	if err != nil {
		t.Fatalf("replayall: %v", err)
	}
	if got := p.Snapshot(); len(got) != 0 {
		t.Errorf("replayAll of empty log should be empty, got %d rows", len(got))
	}
}

func TestState_StringForEveryEnum(t *testing.T) {
	want := map[agent.State]string{
		agent.StateSpawning:      "spawning",
		agent.StateQueued:        "queued",
		agent.StateRunning:       "running",
		agent.StateAwaitingInput: "awaiting-input",
		agent.StateBlocked:       "blocked",
		agent.StatePausedByUser:  "paused-by-user",
		agent.StateCompacting:    "compacting",
		agent.StateCancelling:    "cancelling",
		agent.StateDone:          "done",
		agent.StateFailed:        "failed",
		agent.StateOrphaned:      "orphaned",
	}
	for st, expect := range want {
		if got := st.String(); got != expect {
			t.Errorf("State(%d).String() = %q, want %q", st, got, expect)
		}
	}
	// Out-of-range state - hit the default arm.
	if got := agent.State(99).String(); got != "unknown" {
		t.Errorf("unknown state should stringify as 'unknown', got %q", got)
	}
}

func TestState_IsTerminal(t *testing.T) {
	cases := []struct {
		state    agent.State
		terminal bool
	}{
		{agent.StateSpawning, false},
		{agent.StateRunning, false},
		{agent.StateAwaitingInput, false},
		{agent.StateDone, true},
		{agent.StateFailed, true},
		{agent.StateOrphaned, true},
	}
	for _, tc := range cases {
		if got := tc.state.IsTerminal(); got != tc.terminal {
			t.Errorf("State(%s).IsTerminal() = %v, want %v", tc.state, got, tc.terminal)
		}
	}
}

func TestTransition_TerminalStickyReturnsError(t *testing.T) {
	// Once terminal, no transition should escape.
	for _, st := range []agent.State{agent.StateDone, agent.StateFailed, agent.StateOrphaned} {
		next, err := agent.Transition(st, agent.EvSpawnSucceeded)
		if err == nil {
			t.Errorf("terminal state %s should refuse transitions, got next=%s", st, next)
		}
		if next != st {
			t.Errorf("terminal state %s should be sticky, got next=%s", st, next)
		}
	}
}

func TestTransition_HeartbeatLostFromAnyNonTerminalGoesToOrphaned(t *testing.T) {
	for _, st := range []agent.State{
		agent.StateSpawning, agent.StateQueued, agent.StateRunning,
		agent.StateAwaitingInput, agent.StateBlocked, agent.StatePausedByUser,
		agent.StateCompacting, agent.StateCancelling,
	} {
		next, err := agent.Transition(st, agent.EvHeartbeatLost)
		if err != nil {
			t.Errorf("heartbeat-lost from %s: err=%v", st, err)
		}
		if next != agent.StateOrphaned {
			t.Errorf("heartbeat-lost from %s = %s, want orphaned", st, next)
		}
	}
}

func TestTransition_AllCanonicalLegalEdges(t *testing.T) {
	type edge struct {
		from agent.State
		ev   agent.EventKind
		to   agent.State
	}
	for _, e := range []edge{
		{agent.StateSpawning, agent.EvSpawnSucceeded, agent.StateRunning},
		{agent.StateSpawning, agent.EvSpawnFailed, agent.StateFailed},
		{agent.StateQueued, agent.EvSpawnStarted, agent.StateSpawning},
		{agent.StateRunning, agent.EvAwaitingUserInput, agent.StateAwaitingInput},
		{agent.StateRunning, agent.EvExternalBlocked, agent.StateBlocked},
		{agent.StateRunning, agent.EvUserPaused, agent.StatePausedByUser},
		{agent.StateRunning, agent.EvCompactionStarted, agent.StateCompacting},
		{agent.StateRunning, agent.EvCancelRequested, agent.StateCancelling},
		{agent.StateRunning, agent.EvCompletedSuccess, agent.StateDone},
		{agent.StateRunning, agent.EvCompletedFailure, agent.StateFailed},
		{agent.StateAwaitingInput, agent.EvUserInputReceived, agent.StateRunning},
		{agent.StateAwaitingInput, agent.EvCancelRequested, agent.StateCancelling},
		{agent.StateBlocked, agent.EvExternalUnblocked, agent.StateRunning},
		{agent.StateBlocked, agent.EvCancelRequested, agent.StateCancelling},
		{agent.StatePausedByUser, agent.EvUserResumed, agent.StateRunning},
		{agent.StatePausedByUser, agent.EvCancelRequested, agent.StateCancelling},
		{agent.StateCompacting, agent.EvCompactionEnded, agent.StateRunning},
		{agent.StateCancelling, agent.EvDrainComplete, agent.StateDone},
		{agent.StateCancelling, agent.EvCompletedFailure, agent.StateFailed},
	} {
		next, err := agent.Transition(e.from, e.ev)
		if err != nil {
			t.Errorf("transition(%s, %d): err=%v want next=%s", e.from, e.ev, err, e.to)
			continue
		}
		if next != e.to {
			t.Errorf("transition(%s, %d) = %s, want %s", e.from, e.ev, next, e.to)
		}
	}
}

func TestTransition_IllegalEdges(t *testing.T) {
	type bad struct {
		from agent.State
		ev   agent.EventKind
	}
	for _, e := range []bad{
		{agent.StateSpawning, agent.EvUserPaused},     // not a spawning trigger
		{agent.StateQueued, agent.EvCompletedSuccess}, // queued has only EvSpawnStarted
		{agent.StateRunning, agent.EvSpawnSucceeded},  // not a running trigger
		{agent.StateCompacting, agent.EvCancelRequested},
		{agent.StateBlocked, agent.EvUserPaused},
	} {
		next, err := agent.Transition(e.from, e.ev)
		if err == nil {
			t.Errorf("transition(%s, %d) should be illegal, got next=%s", e.from, e.ev, next)
		}
		if next != e.from {
			t.Errorf("illegal transition should not move state: from=%s got=%s", e.from, next)
		}
	}
}
