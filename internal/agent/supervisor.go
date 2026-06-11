package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// State enumerates the 10 supervisor states from SPEC § Manage mode §
// State machine. Definitions live here because the Supervisor is the
// canonical owner of the value; state.go owns the Transition function
// (the state-machine rules) and is intentionally smaller.
type State int

const (
	StateSpawning State = iota
	StateQueued
	StateRunning
	StateAwaitingInput
	StateBlocked
	StatePausedByUser
	StateCompacting
	StateCancelling
	StateDone
	StateFailed
	StateOrphaned
)

func (s State) String() string {
	switch s {
	case StateSpawning:
		return "spawning"
	case StateQueued:
		return "queued"
	case StateRunning:
		return "running"
	case StateAwaitingInput:
		return "awaiting-input"
	case StateBlocked:
		return "blocked"
	case StatePausedByUser:
		return "paused-by-user"
	case StateCompacting:
		return "compacting"
	case StateCancelling:
		return "cancelling"
	case StateDone:
		return "done"
	case StateFailed:
		return "failed"
	case StateOrphaned:
		return "orphaned"
	}
	return "unknown"
}

func (s State) IsTerminal() bool {
	return s == StateDone || s == StateFailed || s == StateOrphaned
}

type SubAgent struct {
	ID              string
	ParentID        string
	RootID          string
	Attempt         int
	Title           string
	Model           string
	State           State
	TokensIn        int
	TokensOut       int
	CostCents       int
	ToolCalls       int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastHeartbeatAt time.Time
	Err             error
}

// SpawnContract is the typed four-part task spec a parent agent sends
// to a child (SPEC § Manage mode § Parent-child contract). All fields
// except Objective are optional; zero values mean "use defaults" or
// "no boundary set".
type SpawnContract struct {
	Objective       string
	OutputFormat    string
	ToolAllowlist   []string
	MaxTokens       int
	MaxTurns        int
	MaxWallClock    time.Duration
	SuccessCriteria string

	// System is the system prompt the loop hands the provider. Empty
	// means "no system prompt" (provider falls back to its built-in
	// default). Phase F-14: the daemon populates this with
	// agent.SystemPromptWithFrame so scheduled runs see the same frame
	// framing the chat path does.
	System string

	// Model is the provider model id (e.g. "claude-sonnet-4-6"). Empty
	// means the provider picks its built-in default. Phase F-14: the
	// daemon resolves this from the frame's ResolveProvider result.
	Model string

	// OverrideProvider, when non-nil, is used in place of the
	// supervisor's pre-wired provider for this single Spawn. Phase F-14
	// uses it so a scheduled run honours a frame's provider_override
	// without rebuilding the whole supervisor.
	OverrideProvider providers.Provider

	// OverrideRegistry, when non-nil, is used as the child's tool
	// registry directly (NOT filtered through ToolAllowlist). The
	// daemon's per-fire registry is already frame-scoped via
	// tools.NewDefaultRegistryWithBaseDirAndFrames; the supervisor
	// passes it through as-is.
	OverrideRegistry *tools.Registry
}

// Errors surfaced by Spawn / Retry. Exported so Slice 3e's Agent tool
// can errors.Is them and turn them into tool_result text for the
// model.
//
// ErrSpawnRefusedSolo + ErrSpawnBusyTight are the frame-mode variants
// of the concurrency cap: they let the model distinguish "delegation is
// off entirely for this frame" from "one child is already running, try
// again when it finishes" from the legacy "too many siblings at once"
// case. All three wrap ErrConcurrencyExceeded so older callers doing
// errors.Is(err, ErrConcurrencyExceeded) keep working.
var (
	ErrSpawnDepthExceeded       = errors.New("supervisor: spawn depth cap exceeded")
	ErrConcurrencyExceeded      = errors.New("supervisor: concurrency cap exceeded")
	ErrSpawnRefusedSolo         = fmt.Errorf("supervisor: spawn refused, frame mode 'solo' disables delegation: %w", ErrConcurrencyExceeded)
	ErrSpawnBusyTight           = fmt.Errorf("supervisor: spawn refused, frame mode 'tight' allows one in-flight child at a time: %w", ErrConcurrencyExceeded)
	ErrRestartIntensityExceeded = errors.New("supervisor: restart intensity exceeded (circuit broken)")
)

