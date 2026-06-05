// Phase 6 slice 6f — replay-eval for induced skill candidates.
//
// # Why this exists
//
// SkillsBench (arXiv 2602.12670) measured self-generated skills at
// −1.3pp average with 16/84 tasks DEGRADED. The cross-provider judge
// (slice 6e) catches "this isn't a skill at all". Replay catches the
// nastier failure: a skill that LOOKS plausible to the judge but
// actually makes the agent worse on the kind of task it was induced
// from. Without this gate, those 16/84 ship and silently corrupt every
// future conversation that retrieves them.
//
// # Architectural commitments (load-bearing — slice-6f brief)
//
//   - Replay is OPTIONAL per slice. Skills induced from tasks without
//     a deterministic verifier skip the replay step and go straight to
//     human review. We don't fabricate a score we can't measure.
//   - The skill is the variable, not the model. With-skill vs
//     without-skill runs use the SAME provider, SAME model, SAME
//     initial messages — only the System prompt changes.
//   - One verifier per replay (see replay_picker.go). Picking multiple
//     dilutes the signal.
//   - Replay corpus is bounded (default 5 transcripts). The cost
//     scales linearly; >5 was post-v1 in the brief.
//   - PerReplayBudget caps a runaway replay. Each replay = one full
//     agent.Run loop = real provider tokens; the budget gate (slice 5a)
//     refuses cleanly rather than yanking the chair.
//
// # File location
//
// The slice-6f brief lists this file as internal/skills/replay.go but
// the implementation has to import internal/agent (Dispatcher, Budget,
// Run) and agent already imports internal/skills — that closes a
// cycle. skillwire/ exists precisely for this reason (see the
// package-level doc on wire.go). The architectural intent — replay is
// skill-induction's verifier gate, separate from spawn/loop concerns —
// is unchanged.
package skillwire

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/skills"
	"github.com/georgebuilds/carlos/internal/tools"
)

// VerifierDispatcher is the seam the ReplayEvaluator uses to score
// the replay outputs. *agent.Dispatcher satisfies it. The interface
// exists so tests can swap in a fake without spinning up real
// compilers / test runners / network fetches — see
// replay_test.go::fakeDispatcher.
//
// The signature mirrors agent.Dispatcher.Verify exactly: same arg
// order, same return shape. If agent.Dispatcher's signature ever
// drifts, the compiler will catch the mismatch when *agent.Dispatcher
// no longer satisfies this interface.
type VerifierDispatcher interface {
	Verify(ctx context.Context, workdir, kind string, content []byte) ([]agent.VerificationReport, error)
}

// Compile-time check that the real Dispatcher satisfies our interface.
// Failing here means agent.Dispatcher.Verify has drifted and the
// integration is broken — surface that at build time, not runtime.
var _ VerifierDispatcher = (*agent.Dispatcher)(nil)

// ReplayEvaluator scores a candidate skill by replaying the historical
// conversations it was induced from, with and without the skill, and
// comparing verifier outputs.
//
// Construct one Evaluator per induction pipeline (typically held on
// the wire-layer's option struct). The struct is safe to share across
// goroutines — the only mutable state is fields the caller sets at
// construction time.
type ReplayEvaluator struct {
	// Provider is the LLM that runs both arms of the replay. Held
	// constant across with-skill and without-skill so the skill is the
	// only variable (per the slice-6f architectural commitment).
	Provider providers.Provider

	// Model is the model id passed to Provider. Empty = let the
	// provider pick its default. Kept on the Evaluator (not per-call)
	// because varying it between replays would also change the
	// variable.
	Model string

	// Dispatcher routes the verifier kind picked by PickVerifierKind
	// to the actual ToolGroundedVerifier (Compiler / TestRunner /
	// URLRefetcher). The interface (VerifierDispatcher) keeps tests
	// off the real verifiers.
	Dispatcher VerifierDispatcher

	// Workdir is the directory the verifiers run in. Typically a
	// Worktree.Root from slice 3f so the replay's compile/test
	// happens against a known checkout. The Evaluator does NOT
	// chdir or sandbox itself — that's the caller's job. We just
	// pass workdir through to Dispatcher.Verify.
	Workdir string

	// MaxReplays caps the number of historical transcripts we replay
	// against. Zero defaults to DefaultMaxReplays (5). Exceeding the
	// supplied transcript slice is fine — we replay min(len, cap).
	MaxReplays int

	// PerReplayBudget caps each replay's tokens / cost / wall-clock.
	// Zero defaults to a sane cap (see defaultPerReplayBudget). The
	// budget is applied per replay, not summed across replays — a
	// 5-replay run with a 30s budget per arm caps at ~300s total
	// (5 × 2 arms × 30s). Callers that need a global cap layer their
	// own context timeout on top.
	PerReplayBudget agent.Budget

	// Tools is the registry the replay's agent.Run uses. Held on the
	// evaluator (not per-call) because the registry shape must match
	// what the skill was induced from — passing a different toolset
	// between with-skill and without-skill would change the variable.
	// Zero = empty registry (text-only replays, useful for tests).
	Tools *tools.Registry

	// MaxLoopIterations bounds the agent.Run loop per replay arm.
	// Zero defers to agent.Run's own default (25). We expose it on
	// the evaluator so a caller wanting cheaper replays can clamp it.
	MaxLoopIterations int
}

