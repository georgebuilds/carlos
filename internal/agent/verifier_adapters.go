// Phase 5 slice 5d — tool-grounded verifier adapters.
//
// Empirical context (see /Volumes/nas/vault/personal/projects/carlos/
// research/2026-06-04 Supervisor — decisions adopted.md): "tool-grounded
// verification preferred where available; LLM-judge only where no
// deterministic check exists." A compiler exit code, a test runner's
// pass/fail count, and a URL's HTTP status are deterministic — every
// agent run of the same artifact against the same workdir produces the
// same verdict.
//
// # Architectural commitments
//
//   - Tool-grounded verifiers are deterministic. They run a real tool;
//     the tool's exit code + output IS the verdict. No LLM in the loop.
//   - Same VerificationReport shape as the LLM Verifier so callers don't
//     branch. Decision ∈ {accept | needs_revision | reject}. Score is a
//     normalized 1-10 (10 = clean pass, 1 = broken).
//   - One adapter per check kind. Don't fold them — different artifact
//     kinds need different verifiers. A Dispatcher picks the right one
//     (or several) based on artifact Kind + content sniff.
//   - Verifier choice is opt-in per artifact. The supervisor decides
//     whether to run a verifier when an artifact is queued (today: only
//     when artifact Kind matches a known mapping, like kind=="plan" →
//     ToolGroundedDispatcher).
//   - No reliance on a global project root — adapters take a workdir arg
//     so they work inside Worktree sandboxes (Slice 3f).
//
// # Wiring
//
// The Dispatcher and adapters are constructed by the foreground
// integrator (cmd/carlos/main.go runHeadless setup); a separate slice
// wires them into the supervisor approval queue. See the slice-5d notes
// for the integration snippet — this file does not modify supervisor.go.
package agent

import (
	"context"
	"errors"
	"fmt"
)

// ToolGroundedVerifier is the interface every tool-grounded adapter
// implements. Mirrors the surface of the LLM-judge Verifier.Verify but
// dropped the ArtifactRef arg — the adapters care about the bytes and
// the workdir, not the metadata. The Dispatcher carries the kind
// dimension for routing instead.
type ToolGroundedVerifier interface {
	// Name returns the adapter's identifier (e.g. "compiler", "tests",
	// "urls"). Embedded into VerificationReport.JudgeModel as
	// "<name>:<sub-id>" so the queue UI can attribute the verdict.
	Name() string

	// Verify runs the deterministic check against content (the artifact
	// body, e.g. a diff or a research output) in workdir (e.g. a
	// Worktree.Root). Returns the report the queue gate already
	// understands.
	//
	// A nil-error return with Decision=reject is the normal path for a
	// failed check. A non-nil error is reserved for infra failures (no
	// toolchain on PATH, workdir unreadable, etc.) — i.e. the verifier
	// could not run, not that the verifier ran and the artifact failed.
	Verify(ctx context.Context, workdir string, content []byte) (VerificationReport, error)
}

// Dispatcher routes artifact kinds to the registered tool-grounded
// adapters. Zero-or-more adapters per kind: the foreground integrator
// can register a Compiler AND a TestRunner under "plan" to get both
// signals on a plan artifact.
//
// The Dispatcher itself is thread-safe AFTER construction (post-Register
// calls). The registration map is not locked because typical usage is
// register-once-at-boot then dispatch-many — matching how the existing
// tools.Registry behaves.
type Dispatcher struct {
	byKind map[string][]ToolGroundedVerifier
}

// NewDispatcher returns an empty Dispatcher. The default kind mappings
// suggested by slice 5d (plan / diff → Compiler + TestRunner; research →
// URLRefetcher) are NOT pre-registered here — registration is the
// foreground integrator's responsibility so it can opt out of any
// adapter the user's config disables.
//
// Callers that want the default wiring can call RegisterDefaults below.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{byKind: map[string][]ToolGroundedVerifier{}}
}

// Register adds v to the list of adapters that fire for artifacts of
// the given kind. Multiple adapters per kind are allowed and run in
// registration order. Registering the same adapter twice under the same
// kind appends it twice (callers are expected to register once).
func (d *Dispatcher) Register(kind string, v ToolGroundedVerifier) {
	if d == nil || v == nil || kind == "" {
		return
	}
	d.byKind[kind] = append(d.byKind[kind], v)
}

