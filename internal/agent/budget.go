// Phase 5 slice 5a - per-run + per-subtree token/cost/wall-clock caps.
//
// SPEC § Manage mode § Safety rails:
//
//   - Per-run and per-subtree token/cost caps - hard ceilings, enforced
//     before each provider call.
//
// Why before each call (and not "as the call returns"): refusing to call
// gives the model a clean tool_result it can adapt to - "I've hit the
// cost cap; let me wrap up" - instead of yanking the chair at completion
// time. Mid-call cancellation is the existing ctx mechanism; this layer
// is the polite "don't even ask" gate.
//
// # Per-run vs per-subtree
//
// One Tracker covers exactly one scope. The Supervisor maintains:
//
//   - a *parent* Tracker per top-level run (the user's invocation), and
//   - a *subtree* Tracker per sub-agent root (one per Spawn call).
//
// Sibling subtrees have independent Trackers - a runaway child does not
// starve its siblings. The parent Tracker accumulates child increments
// too: every time a subtree's Tracker.Add is called, the supervisor
// propagates the same delta up to the parent (see Tracker.Parent).
// That way a hard per-run cap genuinely caps the whole tree, while the
// per-subtree cap stops one branch from monopolising the parent budget.
//
// # Cost accounting
//
// Providers vary wildly in what they report. Some (Anthropic) expose
// per-call token usage in their stream events; some (OpenAI streaming)
// report usage only at end-of-call; some (Ollama, fake) report nothing.
// The Tracker is deliberately ignorant of that variation - callers
// (today: the loop wrapper) decide what to push in. The default loop
// hook estimates cost from message body length when no usage event has
// arrived; adapters that wire real usage call Tracker.Add with the
// real numbers and the estimate is skipped.
//
// CostCents is in integer cents to dodge float comparison footguns when
// summing many tiny calls. A 100-call run that costs 47.3¢ is stored as
// 47 (not 47.3). At v0 scale this rounding is in the noise; if it ever
// matters, switch to int64 millicents and update the cents accessor.
package agent

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Budget is the immutable cap shape. Zero in any field means "no cap"
// for that dimension. Negative values are invalid (caught at validation
// time). Wall-clock is measured from the Tracker's StartedAt.
type Budget struct {
	MaxTokens    int64         // sum of tokensIn + tokensOut
	MaxCostCents int64         // integer cents; see file header for rationale
	MaxWallClock time.Duration // since Tracker creation
}

// Validate refuses negative caps. Zero is fine (= unlimited on that
// dimension).
func (b Budget) Validate() error {
	if b.MaxTokens < 0 {
		return fmt.Errorf("budget: MaxTokens must be >= 0, got %d", b.MaxTokens)
	}
	if b.MaxCostCents < 0 {
		return fmt.Errorf("budget: MaxCostCents must be >= 0, got %d", b.MaxCostCents)
	}
	if b.MaxWallClock < 0 {
		return fmt.Errorf("budget: MaxWallClock must be >= 0, got %s", b.MaxWallClock)
	}
	return nil
}

// IsUnlimited reports whether every dimension is zero (no caps anywhere).
// Used by the loop to skip the CheckBudget round trip entirely when the
// caller passed a zero Budget.
func (b Budget) IsUnlimited() bool {
	return b.MaxTokens == 0 && b.MaxCostCents == 0 && b.MaxWallClock == 0
}

// Tracker accumulates token / cost / time spend for one scope (a run
// or a subtree). Safe for concurrent use; the loop touches a Tracker
// from one goroutine at a time but parent propagation can be triggered
// from sibling subtree goroutines concurrently.
type Tracker struct {
	mu        sync.Mutex
	tokens    int64 // tokensIn + tokensOut sum
	costCents int64
	startedAt time.Time
	clock     func() time.Time // injectable for tests; defaults to time.Now

	// parent (optional) receives the same Add deltas this Tracker
	// receives, so a per-subtree Tracker rolls its spend up into the
	// per-run Tracker. nil parent = top-level (the run-wide Tracker).
	parent *Tracker
}

// NewTracker constructs a Tracker stamped with time.Now as StartedAt.
// Pass nil for parent at the top level; pass the run-wide Tracker as
// parent when constructing a subtree Tracker so the subtree's spend
// counts against the run cap too.
func NewTracker(parent *Tracker) *Tracker {
	return &Tracker{
		startedAt: time.Now(),
		clock:     time.Now,
		parent:    parent,
	}
}

// newTrackerWithClock is the test seam - lets a test inject a clock
// without exposing it on the public NewTracker (which 99% of callers
// shouldn't think about).
func newTrackerWithClock(parent *Tracker, clock func() time.Time) *Tracker {
	t := NewTracker(parent)
	t.clock = clock
	t.startedAt = clock()
	return t
}

// Add records spend on this Tracker AND propagates to the parent (if
// any) so per-subtree spend also counts against the per-run cap.
// Negative values are clamped to zero - defensive against bad usage
// reports from a provider adapter.
func (t *Tracker) Add(tokensIn, tokensOut, costCents int64) {
	if tokensIn < 0 {
		tokensIn = 0
	}
	if tokensOut < 0 {
		tokensOut = 0
	}
	if costCents < 0 {
		costCents = 0
	}
	t.mu.Lock()
	t.tokens += tokensIn + tokensOut
	t.costCents += costCents
	t.mu.Unlock()
	if t.parent != nil {
		t.parent.Add(tokensIn, tokensOut, costCents)
	}
}