// DefaultMaxReplays is the cap applied when ReplayEvaluator.MaxReplays
// is zero. 5 matches the slice-6f brief. Bump in carlos config (not
// here) if your acceptance-rate telemetry says you need more signal.
const DefaultMaxReplays = 5

// defaultPerReplayBudget is the per-arm cap applied when the caller's
// PerReplayBudget is unlimited. Chosen to be loose enough that a
// healthy replay doesn't trip and tight enough that a runaway aborts
// before burning $1. Tune via PerReplayBudget on the Evaluator.
//
// 20k tokens × 2 arms × 5 replays = 200k tokens worst-case per
// proposal. At mid-tier prices (~$3/M in, $15/M out) the worst case
// is ~$1.80. Brief's per-proposal budget target is $0.10 — so this
// cap is the runaway alarm, not the normal operating point.
func defaultPerReplayBudget() agent.Budget {
	return agent.Budget{
		MaxTokens: 20_000,
	}
}

// ReplayPair captures one historical transcript's with-skill vs
// without-skill outcome. The Delta is the comparison result; the
// caller can re-derive Score from a slice of pairs but we keep it
// pre-computed on ReplayReport so the gate doesn't recompute.
type ReplayPair struct {
	// TranscriptID is the agent ID (or other handle) the caller
	// supplied — passed through verbatim so post-mortems can trace
	// back to the eventlog.
	TranscriptID string

	// Verifier is the agent.ToolGroundedVerifier.Name() that scored
	// this pair. Recorded for audit even when both arms tied.
	Verifier string

	// WithSkill / WithoutSkill are the verifier reports for each arm.
	// VerificationReport.Score (1-10) is the comparison axis.
	WithSkill    agent.VerificationReport
	WithoutSkill agent.VerificationReport

	// Delta is +1 when with-skill scored strictly higher, -1 when
	// without-skill scored strictly higher, 0 on tie or when either
	// arm failed.
	Delta int

	// Errors collected during this pair's execution. Non-nil errors
	// do NOT abort the whole evaluation — they downgrade this pair
	// to a tie (Delta=0) and surface in ReplayReport.Concerns.
	WithSkillErr    string `json:",omitempty"`
	WithoutSkillErr string `json:",omitempty"`
}

// ReplayReport is what Evaluate returns. The gate (in PipelineHook,
// below) consumes Decision; UIs can render the full Replays + Concerns
// list to explain WHY a proposal was auto-rejected or auto-accepted.
type ReplayReport struct {
	// Replays is the per-transcript record. Empty when Skipped=true.
	Replays []ReplayPair

	// Score is the "win rate" — fraction of replays where with-skill
	// strictly outperformed without-skill. Ties don't count for
	// either side; the denominator is len(Replays) but the numerator
	// counts only Delta=+1 pairs. A 0-pair report (no replays
	// completed) has Score=0 and Decision="inconclusive".
	Score float64

	// Decision is the gate verdict.
	//
	//   Score >= 0.6  → "accept"        (with-skill consistently better)
	//   Score <= 0.3  → "reject"        (with-skill consistently worse)
	//   otherwise     → "inconclusive"  (caller falls back to human review)
	//
	// The 0.6 / 0.3 thresholds are intentionally asymmetric. We want
	// strong evidence to AUTO-ACCEPT (cheap downside of "needs more
	// human review") but a clear majority loss to AUTO-REJECT (a bad
	// skill silently degrades every future conversation that
	// retrieves it — the brief's headline risk).
	Decision string

	// Concerns is a free-form list of issues — verifier errors,
	// budget trips, picker fallbacks. Surfaced into the approval
	// queue title when non-empty (see PipelineHook).
	Concerns []string

	// Skipped is true when no verifier fits this skill class — the
	// picker returned an empty kind. Skipped=true means the gate
	// behaves identically to the pre-slice-6f pipeline: proposal
	// queues for human review, no verifier signal attached.
	Skipped bool

	// SkippedReason is the picker's human-readable explanation. Set
	// even when Skipped=false so audit logs can trace why a
	// particular verifier was chosen.
	SkippedReason string

	// VerifierKind records the kind PickVerifierKind returned. Empty
	// iff Skipped=true. Useful for telemetry — once acceptance-rate
	// per-kind is plotted we can tune the picker.
	VerifierKind string
}

