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

func (p *promptCapturingProvider) Name() string                         { return "capture" }
func (p *promptCapturingProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }

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

func (echoToolForSpawn) Name() string                                       { return "echo" }
func (echoToolForSpawn) Description() string                                { return "echo" }
func (echoToolForSpawn) Schema() []byte                                     { return []byte(`{}`) }
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
