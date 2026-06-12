// Package skillwire is the integration layer between internal/skills
// and internal/agent. It lives in its own package because internal/agent
// already imports internal/skills (for the Library type held in
// agent.Config); a direct skills → agent dependency would close a cycle.
//
// Two responsibilities, both ferrying data between the two domains:
//
//   - Propose: take an inducer-produced skills.Proposal and queue it
//     for approval as an agent skill_proposal artifact (the
//     ProposeApproval entry point in approval.go).
//   - Promote: react to an approval-accepted event by materializing
//     the proposal as a real SKILL.md on disk via skills.WriteSkill.
//
// The accept → promote subscription is NOT wired yet - see the
// comment block at the top of PromoteAccepted.
package skillwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/skills"
)

// ProposalTitle returns the user-facing approval-queue title for a
// proposal. Format: "skill: <name>" - matches the example in
// approval.go ("skill: react-test-debug"). Pure function; safe to call
// during artifact serialization.
func ProposalTitle(p *skills.Proposal) string {
	if p == nil {
		return "skill: (unnamed)"
	}
	return "skill: " + p.Name
}

// ProposeOptions controls optional pre-queue steps. Today the only
// optional step is the replay evaluator (slice 6f) - future slices
// may add more (e.g. a second-stage judge, an auto-edit pass) by
// adding fields here without breaking call sites.
//
// All fields are optional; the zero value preserves pre-slice-6f
// behavior exactly. Callers that DON'T want the optional steps keep
// calling ProposeSkill (which delegates to ProposeSkillWithOptions
// with a zero ProposeOptions).
type ProposeOptions struct {
	// Replay, when non-nil, runs ReplayEvaluator.Evaluate after the
	// proposal is built but BEFORE the artifact is queued. The
	// Decision drives the gate:
	//
	//   "accept"        → queue normally; ReplayReport attached to
	//                     the artifact as a sibling JSON record.
	//   "reject"        → DO NOT queue; the proposal is auto-rejected.
	//                     A telemetry record is appended to the
	//                     eventlog so the curator can learn from the
	//                     auto-reject pattern.
	//   "inconclusive"  → queue normally; ReplayReport attached so
	//                     the user can see why it was unclear.
	//
	// When Replay is nil, behavior is identical to pre-slice-6f
	// (queue everything, attach no replay record).
	Replay *ReplayEvaluator

	// Transcripts is the historical conversation set the Replay
	// evaluator scores against. Ignored when Replay is nil. Caller
	// fetches typically via agent.EventLog.Read keyed by
	// proposal.InducedFrom. Empty slice with Replay non-nil produces
	// a Skipped report (no signal but the proposal still queues).
	Transcripts [][]providers.Message
}

// ProposeResult is the rich return shape from ProposeSkillWithOptions.
// ArtifactRef is the queued artifact (zero-valued when AutoRejected is
// true). ReplayReport is nil when no Replay evaluator was configured.
//
// Callers that don't care about the report can keep using the simpler
// ProposeSkill signature, which returns just the ArtifactRef.
type ProposeResult struct {
	Ref          agent.ArtifactRef
	ReplayReport *ReplayReport
	AutoRejected bool
	// AutoRejectReason is a short tag set when AutoRejected=true.
	// Format: "replay: <Decision> score=<N.NN>". Surfaces into the
	// auto-reject telemetry record so the curator can group.
	AutoRejectReason string
}

// ProposeSkill is the inducer's exit path. It:
//
//   - Serializes p to JSON (atomic content-addressable blob via
//     agent.WriteArtifact).
//   - Calls agent.ProposeApproval so the queue surfaces it.
//
// Returns the ArtifactRef so the caller can correlate with later
// approval-resolution events.
//
// This is a thin wrapper around ProposeSkillWithOptions(... zero ...)
// preserved so pre-slice-6f call sites keep compiling unchanged.
func ProposeSkill(ctx context.Context, log *agent.SQLiteEventLog, agentID string, p *skills.Proposal) (agent.ArtifactRef, error) {
	res, err := ProposeSkillWithOptions(ctx, log, agentID, p, ProposeOptions{})
	return res.Ref, err
}

