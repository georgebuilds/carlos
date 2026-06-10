// Phase 11 slice 11d - research as sub-agent.
//
// SpawnResearch wraps the synchronous Engine.Run with sub-agent
// lifecycle plumbing: it mints an agent ID, writes the state-change +
// projection row that lets the manage roster see the in-flight
// research session, runs the engine in its own goroutine, emits one
// EvtResearchPhase event per phase boundary, persists the final
// rendered report as a research_report artifact, and writes the
// terminal state transition.
//
// The chat-side wiring (still slice 11f's synchronous goroutine
// today) will move onto this helper in slice 11e, at which point
// /research becomes truly async: the chat keeps responding while the
// research session lives as an agent in the supervisor roster.
//
// # Why not Supervisor.Spawn directly
//
// Supervisor.Spawn is hard-wired to call agent.Run (the tool-use
// loop). The research engine has its own deterministic six-phase
// machine; routing it through agent.Run would either (a) spend an
// extra provider call on an empty assistant turn or (b) require a
// "no-op tool loop" mode neither the supervisor nor agent.Run knows
// about. We instead replicate the supervisor's bookkeeping inline:
// state_change kind=created, projection-cache InsertAgent,
// running→done transition. The state machine and event shapes match
// what the supervisor writes, so the chat / manage projections render
// research sessions exactly like any other agent.
package research

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// ResearchLog is the subset of *agent.SQLiteEventLog SpawnResearch
// needs. Kept narrow so tests can stub without standing up a real
// SQLite database (or a real supervisor): an in-memory recorder is
// sufficient to assert the event sequence + artifact write.
//
// InsertAgent installs the projection-cache row the manage roster
// reads from; it's required to make the in-flight research session
// visible to the TUI (slice 11e wiring).
//
// InsertArtifact satisfies the artifactWriter contract WriteArtifact
// uses internally - passing the log straight through means we don't
// need to type-assert *agent.SQLiteEventLog at the call site.
type ResearchLog interface {
	Append(ctx context.Context, ev agent.Event) (int64, error)
	InsertAgent(ctx context.Context, r agent.AgentRow) error
	InsertArtifact(ctx context.Context, a agent.Artifact) error
}

// ResearchResult carries the outcome of a SpawnResearch run. Exactly
// one ResearchResult is sent on the done channel (the channel is then
// closed). On success, Report is non-nil and Artifact carries the
// persisted research_report ref; on failure, Err is set and one or
// both of {Report, Artifact} may still be populated with partial
// state - callers can inspect what was gathered before the abort.
type ResearchResult struct {
	AgentID  string
	Report   *Report
	Artifact agent.ArtifactRef
	Err      error
}

// SpawnResearch starts a research session as a sub-agent. Returns
// immediately with the new agent's ID + a buffered (cap 1) channel
// that fires once on completion.
//
// Sub-agent lifecycle (all events flow through log.Append):
//
//	spawning → running → done (artifact written) | failed (err captured)
//
//	EvtStateChange (kind=created)  on spawn
//	EvtStateChange (kind=transition, to=running)  before engine.Run
//	EvtResearchPhase  (one start + one done per phase boundary)
//	EvtArtifactRef  when the final report artifact lands
//	EvtStateChange (kind=transition, to=done|failed)  on completion
//
// Cancellation: cancel parentCtx; the engine's ctx-propagation aborts
// in-flight phases at their next checkpoint. The done channel still
// fires (with Err = context.Canceled and the terminal state recorded
// as failed).
//
// If engine is nil the function returns (("", nil, error)) without
// writing any events - the caller is expected to surface "research
// engine not wired" through its own UI path.
func SpawnResearch(parentCtx context.Context, log ResearchLog, engine *Engine, question string) (string, <-chan ResearchResult, error) {
	if log == nil {
		return "", nil, errors.New("research.SpawnResearch: nil log")
	}
	if engine == nil {
		return "", nil, errors.New("research.SpawnResearch: nil engine")
	}
	if strings.TrimSpace(question) == "" {
		return "", nil, errors.New("research.SpawnResearch: empty question")
	}

	agentID := newResearchAgentID(time.Now().UTC())
	now := time.Now().UTC().Truncate(time.Millisecond)

	// 1. state_change kind=created - installs the agent in the
	//    projection cache via the standard event shape (no
	//    research-specific schema drift).
	created, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID:     agentID,
		RootID: agentID,
		Title:  "research: " + question,
	})
	if err != nil {
		return "", nil, fmt.Errorf("research.SpawnResearch: marshal created: %w", err)
	}
	if _, err := log.Append(parentCtx, agent.Event{
		AgentID: agentID, TS: now, Type: agent.EvtStateChange, Payload: created,
	}); err != nil {
		return "", nil, fmt.Errorf("research.SpawnResearch: append created: %w", err)
	}
	if err := log.InsertAgent(parentCtx, agent.AgentRow{
		ID:              agentID,
		RootID:          agentID,
		State:           agent.StateSpawning,
		Attempt:         1,
		Title:           "research: " + question,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}); err != nil {
		return "", nil, fmt.Errorf("research.SpawnResearch: insert agent: %w", err)
	}

	done := make(chan ResearchResult, 1)
	go runResearchSession(parentCtx, log, engine, agentID, question, done)
	return agentID, done, nil
}

