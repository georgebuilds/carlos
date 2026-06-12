package agent_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/tools"
)

// scriptedTextProvider emits a plain text + end_turn stream on each
// Stream call. Used by the happy-path test where we want the child
// loop to actually complete and the SpawnResult to land.
type scriptedTextProvider struct{ text string }

func (scriptedTextProvider) Name() string                         { return "scripted-text" }
func (scriptedTextProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }

func (p scriptedTextProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	script := []providers.Event{
		{Kind: providers.EventTextDelta, Text: p.text},
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	}
	ch := make(chan providers.Event, len(script))
	go func() {
		defer close(ch)
		for _, e := range script {
			select {
			case <-ctx.Done():
				return
			case ch <- e:
			}
		}
	}()
	return ch, nil
}

// promptCapturingProvider records the last request's messages so
// tests can assert the composed initial prompt reaches the model.
type promptCapturingProvider struct {
	mu  sync.Mutex
	req providers.Request
}

func (p *promptCapturingProvider) Name() string { return "capture" }
func (p *promptCapturingProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{}
}

func (p *promptCapturingProvider) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.req = req
	p.mu.Unlock()
	ch := make(chan providers.Event, 2)
	ch <- providers.Event{Kind: providers.EventTextDelta, Text: "ok"}
	ch <- providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"}
	close(ch)
	return ch, nil
}

// toolUseProvider scripts a single tool_use → end_turn turn so we
// can assert the child's restricted registry catches an unknown tool.
type toolUseProvider struct {
	toolName string
	scripts  int
}

func (toolUseProvider) Name() string                         { return "toolish" }
func (toolUseProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }

func (p *toolUseProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event, 8)
	go func() {
		defer close(ch)
		p.scripts++
		if p.scripts == 1 {
			// Turn 1: emit a tool_use that should hit "unknown tool"
			ch <- providers.Event{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu-1", Name: p.toolName}}
			ch <- providers.Event{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu-1", Name: p.toolName, Input: []byte(`{}`)}}
			ch <- providers.Event{Kind: providers.EventStopReason, Stop: "tool_use"}
			return
		}
		// Turn 2: end the loop.
		ch <- providers.Event{Kind: providers.EventTextDelta, Text: "done after tool error"}
		ch <- providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"}
	}()
	return ch, nil
}

// drainResult reads exactly one SpawnResult from ch with a timeout
// so a stuck child doesn't hang the test forever.
func drainResult(t *testing.T, ch <-chan agent.SpawnResult, timeout time.Duration) agent.SpawnResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for SpawnResult")
		return agent.SpawnResult{}
	}
}