// Supervisor owns the per-process supervision state: the heartbeat
// tickers, the orphan sweeper, and (post-3b) the active-children map +
// retry-intensity counters that gate Spawn / Retry.
type Supervisor struct {
	maxConcurrentChildren int
	maxSpawnDepth         int
	restartMaxR           int
	restartMaxT           time.Duration

	// mode is the active frame's orchestrator mode (solo / tight /
	// orchestrator). It drives the per-parent spawn cap: solo refuses
	// every spawn, tight allows one in-flight child, orchestrator
	// allows up to maxConcurrentChildren. cmd/carlos calls SetMode at
	// session boot and again whenever /frame switch or /mode flips the
	// active frame so the cap stays in lock-step with the sysprompt.
	// Empty defaults to ModeOrchestrator to preserve the pre-modes
	// behaviour (cap 5) for tests + headless callers.
	mode string

	log       *SQLiteEventLog
	provider  providers.Provider
	baseReg   *tools.Registry
	heartbeat *HeartbeatTicker
	sweeper   *OrphanSweeper

	// defaultModel is the provider model id (e.g. "claude-sonnet-4-6"
	// or "google/gemini-3.5-flash") the supervisor hands to a child
	// when SpawnContract.Model is empty. Empty here means "no
	// fallback" — the child loop will hit the provider with whatever
	// it had, which for OpenAI-compatible endpoints means an HTTP 400.
	// cmd/carlos calls SetDefaultModel at session boot with the
	// active frame's resolved model so the chat-side `agent`
	// delegation tool doesn't have to thread it through every Spawn.
	defaultModel string

	// Slice 3b mutex-guarded state. children tracks every in-flight
	// child agent (one entry per active spawn); retries tracks
	// per-agent attempt timestamps for the OTP restart-intensity
	// breaker. Same mutex because Spawn / Retry / runChild touch both
	// together.
	mu       sync.Mutex
	children map[string]*runningChild
	retries  map[string]*retryAttempts

	// Phase 5 slice 5a: per-run + per-subtree budget plumbing.
	// parentTracker is the run-wide cumulative counter; each Spawn
	// allocates a fresh subtreeTracker whose parent is parentTracker,
	// so per-subtree spend rolls up into the per-run cap.
	// SetRunBudget installs both. nil = no enforcement (legacy default).
	parentTracker *Tracker

	// Phase F-12 (Fix 4) sub-agent approver inheritance. When a parent
	// in frame X spawns a child, the child's tool calls must hit the
	// same cross-frame WRITE detector the parent's calls hit; otherwise
	// the parent could bypass the cross-frame prompt by saying "delegate
	// this write to a subagent". cmd/carlos calls SetSubAgentApprover
	// with the same *LayeredApprover the parent loop uses, so the
	// child's loop sees the live activeFrame + subtree map (the approver
	// is mutex-guarded on SetFrameSubtrees so mid-conversation /frame
	// switches propagate to in-flight children too). nil falls back to
	// AutoApprover{}, preserving the pre-fix behaviour for tests and any
	// caller that doesn't wire one.
	subAgentApprover Approver

	// Phase 7 slice 7e/7f: per-agent worktree handles. The foreground
	// (cmd/carlos --worktree) opens a sandbox.Worktree for a session
	// and registers it under the top-level agent id so the apply
	// handler can find it on EvtApprovalAccepted / EvtApprovalRejected.
	//
	// The map is in-memory only - NOT persisted to the event log. If
	// carlos crashes between Propose and Accept the entry is gone and
	// the user has to re-run the session (the worktree on disk is also
	// orphaned and gets cleaned up by the next `git worktree prune`).
	// This is an explicit v0 limitation; a future slice could persist
	// worktree state alongside the agent row.
	//
	// Keyed by agentID. We accept the generic interface here rather
	// than *sandbox.Worktree to keep the supervisor free of a sandbox
	// import (and to keep tests fakeable).
	worktrees map[string]AgentWorktree
}

// AgentWorktree is the subset of *sandbox.Worktree the supervisor +
// apply-handler need. Defining it here (rather than importing the
// sandbox package directly) avoids a circular dependency on the
// transitive test wiring and lets tests stand up a fake worktree
// without git.
type AgentWorktree interface {
	Apply() error
	Discard() error
	Close() error
}