// runResearchSession is the per-spawn goroutine body. It:
//
//  1. Transitions spawning→running.
//  2. Wires the engine's phase callbacks to emit EvtResearchPhase events.
//  3. Calls engine.Run.
//  4. On a non-empty synthesis, persists the rendered Markdown as a
//     research_report artifact and appends an EvtArtifactRef event so
//     the projection cache + manage roster can render the deliverable.
//  5. Transitions running→done (success) or running→failed (any err).
//  6. Sends ResearchResult and closes the channel.
//
// Errors from any sub-step (state transitions, artifact write) are
// folded into the result's Err alongside the engine's runtime error;
// we never panic in the worker.
func runResearchSession(ctx context.Context, log ResearchLog, engine *Engine, agentID, question string, done chan<- ResearchResult) {
	defer close(done)

	// 1. spawning → running.
	if err := emitTransition(ctx, log, agentID, agent.StateRunning); err != nil {
		// Couldn't even mark the agent running; fold into failed and
		// bail out. We attempt a failed transition next so the row
		// doesn't get stuck in spawning forever.
		_ = emitTransition(ctx, log, agentID, agent.StateFailed)
		done <- ResearchResult{AgentID: agentID, Err: fmt.Errorf("research: transition to running: %w", err)}
		return
	}

	// 2. Wire phase callbacks on a per-call SHALLOW COPY of the engine.
	//    The Engine struct only holds config + interface-typed
	//    collaborators (Provider/Search/Fetcher/Judge) that are
	//    designed for concurrent use; it carries no per-Engine
	//    mutable state (no pool, no cache, no mutex). Working on a
	//    local copy means:
	//
	//      - Concurrent SpawnResearch calls don't race on the shared
	//        OnPhaseStart/OnPhaseDone fields (the original Bug 11d-r).
	//      - The defaults-assignment block at the top of Engine.Run
	//        (which writes to MaxSubQueries / SourcesPerQuery /
	//        Budget) writes only to the local copy, also no longer a
	//        cross-spawn race.
	//      - The caller's engine value (and any callbacks they had
	//        installed before SpawnResearch) is left untouched, so
	//        there's no stale-restore order dependency.
	//
	//    Pre-existing callbacks set by the caller are composed by
	//    invoking them through from the local engine's wrapper
	//    closure. The closures capture log+agentID by value; they
	//    MUST not mutate engine state (the contract promised in
	//    engine.go's docstring).
	localEng := *engine
	prevStart := engine.OnPhaseStart
	prevDone := engine.OnPhaseDone
	localEng.OnPhaseStart = func(phase string) {
		if prevStart != nil {
			prevStart(phase)
		}
		emitResearchPhase(ctx, log, agentID, agent.ResearchPhasePayload{Phase: phase})
	}
	localEng.OnPhaseDone = func(phase string, elapsed time.Duration, err error) {
		if prevDone != nil {
			prevDone(phase, elapsed, err)
		}
		pl := agent.ResearchPhasePayload{Phase: phase, Elapsed: elapsed, Done: true}
		if err != nil {
			pl.Err = err.Error()
		}
		emitResearchPhase(ctx, log, agentID, pl)
	}

	// 3. Run the engine. The Engine.Run guarantees a non-nil Report
	//    even on failure, so we always have something to inspect.
	report, runErr := localEng.Run(ctx, question)

	// 4. Persist the rendered report (best-effort: write failure does
	//    NOT degrade the engine's success/failure classification).
	var artifactRef agent.ArtifactRef
	if report != nil && report.Synthesis != "" {
		md := RenderMarkdown(report)
		ref, werr := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindResearch, []byte(md))
		if werr == nil {
			artifactRef = ref
			// Append an EvtArtifactRef so the projection cache /
			// chat transcript can surface the deliverable. The
			// payload mirrors the agent package's existing
			// artifact-ref shape (id, kind, sha256, size) so any
			// future renderer doesn't need a research-specific
			// schema.
			emitArtifactRef(ctx, log, agentID, ref)
		} else if runErr == nil {
			// Surface the persistence failure on the result so the
			// chat-side can render a "report ran but couldn't save"
			// concern; this never masks a real engine error.
			runErr = fmt.Errorf("research: artifact write: %w", werr)
		}
	}

	// 5. Final state transition.
	terminal := agent.StateDone
	if runErr != nil {
		terminal = agent.StateFailed
	}
	if err := emitTransition(ctx, log, agentID, terminal); err != nil && runErr == nil {
		runErr = fmt.Errorf("research: transition to %s: %w", terminal, err)
	}

	// 6. Deliver the result.
	done <- ResearchResult{
		AgentID:  agentID,
		Report:   report,
		Artifact: artifactRef,
		Err:      runErr,
	}
}

