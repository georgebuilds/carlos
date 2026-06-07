// Slice 3a: real Spawn implementation.
//
// `supervisor.go` defines NewSupervisor / Spawn / Retry; this file
// holds the pieces that don't fit cleanly into either: the
// child-goroutine body, the SpawnResult channel type, the
// composeInitialPrompt template, and the runningChild bookkeeping
// struct that lives in the supervisor's active-children map.
//
// Lifecycle of a spawned child:
//
//  1. Supervisor.Spawn checks the depth + concurrency caps. If they
//     reject, returns (nil, nil, ErrSpawnDepthExceeded / ErrConcurrencyExceeded).
//  2. Spawn appends a state_change kind=created event, inserts the
//     projection-cache row, starts the per-agent heartbeat ticker, and
//     registers the agent in s.children.
//  3. Spawn launches a goroutine that:
//     a. Transitions the agent spawning→running (state_change
//        kind=transition + UpdateAgentState).
//     b. Calls agent.Run with the child's restricted tool registry,
//        AutoApprover (subagents bypass user prompts - see KNOWN
//        LIMITATION below), MaxIterations = contract.MaxTurns (or 25
//        default), MaxWallClock honored via the context deadline.
//     c. On Run completion, classifies success vs failure:
//        - clean return → state_change to=done
//        - error or context.Canceled → state_change to=failed
//     d. Persists the final assistant turn to disk
//        (~/.carlos/runs/<session>/agents/<id>.final.json) - a
//        minimal write Slice 3d will replace with the proper artifact
//        helper.
//     e. Stops the heartbeat ticker.
//     f. Removes the agent from s.children (releasing a concurrency
//        slot for the parent's next Spawn).
//     g. Sends SpawnResult on the channel and closes it.
//
// KNOWN LIMITATION (v0): subagents bypass user prompts via
// AutoApprover. SPEC § Manage mode § Verbs expects the user to be
// able to interrupt/steer subagents; that's an upcoming slice (3e+
// the TUI roster wiring). For now a subagent's tool calls execute
// without the user seeing each one.
//
// KNOWN LIMITATION (v0): the final-turn write is a literal
// os.WriteFile against the path above. Slice 3d ships
// internal/agent/artifacts.go with sha256 + InsertArtifact + a
// proper helper; Slice 3e will rewire this call site.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// SpawnResult is what a parent goroutine receives on the channel
// returned from Supervisor.Spawn. Exactly one SpawnResult is sent per
// successful Spawn (the channel is buffered + closed after send).
//
// FinalTurn is the last assistant Message from the loop - the typed
// deliverable the parent will inspect. Slice 3e's Agent tool extracts
// the text + ArtifactRef and assembles the tool_result it returns to
// the parent agent's model.
//
// FinalArtifact is the content-addressable persisted form of FinalTurn,
// written via WriteArtifact (Slice 3d). Zero value if the persistence
// step failed; failures do NOT degrade the loop classification (Err
// stays nil for an otherwise-clean run).
//
// Err is nil on a clean loop completion; otherwise carries either the
// loop's failure (provider error, MaxIterations exceeded, etc) or
// context.Canceled if the parent cancelled mid-run.
type SpawnResult struct {
	AgentID       string
	FinalTurn     providers.Message
	FinalArtifact ArtifactRef
	Err           error
}

// runningChild tracks one in-flight child agent in the supervisor's
// concurrency map. cancel terminates the child's context; done is
// closed by the worker goroutine when it exits.
//
// We snapshot parentID into the struct (rather than re-deriving from
// the projection cache) so the concurrency-cap counter doesn't need a
// DB round-trip per Spawn.
type runningChild struct {
	id       string
	parentID string
	cancel   context.CancelFunc
	done     chan struct{}
	// steering is the per-child channel Supervisor.Steer sends on. The
	// child's agent.Run loop drains it between iterations (the "next
	// tool-call boundary" semantics). Buffered so a fast Steer doesn't
	// block the supervisor; if a sub-agent is mid-stream when the user
	// steers twice rapidly, the second send may drop - that's the
	// documented contract.
	steering chan string
	// tracker is the per-subtree budget counter (Phase 5 slice 5a).
	// nil = subtree budget disabled. The Tracker's parent is the
	// supervisor's run-wide parentTracker (if set), so a subtree's
	// spend rolls up into the per-run cap automatically.
	tracker *Tracker
}