// NewSupervisor constructs a Supervisor backed by the given event log,
// provider, and base tool registry.
//
// Signature evolution:
//   - Slice 1g: NewSupervisor(log)
//   - Slice 3a: NewSupervisor(log, provider, baseReg)
//
// No backwards-compat alias is kept because pre-3a callers existed
// only in tests; they were updated alongside this signature change.
//
// Both `provider` and `baseReg` may be nil for supervisors that will
// never Spawn (e.g. tests that only exercise the heartbeat path).
// Spawn surfaces a clear error if invoked without them.
//
// After construction, callers MUST call Supervisor.Run(ctx) exactly
// once to launch the orphan sweep goroutine. Spawn / Stop / etc are
// safe before Run, but heartbeat liveness only matters once the
// sweeper is running.
func NewSupervisor(log *SQLiteEventLog, p providers.Provider, baseReg *tools.Registry) *Supervisor {
	s := &Supervisor{
		maxConcurrentChildren: 5,
		maxSpawnDepth:         1,
		restartMaxR:           3,
		restartMaxT:           60 * time.Second,
		// Default to orchestrator so pre-modes callers (tests, headless
		// dispatch before frame resolution) keep the legacy cap of 5.
		// cmd/carlos overrides this with the active frame's mode at
		// session boot.
		mode:      frame.ModeOrchestrator,
		log:       log,
		provider:  p,
		baseReg:   baseReg,
		children:  map[string]*runningChild{},
		retries:   map[string]*retryAttempts{},
		worktrees: map[string]AgentWorktree{},
	}
	if log != nil {
		s.heartbeat = NewHeartbeatTicker(log, RealClock{}, HeartbeatInterval)
		s.sweeper = NewOrphanSweeper(log, RealClock{}, SweepInterval, StalenessTolerance)
	}
	return s
}

// Run starts the per-process orphan sweep goroutine. Idempotent
// (subsequent calls are no-ops). The sweeper exits on ctx.Done() OR
// when the caller invokes Supervisor.Shutdown.
//
// Heartbeat tickers are started per-agent inside Spawn, not here.
func (s *Supervisor) Run(ctx context.Context) {
	if s.sweeper != nil {
		s.sweeper.Start(ctx)
	}
}

// Shutdown stops the orphan sweeper, every active heartbeat ticker,
// and cancels every in-flight child context. Idempotent.
//
// Shutdown does NOT block on child completion - children's
// SpawnResult channels still close in their own time once their
// goroutines unwind. Callers that need to wait should drain the
// channels they received from Spawn.
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	for _, c := range s.children {
		c.cancel()
	}
	s.mu.Unlock()
	if s.heartbeat != nil {
		s.heartbeat.StopAll()
	}
	if s.sweeper != nil {
		s.sweeper.Stop()
	}
}

