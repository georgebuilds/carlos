package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// Slice 5a Budget tests — see internal/agent/budget.go header for the
// design context.

func TestBudget_ValidateRejectsNegatives(t *testing.T) {
	cases := []struct {
		name string
		b    agent.Budget
	}{
		{"neg tokens", agent.Budget{MaxTokens: -1}},
		{"neg cost", agent.Budget{MaxCostCents: -1}},
		{"neg time", agent.Budget{MaxWallClock: -1 * time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.b.Validate(); err == nil {
				t.Fatalf("expected validate error on %+v", tc.b)
			}
		})
	}
}

func TestBudget_ValidateAcceptsZeroAndPositive(t *testing.T) {
	zero := agent.Budget{}
	if err := zero.Validate(); err != nil {
		t.Fatalf("zero Budget should validate: %v", err)
	}
	pos := agent.Budget{MaxTokens: 1, MaxCostCents: 1, MaxWallClock: time.Second}
	if err := pos.Validate(); err != nil {
		t.Fatalf("positive Budget should validate: %v", err)
	}
}

func TestBudget_IsUnlimited(t *testing.T) {
	if !(agent.Budget{}).IsUnlimited() {
		t.Errorf("zero Budget should be unlimited")
	}
	if (agent.Budget{MaxTokens: 1}).IsUnlimited() {
		t.Errorf("non-zero Budget should not be unlimited")
	}
}

func TestTracker_AddAccumulates(t *testing.T) {
	tr := agent.NewTracker(nil)
	tr.Add(10, 20, 5)
	tr.Add(100, 200, 50)
	if got := tr.Tokens(); got != 10+20+100+200 {
		t.Errorf("Tokens = %d", got)
	}
	if got := tr.CostCents(); got != 55 {
		t.Errorf("CostCents = %d", got)
	}
}

func TestTracker_AddClampsNegativesToZero(t *testing.T) {
	tr := agent.NewTracker(nil)
	tr.Add(-5, -10, -3) // negative inputs should clamp to zero
	if got := tr.Tokens(); got != 0 {
		t.Errorf("Tokens after neg add = %d", got)
	}
	if got := tr.CostCents(); got != 0 {
		t.Errorf("CostCents after neg add = %d", got)
	}
}

func TestTracker_AddIsConcurrentSafe(t *testing.T) {
	tr := agent.NewTracker(nil)
	const N = 100
	const M = 50
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < M; j++ {
				tr.Add(1, 1, 1)
			}
		}()
	}
	wg.Wait()
	wantTokens := int64(N * M * 2)
	wantCost := int64(N * M)
	if got := tr.Tokens(); got != wantTokens {
		t.Errorf("Tokens = %d want %d", got, wantTokens)
	}
	if got := tr.CostCents(); got != wantCost {
		t.Errorf("CostCents = %d want %d", got, wantCost)
	}
}

func TestTracker_ParentPropagation(t *testing.T) {
	parent := agent.NewTracker(nil)
	child := agent.NewTracker(parent)
	child.Add(100, 200, 30)
	// Parent should also see the increment.
	if got := parent.Tokens(); got != 300 {
		t.Errorf("parent Tokens = %d want 300", got)
	}
	if got := parent.CostCents(); got != 30 {
		t.Errorf("parent CostCents = %d", got)
	}
	// Child sees only its own (which equals what it added).
	if got := child.Tokens(); got != 300 {
		t.Errorf("child Tokens = %d", got)
	}
}

func TestTracker_SiblingsAreIndependent(t *testing.T) {
	parent := agent.NewTracker(nil)
	a := agent.NewTracker(parent)
	b := agent.NewTracker(parent)
	a.Add(100, 0, 5)
	b.Add(50, 0, 3)
	if a.Tokens() != 100 || b.Tokens() != 50 {
		t.Errorf("siblings polluted each other: a=%d b=%d", a.Tokens(), b.Tokens())
	}
	if parent.Tokens() != 150 {
		t.Errorf("parent accumulation: %d want 150", parent.Tokens())
	}
	if parent.CostCents() != 8 {
		t.Errorf("parent cost: %d want 8", parent.CostCents())
	}
}

func TestTracker_CheckBudgetPassesUnderCap(t *testing.T) {
	tr := agent.NewTracker(nil)
	tr.Add(50, 0, 5)
	b := agent.Budget{MaxTokens: 100, MaxCostCents: 100}
	if err := tr.CheckBudget(b); err != nil {
		t.Errorf("CheckBudget should pass: %v", err)
	}
}