// ProposeSkillWithOptions is the slice-6f extension of ProposeSkill.
// The execution order is:
//
//  1. (existing) validate inputs.
//  2. (NEW)      if opts.Replay != nil, run ReplayEvaluator.Evaluate.
//     If Decision="reject", append an auto-reject record
//     to the eventlog and return WITHOUT queueing.
//  3. (existing) write the artifact + queue approval.
//  4. (NEW)      if opts.Replay produced a non-Skipped report, write
//     the report as a sibling artifact and append a
//     telemetry event linking the two.
//
// Returns a ProposeResult so the caller can branch on AutoRejected
// without re-running the replay. A successful auto-reject is NOT an
// error - the gate did its job - so err is nil and AutoRejected=true.
func ProposeSkillWithOptions(ctx context.Context, log *agent.SQLiteEventLog, agentID string, p *skills.Proposal, opts ProposeOptions) (ProposeResult, error) {
	if log == nil {
		return ProposeResult{}, errors.New("propose: nil log")
	}
	if p == nil {
		return ProposeResult{}, errors.New("propose: nil proposal")
	}
	if agentID == "" {
		return ProposeResult{}, errors.New("propose: empty agentID")
	}

	var replayReport *ReplayReport
	if opts.Replay != nil {
		r, err := opts.Replay.Evaluate(ctx, p, opts.Transcripts)
		if err != nil {
			// Infra failure in the replay evaluator should NOT block
			// the proposal - we fall back to "no signal" (queue as
			// usual) and surface the error on the result. The brief's
			// architectural commitment is "skills induced from tasks
			// that don't have a deterministic verifier skip the
			// replay step and go straight to human review" - an
			// evaluator that itself failed is morally the same case.
			replayReport = &ReplayReport{
				Skipped:       true,
				SkippedReason: fmt.Sprintf("replay infra error: %v", err),
				Decision:      ReplayDecisionInconclusive,
			}
		} else {
			replayReport = r
		}

		// Auto-reject path: skip the queue entirely. The proposal is
		// dropped on the floor (no SKILL.md ever written) but we DO
		// persist a telemetry record so the curator's "rejected
		// reasons" view has data.
		if replayReport != nil && !replayReport.Skipped && replayReport.Decision == ReplayDecisionReject {
			reason := fmt.Sprintf("replay: %s score=%.2f", replayReport.Decision, replayReport.Score)
			if err := logAutoReject(ctx, log, agentID, p, replayReport, reason); err != nil {
				// Logging failure is not fatal - the auto-reject still
				// stands; we just lose one telemetry row.
				reason = reason + " (telemetry log failed: " + err.Error() + ")"
			}
			return ProposeResult{
				ReplayReport:     replayReport,
				AutoRejected:     true,
				AutoRejectReason: reason,
			}, nil
		}
	}

	blob, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return ProposeResult{ReplayReport: replayReport}, fmt.Errorf("propose: marshal: %w", err)
	}
	ref, err := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindSkillProposal, blob)
	if err != nil {
		return ProposeResult{ReplayReport: replayReport}, fmt.Errorf("propose: write artifact: %w", err)
	}
	title := ProposalTitle(p)
	if replayReport != nil && !replayReport.Skipped {
		// Decorate the title so the human reviewer sees the verifier
		// verdict at a glance. "(verifier: accept score=0.80)" or
		// "(verifier: inconclusive score=0.50)".
		title = fmt.Sprintf("%s (verifier: %s score=%.2f)", title, replayReport.Decision, replayReport.Score)
	}
	if _, err := agent.ProposeApproval(ctx, log, agentID, title, ref); err != nil {
		return ProposeResult{Ref: ref, ReplayReport: replayReport}, fmt.Errorf("propose: queue approval: %w", err)
	}
	return ProposeResult{Ref: ref, ReplayReport: replayReport}, nil
}