// Spawn creates a new child agent under parentID and starts its
// inner tool-use loop in a goroutine. Returns the SubAgent snapshot
// (state = spawning at the moment of return), a SpawnResult channel
// that will receive exactly one value when the child terminates, and
// an error.
//
// Cap-check order (matches the docstring of slice 3b's spec):
//
//  1. nil-log guard
//  2. nil-provider guard (only when actually spawning a loop)
//  3. spawn-depth cap (ErrSpawnDepthExceeded)
//  4. circuit-breaker check on parentID's subtree
//  5. concurrency cap, scoped per-parent (ErrConcurrencyExceeded)
//  6. event-log writes (state_change kind=created + InsertAgent)
//  7. heartbeat ticker start + active-children registration
//  8. child goroutine launch
//
// Returned channel is buffered (cap 1) and closed by the worker after
// the single send, so a caller that never reads it doesn't leak the
// goroutine.
func (s *Supervisor) Spawn(ctx context.Context, parentID string, contract SpawnContract) (*SubAgent, <-chan SpawnResult, error) {
	if s.log == nil {
		return nil, nil, errors.New("supervisor.Spawn: nil log (constructed without state.db)")
	}
	// Pick the per-spawn provider: contract override wins so the daemon
	// can honour a frame's provider_override without rebuilding the
	// whole supervisor.
	spawnProvider := contract.OverrideProvider
	if spawnProvider == nil {
		spawnProvider = s.provider
	}
	if spawnProvider == nil {
		return nil, nil, errors.New("supervisor.Spawn: nil provider (constructor passed nil)")
	}

	// 3. Spawn-depth cap. computeDepth returns the depth of parentID
	//    in the projection cache; child depth = parent depth + 1.
	parentDepth, err := s.computeDepth(ctx, parentID)
	if err != nil {
		return nil, nil, fmt.Errorf("supervisor.Spawn: depth check: %w", err)
	}
	// Snapshot maxSpawnDepth under s.mu so a concurrent
	// SetMaxSpawnDepth doesn't race the read.
	s.mu.Lock()
	maxDepth := s.maxSpawnDepth
	s.mu.Unlock()
	if parentDepth+1 > maxDepth {
		return nil, nil, ErrSpawnDepthExceeded
	}

	// 4. Circuit-breaker check on the parent (and conceptually its
	//    subtree). For 3b we just check the direct parent; a future
	//    slice can walk descendants.
	if parentID != "" && s.IsCircuitBroken(parentID) {
		return nil, nil, ErrRestartIntensityExceeded
	}

	// 5. Concurrency cap, per-parent. A manager with N siblings
	//    shouldn't be starved by a peer subtree, so we count only
	//    children whose parent_id == this Spawn's parentID. The
	//    effective cap is the smaller of maxConcurrentChildren (the
	//    legacy hard ceiling, default 5) and the frame mode's cap (solo
	//    = 0, tight = 1, orchestrator = maxConcurrentChildren). Solo
	//    and tight return distinct errors so the model sees which
	//    mode-level constraint refused the delegation.
	s.mu.Lock()
	active := 0
	for _, c := range s.children {
		if c.parentID == parentID {
			active++
		}
	}
	cap := s.effectiveSpawnCapLocked()
	mode := s.mode
	s.mu.Unlock()
	if active >= cap {
		switch mode {
		case frame.ModeSolo:
			return nil, nil, ErrSpawnRefusedSolo
		case frame.ModeTight:
			return nil, nil, ErrSpawnBusyTight
		default:
			return nil, nil, ErrConcurrencyExceeded
		}
	}

	// 6. Event-log writes.
	id := newSpawnIDStrong()
	now := time.Now().UTC().Truncate(time.Millisecond)
	rootID := parentID
	if rootID == "" {
		rootID = id
	}
	created, err := NewStateChangeCreated(AgentCreated{
		ID:       id,
		ParentID: parentID,
		RootID:   rootID,
		Title:    contract.Objective,
		Model:    "", // Slice 3e will pull from provider config
	})
	if err != nil {
		return nil, nil, fmt.Errorf("supervisor.Spawn: marshal created: %w", err)
	}
	if _, err := s.log.Append(ctx, Event{
		AgentID: id, TS: now, Type: EvtStateChange, Payload: created,
	}); err != nil {
		return nil, nil, fmt.Errorf("supervisor.Spawn: append created: %w", err)
	}
	row := AgentRow{
		ID:              id,
		ParentID:        parentID,
		RootID:          rootID,
		State:           StateSpawning,
		Attempt:         1,
		Title:           contract.Objective,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}
	if err := s.log.InsertAgent(ctx, row); err != nil {
		return nil, nil, fmt.Errorf("supervisor.Spawn: insert row: %w", err)
	}

	// 7. Heartbeat + active-children registration. We derive a
	//    cancellable childCtx that is a *sibling* of the caller's
	//    ctx - Shutdown can cancel it independently of the parent.
	//
	// MaxWallClock > 0 layers a WithTimeout on top of the WithCancel.
	// We compose a single childCancel that releases BOTH so the inner
	// cancel returned by WithCancel is never orphaned (govet's
	// lostcancel rule). Calling the composed cancel is idempotent and
	// safe to invoke from multiple goroutines (the bridge goroutine
	// below + Shutdown + runChild's defer-chain).
	baseCtx, baseCancel := context.WithCancel(context.Background())
	childCtx := baseCtx
	childCancel := baseCancel
	if contract.MaxWallClock > 0 {
		var timeoutCancel context.CancelFunc
		childCtx, timeoutCancel = context.WithTimeout(baseCtx, contract.MaxWallClock)
		childCancel = func() {
			timeoutCancel()
			baseCancel()
		}
	}
	// Honor caller-side cancel too: bridge ctx.Done into childCtx.
	go func() {
		select {
		case <-ctx.Done():
			childCancel()
		case <-childCtx.Done():
		}
	}()

	// Phase 5 slice 5a: allocate a per-subtree Tracker whose parent is
	// the supervisor's run-wide parentTracker (if installed). Sibling
	// subtrees end up with independent Trackers but all roll up into
	// the same per-run cap.
	var subtreeTracker *Tracker
	if s.parentTracker != nil {
		subtreeTracker = NewTracker(s.parentTracker)
	}

	child := &runningChild{
		id:       id,
		parentID: parentID,
		cancel:   childCancel,
		done:     make(chan struct{}),
		// Buffered so a rapid-fire Steer doesn't block the supervisor.
		// Capacity 16 is more than the user could plausibly fire in
		// one tool-call boundary's worth of think time.
		steering: make(chan string, 16),
		tracker:  subtreeTracker,
	}
	s.mu.Lock()
	s.children[id] = child
	s.mu.Unlock()

	if s.heartbeat != nil {
		s.heartbeat.Start(childCtx, id)
	}

	// 8. Launch the worker.
	resultCh := make(chan SpawnResult, 1)
	// Phase F-14: a contract may carry its own pre-built registry
	// (the daemon does this so scheduled fires see a frame-scoped tool
	// set without round-tripping through ToolAllowlist). When set we
	// hand it through verbatim; otherwise fall back to the legacy
	// allowlist-filtered child registry.
	var childReg *tools.Registry
	if contract.OverrideRegistry != nil {
		childReg = contract.OverrideRegistry
	} else {
		childReg = buildChildRegistry(s.baseReg, contract.ToolAllowlist)
	}
	go s.runChild(childCtx, child, spawnProvider, childReg, contract, resultCh)

	return &SubAgent{
		ID:              id,
		ParentID:        parentID,
		RootID:          rootID,
		Attempt:         1,
		Title:           contract.Objective,
		State:           StateSpawning,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}, resultCh, nil
}

