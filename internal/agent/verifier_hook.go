// Phase 5 slice 5c — verifier hook into the approval queue.
//
// This file holds the small wiring layer that calls Verifier.Verify and
// translates the result into an approval-queue entry. It's separated
// from verifier.go so the verifier core (one LLM call, one parser) can
// be unit-tested without dragging the approval queue + SQLite event log
// into the test.
//
// # Contract
//
// VerifyAndQueue is called by the parent of a sub-agent (or, in v0, by
// the foreground integrator) after a child produces an artifact. It:
//
//  1. Runs the verifier on the artifact body.
//  2. If the verifier returned an infra error (no judge, malformed
//     response, transport failure), it surfaces the artifact for
//     human review with a "verifier-failed" badge — never silently
//     accepts. The "necessary but not sufficient" rule from MAST: a
//     broken verifier should be loud.
//  3. If the verifier accepted cleanly (decision=accept AND score >=
//     acceptScoreThreshold), NO approval is queued — the artifact is
//     considered cleared.
//  4. Otherwise, ProposeApproval is called with the verifier's
//     concerns embedded into the title so the queue UI surfaces the
//     reason at a glance.
//
// # Wire-up
//
// The hook is INTENTIONALLY not called from spawn.go's runChild today.
// Per the task brief: "this is the wiring spawn.go's runChild calls on
// each child's agent_final artifact (for kind selection). Don't modify
// spawn.go directly — provide the hook and document the wire-up for
// the foreground integrator."
//
// Future wiring (cmd/carlos main + the foreground integrator):
//
//	if shouldVerify(ref) {
//	    _ = agent.VerifyAndQueue(ctx, log, verifier, ref, body)
//	}
//
// where shouldVerify returns true for ref.Kind == ArtifactKindAgentFinal
// AND for any future artifact with a requires_verification flag.
//
// # Phase 5d note
//
// Phase 5d (tool-grounded verification — running tests, compiling code,
// checking citations against real files) lands later and reuses this
// same hook surface: VerifyAndQueue gets richer evidence and can
// auto-accept with higher confidence. Today it's an LLM-as-judge gate
// only.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ArtifactKindAgentFinal is the artifact kind a sub-agent emits at
// terminal time (today: spawn.go writes one of these per Spawn). The
// verifier hook fires on this kind by default; see shouldVerify in
// the foreground integrator's wiring.
const ArtifactKindAgentFinal = "agent_final"

// acceptScoreThreshold is the minimum judge score that bypasses the
// approval queue. Below this, the artifact is queued even on a
// decision=accept verdict so the user can sanity-check borderline
// outputs. 8 picks the "accept with minor concerns" boundary from the
// system prompt's scoring guide.
const acceptScoreThreshold = 8

// VerifyAndQueue runs verifier against (ref, content) and, based on the
// outcome, optionally enqueues the artifact for human approval.
//
// Returns the resulting VerificationReport plus any error from the
// verifier OR approval-queue write. A non-nil error does NOT mean the
// artifact is "broken" — it means the verification machinery itself
// hit a problem (e.g. judge transport failure). In that case the
// artifact IS queued for human review with a verifier-failed title;
// the error is surfaced for logging.
//
// On a clean accept (no error, decision=accept, score >= threshold),
// no approval is queued and the returned error is nil.
func VerifyAndQueue(ctx context.Context, log *SQLiteEventLog, verifier *Verifier, ref ArtifactRef, content []byte) (VerificationReport, error) {
	if log == nil {
		return VerificationReport{}, errors.New("verifier_hook: nil log")
	}
	if ref.ID == "" {
		return VerificationReport{}, errors.New("verifier_hook: artifact ref ID required")
	}

	// Verifier may be nil (no judge configured) — fall back to
	// human-only review with a "no judge" title.
	if verifier == nil || verifier.Judge == nil {
		title := fmt.Sprintf("(unverified — no judge) %s artifact from %s", ref.Kind, ref.AgentID)
		if _, err := ProposeApproval(ctx, log, ref.AgentID, title, ref); err != nil {
			return VerificationReport{}, fmt.Errorf("verifier_hook: propose: %w", err)
		}
		return VerificationReport{}, nil
	}

	report, verifyErr := verifier.Verify(ctx, ref, content)
	if verifyErr != nil {
		// Loud surface: queue with a verifier-failed badge so the user
		// reviews even though the judge couldn't speak. Per MAST: a
		// broken verifier should not silently accept.
		title := fmt.Sprintf("(verifier-failed: %s) %s artifact from %s", trimErrForTitle(verifyErr), ref.Kind, ref.AgentID)
		if _, err := ProposeApproval(ctx, log, ref.AgentID, title, ref); err != nil {
			return report, fmt.Errorf("verifier_hook: propose after verifier err: %w (verifier err: %v)", err, verifyErr)
		}
		return report, verifyErr
	}

	// Clean accept path.
	if report.Decision == VerificationAccept && report.Score >= acceptScoreThreshold {
		return report, nil
	}

	// Needs revision / reject / low-score accept → queue.
	title := composeApprovalTitle(ref, report)
	if _, err := ProposeApproval(ctx, log, ref.AgentID, title, ref); err != nil {
		return report, fmt.Errorf("verifier_hook: propose: %w", err)
	}
	return report, nil
}

// composeApprovalTitle renders the queue title for a queued (non-
// clean-accept) verifier outcome. Title shape:
//
//	"(verifier <decision> <score>/10: <first concern>) <kind> artifact from <agentID>"
//
// First concern is truncated to 80 chars so the queue list stays
// scannable. If concerns is empty we omit the trailing colon.
func composeApprovalTitle(ref ArtifactRef, r VerificationReport) string {
	head := fmt.Sprintf("(verifier %s %d/10", r.Decision, r.Score)
	if len(r.Concerns) > 0 {
		c := strings.TrimSpace(r.Concerns[0])
		if len(c) > 80 {
			c = c[:77] + "..."
		}
		head += ": " + c
	}
	head += ")"
	return fmt.Sprintf("%s %s artifact from %s", head, ref.Kind, ref.AgentID)
}

// trimErrForTitle takes a verifier err and returns the short form for
// the queue title (no newlines, capped at 50 chars). Errors like
// "verifier: malformed judge response: json: ..." get truncated to
// keep the title row short.
func trimErrForTitle(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(s) > 50 {
		s = s[:47] + "..."
	}
	return s
}
