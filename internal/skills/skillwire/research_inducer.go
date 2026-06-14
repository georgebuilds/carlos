// research_inducer.go - slice 11h. The adapter that lets a finished
// research run feed the EXISTING skill-induction + approval pipeline.
//
// The research engine (internal/research) defines a small
// research.SkillProposer seam and calls it at the tail of a successful,
// quality-gated run. This file implements that seam by composing the
// pieces coding sessions already use:
//
//   - research.SummarizeResearchSession -> the transcript.
//   - skills.Inducer.Induce             -> the (nil-or-Proposal) call.
//   - skillwire.ProposeSkill            -> queue via agent.WriteArtifact
//     (kind skill_proposal) +
//     agent.ProposeApproval.
//
// It lives in skillwire (not research) because skillwire already imports
// internal/skills + internal/agent; pointing it at internal/research
// adds one edge (skillwire -> research) and closes no cycle, whereas
// research -> skills would.
//
// # Best-effort contract (load-bearing)
//
// ProposeFromResearch implements research.SkillProposer, whose contract
// is: never fail or slow the research run. Every failure path here -
// nil dependencies, an inducer error, the inducer judging the run
// not-reusable, a queue-write error - is swallowed to the optional
// Diag sink. The research run's success/failure classification is
// already settled by the time this runs; nothing here can change it.
package skillwire

import (
	"context"
	"fmt"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/skills"
)

// ResearchInducer is the narrow slice of *skills.Inducer the adapter
// needs. Exporting it lets tests inject a fake without standing up a
// real provider, and documents exactly what the adapter calls.
// *skills.Inducer satisfies it.
type ResearchInducer interface {
	Induce(ctx context.Context, transcript string, existingDescriptions []string, opts skills.InducerOptions) (*skills.Proposal, error)
}

// ResearchSkillProposer adapts the skill-induction pipeline onto the
// research.SkillProposer seam. Construct one with NewResearchSkillProposer
// and install it on research.Engine.SkillProposer (typically at the
// engine-build site in cmd/carlos).
//
// Concurrency: a single proposer may be shared across concurrent
// research sessions. It holds only immutable configuration; the
// per-session agent ID arrives as a call argument.
type ResearchSkillProposer struct {
	// Log is the event log the proposal artifact + approval event are
	// written to. Required; a nil Log makes ProposeFromResearch a no-op.
	Log *agent.SQLiteEventLog

	// Inducer runs the single-call induction. Required; nil makes
	// ProposeFromResearch a no-op.
	Inducer ResearchInducer

	// Model overrides the inducer's provider model. Empty = provider
	// default.
	Model string

	// ExistingDescriptions is the active skill set fed to the inducer's
	// dedup-prevention block. May be nil (cold-start library). Callers
	// typically pass skills.Library.Descriptions().
	ExistingDescriptions []string

	// Diag, when non-nil, receives a one-line diagnostic on every
	// non-proposal outcome (not reusable, inducer error, queue error).
	// nil = silent. This is the swallow-to-diagnostic sink the
	// best-effort contract calls for.
	Diag func(string)
}

// NewResearchSkillProposer builds a proposer over a real inducer. The
// caller supplies the inducer (so the provider/model choice stays with
// the wiring site) plus the active skill descriptions for dedup.
func NewResearchSkillProposer(log *agent.SQLiteEventLog, ind *skills.Inducer, model string, existing []string, diag func(string)) *ResearchSkillProposer {
	return &ResearchSkillProposer{
		Log:                  log,
		Inducer:              ind,
		Model:                model,
		ExistingDescriptions: existing,
		Diag:                 diag,
	}
}

// ProposeFromResearch implements research.SkillProposer. It summarizes
// the report, runs the inducer, and - if the inducer returns a Proposal
// - queues it through the existing approval pipeline. All failure paths
// are swallowed to p.Diag; the method never panics and never returns an
// error (the seam is signature-free for exactly this reason).
//
// agentID is the research session's agent ID; the proposal artifact is
// attributed to it so the artifacts foreign key is satisfied by the
// agents row SpawnResearch already created.
func (p *ResearchSkillProposer) ProposeFromResearch(ctx context.Context, agentID string, report *research.Report) {
	if p == nil || p.Log == nil || p.Inducer == nil {
		p.diag("research induction skipped: proposer not fully wired")
		return
	}
	if agentID == "" || report == nil {
		p.diag("research induction skipped: missing agentID or report")
		return
	}

	// SummarizeResearchSession always returns a non-empty summary for a
	// non-nil report (it writes a header + the question), so there is no
	// empty-transcript branch to guard here; the inducer's own
	// empty-transcript check is the backstop if that ever changes.
	transcript := research.SummarizeResearchSession(report)

	proposal, err := p.Inducer.Induce(ctx, transcript, p.ExistingDescriptions, skills.InducerOptions{
		Model:       p.Model,
		InducedFrom: []string{agentID},
	})
	if err != nil {
		p.diag(fmt.Sprintf("research induction error (swallowed): %v", err))
		return
	}
	if proposal == nil {
		// The common, expected case: the run was not a reusable
		// procedure. Not an error.
		p.diag("research induction: run not reusable, no skill proposed")
		return
	}

	if _, err := ProposeSkill(ctx, p.Log, agentID, proposal); err != nil {
		p.diag(fmt.Sprintf("research skill queue error (swallowed): %v", err))
		return
	}
	p.diag(fmt.Sprintf("research skill candidate queued: %s", proposal.Name))
}

// diag fires the optional diagnostic sink. nil-safe on the receiver so
// the early-return guard can call it even when p is nil.
func (p *ResearchSkillProposer) diag(msg string) {
	if p == nil || p.Diag == nil {
		return
	}
	p.Diag(msg)
}