// Decision constants. Strings (not enums) so they serialize cleanly
// into the approval-queue artifact metadata and into telemetry rows.
const (
	ReplayDecisionAccept       = "accept"
	ReplayDecisionReject       = "reject"
	ReplayDecisionInconclusive = "inconclusive"
)

// Evaluate runs the replay loop and returns the report. Cancellation
// via ctx aborts the in-flight replay arm; partial results from
// already-completed pairs are still included in the returned report.
//
// transcripts is the (last N) historical conversations the skill was
// induced from. The caller typically fetches them via
// agent.EventLog.Read keyed by proposal.InducedFrom (agent IDs) and
// reduces each to a []providers.Message (the assistant/user turns
// from agent.Run). Slice 6f does NOT prescribe the reduction — that's
// induction-side concern (slice 6e).
//
// Returns a non-nil error only for INFRA failures — nil-receiver, nil
// proposal, nil provider, nil dispatcher. A successful run with all
// replays failing returns a report (Score=0, Decision="reject" or
// "inconclusive" depending on counts) and nil error; the gate is then
// free to act on the verdict.
func (r *ReplayEvaluator) Evaluate(ctx context.Context, proposal *skills.Proposal, transcripts [][]providers.Message) (*ReplayReport, error) {
	if r == nil {
		return nil, errors.New("replay: nil evaluator")
	}
	if proposal == nil {
		return nil, errors.New("replay: nil proposal")
	}
	if r.Provider == nil {
		return nil, errors.New("replay: nil provider")
	}
	if r.Dispatcher == nil {
		return nil, errors.New("replay: nil dispatcher")
	}

	// Pick verifier kind. Skipped path returns early — same shape as
	// the pre-slice-6f pipeline. The report carries the picker reason
	// so the caller can surface "no verifier fit" in audit logs.
	pick := PickVerifierKind(proposal, transcripts)
	if pick.Kind == "" {
		return &ReplayReport{
			Skipped:       true,
			SkippedReason: pick.Reason,
			Decision:      ReplayDecisionInconclusive,
		}, nil
	}

	max := r.MaxReplays
	if max <= 0 {
		max = DefaultMaxReplays
	}
	if len(transcripts) < max {
		max = len(transcripts)
	}
	if max == 0 {
		// No transcripts to replay against — there's a verifier kind
		// in principle but nothing to score. Surface as inconclusive
		// (not Skipped — the difference matters for telemetry: this
		// is "we COULD have replayed if you'd given us data" vs the
		// picker's "we WOULDN'T have replayed even with data").
		return &ReplayReport{
			VerifierKind: pick.Kind,
			Concerns:     []string{"no transcripts supplied for replay"},
			Decision:     ReplayDecisionInconclusive,
		}, nil
	}

	budget := r.PerReplayBudget
	if budget.IsUnlimited() {
		budget = defaultPerReplayBudget()
	}

	report := &ReplayReport{
		VerifierKind:  pick.Kind,
		SkippedReason: pick.Reason,
	}

	wins := 0
	losses := 0
	completed := 0
	for i := 0; i < max; i++ {
		// Short-circuit on context cancellation. Already-completed
		// pairs stay in the report; the partial Score is computed at
		// the end.
		if ctx.Err() != nil {
			report.Concerns = append(report.Concerns, fmt.Sprintf("context cancelled after %d replays: %v", completed, ctx.Err()))
			break
		}

		pair := r.runOnePair(ctx, proposal, transcripts[i], pick.Kind, budget, transcriptID(transcripts[i], i))
		report.Replays = append(report.Replays, pair)
		completed++
		switch pair.Delta {
		case +1:
			wins++
		case -1:
			losses++
		}
		if pair.WithSkillErr != "" || pair.WithoutSkillErr != "" {
			report.Concerns = append(report.Concerns, fmt.Sprintf("replay %d had verifier-arm errors", i))
		}
	}

	report.Score = computeScore(wins, completed)
	report.Decision = decideFromScore(report.Score, completed)
	return report, nil
}

