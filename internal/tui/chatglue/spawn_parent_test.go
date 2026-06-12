package chatglue

// Regression for the web/TUI sub-agent lineage bug: tools executed
// inside a turn must see the thread's id via agent.SpawnParentFromContext,
// so the Agent delegation tool spawns children parented to the thread
// (not as parentless top-level rows that pollute the session roster).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/tools"
)

// spawnParentProbe records the spawn parent its Execute ctx carries.
type spawnParentProbe struct {
	mu   sync.Mutex
	seen []string
}

func (p *spawnParentProbe) Name() string        { return "probe" }
func (p *spawnParentProbe) Description() string { return "records ctx spawn parent" }
func (p *spawnParentProbe) Schema() []byte      { return []byte(`{"type":"object"}`) }
func (p *spawnParentProbe) Execute(ctx context.Context, _ []byte) ([]byte, error) {
	p.mu.Lock()
	p.seen = append(p.seen, agent.SpawnParentFromContext(ctx))
	p.mu.Unlock()
	return []byte("ok"), nil
}

func (p *spawnParentProbe) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// TestLoop_HandleUserMessage_InjectsSpawnParent drives one full turn in
// which the model calls a tool, and asserts the tool's ctx carried the
// loop's agent id as the spawn parent. This is the seam the Agent
// delegation tool reads, for both the TUI and the web backend (both
// drive turns through this Loop).
func TestLoop_HandleUserMessage_InjectsSpawnParent(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-spawn-parent"
	seedAgent(t, log, id)

	probe := &spawnParentProbe{}
	reg := tools.NewRegistry()
	reg.Register(probe)

	// The loop only executes tools on stop=tool_use, and the fake
	// provider replays the same script on every Stream call, so the turn
	// ends via MaxIterations=1: one stream, one probe execution, then the
	// loop surfaces ErrMaxIterations as an error-card assistant message
	// (which is what waitForAssistant keys on). The probe has run by then.
	script := fake.Script{
		{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "t1", Name: "probe", Input: []byte(`{}`)}},
		{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "t1", Name: "probe", Input: []byte(`{}`)}},
		{Kind: providers.EventStopReason, Stop: "tool_use"},
	}
	l := NewLoop(Config{
		Provider:      fake.New("fake", script),
		Tools:         reg,
		Approver:      agent.AutoApprover{},
		MaxIterations: 1,
	}, log, newMemSource(), id)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()
	time.Sleep(50 * time.Millisecond)
	appendUserMessage(t, log, id, "go probe")
	_ = waitForAssistant(t, log, id, "max iterations")

	calls := probe.calls()
	if len(calls) == 0 {
		t.Fatal("probe tool never executed")
	}
	for i, got := range calls {
		if got != id {
			t.Errorf("probe call %d saw spawn parent %q, want %q (the thread id)", i, got, id)
		}
	}
}