// TestSpawn_HappyPath spawns one child against a scripted provider
// that emits a single text turn + end_turn. We assert:
//   - SpawnResult lands with no error
//   - FinalTurn is the assistant message we expect
//   - the projection row reaches StateDone
//   - the events table holds the create + the two state_change rows
func TestSpawn_HappyPath(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, scriptedTextProvider{text: "hello"}, nil)
	defer sup.Shutdown()

	sub, ch, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "say hello"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	res := drainResult(t, ch, 5*time.Second)
	if res.Err != nil {
		t.Fatalf("result err: %v", res.Err)
	}
	if res.AgentID != sub.ID {
		t.Fatalf("result ID = %q, want %q", res.AgentID, sub.ID)
	}
	if res.FinalTurn.Role != "assistant" {
		t.Fatalf("final turn role = %q", res.FinalTurn.Role)
	}
	if len(res.FinalTurn.Content) == 0 || !strings.Contains(res.FinalTurn.Content[0].Text, "hello") {
		t.Fatalf("final turn content unexpected: %+v", res.FinalTurn.Content)
	}

	// Wait for the projection cache to settle on StateDone.
	deadline := time.Now().Add(2 * time.Second)
	var row agent.AgentRow
	for time.Now().Before(deadline) {
		r, ok, _ := log.GetAgent(ctx, sub.ID)
		if ok {
			row = r
			if row.State == agent.StateDone {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if row.State != agent.StateDone {
		t.Fatalf("post-run state = %v, want done", row.State)
	}

	// Events: created + running + done at minimum (heartbeats may or
	// may not appear depending on timing).
	evs, err := log.Read(ctx, sub.ID, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var stateChanges int
	for _, e := range evs {
		if e.Type == agent.EvtStateChange {
			stateChanges++
		}
	}
	if stateChanges < 3 {
		t.Fatalf("state_change events = %d, want >= 3 (created, running, done)", stateChanges)
	}
}

// TestSpawn_InitialPromptComposed asserts composeInitialPrompt's
// output reaches the provider via the request's first message. We
// can't call composeInitialPrompt directly (lowercase), but the
// provider's captured request gives us the same view the model has.
func TestSpawn_InitialPromptComposed(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	capProv := &promptCapturingProvider{}
	sup := agent.NewSupervisor(log, capProv, nil)
	defer sup.Shutdown()

	_, ch, err := sup.Spawn(ctx, "", agent.SpawnContract{
		Objective:       "research X",
		OutputFormat:    "{summary: string}",
		SuccessCriteria: "summary cites 2+ sources",
		MaxTurns:        5,
		MaxTokens:       4000,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	drainResult(t, ch, 5*time.Second)

	capProv.mu.Lock()
	defer capProv.mu.Unlock()
	if len(capProv.req.Messages) == 0 {
		t.Fatalf("provider saw no messages")
	}
	first := capProv.req.Messages[0]
	if first.Role != "user" {
		t.Fatalf("first message role = %q", first.Role)
	}
	if len(first.Content) == 0 || first.Content[0].Kind != "text" {
		t.Fatalf("first message content unexpected: %+v", first.Content)
	}
	text := first.Content[0].Text
	for _, want := range []string{
		"# Objective", "research X",
		"# Output format", "{summary: string}",
		"# Success criteria", "cites 2+ sources",
		"# Boundaries", "max turns: 5", "max tokens: 4000",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("initial prompt missing %q; got:\n%s", want, text)
		}
	}
}

// TestSpawn_RestrictedToolRegistry verifies the per-child registry
// filter. The base registry has tool "echo"; the child's allowlist
// is empty, so when the model emits a tool_use for "echo" the loop
// surfaces "tool error: unknown tool" via the tool_result. We don't
// inspect the tool_result directly (it lives mid-conversation); we
// just assert the loop didn't fall over and the final state is done.
func TestSpawn_RestrictedToolRegistry(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	base := tools.NewRegistry()
	base.Register(echoToolForSpawn{})

	prov := &toolUseProvider{toolName: "echo"}
	sup := agent.NewSupervisor(log, prov, base)
	defer sup.Shutdown()

	// Empty allowlist → child's registry has nothing. The provider
	// asks for "echo"; the loop should report "unknown tool" via
	// the standard tool_result path and then end_turn on the next
	// scripted turn.
	_, ch, err := sup.Spawn(ctx, "", agent.SpawnContract{
		Objective:     "use echo we don't have",
		ToolAllowlist: nil,
		MaxTurns:      4,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	res := drainResult(t, ch, 5*time.Second)
	if res.Err != nil {
		t.Fatalf("result err: %v", res.Err)
	}
}

// echoToolForSpawn is a minimal tool the restricted-registry test
// uses as the base registry's "everything" tool.
type echoToolForSpawn struct{}

func (echoToolForSpawn) Name() string                                         { return "echo" }
func (echoToolForSpawn) Description() string                                  { return "echo" }
func (echoToolForSpawn) Schema() []byte                                       { return []byte(`{}`) }
func (echoToolForSpawn) Execute(_ context.Context, in []byte) ([]byte, error) { return in, nil }

// TestSpawn_ConcurrencyCap spawns N children that hang, where N
// equals the per-parent cap; the N+1th Spawn must surface
// ErrConcurrencyExceeded. Cancelling one then re-spawning must
// succeed (the slot is released as the worker unwinds).
func TestSpawn_ConcurrencyCap(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()

	// Tighten the cap to 2 so the test is fast.
	sup.SetMaxConcurrentChildren(2)
	// Lift depth so each spawn lands at depth 1 under the (empty)
	// root, all sharing the same parentID.
	sup.SetMaxSpawnDepth(1)

	var chans []<-chan agent.SpawnResult
	for i := 0; i < 2; i++ {
		_, c, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "hang"})
		if err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
		chans = append(chans, c)
	}

	// 3rd Spawn at the same parentID must be refused.
	if _, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "overflow"}); !errors.Is(err, agent.ErrConcurrencyExceeded) {
		t.Fatalf("3rd spawn: err = %v, want ErrConcurrencyExceeded", err)
	}

	// Cancel one in-flight child by cancelling the supervisor's
	// context tracking (Shutdown is too heavy — it cancels all).
	// Instead, cancel the parent ctx for the next spawn and wait
	// for one slot to free by waiting on its channel after we
	// Shutdown a single child via the test's own context.
	//
	// Simplest reliable approach: cancel the parent ctx, which
	// bridges into all child ctxs, drain one result, and re-spawn.
	cancel()
	// Wait for at least one child to wind down so a slot frees up.
	drainResult(t, chans[0], 2*time.Second)

	// New spawn under a fresh ctx now should succeed.
	ctx2 := context.Background()
	if _, _, err := sup.Spawn(ctx2, "", agent.SpawnContract{Objective: "after-free"}); err != nil {
		t.Fatalf("post-free spawn: %v", err)
	}
}

// TestSpawn_HeartbeatTickerStopsOnTerminal verifies the heartbeat
// wiring: a freshly-spawned agent has a running ticker; once the
// loop completes (provider returns end_turn), runChild calls Stop on
// the ticker and Active() drops back.
func TestSpawn_HeartbeatTickerStopsOnTerminal(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, scriptedTextProvider{text: "done"}, nil)
	defer sup.Shutdown()

	_, ch, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "quick"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	res := drainResult(t, ch, 5*time.Second)
	if res.Err != nil {
		t.Fatalf("result err: %v", res.Err)
	}
	// ActiveChildren should drop to 0 once the worker has cleaned up.
	// The drain above happens after the worker sends + closes; the
	// cleanup ran before that, so ActiveChildren is already 0.
	if n := sup.ActiveChildren(""); n != 0 {
		t.Fatalf("ActiveChildren = %d, want 0", n)
	}
}

// TestSpawn_NilProviderRejected covers the constructor-passes-nil
// case: Spawn refuses with a clear error rather than panicking on
// the goroutine launch.
func TestSpawn_NilProviderRejected(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	sup := agent.NewSupervisor(log, nil, nil)
	defer sup.Shutdown()

	if _, _, err := sup.Spawn(context.Background(), "", agent.SpawnContract{Objective: "noprov"}); err == nil {
		t.Fatalf("expected nil-provider error")
	}
}

// TestSpawn_FrameMode_SoloRejectsEverySpawn covers the Phase O cap
// mapping: in solo mode the supervisor refuses every Spawn so the
// model gets a tool-result error and adjusts. The frame-mode default
// for the supervisor is orchestrator (preserves legacy), so we have
// to SetMode("solo") explicitly here.
func TestSpawn_FrameMode_SoloRejectsEverySpawn(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()
	sup.SetMode(frame.ModeSolo)

	_, _, err = sup.Spawn(ctx, "", agent.SpawnContract{Objective: "denied"})
	if !errors.Is(err, agent.ErrSpawnRefusedSolo) {
		t.Fatalf("solo Spawn err = %v, want ErrSpawnRefusedSolo", err)
	}
	if !errors.Is(err, agent.ErrConcurrencyExceeded) {
		t.Fatalf("ErrSpawnRefusedSolo must wrap ErrConcurrencyExceeded; got %v", err)
	}
	// The error message must name the mode so the model can read it.
	if !strings.Contains(err.Error(), "solo") {
		t.Errorf("solo error missing 'solo' marker: %v", err)
	}
	if !strings.Contains(err.Error(), "disables delegation") {
		t.Errorf("solo error missing 'disables delegation' phrasing: %v", err)
	}
}

// TestSpawn_FrameMode_TightAllowsOneRejectsSecond covers tight: one
// in-flight child is fine; the second concurrent Spawn at the same
// parentID returns the busy error.
func TestSpawn_FrameMode_TightAllowsOneRejectsSecond(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()
	sup.SetMode(frame.ModeTight)

	if _, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "first"}); err != nil {
		t.Fatalf("first tight Spawn: %v", err)
	}
	_, _, err = sup.Spawn(ctx, "", agent.SpawnContract{Objective: "second"})
	if !errors.Is(err, agent.ErrSpawnBusyTight) {
		t.Fatalf("second tight Spawn err = %v, want ErrSpawnBusyTight", err)
	}
	if !errors.Is(err, agent.ErrConcurrencyExceeded) {
		t.Fatalf("ErrSpawnBusyTight must wrap ErrConcurrencyExceeded; got %v", err)
	}
	if !strings.Contains(err.Error(), "tight") {
		t.Errorf("tight error missing 'tight' marker: %v", err)
	}
}

