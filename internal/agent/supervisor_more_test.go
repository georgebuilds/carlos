package agent_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// Supervisor branch tests: cap modes, worktree map edge cases, error
// paths in Spawn / Steer / Interrupt / Retry that the existing suite
// doesn't exercise.

// capturingProvider records the most recent Request.Model so tests can
// assert what the supervisor handed the child loop. Returns an empty
// end-of-turn stream so the child terminates immediately. Mutex-
// guarded because the supervisor spawns a goroutine for the child.
type capturingProvider struct {
	mu    sync.Mutex
	model string
}

func (p *capturingProvider) Name() string                         { return "capture" }
func (p *capturingProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p *capturingProvider) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.model = req.Model
	p.mu.Unlock()
	ch := make(chan providers.Event, 1)
	ch <- providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"}
	close(ch)
	return ch, nil
}
func (p *capturingProvider) lastModel() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model
}

func TestSupervisor_Spawn_NilProviderErrors(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	sup := agent.NewSupervisor(log, nil, nil) // no provider
	defer sup.Shutdown()
	if _, _, err := sup.Spawn(context.Background(), "", agent.SpawnContract{Objective: "x"}); err == nil {
		t.Fatal("Spawn with nil provider should error")
	}
}

func TestSupervisor_Steer_EmptyMessageErrors(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	if err := sup.Steer("any", ""); err == nil {
		t.Fatal("empty steering message should error")
	}
	if err := sup.Steer("any", "   "); err == nil {
		t.Fatal("whitespace-only steering message should error")
	}
}

func TestSupervisor_Steer_UnknownAgentErrors(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	if err := sup.Steer("ghost", "hi"); err == nil {
		t.Fatal("Steer to unknown agent should error")
	}
}

func TestSupervisor_Interrupt_UnknownAgentErrors(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	if err := sup.Interrupt("ghost"); err == nil {
		t.Fatal("Interrupt unknown agent should error")
	}
}

func TestSupervisor_StopKill_UnknownAgentErrors(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	if err := sup.Stop("ghost"); err == nil {
		t.Fatal("Stop unknown agent should error")
	}
	if err := sup.Kill("ghost"); err == nil {
		t.Fatal("Kill unknown agent should error")
	}
}

// TestSupervisor_DefaultModel_FallsBackInChildSpawn pins the v0.7.6
// fix: when SpawnContract.Model is empty (the chat-side `agent`
// delegation tool never sets it), runChild substitutes the
// supervisor's installed defaultModel before calling provider.Stream.
// Before the fix, the empty model id reached OpenAI-compatible
// endpoints and OpenRouter rejected with HTTP 400 "No models provided".
//
// The test stands up a capturing provider, installs a default model
// on the supervisor, spawns a contract WITHOUT Model, drains the
// child to completion, then asserts the captured request carried the
// supervisor's default. A SpawnContract.Model SET to something
// explicit must NOT be overridden (second sub-test).
func TestSupervisor_DefaultModel_FallsBackInChildSpawn(t *testing.T) {
	t.Run("empty contract.Model uses supervisor default", func(t *testing.T) {
		dir := t.TempDir()
		log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer agent.CloseStateDB(log)

		cap := &capturingProvider{}
		sup := agent.NewSupervisor(log, cap, tools.NewRegistry())
		defer sup.Shutdown()
		sup.SetMode(frame.ModeOrchestrator)
		sup.SetDefaultModel("google/gemini-3.5-flash")

		_, res, err := sup.Spawn(context.Background(), "", agent.SpawnContract{Objective: "x"})
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		// Wait for the child to finish so the request is recorded
		// (the capturing provider returns immediately on Stream).
		select {
		case <-res:
		case <-time.After(2 * time.Second):
			t.Fatal("child did not complete within 2s")
		}

		if got, want := cap.lastModel(), "google/gemini-3.5-flash"; got != want {
			t.Errorf("child request Model = %q, want %q (supervisor default)", got, want)
		}
	})

	t.Run("explicit contract.Model is preserved", func(t *testing.T) {
		dir := t.TempDir()
		log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer agent.CloseStateDB(log)

		cap := &capturingProvider{}
		sup := agent.NewSupervisor(log, cap, tools.NewRegistry())
		defer sup.Shutdown()
		sup.SetMode(frame.ModeOrchestrator)
		sup.SetDefaultModel("default-fallback")

		_, res, err := sup.Spawn(context.Background(), "", agent.SpawnContract{
			Objective: "x",
			Model:     "explicit-override",
		})
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		select {
		case <-res:
		case <-time.After(2 * time.Second):
			t.Fatal("child did not complete within 2s")
		}

		if got, want := cap.lastModel(), "explicit-override"; got != want {
			t.Errorf("explicit Model = %q, want %q (no override expected)", got, want)
		}
	})
}

// TestSupervisor_DefaultModel_GetterSetter pins the small public
// surface added in v0.7.6.
func TestSupervisor_DefaultModel_GetterSetter(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	if got := sup.DefaultModel(); got != "" {
		t.Errorf("zero DefaultModel = %q, want empty", got)
	}
	sup.SetDefaultModel("anthropic/claude-sonnet-4-6")
	if got := sup.DefaultModel(); got != "anthropic/claude-sonnet-4-6" {
		t.Errorf("DefaultModel after set = %q", got)
	}
	sup.SetDefaultModel("")
	if got := sup.DefaultModel(); got != "" {
		t.Errorf("DefaultModel after reset = %q, want empty", got)
	}
}