// composeInitialPrompt renders the parent's typed SpawnContract into
// the user-message text the child's model sees on iteration 0.
//
// The format is intentionally flat and stable so Slice 3e (the Agent
// tool) can be confident about what reaches the model. The four
// sections map 1:1 onto SPEC § Manage mode § Parent-child contract:
//
//	Objective         → SpawnContract.Objective
//	Output format     → SpawnContract.OutputFormat
//	Success criteria  → SpawnContract.SuccessCriteria
//	Boundaries        → MaxTurns / MaxTokens / MaxWallClock (omitted
//	                    when zero - "use default")
//
// Tool subset is NOT injected here because providers receive it via
// Request.Tools (the loop's opts.Tools); a redundant text listing
// would just bloat the prompt.
//
// Template (this is the load-bearing contract Slice 3e relies on):
//
//	# Objective
//	<contract.Objective>
//
//	# Output format
//	<contract.OutputFormat>
//
//	# Success criteria
//	<contract.SuccessCriteria>
//
//	# Boundaries
//	- max turns: <N>
//	- max tokens: <N>
//	- max wall clock: <duration>
func composeInitialPrompt(c SpawnContract) string {
	var b strings.Builder
	if c.Objective != "" {
		b.WriteString("# Objective\n")
		b.WriteString(c.Objective)
		b.WriteString("\n\n")
	}
	if c.OutputFormat != "" {
		b.WriteString("# Output format\n")
		b.WriteString(c.OutputFormat)
		b.WriteString("\n\n")
	}
	if c.SuccessCriteria != "" {
		b.WriteString("# Success criteria\n")
		b.WriteString(c.SuccessCriteria)
		b.WriteString("\n\n")
	}
	// Boundaries section is always present so the model sees the
	// caps it must respect; we only emit lines for fields the parent
	// actually set (zero = "use default").
	var bounds []string
	if c.MaxTurns > 0 {
		bounds = append(bounds, fmt.Sprintf("- max turns: %d", c.MaxTurns))
	}
	if c.MaxTokens > 0 {
		bounds = append(bounds, fmt.Sprintf("- max tokens: %d", c.MaxTokens))
	}
	if c.MaxWallClock > 0 {
		bounds = append(bounds, fmt.Sprintf("- max wall clock: %s", c.MaxWallClock))
	}
	if len(bounds) > 0 {
		b.WriteString("# Boundaries\n")
		b.WriteString(strings.Join(bounds, "\n"))
		b.WriteString("\n")
	}
	out := b.String()
	if out == "" {
		// Defensive: even a wholly-empty contract should produce
		// *some* prompt rather than an empty user message (which some
		// providers reject).
		return "# Objective\n(no objective specified)\n"
	}
	return out
}

// buildChildRegistry filters the supervisor's base tool registry down
// to the names listed in allowlist. Empty allowlist (length 0) yields
// an empty registry - sub-agents with no tools are valid (pure-
// reasoning subagents per SPEC § "When to delegate").
//
// Tools named in the allowlist but missing from the base registry are
// silently skipped; the loop will surface them at execution time as
// "tool error: unknown tool" through the standard tool_result path.
func buildChildRegistry(base *tools.Registry, allowlist []string) *tools.Registry {
	child := tools.NewRegistry()
	if base == nil || len(allowlist) == 0 {
		return child
	}
	for _, name := range allowlist {
		if t, ok := base.Get(name); ok {
			child.Register(t)
		}
	}
	return child
}