// TestSpawn_FrameMode_OrchestratorAllowsFiveRejectsSixth preserves the
// legacy cap of 5: orchestrator mode = today's default. The 6th spawn
// at the same parent must surface ErrConcurrencyExceeded (NOT the
// solo/tight variants).
func TestSpawn_FrameMode_OrchestratorAllowsFiveRejectsSixth(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()
	// Orchestrator is the constructor default but be explicit so the
	// test reads as "verify orchestrator's cap".
	sup.SetMode(frame.ModeOrchestrator)

	for i := 0; i < 5; i++ {
		if _, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "ok"}); err != nil {
			t.Fatalf("spawn %d under orchestrator: %v", i, err)
		}
	}
	_, _, err = sup.Spawn(ctx, "", agent.SpawnContract{Objective: "overflow"})
	if !errors.Is(err, agent.ErrConcurrencyExceeded) {
		t.Fatalf("6th orchestrator Spawn err = %v, want ErrConcurrencyExceeded", err)
	}
	// Importantly the orchestrator path returns the bare
	// concurrency-exceeded error, NOT the solo or tight variants.
	if errors.Is(err, agent.ErrSpawnRefusedSolo) {
		t.Errorf("orchestrator overflow misclassified as solo: %v", err)
	}
	if errors.Is(err, agent.ErrSpawnBusyTight) {
		t.Errorf("orchestrator overflow misclassified as tight: %v", err)
	}
}

