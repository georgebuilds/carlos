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

// TestSupervisor_SettersAreRaceFreeUnderConcurrentSpawn drives the
// public knob setters (SetMaxConcurrentChildren, SetMaxSpawnDepth,
// SetRestartIntensity) concurrently with active Spawn calls. Without
// the fix the setters wrote the fields lock-free while Spawn /
// effectiveSpawnCapLocked / Retry read them under s.mu, producing a
// reliable data race under -race. With the fix each setter takes
// s.mu so the writes and reads are ordered. We don't assert any
// specific schedule - just that -race stays clean across the run.
func TestSupervisor_SettersAreRaceFreeUnderConcurrentSpawn(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	sup := agent.NewSupervisor(log, newHangingProvider(), tools.NewRegistry())
	defer sup.Shutdown()
	sup.SetMode(frame.ModeOrchestrator)
	sup.SetMaxConcurrentChildren(8)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Knob writer: hammers each setter on a short cadence.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			sup.SetMaxConcurrentChildren(1 + (i % 8))
			sup.SetMaxSpawnDepth(1 + (i % 4))
			sup.SetRestartIntensity(1+(i%5), time.Duration(10+i)*time.Millisecond)
			i++
		}
	}()

	// Reader/spawner: continuously calls Spawn (and Retry, which also
	// reads restart fields). Spawns may legitimately fail with a
	// concurrency-cap error when the writer drops the cap to 1; that's
	// fine - we only care that the race detector stays silent.
	spawnedIDs := make(chan string, 64)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				sub, _, err := sup.Spawn(context.Background(), "", agent.SpawnContract{Objective: "x"})
				if err == nil {
					select {
					case spawnedIDs <- sub.ID:
					default:
					}
				}
				_, _ = sup.Retry("noop")
			}
		}()
	}

	// Let the contention run briefly. 75ms is plenty for the race
	// detector to fire on the original implementation while staying
	// fast enough not to hurt CI.
	time.Sleep(75 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Drain so Shutdown cancels the hanging children cleanly. We don't
	// need to read the spawnedIDs channel - sup.Shutdown cancels every
	// in-flight child.
}

// TestSupervisor_Spawn_CancelReleasesBothContexts pins fix #2: when
// MaxWallClock > 0 the composed childCancel must release BOTH the
// timeout context AND the underlying parent cancel context. Before
// the fix the WithCancel return was overwritten by WithTimeout and
// the parent cancel leaked - calling the returned childCancel only
// fired the timeout, never the underlying parent.
//
// We observe the invariant indirectly: spawn with a generous wall
// clock so the timeout cannot have fired naturally; cancel via
// Supervisor.Shutdown; the runChild goroutine must observe Done()
// and the result channel must close. A leaked cancel would NOT
// surface as a build failure (govet's lostcancel rule already
// passes), but the runtime contract - "cancelling the child stops
// the loop" - would still hold either way for THIS path. The harder
// assertion is the absence of leaks; we rely on govet + the
// existing -race detector + the fact that the test exits without
// hanging.
func TestSupervisor_Spawn_CancelReleasesBothContexts(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	sup := agent.NewSupervisor(log, newHangingProvider(), tools.NewRegistry())
	sup.SetMode(frame.ModeOrchestrator)

	_, res, err := sup.Spawn(context.Background(), "", agent.SpawnContract{
		Objective:    "hang please",
		MaxWallClock: 10 * time.Minute, // never fires naturally during test
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Shutdown is the path that calls the composed childCancel. After
	// fix the inner WithCancel allocation is released; before fix it
	// would have been leaked. The user-visible signal is that the
	// child's result channel still closes - which it does either way
	// when the timeout fires, but the leak would only surface to
	// govet's lostcancel rule (already passing) or a goroutine-leak
	// detector. The runtime contract we CAN assert here is that
	// Shutdown propagates promptly: the result lands within a tight
	// budget even though MaxWallClock is 10 minutes.
	go sup.Shutdown()

	select {
	case r := <-res:
		// Hanging provider's stream returns when ctx.Done() fires; the
		// loop then sees no events, classifies as done, and sends.
		// Either an err or a clean classification is fine - we only
		// care that the channel actually closed (i.e. cancellation
		// propagated and runChild unwound).
		_ = r
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not propagate cancel to child within 3s; both contexts must release")
	}
}