func TestTracker_CheckBudgetTokensExceeded(t *testing.T) {
	tr := agent.NewTracker(nil)
	tr.Add(150, 0, 0)
	b := agent.Budget{MaxTokens: 100}
	err := tr.CheckBudget(b)
	if err == nil {
		t.Fatalf("expected exceeded error")
	}
	if !errors.Is(err, agent.ErrBudgetExceeded) {
		t.Errorf("want ErrBudgetExceeded, got %v", err)
	}
	if !errors.Is(err, agent.ErrBudgetExceededTokens) {
		t.Errorf("want ErrBudgetExceededTokens, got %v", err)
	}
}

func TestTracker_CheckBudgetCostExceeded(t *testing.T) {
	tr := agent.NewTracker(nil)
	tr.Add(0, 0, 200)
	b := agent.Budget{MaxCostCents: 100}
	err := tr.CheckBudget(b)
	if !errors.Is(err, agent.ErrBudgetExceededCost) {
		t.Errorf("want ErrBudgetExceededCost, got %v", err)
	}
}

func TestTracker_CheckBudgetTimeExceeded(t *testing.T) {
	tr := agent.NewTracker(nil)
	// Tiny budget; sleep past it.
	b := agent.Budget{MaxWallClock: 5 * time.Millisecond}
	time.Sleep(20 * time.Millisecond)
	err := tr.CheckBudget(b)
	if !errors.Is(err, agent.ErrBudgetExceededTime) {
		t.Errorf("want ErrBudgetExceededTime, got %v", err)
	}
}

func TestTracker_CheckBudgetUnlimitedNoError(t *testing.T) {
	tr := agent.NewTracker(nil)
	tr.Add(1000000, 0, 1000000)
	if err := tr.CheckBudget(agent.Budget{}); err != nil {
		t.Errorf("unlimited budget should never exceed: %v", err)
	}
}

func TestTracker_Remaining(t *testing.T) {
	tr := agent.NewTracker(nil)
	tr.Add(30, 0, 10)
	b := agent.Budget{MaxTokens: 100, MaxCostCents: 50}
	tokens, cost, _, exceeded := tr.Remaining(b)
	if tokens != 70 {
		t.Errorf("tokens remaining = %d want 70", tokens)
	}
	if cost != 40 {
		t.Errorf("cost remaining = %d want 40", cost)
	}
	if exceeded {
		t.Errorf("should not be exceeded")
	}
}

func TestTracker_RemainingClampsAtZero(t *testing.T) {
	tr := agent.NewTracker(nil)
	tr.Add(150, 0, 0)
	tokens, _, _, exceeded := tr.Remaining(agent.Budget{MaxTokens: 100})
	if tokens != 0 {
		t.Errorf("over-cap remaining = %d should clamp to 0", tokens)
	}
	if !exceeded {
		t.Errorf("should be exceeded")
	}
}

func TestTracker_Snapshot(t *testing.T) {
	tr := agent.NewTracker(nil)
	tr.Add(42, 8, 13)
	s := tr.Snapshot()
	if s.Tokens != 50 {
		t.Errorf("snapshot Tokens = %d want 50", s.Tokens)
	}
	if s.CostCents != 13 {
		t.Errorf("snapshot CostCents = %d want 13", s.CostCents)
	}
	if s.Elapsed <= 0 {
		// Elapsed should be small but positive.
		t.Errorf("snapshot Elapsed should be positive: %s", s.Elapsed)
	}
}

func TestEstimateCallCostAndTokens(t *testing.T) {
	// Empty inputs → zero.
	if c := agent.EstimateCallCost(0, 0); c != 0 {
		t.Errorf("EstimateCallCost(0,0) = %d", c)
	}
	if c := agent.EstimateCallTokens(0, 0); c != 0 {
		t.Errorf("EstimateCallTokens(0,0) = %d", c)
	}
	// Non-empty body → at least 1 cent (rounding floor).
	if c := agent.EstimateCallCost(0, 100); c != 1 {
		t.Errorf("EstimateCallCost(0,100) = %d want 1 (floor)", c)
	}
	// Token estimate roughly chars/4.
	if c := agent.EstimateCallTokens(0, 40); c != 10 {
		t.Errorf("EstimateCallTokens(0,40) = %d want 10", c)
	}
}

// ---- loop integration ----

// streamProvider is a minimal scripted provider for budget-loop tests
// — kept distinct from loop_test.go's sequenceProvider so a future
// edit there doesn't accidentally couple test suites.
type streamProvider struct {
	mu      sync.Mutex
	scripts [][]providers.Event
	calls   int
}