// logAutoReject writes a telemetry record (a small JSON artifact of
// kind "other") so the curator/metrics layer can surface the rejected
// proposal without resurrecting the SKILL.md. The record carries the
// proposal name + replay score + reason - enough for "show me what
// the replay-eval rejected last week".
//
// Failure to log is NOT fatal; the caller still treats the proposal
// as auto-rejected. This function only ever returns the error so the
// caller can decorate the user-visible reason string.
func logAutoReject(ctx context.Context, log *agent.SQLiteEventLog, agentID string, p *skills.Proposal, report *ReplayReport, reason string) error {
	record := struct {
		Kind     string           `json:"kind"`
		Proposal *skills.Proposal `json:"proposal"`
		Report   *ReplayReport    `json:"report"`
		Reason   string           `json:"reason"`
	}{
		Kind:     "skill_proposal_auto_reject",
		Proposal: p,
		Report:   report,
		Reason:   reason,
	}
	blob, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("auto-reject telemetry: marshal: %w", err)
	}
	if _, err := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindOther, blob); err != nil {
		return fmt.Errorf("auto-reject telemetry: write: %w", err)
	}
	return nil
}

// PromoteParams captures everything PromoteAccepted needs that isn't
// already on the artifact. Separated into a struct so future fields
// (e.g. an alternative WriteRoot override for daemon mode) don't break
// the call site.
type PromoteParams struct {
	// Cfg drives skills.WriteRoot's choice of `.agents/skills/` vs
	// `.claude/skills/`. MAY be nil; defaults to the open standard.
	Cfg *config.Config
	// Home is the user's home dir (typically os.UserHomeDir()); passed
	// in so tests can substitute a tmpdir.
	Home string
	// ProjectRoot, if non-empty, scopes the promoted skill to the
	// project's `.agents|.claude/skills/` rather than user-level.
	ProjectRoot string
	// JudgeModel is the provider:model label of the score that gated
	// the acceptance, if any. Empty when the human accepted without a
	// judge having run.
	JudgeModel string
}

// PromoteAccepted reads ref (which the caller has already confirmed is
// a skill_proposal kind), decodes the embedded Proposal, and writes a
// real SKILL.md to the user's convention path. Returns the absolute
// directory the skill landed in.
//
// # Subscription seam (NOT auto-wired in v0)
//
// Nothing today subscribes to agent.EvtApprovalAccepted to fire this
// function. A future slice (Phase 7 supervisor wiring or 6j) will:
//
//  1. Subscribe to EvtApprovalAccepted on the event log.
//  2. For each event, look up the original Propose payload to learn
//     the artifact Kind.
//  3. If Kind == agent.ArtifactKindSkillProposal, call PromoteAccepted.
//
// Ship the function now so the loop closes the moment the subscriber
// lands; until then, the CLI / TUI accept paths can call this directly
// after they invoke agent.AcceptApproval.
func PromoteAccepted(ctx context.Context, log *agent.SQLiteEventLog, ref agent.ArtifactRef, params PromoteParams) (string, error) {
	if ref.ID == "" {
		return "", errors.New("promote: empty ref ID")
	}
	if ref.Kind != agent.ArtifactKindSkillProposal {
		return "", fmt.Errorf("promote: artifact kind %q is not %q", ref.Kind, agent.ArtifactKindSkillProposal)
	}

	basePath := agent.ArtifactBasePath(params.Home)
	blob, err := agent.ReadArtifact(basePath, ref.SHA256)
	if err != nil {
		return "", fmt.Errorf("promote: read artifact: %w", err)
	}

	var p skills.Proposal
	if err := json.Unmarshal(blob, &p); err != nil {
		return "", fmt.Errorf("promote: unmarshal proposal: %w", err)
	}

	skill := p.IntoSkill(params.JudgeModel)
	root := skills.WriteRoot(params.Cfg, params.Home, params.ProjectRoot)
	dir := filepath.Join(root, skill.Name)
	if err := skills.WriteSkill(dir, skill); err != nil {
		return "", fmt.Errorf("promote: write skill: %w", err)
	}
	_ = ctx // reserved: a future slice will append an event_log entry here
	return dir, nil
}
