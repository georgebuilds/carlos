package research

import (
	"context"
	"fmt"

	"github.com/georgebuilds/carlos/internal/agent"
)

// runVerify runs two independent quality signals on the synthesis:
//
//   1. CitationAuditor - pure-Go heuristic that scores claim-by-
//      claim citation coverage. Always runs; cheap; deterministic.
//   2. Verifier (LLM judge) - only runs when e.Judge is configured.
//      The judge is intentionally a separate provider (per the
//      "Too Consistent to Detect" finding); when no separate judge
//      is available, we skip the LLM pass and rely on the
//      heuristic alone.
//
// Verifier failures (malformed response, provider down, judge ran
// out of budget) are NOT fatal - the synthesis still ships; the
// failure is recorded in Concerns. The citation audit always
// succeeds (it's pure compute).
func (e *Engine) runVerify(ctx context.Context, report *Report) (err error) {
	t0 := e.beginPhase("verify")
	defer func() { e.endPhase("verify", t0, err) }()

	if report.Synthesis == "" {
		return fmt.Errorf("nothing to verify (empty synthesis)")
	}

	// 1. Citation audit (always).
	a := auditCitations(report.Synthesis)
	report.Citations = &a

	// 2. LLM judge (optional).
	if e.Judge == nil {
		report.Concerns = append(report.Concerns,
			"verify: no separate-provider judge configured; skipping LLM verification")
		return nil
	}
	// Budget gate - the judge is a separate LLM call, so we charge
	// it against the same budget. If we're at the cap, skip rather
	// than fail.
	if report.Budget.ProviderCalls >= e.Budget.MaxProviderCalls {
		report.Concerns = append(report.Concerns,
			fmt.Sprintf("verify: provider-call budget exhausted at %d calls; skipping LLM judge",
				report.Budget.ProviderCalls))
		return nil
	}
	v := &agent.Verifier{
		Judge:      e.Judge,
		JudgeModel: e.Model, // best-effort; caller can override by passing a Judge already bound to a model
	}
	ref := agent.ArtifactRef{
		ID:      "synthesis",
		AgentID: "research-engine",
		Kind:    agent.ArtifactKindResearch,
		Size:    int64(len(report.Synthesis)),
	}
	vr, err := v.Verify(ctx, ref, []byte(report.Synthesis))
	// Charge the judge call no matter what - even a malformed
	// response cost a round-trip.
	report.Budget.ProviderCalls++
	if err != nil {
		report.Concerns = append(report.Concerns,
			fmt.Sprintf("verify: judge failed: %v", err))
		// We still keep the partial report if there is one (Score / Decision
		// will be zero-valued on parse failure).
		if vr.Score > 0 || vr.Decision != "" {
			report.Verification = &vr
		}
		return nil
	}
	report.Verification = &vr
	if vr.Decision != agent.VerificationAccept {
		report.Concerns = append(report.Concerns,
			fmt.Sprintf("verify: judge decision=%s score=%d", vr.Decision, vr.Score))
	}
	return nil
}