// runOnePair executes both arms of one replay and scores them. Returns
// a ReplayPair with the Delta filled in. Errors are recorded on the
// pair (not bubbled up) so the caller's loop can keep going.
//
// Execution shape:
//
//	with-skill: System = base + skill.Body
//	without-skill: System = base
//
// "base" is empty by default — the replay isn't trying to reproduce
// the original conversation's system prompt (that would be fragile and
// is induction-side concern), it's measuring whether the skill body
// helps on the same initial messages.
func (r *ReplayEvaluator) runOnePair(ctx context.Context, proposal *skills.Proposal, transcript []providers.Message, kind string, budget agent.Budget, transcriptID string) ReplayPair {
	pair := ReplayPair{
		TranscriptID: transcriptID,
	}

	// The "initial" message set for each replay arm is the prompt the
	// transcript started with. We take the FIRST user message in the
	// transcript and use it as the seed. This is deliberately
	// minimal: replaying the full transcript would just have the
	// model regenerate the same tool-call sequence, which doesn't
	// test the skill — it tests determinism. Replaying just the
	// seed exercises the skill's planning value.
	seed := initialUserMessage(transcript)
	if len(seed) == 0 {
		pair.WithSkillErr = "no initial user message in transcript"
		pair.WithoutSkillErr = "no initial user message in transcript"
		return pair
	}

	// With-skill arm: prepend the skill body to a system prompt that
	// says "this skill is loaded".
	withSkillSystem := buildReplaySystem(proposal)
	withSkillOut, err := r.runArm(ctx, withSkillSystem, seed, budget)
	if err != nil {
		pair.WithSkillErr = err.Error()
	}

	// Without-skill arm: bare system prompt, same seed.
	withoutSkillOut, err := r.runArm(ctx, "", seed, budget)
	if err != nil {
		pair.WithoutSkillErr = err.Error()
	}

	// Verify each arm. Even if one arm errored we still try to verify
	// the other — the verifier's job is to score the OUTPUT, and an
	// empty output is a legitimate (low) score, not an infra failure.
	withSkillReports, vErr := r.Dispatcher.Verify(ctx, r.Workdir, kind, withSkillOut)
	if vErr != nil && pair.WithSkillErr == "" {
		pair.WithSkillErr = vErr.Error()
	}
	withoutSkillReports, vErr := r.Dispatcher.Verify(ctx, r.Workdir, kind, withoutSkillOut)
	if vErr != nil && pair.WithoutSkillErr == "" {
		pair.WithoutSkillErr = vErr.Error()
	}

	pair.WithSkill = bestReport(withSkillReports)
	pair.WithoutSkill = bestReport(withoutSkillReports)
	pair.Verifier = pair.WithSkill.JudgeModel
	if pair.Verifier == "" {
		pair.Verifier = pair.WithoutSkill.JudgeModel
	}
	pair.Delta = compareScores(pair.WithSkill, pair.WithoutSkill, pair.WithSkillErr, pair.WithoutSkillErr)
	return pair
}

// runArm executes one agent.Run call and returns the final assistant
// text concatenated into a single byte slice — that's the content the
// verifier scores. Errors from agent.Run (including budget exceeded,
// which is the deliberate runaway-stop signal) are returned to the
// caller; the caller records them on the pair.
func (r *ReplayEvaluator) runArm(ctx context.Context, system string, seed []providers.Message, budget agent.Budget) ([]byte, error) {
	tracker := agent.NewTracker(nil)
	opts := agent.LoopOptions{
		Model:         r.Model,
		System:        system,
		MaxIterations: r.MaxLoopIterations,
		Budget:        budget,
		BudgetTracker: tracker,
	}
	msgs, err := agent.Run(ctx, r.Provider, r.Tools, opts, seed)

	// Even on error (e.g. budget exceeded) the loop returns the
	// partial message history. Extract the final assistant text so
	// the verifier can score what we got.
	out := finalAssistantText(msgs)
	if err != nil {
		// Wrap budget-exceeded specifically so the caller can
		// errors.Is(err, agent.ErrBudgetExceeded) for telemetry. We
		// still return the partial output for verification.
		return out, fmt.Errorf("replay: run: %w", err)
	}
	return out, nil
}