// Tokens returns the running token total (in + out).
func (t *Tracker) Tokens() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tokens
}

// CostCents returns the running cost total in integer cents.
func (t *Tracker) CostCents() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.costCents
}

// Elapsed returns wall-clock time since the Tracker was constructed.
func (t *Tracker) Elapsed() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.clock().Sub(t.startedAt)
}

// Remaining reports what's left on each dimension of b. exceeded is true
// if any dimension is already over its cap. Returned remainders are
// clamped to zero on an exceeded dimension (never negative).
//
// A zero cap is treated as unlimited and the corresponding remainder is
// returned as 0 with exceeded staying false on that dimension - callers
// should consult Budget.IsUnlimited to disambiguate.
func (t *Tracker) Remaining(b Budget) (tokens, cost int64, wall time.Duration, exceeded bool) {
	t.mu.Lock()
	usedTokens := t.tokens
	usedCost := t.costCents
	now := t.clock()
	started := t.startedAt
	t.mu.Unlock()

	if b.MaxTokens > 0 {
		tokens = b.MaxTokens - usedTokens
		if tokens < 0 {
			tokens = 0
			exceeded = true
		}
	}
	if b.MaxCostCents > 0 {
		cost = b.MaxCostCents - usedCost
		if cost < 0 {
			cost = 0
			exceeded = true
		}
	}
	if b.MaxWallClock > 0 {
		wall = b.MaxWallClock - now.Sub(started)
		if wall < 0 {
			wall = 0
			exceeded = true
		}
	}
	return tokens, cost, wall, exceeded
}

// CheckBudget returns nil if the Tracker has headroom on every capped
// dimension of b. Returns one of the ErrBudgetExceeded* sentinels
// otherwise (the first dimension that trips wins; order: tokens, cost,
// time - same order as the Budget struct fields).
//
// Designed to be called BEFORE each provider stream - see SPEC for the
// rationale on "refuse with a clean tool_result the model can adapt to"
// vs cancelling mid-call.
func (t *Tracker) CheckBudget(b Budget) error {
	if b.IsUnlimited() {
		return nil
	}
	t.mu.Lock()
	usedTokens := t.tokens
	usedCost := t.costCents
	elapsed := t.clock().Sub(t.startedAt)
	t.mu.Unlock()

	if b.MaxTokens > 0 && usedTokens >= b.MaxTokens {
		return fmt.Errorf("%w: %d tokens used, cap %d", ErrBudgetExceededTokens, usedTokens, b.MaxTokens)
	}
	if b.MaxCostCents > 0 && usedCost >= b.MaxCostCents {
		return fmt.Errorf("%w: %d cents used, cap %d", ErrBudgetExceededCost, usedCost, b.MaxCostCents)
	}
	if b.MaxWallClock > 0 && elapsed >= b.MaxWallClock {
		return fmt.Errorf("%w: %s elapsed, cap %s", ErrBudgetExceededTime, elapsed, b.MaxWallClock)
	}
	return nil
}

// Snapshot is a point-in-time copy of a Tracker's counters. Useful for
// tests and for the TUI's runaway-cost view. Lock-free read happens
// inside the method; the returned struct is a value type.
type Snapshot struct {
	Tokens    int64
	CostCents int64
	Elapsed   time.Duration
	StartedAt time.Time
}

// Snapshot returns a value-typed view of the Tracker's current counters.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Snapshot{
		Tokens:    t.tokens,
		CostCents: t.costCents,
		Elapsed:   t.clock().Sub(t.startedAt),
		StartedAt: t.startedAt,
	}
}

// Sentinels classified into one umbrella + per-dimension variants. The
// loop wraps these so callers can do errors.Is(err, ErrBudgetExceeded)
// without caring which dimension tripped, OR errors.Is on a specific
// variant when the runaway-cost view wants to display it.
var (
	ErrBudgetExceeded       = errors.New("budget: cap exceeded")
	ErrBudgetExceededTokens = fmt.Errorf("%w: tokens", ErrBudgetExceeded)
	ErrBudgetExceededCost   = fmt.Errorf("%w: cost", ErrBudgetExceeded)
	ErrBudgetExceededTime   = fmt.Errorf("%w: wall-clock", ErrBudgetExceeded)
)

// EstimateCallCost is a rough fallback for adapters that don't report
// usage. We bill 1 cent per ~10k chars of body - a deliberately
// loose number that just keeps the runaway alarm honest until adapters
// wire real usage. Callers that have real numbers should NOT use this
// and instead pass actual usage to Tracker.Add.
//
// Bytes counted: every Text and ToolResult body in messages, plus
// system + every tool spec description. Tool inputs the model emitted
// are billed on the assistant side via the response stream - we don't
// double-count them on the request side.
func EstimateCallCost(systemBytes int, requestBodyBytes int) int64 {
	total := systemBytes + requestBodyBytes
	if total <= 0 {
		return 0
	}
	cents := int64(total / 10000)
	if cents < 1 {
		cents = 1
	}
	return cents
}

// EstimateCallTokens is the parallel fallback for tokens. We bill 1
// token per 4 chars (the standard rough heuristic). Same caveat as
// EstimateCallCost: real adapters should report real usage.
func EstimateCallTokens(systemBytes int, requestBodyBytes int) int64 {
	total := systemBytes + requestBodyBytes
	if total <= 0 {
		return 0
	}
	return int64(total / 4)
}