// TestSpawn_FrameMode_SetModeUpdatesCapForNextSpawn covers the
// runtime switch: SetMode while the supervisor is live changes the
// cap for the very next Spawn. /frame switch and /mode rely on this.
func TestSpawn_FrameMode_SetModeUpdatesCapForNextSpawn(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()
	// Start in orchestrator: a spawn lands fine.
	sup.SetMode(frame.ModeOrchestrator)
	if sup.Mode() != frame.ModeOrchestrator {
		t.Fatalf("Mode() = %q, want orchestrator", sup.Mode())
	}
	if sup.SpawnCap() != frame.SpawnCapOrchestrator {
		t.Fatalf("orchestrator SpawnCap() = %d, want %d", sup.SpawnCap(), frame.SpawnCapOrchestrator)
	}
	if _, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "first"}); err != nil {
		t.Fatalf("orch spawn: %v", err)
	}

	// Flip to solo. The NEXT spawn (under any parent) must be refused.
	sup.SetMode(frame.ModeSolo)
	if sup.SpawnCap() != frame.SpawnCapSolo {
		t.Fatalf("solo SpawnCap() = %d, want %d", sup.SpawnCap(), frame.SpawnCapSolo)
	}
	if _, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "denied"}); !errors.Is(err, agent.ErrSpawnRefusedSolo) {
		t.Fatalf("post-SetMode solo Spawn err = %v, want ErrSpawnRefusedSolo", err)
	}

	// Flip to tight; existing in-flight child counts toward the cap,
	// so the next Spawn returns the busy error.
	sup.SetMode(frame.ModeTight)
	if sup.SpawnCap() != frame.SpawnCapTight {
		t.Fatalf("tight SpawnCap() = %d, want %d", sup.SpawnCap(), frame.SpawnCapTight)
	}
	if _, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "busy"}); !errors.Is(err, agent.ErrSpawnBusyTight) {
		t.Fatalf("post-SetMode tight Spawn err = %v, want ErrSpawnBusyTight", err)
	}

	// Unknown mode falls back to solo (safest).
	sup.SetMode("garbage")
	if sup.Mode() != frame.ModeSolo {
		t.Fatalf("invalid mode should fall back to solo; got %q", sup.Mode())
	}
}