// RegisterDefaults wires the canonical kind → adapter mapping documented
// in the slice-5d notes:
//
//	"plan"     → CompilerVerifier, TestRunnerVerifier
//	"diff"     → CompilerVerifier, TestRunnerVerifier
//	"research" → URLRefetcherVerifier
//
// Helper so the foreground integrator can opt into the whole default
// set in one line. Callers that want a custom mapping skip this and
// call Register directly.
func (d *Dispatcher) RegisterDefaults() {
	if d == nil {
		return
	}
	compiler := NewCompilerVerifier()
	tests := NewTestRunnerVerifier()
	urls := NewURLRefetcherVerifier()
	d.Register("plan", compiler)
	d.Register("plan", tests)
	d.Register("diff", compiler)
	d.Register("diff", tests)
	d.Register("research", urls)
}

// Verify runs every adapter registered for kind, in registration order,
// and returns each adapter's report (or an empty slice if no adapter is
// registered for kind). The returned error is non-nil if AT LEAST ONE
// adapter returned an infra error; the per-adapter reports are still
// returned so the caller can surface partial signal.
//
// Adapter infra errors are joined with errors.Join so the caller can
// errors.Is each sentinel independently.
func (d *Dispatcher) Verify(ctx context.Context, workdir, kind string, content []byte) ([]VerificationReport, error) {
	if d == nil {
		return nil, errors.New("dispatcher: nil receiver")
	}
	adapters := d.byKind[kind]
	if len(adapters) == 0 {
		return nil, nil
	}
	reports := make([]VerificationReport, 0, len(adapters))
	var errs []error
	for _, a := range adapters {
		r, err := a.Verify(ctx, workdir, content)
		// Always record the report — even on infra error the adapter
		// may have filled in JudgeModel + concerns we want to surface.
		reports = append(reports, r)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Name(), err))
		}
	}
	if len(errs) > 0 {
		return reports, errors.Join(errs...)
	}
	return reports, nil
}

// KindsRegistered returns the registered kinds in arbitrary order —
// handy for tests and for the diagnostics command.
func (d *Dispatcher) KindsRegistered() []string {
	if d == nil {
		return nil
	}
	out := make([]string, 0, len(d.byKind))
	for k := range d.byKind {
		out = append(out, k)
	}
	return out
}

// scoreFromRatio maps a healthy-fraction in [0.0, 1.0] to the
// VerificationReport.Score 1-10 scale used by the rest of the verifier
// stack. Used by URLRefetcher and TestRunner to keep their scoring
// consistent.
//
// Boundaries:
//
//	1.0  → 10 (clean pass)
//	0.0  → 1  (broken; never zero — Score is 1-based per verifier.go)
//	mid  → round(1 + 9*ratio); clamped to [1, 10].
//
// We never return 0 because verifier.parseJudgeResponse rejects scores
// outside 1-10 and we want our reports to be valid against the same
// gate. A truly empty input (zero claims, zero URLs, no tests) is a
// caller-side semantic we handle separately — typically by returning a
// clean accept with score 10.
func scoreFromRatio(ratio float64) int {
	if ratio >= 1.0 {
		return 10
	}
	if ratio <= 0.0 {
		return 1
	}
	s := 1 + int(9.0*ratio+0.5)
	if s < 1 {
		s = 1
	}
	if s > 10 {
		s = 10
	}
	return s
}

// decisionFromRatio maps a healthy-fraction in [0.0, 1.0] to a
// VerificationDecision using the thresholds from the slice-5d brief:
//
//	ratio >= 0.95 → accept
//	ratio <  0.5  → reject
//	otherwise     → needs_revision
//
// Shared between TestRunner and URLRefetcher to keep their decision
// boundaries aligned.
func decisionFromRatio(ratio float64) VerificationDecision {
	switch {
	case ratio >= 0.95:
		return VerificationAccept
	case ratio < 0.5:
		return VerificationReject
	default:
		return VerificationNeedsRevision
	}
}