func (p *streamProvider) Name() string                         { return "stream" }
func (p *streamProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p *streamProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls >= len(p.scripts) {
		return nil, errors.New("stream: no more scripts")
	}
	s := p.scripts[p.calls]
	p.calls++
	ch := make(chan providers.Event, len(s))
	for _, ev := range s {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func TestLoop_BudgetExceededExitsCleanly(t *testing.T) {
	// Two-turn loop: first stream emits a tool_use forcing iter 2;
	// before iter 2, the Tracker is over cap. We expect the loop to
	// return with ErrBudgetExceeded after iter 1.
	p := &streamProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventTextDelta, Text: "step 1"},
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu", Name: "echo"}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu", Name: "echo", Input: []byte(`{}`)}},
			{Kind: providers.EventStopReason, Stop: "tool_use"},
		},
		{
			{Kind: providers.EventTextDelta, Text: "step 2"},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	tr := agent.NewTracker(nil)
	// Pre-load JUST under the cap so iter 1 passes the gate but the
	// post-call estimate push (token estimate from message lengths)
	// crosses it. Iter 2's gate then trips and the loop exits cleanly
	// with ErrBudgetExceeded.
	tr.Add(99, 0, 0)
	_, err := agent.Run(context.Background(), p, reg, agent.LoopOptions{
		Model:         "x",
		Approver:      agent.AutoApprover{},
		Budget:        agent.Budget{MaxTokens: 100},
		BudgetTracker: tr,
		MaxIterations: 5,
	}, []providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}})
	if !errors.Is(err, agent.ErrBudgetExceeded) {
		t.Fatalf("want ErrBudgetExceeded, got %v", err)
	}
	if p.calls != 1 {
		// Iter 1 should have run; iter 2 should have been refused at
		// the gate.
		t.Errorf("calls = %d want 1", p.calls)
	}
}

func TestLoop_BudgetUnlimitedDoesNotEnforce(t *testing.T) {
	p := &streamProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventTextDelta, Text: "done"},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	tr := agent.NewTracker(nil)
	tr.Add(99999999, 0, 99999999) // huge pre-load; would trip any cap
	_, err := agent.Run(context.Background(), p, tools.NewRegistry(), agent.LoopOptions{
		Model:         "x",
		Budget:        agent.Budget{}, // unlimited
		BudgetTracker: tr,
	}, []providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}})
	if err != nil {
		t.Errorf("unlimited budget should not error: %v", err)
	}
}

func TestLoop_BudgetNilTrackerDoesNotEnforce(t *testing.T) {
	p := &streamProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventTextDelta, Text: "done"},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	// No tracker; any Budget value should be ignored.
	_, err := agent.Run(context.Background(), p, tools.NewRegistry(), agent.LoopOptions{
		Model:  "x",
		Budget: agent.Budget{MaxTokens: 1}, // would trip if a Tracker were set
	}, []providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}})
	if err != nil {
		t.Errorf("nil tracker should disable enforcement: %v", err)
	}
}

func TestLoop_BudgetPushesEstimateAfterCall(t *testing.T) {
	// A clean text-only response should still result in the tracker
	// accumulating something via the estimate path.
	p := &streamProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventTextDelta, Text: strings.Repeat("x", 200)},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	tr := agent.NewTracker(nil)
	_, err := agent.Run(context.Background(), p, tools.NewRegistry(), agent.LoopOptions{
		Model:         "x",
		BudgetTracker: tr,
		Budget:        agent.Budget{MaxTokens: 10_000_000}, // huge so we don't trip
	}, []providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tr.Tokens() == 0 {
		t.Errorf("expected token estimate to land in tracker; got 0")
	}
}

// TestSupervisor_SetRunBudgetWiresTracker exercises the supervisor's
// per-run + per-subtree plumbing: after SetRunBudget, each Spawn
// allocates a fresh subtree Tracker whose parent receives the
// subtree's spend.
func TestSupervisor_SetRunBudgetWiresTracker(t *testing.T) {
	parent := agent.NewTracker(nil)
	// Build a minimal supervisor with no log / provider; we're only
	// asserting SetRunBudget + RunTracker round-trip, not Spawn.
	sup := agent.NewSupervisor(nil, nil, nil)
	defer sup.Shutdown()
	if got := sup.RunTracker(); got != nil {
		t.Errorf("RunTracker pre-set should be nil, got %v", got)
	}
	sup.SetRunBudget(parent)
	if got := sup.RunTracker(); got != parent {
		t.Errorf("RunTracker after SetRunBudget should be parent")
	}
	sup.SetRunBudget(nil)
	if got := sup.RunTracker(); got != nil {
		t.Errorf("RunTracker after SetRunBudget(nil) should be nil")
	}
}