// Roster is implemented as a thin wrapper around the projection cache.
// Slice 4 (TUI) will own the rendering; this just returns the
// non-nil-active list so tests can introspect.
func (s *Supervisor) Roster() []*SubAgent { return nil }

// ErrAgentNotFound is what the three user-facing verbs return when the
// agent ID doesn't match any in-flight child. The TUI surfaces this as
// "agent already finished" or similar.
var ErrAgentNotFound = errors.New("supervisor: agent not found among in-flight children")

// Steer appends a `steering` event to the target agent's log (for
// audit) and queues the message for delivery at the next tool-call
// boundary (the agent.Run loop's between-iterations seam).
//
// SPEC contract: "inject a [steering] message into the agent's event
// log; delivered at the next tool-call boundary, never mid-inference."
// The loop's drainSteering picks it up before the next provider call
// and prepends it as a user-role message tagged "[steer] ".
//
// If the child's steering channel is full (cap 16; user fired too many
// nudges between tool-call boundaries), Steer returns nil - the audit
// event is appended but the runtime injection drops. The user can
// re-steer once the queue drains.
func (s *Supervisor) Steer(id, message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("supervisor.Steer: empty message")
	}
	s.mu.Lock()
	child, ok := s.children[id]
	s.mu.Unlock()
	if !ok {
		return ErrAgentNotFound
	}
	// Audit event first - the log is the source of truth, even if the
	// runtime delivery drops on a full channel. An Append failure here
	// is a real storage error (not the documented runtime-delivery drop
	// that justifies the non-blocking send below), so surface it.
	payload, _ := json.Marshal(struct {
		Message string `json:"message"`
	}{Message: message})
	if _, err := s.log.Append(context.Background(), Event{
		AgentID: id,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    EvtSteering,
		Payload: payload,
	}); err != nil {
		return fmt.Errorf("supervisor.Steer: append audit event: %w", err)
	}
	// Non-blocking send: drop on full so we never wedge the supervisor.
	select {
	case child.steering <- message:
	default:
	}
	return nil
}

// Interrupt cancels the agent's current turn. Per SPEC: "abort the
// current turn (soft), keep the session and context alive, return
// control." V0 implementation: we cancel the entire child context,
// which terminates the agent.Run loop in flight. Tool calls already
// completed (and committed to the event log) stay in place - the loop
// runs to completion of the current iteration, then returns.
//
// V0 limitation flagged in SPEC: this collapses Interrupt and Stop's
// runtime behavior (both cancel the loop) - a future refinement
// introduces a per-iteration ctx so Interrupt truly aborts only the
// current turn, not the whole agent.
func (s *Supervisor) Interrupt(id string) error {
	s.mu.Lock()
	child, ok := s.children[id]
	s.mu.Unlock()
	if !ok {
		return ErrAgentNotFound
	}
	child.cancel()
	return nil
}