// initialUserMessage returns the seed message set for a replay arm:
// the FIRST user message in the transcript, as a single-message
// slice. Returns nil if the transcript has no user message.
func initialUserMessage(transcript []providers.Message) []providers.Message {
	for _, m := range transcript {
		if m.Role == "user" {
			return []providers.Message{m}
		}
	}
	return nil
}

// finalAssistantText concatenates every text block in the LAST
// assistant message. Tool-use blocks are skipped — the verifier
// scores the model's final answer, not its tool calls. Returns
// nil for an empty or all-tool-use final turn.
func finalAssistantText(msgs []providers.Message) []byte {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		var b strings.Builder
		for _, blk := range msgs[i].Content {
			if blk.Kind == "text" {
				b.WriteString(blk.Text)
			}
		}
		if b.Len() == 0 {
			return nil
		}
		return []byte(b.String())
	}
	return nil
}

// buildReplaySystem renders the with-skill system prompt: a one-line
// frame + the skill body verbatim. Matches how progressive disclosure
// loads a skill body in normal operation (Anthropic skill spec).
func buildReplaySystem(p *skills.Proposal) string {
	var b strings.Builder
	b.WriteString("# Loaded skill: ")
	b.WriteString(p.Name)
	b.WriteString("\n\n")
	b.WriteString(p.Description)
	b.WriteString("\n\n")
	b.WriteString(p.Body)
	return b.String()
}

// bestReport picks the highest-scoring report from a slice. Dispatcher
// may return zero, one, or many reports per Verify call (e.g. the
// "diff" kind fires both Compiler AND TestRunner). The replay's
// per-arm score is the BEST of those signals — a project that
// compiles AND tests cleanly is two confirmations; we don't want to
// dilute that by averaging with a no-signal report.
//
// Tie-break: first highest wins (preserves registration order, which
// is documented as the dispatch order on agent.Dispatcher.Register).
func bestReport(reports []agent.VerificationReport) agent.VerificationReport {
	if len(reports) == 0 {
		return agent.VerificationReport{Score: 0, Decision: agent.VerificationReject}
	}
	best := reports[0]
	for _, r := range reports[1:] {
		if r.Score > best.Score {
			best = r
		}
	}
	return best
}

// compareScores returns +1 / -1 / 0 per the ReplayPair.Delta contract.
// Errors on EITHER arm force a tie (Delta=0): a verifier-broken pair
// shouldn't tip the score in either direction.
func compareScores(withSkill, withoutSkill agent.VerificationReport, withSkillErr, withoutSkillErr string) int {
	if withSkillErr != "" || withoutSkillErr != "" {
		return 0
	}
	switch {
	case withSkill.Score > withoutSkill.Score:
		return +1
	case withSkill.Score < withoutSkill.Score:
		return -1
	default:
		return 0
	}
}

// computeScore is the win-rate. Ties count toward the denominator but
// not the numerator. A zero-replay report returns 0 (the caller maps
// that to "inconclusive" via decideFromScore).
func computeScore(wins, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(wins) / float64(total)
}

// decideFromScore maps the win-rate to the Decision string. Thresholds
// per the slice-6f brief: >=0.6 accept, <=0.3 reject, else
// inconclusive. A zero-replay report is always inconclusive regardless
// of the (zero) score.
func decideFromScore(score float64, completed int) string {
	if completed == 0 {
		return ReplayDecisionInconclusive
	}
	switch {
	case score >= 0.6:
		return ReplayDecisionAccept
	case score <= 0.3:
		return ReplayDecisionReject
	default:
		return ReplayDecisionInconclusive
	}
}

// transcriptID returns a stable identifier for a transcript. We
// don't have a first-class transcript ID — the caller passes a slice
// of message-lists — so we fall back to a positional ID. If the
// caller wants real IDs they can wrap Evaluate themselves.
func transcriptID(_ []providers.Message, idx int) string {
	return fmt.Sprintf("transcript-%d", idx)
}