// emitTransition writes a state_change kind=transition event. Unlike
// the supervisor's transition() helper, we don't call UpdateAgentState
// - the projection cache will see the event via Apply once the chat /
// manage subscribers consume it, which is the path the manage roster
// already uses for live state. Bypassing UpdateAgentState keeps the
// research package independent of the SQL projection-row schema and
// lets tests stub a minimal ResearchLog.
//
// The trade-off is that a fresh Replay() (e.g. after carlos restart)
// will reconstruct the row from the event stream, not from the
// projection table - which is the documented v0 contract anyway
// (events are the source of truth).
func emitTransition(ctx context.Context, log ResearchLog, agentID string, next agent.State) error {
	payload, err := agent.NewStateChangeTransition(next)
	if err != nil {
		return fmt.Errorf("marshal transition: %w", err)
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    agent.EvtStateChange,
		Payload: payload,
	}); err != nil {
		return fmt.Errorf("append transition: %w", err)
	}
	return nil
}

// emitResearchPhase appends one EvtResearchPhase event. Best-effort:
// log-append failures are swallowed so a transient SQLite hiccup
// can't tank a long research run. The engine state and the result
// channel remain the authoritative outcome path; events are the
// audit/visibility surface.
func emitResearchPhase(ctx context.Context, log ResearchLog, agentID string, pl agent.ResearchPhasePayload) {
	payload, err := json.Marshal(pl)
	if err != nil {
		return
	}
	_, _ = log.Append(ctx, agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    agent.EvtResearchPhase,
		Payload: payload,
	})
}

// emitArtifactRef appends one EvtArtifactRef event pointing at the
// research_report blob. Best-effort; the artifact itself is already
// on disk + recorded in the artifacts table by WriteArtifact, so a
// missed event doesn't lose the deliverable, only the rendering
// breadcrumb.
//
// Payload shape mirrors the field set parents already consume from
// SpawnResult.FinalArtifact (id, kind, sha256, size, path) - no new
// schema, just a JSON encoding of the existing ArtifactRef.
func emitArtifactRef(ctx context.Context, log ResearchLog, agentID string, ref agent.ArtifactRef) {
	payload, err := json.Marshal(struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
		Path   string `json:"path,omitempty"`
	}{
		ID:     ref.ID,
		Kind:   ref.Kind,
		SHA256: ref.SHA256,
		Size:   ref.Size,
		Path:   ref.Path,
	})
	if err != nil {
		return
	}
	_, _ = log.Append(ctx, agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    agent.EvtArtifactRef,
		Payload: payload,
	})
}