// Stop terminates the agent gracefully - cancels the child context;
// the in-flight iteration completes naturally; transition to done /
// failed is recorded by runChild's classifier. SPEC: "Default is
// graceful drain: signal stop, wait for the agent to reach a safe
// boundary, then transition to done/failed. Drain timeout escalates to
// hard-kill if the agent doesn't reach a boundary within the budget."
//
// V0 implementation: ctx cancellation IS the drain signal. The
// agent.Run loop checks ctx at every iteration boundary. Hard-kill on
// timeout is Kill's responsibility (and a future refinement: today
// Stop and Kill are the same cancel).
func (s *Supervisor) Stop(id string) error {
	s.mu.Lock()
	child, ok := s.children[id]
	s.mu.Unlock()
	if !ok {
		return ErrAgentNotFound
	}
	child.cancel()
	return nil
}

// Kill is the hard-cancel verb. Today same as Stop (ctx cancel); the
// future "hard-kill after drain timeout" semantic lives behind a
// configurable timeout the supervisor wraps Stop with - TODO Phase 5.
func (s *Supervisor) Kill(id string) error {
	return s.Stop(id)
}

// Retry implements the OTP restart-intensity counter. Slice 3e (the
// Agent tool) will be the first production caller; today the contract
// is: every call records a timestamp; if more than restartMaxR calls
// land within restartMaxT, the breaker trips for id and we return
// ErrRestartIntensityExceeded.
//
// Retry does NOT actually re-spawn the child today - that's Slice 3e
// + future work. The breaker check is what slice 3b owes; the
// concrete restart pipeline is the Agent tool's responsibility.
//
// TODO Slice 4: on breaker trip, walk descendants of `id` in the
// projection cache and cancel each in-flight child + surface a roster
// badge.
func (s *Supervisor) Retry(id string) (*SubAgent, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.recordRetry(id, now)
	count := s.retryCount(id, now)
	if count > s.restartMaxR {
		s.markCircuitBroken(id)
		s.mu.Unlock()
		return nil, ErrRestartIntensityExceeded
	}
	s.mu.Unlock()
	// The real respawn path lives in Slice 3e; for slice 3b we just
	// return nil + nil to signal "breaker not tripped, caller may
	// proceed with their own respawn logic".
	return nil, nil
}

// ActiveChildren returns the count of in-flight children whose parent
// is parentID. Exposed (lowercase parent param, exported func) so
// tests can assert the concurrency-cap accounting without poking at
// internals.
func (s *Supervisor) ActiveChildren(parentID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.children {
		if c.parentID == parentID {
			n++
		}
	}
	return n
}

// ChildSnapshot is the per-child datum the inline chat panel renders.
// One row per in-flight child of the parentID passed to
// SnapshotChildrenOf. The row reads from the projection cache, so
// state + spend reflect whatever the supervisor has already committed
// to the log; reads are best-effort and a child that just terminated
// may drop out of the slice on the next call.
type ChildSnapshot struct {
	AgentID string
	State   State
	Title   string
	// LastTool is the name of the sub-agent's most recently issued tool
	// call. Empty until the sub-agent appends its first EvtToolCall (or
	// when the projection-cache read raced the log). The parent's
	// bordered agent card surfaces this as "running {tool}" so the
	// viewer sees the live action signal, not the stale objective.
	LastTool  string
	Tokens    int
	CostCents int
	StartedAt time.Time
}