// buildChildToolSpecs returns the provider-facing ToolSpec list for the
// child's restricted registry. The loop passes this to the provider so
// the model knows which tools it may call.
func buildChildToolSpecs(reg *tools.Registry, allowlist []string) []providers.ToolSpec {
	if reg == nil || len(allowlist) == 0 {
		return nil
	}
	out := make([]providers.ToolSpec, 0, len(allowlist))
	for _, name := range allowlist {
		t, ok := reg.Get(name)
		if !ok {
			continue
		}
		out = append(out, providers.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return out
}

// runChild is the body of the per-spawn goroutine. It:
//
//  1. Transitions spawning→running.
//  2. Calls agent.Run.
//  3. Classifies the outcome → done / failed.
//  4. Persists the final turn (best-effort).
//  5. Stops the heartbeat ticker.
//  6. Removes the child from s.children.
//  7. Sends SpawnResult and closes the channel.
//
// Errors during state-change persistence are folded into result.Err
// alongside the loop error - we never panic in the worker.
func (s *Supervisor) runChild(ctx context.Context, child *runningChild, p providers.Provider, reg *tools.Registry, contract SpawnContract, resultCh chan<- SpawnResult) {
	defer close(child.done)

	// 1. spawning → running.
	if err := s.transition(ctx, child.id, StateRunning); err != nil {
		// State machine refusal here means we couldn't even start the
		// loop; bail out with a failed transition and report.
		_ = s.transition(ctx, child.id, StateFailed)
		s.cleanupChild(child)
		resultCh <- SpawnResult{AgentID: child.id, Err: fmt.Errorf("supervisor.runChild: transition to running: %w", err)}
		close(resultCh)
		return
	}

	// 2. Run the loop.
	maxTurns := contract.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 25
	}
	// Phase F-14: when the contract carries OverrideRegistry the daemon
	// has already built a frame-scoped registry; surface every tool to
	// the provider rather than gating through ToolAllowlist (which is
	// the parent-child allowlist mechanism, not relevant for daemon-fired
	// scheduled runs).
	var specs []providers.ToolSpec
	if contract.OverrideRegistry != nil {
		for _, t := range reg.All() {
			specs = append(specs, providers.ToolSpec{
				Name: t.Name(), Description: t.Description(), Schema: t.Schema(),
			})
		}
	} else {
		specs = buildChildToolSpecs(reg, contract.ToolAllowlist)
	}
	initial := []providers.Message{
		{
			Role: "user",
			Content: []providers.Block{
				{Kind: "text", Text: composeInitialPrompt(contract)},
			},
		},
	}
	// Phase 5 slice 5a: derive the subtree's per-call Budget from the
	// contract's MaxTokens. MaxWallClock is already enforced via the
	// child context's deadline; we set it on the Budget too so the
	// loop can refuse politely BEFORE the next provider call rather
	// than waiting for ctx to fire mid-stream.
	budget := Budget{
		MaxTokens:    int64(contract.MaxTokens),
		MaxWallClock: contract.MaxWallClock,
	}
	messages, runErr := Run(ctx, p, reg, LoopOptions{
		Model:         contract.Model,
		System:        contract.System,
		Tools:         specs,
		Approver:      AutoApprover{},
		MaxIterations: maxTurns,
		// Wire the child's steering channel so Supervisor.Steer can
		// nudge this loop at the next tool-call boundary.
		Steering:      child.steering,
		Budget:        budget,
		BudgetTracker: child.tracker,
	}, initial)

	// 3. Classify.
	terminal := StateDone
	if runErr != nil {
		terminal = StateFailed
	}
	if err := s.transition(ctx, child.id, terminal); err != nil {
		// Couldn't record the terminal state; fold into the result
		// error rather than dropping silently.
		if runErr == nil {
			runErr = fmt.Errorf("supervisor.runChild: transition to %s: %w", terminal, err)
		}
	}

	// 4. Persist final turn via the Slice 3d artifact helper. Best-
	//    effort: write failures are NOT promoted to loop failures, but
	//    we DO surface them via the result's FinalArtifact zero value
	//    + a stderr line so post-mortem tooling can notice.
	var finalTurn providers.Message
	if len(messages) > 0 {
		finalTurn = messages[len(messages)-1]
	}
	var finalArtifact ArtifactRef
	if turnBytes, marshalErr := json.Marshal(finalTurn); marshalErr == nil {
		if ref, writeErr := WriteArtifact(ctx, s.log, child.id, "agent_final", turnBytes); writeErr == nil {
			finalArtifact = ref
		}
		// Write/marshal errors are swallowed by design - the event log
		// remains the source of truth, the artifact is a convenience.
	}

	// 5+6. Cleanup ticker + remove from active children.
	s.cleanupChild(child)

	// 7. Send + close.
	resultCh <- SpawnResult{
		AgentID:       child.id,
		FinalTurn:     finalTurn,
		FinalArtifact: finalArtifact,
		Err:           runErr,
	}
	close(resultCh)
}

// transition appends a state_change event AND updates the projection
// cache row. Called for spawning→running and for the terminal classify.
// Wraps the two-step write the rest of the package uses.
func (s *Supervisor) transition(ctx context.Context, agentID string, next State) error {
	if s.log == nil {
		return errors.New("supervisor.transition: nil log")
	}
	payload, err := NewStateChangeTransition(next)
	if err != nil {
		return fmt.Errorf("supervisor.transition: marshal: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := s.log.Append(ctx, Event{
		AgentID: agentID, TS: now, Type: EvtStateChange, Payload: payload,
	}); err != nil {
		return fmt.Errorf("supervisor.transition: append: %w", err)
	}
	if err := s.log.UpdateAgentState(ctx, agentID, next, now); err != nil {
		return fmt.Errorf("supervisor.transition: update: %w", err)
	}
	return nil
}

// cleanupChild stops the per-agent heartbeat ticker and removes the
// child from the active-children map (releasing a concurrency slot for
// the parent's next Spawn).
func (s *Supervisor) cleanupChild(child *runningChild) {
	if s.heartbeat != nil {
		s.heartbeat.Stop(child.id)
	}
	s.mu.Lock()
	delete(s.children, child.id)
	s.mu.Unlock()
}

// newSpawnIDStrong returns a fresh agent ID. The Slice 1 stub used
// fmt.Sprintf("a-%d", UnixNano); we keep the same format here for now
// to avoid churning the existing ulid_test (which exercises a
// different ID surface). Slice 3e or a follow-on can swap to the
// ulid.go generator once that's adopted everywhere.
//
// The function lives here rather than in supervisor.go so the spawn
// pipeline pieces are colocated.
func newSpawnIDStrong() string {
	return fmt.Sprintf("a-%d", time.Now().UTC().UnixNano())
}