func TestSupervisor_SetMode_InvalidFallsBackToSolo(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	sup.SetMode("nonsense")
	if got := sup.Mode(); got != frame.ModeSolo {
		t.Errorf("invalid mode should fall back to solo; got %q", got)
	}
	sup.SetMode(frame.ModeTight)
	if got := sup.Mode(); got != frame.ModeTight {
		t.Errorf("Mode = %q want %q", got, frame.ModeTight)
	}
}

func TestSupervisor_SpawnCap_HonorsMode(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	sup.SetMode(frame.ModeSolo)
	if got := sup.SpawnCap(); got != 0 {
		t.Errorf("solo SpawnCap = %d want 0", got)
	}
	sup.SetMode(frame.ModeTight)
	if got := sup.SpawnCap(); got != 1 {
		t.Errorf("tight SpawnCap = %d want 1", got)
	}
	sup.SetMode(frame.ModeOrchestrator)
	if got := sup.SpawnCap(); got <= 0 {
		t.Errorf("orchestrator SpawnCap should be positive, got %d", got)
	}
	// MaxConcurrentChildren lower than mode cap wins.
	sup.SetMaxConcurrentChildren(2)
	sup.SetMode(frame.ModeOrchestrator)
	if got := sup.SpawnCap(); got != 2 {
		t.Errorf("orchestrator + cap=2 should give 2, got %d", got)
	}
}

func TestSupervisor_Spawn_SoloAndTightDistinctErrors(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	// Solo refuses every Spawn.
	sup := agent.NewSupervisor(log, newHangingProvider(), tools.NewRegistry())
	defer sup.Shutdown()
	sup.SetMode(frame.ModeSolo)
	if _, _, err := sup.Spawn(context.Background(), "", agent.SpawnContract{Objective: "x"}); err == nil {
		t.Fatal("solo Spawn should refuse")
	}

	// Tight allows one in-flight child; second Spawn is refused.
	sup2 := agent.NewSupervisor(log, newHangingProvider(), tools.NewRegistry())
	defer sup2.Shutdown()
	sup2.SetMode(frame.ModeTight)
	_, res, err := sup2.Spawn(context.Background(), "", agent.SpawnContract{Objective: "x"})
	if err != nil {
		t.Fatalf("first tight Spawn: %v", err)
	}
	if _, _, err := sup2.Spawn(context.Background(), "", agent.SpawnContract{Objective: "x"}); err == nil {
		t.Fatal("second tight Spawn should refuse")
	}
	// Drain so we don't leak the hanging child.
	sup2.Shutdown()
	<-res
}

func TestSupervisor_SetAgentWorktree_NilDeletes(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	sup.SetAgentWorktree("", nil) // empty agentID = no-op
	if _, ok := sup.AgentWorktreeFor(""); ok {
		t.Error("empty agentID should not be tracked")
	}
	wt := &fakeWorktree{}
	sup.SetAgentWorktree("a", wt)
	if _, ok := sup.AgentWorktreeFor("a"); !ok {
		t.Error("after Set, expected presence")
	}
	// Nil arg deletes.
	sup.SetAgentWorktree("a", nil)
	if _, ok := sup.AgentWorktreeFor("a"); ok {
		t.Error("after Set nil, expected absence")
	}
}

func TestSupervisor_ActiveChildren_EmptyReturnsZero(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	if got := sup.ActiveChildren(""); got != 0 {
		t.Errorf("ActiveChildren on empty = %d", got)
	}
}

func TestSupervisor_SnapshotChildrenOf_NilLogReturnsNil(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	if got := sup.SnapshotChildrenOf(context.Background(), ""); got != nil {
		t.Errorf("nil-log Snapshot should be nil, got %v", got)
	}
	// Nil receiver also OK.
	var s *agent.Supervisor
	if got := s.SnapshotChildrenOf(context.Background(), ""); got != nil {
		t.Errorf("nil supervisor should yield nil, got %v", got)
	}
}

func TestSupervisor_SnapshotChildrenOf_NoChildrenReturnsNil(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	sup := agent.NewSupervisor(log, nil, nil)
	defer sup.Shutdown()
	if got := sup.SnapshotChildrenOf(context.Background(), "ghost-parent"); got != nil {
		t.Errorf("no-children case should yield nil, got %v", got)
	}
}

func TestSupervisor_SetRestartIntensity_Knob(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	sup.SetRestartIntensity(1, 100*time.Millisecond)
	// First retry should not trip; second should trip the breaker.
	if _, err := sup.Retry("agent-x"); err != nil {
		t.Errorf("first retry: %v", err)
	}
	if sup.IsCircuitBroken("agent-x") {
		t.Errorf("breaker should not be tripped yet")
	}
	if _, err := sup.Retry("agent-x"); err == nil {
		t.Errorf("second retry should trip breaker (maxR=1)")
	}
	if !sup.IsCircuitBroken("agent-x") {
		t.Errorf("breaker should be tripped after maxR+1 retries")
	}
}

func TestSupervisor_SetMaxSpawnDepth_Knob(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	sup.SetMaxSpawnDepth(5)
	// No way to assert without computeDepth being exported; we exercise
	// the setter for coverage.
}

func TestSupervisor_IsCircuitBroken_UnknownIsFalse(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	if sup.IsCircuitBroken("nope") {
		t.Error("unknown agent should not be circuit-broken")
	}
}

func TestSupervisor_ClearAgentWorktreeNoop(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	sup.ClearAgentWorktree("never-set") // no panic
}