// SnapshotChildrenOf returns one ChildSnapshot per in-flight child of
// parentID. Reads the supervisor's children map for the active set,
// then enriches each row from the agents projection cache. A child
// whose row hasn't landed in the cache yet (Spawn raced with the
// projection insert) is skipped rather than reported with zero state.
//
// The caller is the inline chat panel; nil log makes this return an
// empty slice so tests that drive the chat without a real DB don't
// have to stand one up.
func (s *Supervisor) SnapshotChildrenOf(ctx context.Context, parentID string) []ChildSnapshot {
	if s == nil || s.log == nil {
		return nil
	}
	s.mu.Lock()
	ids := make([]string, 0, len(s.children))
	for id, c := range s.children {
		if c.parentID == parentID {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	out := make([]ChildSnapshot, 0, len(ids))
	for _, id := range ids {
		row, ok, err := s.log.GetAgent(ctx, id)
		if err != nil || !ok {
			continue
		}
		// Best-effort: surface the most recent tool call for the
		// running-card body. A failure here (corrupt payload, DB hiccup)
		// must not skip the row; we just render the static objective
		// instead of the live tool name.
		lastTool, _, _ := s.log.LastToolCall(ctx, id)
		out = append(out, ChildSnapshot{
			AgentID:   row.ID,
			State:     row.State,
			Title:     row.Title,
			LastTool:  lastTool,
			Tokens:    int(row.TokensIn + row.TokensOut),
			CostCents: int(row.CostCents),
			StartedAt: row.CreatedAt,
		})
	}
	return out
}

// SetMaxSpawnDepth overrides the default depth cap. Useful for tests
// that want to exercise deeper chains without rebuilding the whole
// supervisor.
//
// Mutex-guarded because Spawn reads maxSpawnDepth under s.mu when
// computing the depth gate; a lock-free write here would race the
// reader under -race.
func (s *Supervisor) SetMaxSpawnDepth(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxSpawnDepth = n
}

// SetMaxConcurrentChildren overrides the default per-parent
// concurrency cap. Test-only knob.
//
// Mutex-guarded because effectiveSpawnCapLocked reads
// maxConcurrentChildren under s.mu; a lock-free write here would race
// concurrent Spawn callers under -race.
func (s *Supervisor) SetMaxConcurrentChildren(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxConcurrentChildren = n
}

// SetMode updates the active frame mode that drives the per-parent
// spawn cap. cmd/carlos calls this once at session boot with the
// resolved frame's mode and again whenever /frame switch or /mode
// flips the active frame, so the cap stays in lock-step with the
// system prompt. Empty / unknown modes fall back to ModeSolo (the
// safest stance: no delegation), matching frame.SpawnCapFor.
func (s *Supervisor) SetMode(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if frame.IsValidMode(mode) {
		s.mode = mode
	} else {
		s.mode = frame.ModeSolo
	}
}

// Mode returns the supervisor's current frame mode. Exposed so the
// manage TUI and tests can render / assert the active cap-driver.
func (s *Supervisor) Mode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// SetDefaultModel installs the fallback provider model id used when a
// SpawnContract carries an empty Model field. cmd/carlos calls this at
// session boot (with the active frame's resolved model) and again on
// /frame switch so the chat-side `agent` tool — which has no plumbing
// for per-call model selection — can rely on the supervisor providing a
// sane default. Pass "" to disable the fallback.
//
// Background: through v0.7.5 the AgentTool built a SpawnContract with
// no Model field, runChild passed contract.Model verbatim to
// providers.Request, and OpenAI-compatible endpoints (notably
// OpenRouter) rejected the call with `HTTP 400: No models provided`.
// Routing the parent's model through here closes that loop without
// teaching every Spawn callsite about the active frame.
func (s *Supervisor) SetDefaultModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultModel = model
}

// DefaultModel returns the supervisor's fallback model id (the empty
// string if none is configured). Exposed primarily for tests + the
// /whoami diagnostic surface.
func (s *Supervisor) DefaultModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.defaultModel
}

// SpawnCap returns the effective per-parent spawn cap. Exposed so the
// manage TUI can render the "mode=orchestrator (cap 5)" line without
// reaching into Supervisor internals.
func (s *Supervisor) SpawnCap() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effectiveSpawnCapLocked()
}

// effectiveSpawnCapLocked computes the per-parent spawn cap from the
// active mode + the legacy hard ceiling. Caller must hold s.mu.
// Orchestrator mode honours maxConcurrentChildren (default 5);
// solo / tight cap below it; the smaller wins so test knobs that lower
// the ceiling stay authoritative.
func (s *Supervisor) effectiveSpawnCapLocked() int {
	modeCap := frame.SpawnCapFor(s.mode)
	if s.mode == frame.ModeOrchestrator {
		// Orchestrator wants the legacy hard ceiling; SpawnCapFor's
		// constant (5) matches today's default but the
		// SetMaxConcurrentChildren test knob may lower it.
		if s.maxConcurrentChildren < modeCap {
			return s.maxConcurrentChildren
		}
		return modeCap
	}
	if s.maxConcurrentChildren < modeCap {
		return s.maxConcurrentChildren
	}
	return modeCap
}