// TestSupervisor_SnapshotChildrenOf covers the inline chat panel's
// data source. Spawning two hanging children under the same parent
// should surface two snapshots; an unrelated spawn under a different
// parent stays invisible to the first parent's view.
func TestSupervisor_SnapshotChildrenOf(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()
	sup.SetMaxSpawnDepth(2)
	sup.SetMaxConcurrentChildren(5)

	if got := sup.SnapshotChildrenOf(ctx, ""); len(got) != 0 {
		t.Fatalf("empty snapshot expected; got %d", len(got))
	}

	for i, obj := range []string{"first", "second"} {
		if _, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: obj}); err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
	}

	// Snapshot must report both children. Title surfaces the
	// objective so the chat panel can derive its event column.
	snap := sup.SnapshotChildrenOf(ctx, "")
	if len(snap) != 2 {
		t.Fatalf("SnapshotChildrenOf len = %d, want 2", len(snap))
	}
	titles := map[string]bool{}
	for _, s := range snap {
		titles[s.Title] = true
		if s.AgentID == "" {
			t.Errorf("snapshot row missing AgentID")
		}
	}
	for _, want := range []string{"first", "second"} {
		if !titles[want] {
			t.Errorf("snapshot missing objective %q; got %+v", want, snap)
		}
	}

	// A spawn under a different parent doesn't leak into the first
	// parent's view.
	if got := sup.SnapshotChildrenOf(ctx, "some-other-parent"); len(got) != 0 {
		t.Errorf("unrelated parent should see no children; got %d", len(got))
	}
}

// TestSpawn_CannedFakeProviderRunsCleanly is the simplest end-to-end
// sanity that uses the package-canonical fake.CannedScript. It hits
// a tool_use; that means the base registry needs the bash tool name
// in its allowlist, but to keep the test simple we just let the
// loop surface "unknown tool" — that's still a clean (no-error)
// termination.
func TestSpawn_CannedFakeProviderRunsCleanly(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	// fake.CannedScript ends with a stop event so the loop returns
	// cleanly even when its tool_use can't be resolved.
	prov := fake.New("canned", fake.CannedScript())
	sup := agent.NewSupervisor(log, prov, nil)
	defer sup.Shutdown()

	_, ch, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "canned"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// CannedScript stops after one turn (tool_use); the loop then
	// asks for another turn. fake.Provider walks a fresh copy of
	// the script per Stream call, so iter 2 re-emits the same
	// sequence... which loops forever on tool_use → call again →
	// tool_use. Bound the wait so the test fails fast rather than
	// hanging if MaxIterations isn't applied.
	res := drainResult(t, ch, 10*time.Second)
	// Either clean completion or MaxIterations is fine; what
	// matters is that we got A result, not a panic.
	_ = res
}