// researchAgentIDMu serializes monotonic-suffix increments so two
// SpawnResearch calls inside the same nanosecond still produce
// distinct IDs (defensive - time.UnixNano() is usually unique enough
// on its own, but tests that fire spawns from a tight loop have
// surprised us before).
var (
	researchAgentIDMu     sync.Mutex
	researchAgentIDLastNs int64
)

// newResearchAgentID returns a fresh agent ID for a research session.
// Format: "r-<nanosec>-<rand4>". The "r-" prefix lets a future log
// filter ("show me only research sessions") work without consulting
// the title field. Random suffix collides only on simultaneous
// crypto/rand exhaustion, which would manifest as an Append error
// elsewhere first.
func newResearchAgentID(now time.Time) string {
	ns := now.UnixNano()
	researchAgentIDMu.Lock()
	if ns <= researchAgentIDLastNs {
		ns = researchAgentIDLastNs + 1
	}
	researchAgentIDLastNs = ns
	researchAgentIDMu.Unlock()
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Should never happen in practice; fall back to a fixed
		// suffix so the ID stays well-formed.
		return fmt.Sprintf("r-%d-0000", ns)
	}
	return fmt.Sprintf("r-%d-%s", ns, hex.EncodeToString(buf[:]))
}

// RenderMarkdown turns a *Report into a human-readable Markdown
// document. Mirrors internal/tui/chat.RenderReportMarkdown so the
// artifact persisted by SpawnResearch (this package) and the chat-
// transcript message (chat package) carry the same prose - they're
// the same content, materialized in two places.
//
// We duplicate the renderer rather than depending on the chat
// package because the research package sits below chat in the
// dependency graph; chat already imports research, and inverting
// that edge would require a third package to host the shared
// renderer. The two are kept in sync by a smoke test (slice 11d's
// TestRenderMarkdown_MirrorsChatRenderer).
func RenderMarkdown(r *Report) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Research report: %s\n\n", r.Question)
	if len(r.Query.Sub) > 0 {
		b.WriteString("## Sub-queries\n\n")
		for _, s := range r.Query.Sub {
			fmt.Fprintf(&b, "- %s\n", s)
		}
		b.WriteString("\n")
	}
	if r.Synthesis != "" {
		b.WriteString("## Synthesis\n\n")
		b.WriteString(r.Synthesis)
		b.WriteString("\n\n")
	}
	if len(r.Sources) > 0 {
		b.WriteString("## Sources\n\n")
		for _, s := range r.Sources {
			title := s.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(&b, "- **%s** - %s - <%s>\n", s.ID, title, s.URL)
		}
		b.WriteString("\n")
	}
	if len(r.Passages) > 0 {
		b.WriteString("## Passages\n\n")
		for _, p := range r.Passages {
			fmt.Fprintf(&b, "- **[%s]** (relevance %d, source %s): %s\n",
				p.ID, p.Relevance, p.SourceID, p.Text)
		}
		b.WriteString("\n")
	}
	if r.Citations != nil {
		fmt.Fprintf(&b, "## Citation audit\n\n- claims: %d\n- coverage score: %.2f\n- unsupported: %d\n\n",
			r.Citations.ClaimCount, r.Citations.Score, len(r.Citations.Unsupported))
	}
	if r.Verification != nil {
		fmt.Fprintf(&b, "## Verifier\n\n- decision: %s\n- score: %d\n- judge: %s\n",
			r.Verification.Decision, r.Verification.Score, r.Verification.JudgeModel)
		if len(r.Verification.Concerns) > 0 {
			b.WriteString("- concerns:\n")
			for _, c := range r.Verification.Concerns {
				fmt.Fprintf(&b, "  - %s\n", c)
			}
		}
		b.WriteString("\n")
	}
	if len(r.Concerns) > 0 {
		b.WriteString("## Engine concerns\n\n")
		for _, c := range r.Concerns {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## Budget\n\n- provider calls: %d\n- fetched bytes: %d\n- elapsed: %s\n",
		r.Budget.ProviderCalls, r.Budget.FetchedBytes, r.Budget.Elapsed)
	return b.String()
}