// SetRestartIntensity overrides MaxR + MaxT. Test-only knob.
//
// Mutex-guarded because Retry reads restartMaxR / restartMaxT under
// s.mu when sliding the retry-count window; a lock-free write here
// would race that reader under -race.
func (s *Supervisor) SetRestartIntensity(maxR int, maxT time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restartMaxR = maxR
	s.restartMaxT = maxT
}

// SetSubAgentApprover installs the Approver every Spawn-launched child
// loop should use in place of the legacy AutoApprover. cmd/carlos calls
// this at session boot with the parent's LayeredApprover so a child's
// write/edit calls flow through the same Phase F-12 cross-frame
// detector. The supervisor stores the Approver by reference; subsequent
// LayeredApprover.SetFrameSubtrees calls (e.g. on /frame switch)
// propagate to every in-flight child automatically because the loop
// re-invokes ApproveToolCall on the same instance.
//
// Pass nil to restore the AutoApprover fallback (tests + the headless
// dispatch path that runs without a layered approver). The sub-agent
// approver does NOT affect the parent loop's approver — the parent
// remains wired through chatglue/loop options.
func (s *Supervisor) SetSubAgentApprover(a Approver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subAgentApprover = a
}

// SubAgentApprover returns the currently-installed sub-agent approver,
// or nil if none has been set. Exposed for tests + diagnostic surfaces.
func (s *Supervisor) SubAgentApprover() Approver {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subAgentApprover
}

// SetRunBudget installs the supervisor's run-wide Tracker. After this
// call, every subsequent Spawn allocates a per-subtree Tracker whose
// parent is the run-wide one, so per-subtree spend rolls up into the
// per-run cap automatically. Passing nil disables enforcement again
// (children spawned afterwards get no Tracker).
//
// Phase 5 slice 5a. The per-run Budget itself is supplied to the loop
// via LoopOptions.Budget - Spawn pulls per-subtree caps from the
// SpawnContract (MaxTokens, MaxWallClock). Foreground integration
// (cmd/carlos) is responsible for setting the parent Budget on its
// own top-level Run invocation.
func (s *Supervisor) SetRunBudget(t *Tracker) {
	s.mu.Lock()
	s.parentTracker = t
	s.mu.Unlock()
}

// RunTracker returns the supervisor's run-wide Tracker, or nil if
// SetRunBudget hasn't been called. Useful for TUI runaway-cost views.
// StartHeartbeat begins emitting heartbeat events for agentID under
// the supervisor's existing ticker. Idempotent (per-id no-op if
// already started). Used by cmd/carlos.runDefault to keep the
// stable chat-default agent alive across user-idle periods - without
// it, agent.Recover would orphan the chat agent on every restart.
func (s *Supervisor) StartHeartbeat(ctx context.Context, agentID string) {
	if s == nil || s.heartbeat == nil || agentID == "" {
		return
	}
	s.heartbeat.Start(ctx, agentID)
}

func (s *Supervisor) RunTracker() *Tracker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parentTracker
}

// SetAgentWorktree registers w as the sandbox for agentID. The apply
// handler looks up the entry on EvtApprovalAccepted / EvtApprovalRejected
// to decide what to Apply or Discard. Calling SetAgentWorktree twice
// for the same agentID overwrites the previous entry - the foreground
// is the sole owner and is expected to call this once per session.
//
// Phase 7 slice 7e/7f. The map is in-memory only; see the worktrees
// field doc for crash semantics.
func (s *Supervisor) SetAgentWorktree(agentID string, w AgentWorktree) {
	if agentID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if w == nil {
		delete(s.worktrees, agentID)
		return
	}
	s.worktrees[agentID] = w
}

// AgentWorktreeFor returns the registered worktree for agentID, or
// (nil, false) if none has been set. Used by the apply handler to
// look up the sandbox on an approval-resolution event.
func (s *Supervisor) AgentWorktreeFor(agentID string) (AgentWorktree, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.worktrees[agentID]
	return w, ok
}

// ClearAgentWorktree drops the registered worktree for agentID. The
// foreground calls this after the worktree has been Closed so the
// supervisor doesn't hold a stale handle. No-op if no entry is
// registered.
func (s *Supervisor) ClearAgentWorktree(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.worktrees, agentID)
}
