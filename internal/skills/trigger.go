// trigger.go — conjunctive online-induction trigger.
//
// # Why conjunctive
//
// SPEC § Skill induction § Online trigger: "the conjunction is
// deliberate. Trading recall for precision is correct because the
// dominant failure mode is noise drafts the user ignores, not missed
// skills." SkillsBench measured self-generated skills at -1.3pp average
// with 16/84 tasks degraded (arXiv 2602.12670); "Library Learning
// Doesn't" (arXiv 2410.20274) found direct reuse of induced functions
// is negligible — gains come from the abstraction act, not later
// reuse. The asymmetry is: a bad single answer costs one answer, a bad
// skill silently degrades every future conversation that retrieves it.
//
// So the trigger insists on ALL of:
//
//  1. Verified-or-user-confirmed success — model self-assessment of
//     "did I succeed?" does NOT count.
//  2. Pattern signal — repeated tool-call sequence OR error-recovery
//     OR explicit user correction.
//  3. Novelty — low max cosine vs. existing description embeddings.
//  4. Length pre-filter — conversation crossed a minimum complexity
//     threshold. Cheap gate to skip trivial exchanges.
//
// Below trigger acceptance-rate of 50% the recommendation is to
// TIGHTEN the conjunction, not add new induction features. The
// thresholds here (noveltyFloor) are exposed as parameters precisely
// so a future telemetry slice can A/B-tune them.
//
// # Pure by construction
//
// Evaluate does no IO, no clock reads, no allocations beyond the
// reasons slice. The caller assembles signals from the supervisor /
// projection / library and asks "should the inducer fire?".
package skills

// TriggerSignals holds every input the trigger considers. Booleans for
// the categorical conjuncts; a single float for novelty (so the caller
// can compute it from an Index.MaxSimilarity directly).
//
// The "load-bearing" semantics:
//
//   - VerifiedSuccess: a checkable success state was reached (test
//     passed, file written, API returned 2xx). Model self-assessment
//     does NOT count for this field.
//   - UserConfirmedSuccess: the user explicitly marked the task done
//     (typically the gateway / supervisor "approve plan" path).
//   - RepeatedToolPattern: at least one tool-call sequence appeared
//     more than once in the conversation (the "we keep doing the same
//     three calls" signal).
//   - ErrorRecoverySuccess: the agent recovered from an error path and
//     reached the success state ("found the working path").
//   - UserCorrected: the user pushed back on the agent's approach and
//     the redirected path succeeded.
//   - Novelty: 1.0 - maxCosineSimilarity(this conversation's summary,
//     existing skill descriptions). 1.0 = totally novel, 0.0 = exact
//     overlap. Cosine similarity for unit vectors is in [-1, +1]; for
//     SHA-derived vectors here it's in [-1, +1] too. Novelty is in
//     [0, 2]; the caller should clamp to [0, 1] for the floor compare.
//   - LengthAboveFloor: conversation passed the cheap complexity gate.
//
// Reasons fired (returned by Evaluate) are pure telemetry — pass them
// through to the event log so the trigger thresholds are tunable by
// post-hoc analysis.
type TriggerSignals struct {
	VerifiedSuccess      bool
	UserConfirmedSuccess bool
	RepeatedToolPattern  bool
	ErrorRecoverySuccess bool
	UserCorrected        bool
	Novelty              float64
	LengthAboveFloor     bool
}

// Default novelty floor. Below this, we treat the candidate as too
// similar to existing skills and skip induction. 0.4 is a starting
// guess (no prior literature); the telemetry slice will retune.
const DefaultNoveltyFloor = 0.4

// Conjunct labels used in the reasons slice. Stable strings — they
// land in the event log payload and downstream dashboards group on
// them, so renaming is a schema change.
const (
	ReasonVerifiedSuccess      = "verified_success"
	ReasonUserConfirmedSuccess = "user_confirmed_success"
	ReasonRepeatedToolPattern  = "repeated_tool_pattern"
	ReasonErrorRecoverySuccess = "error_recovery_success"
	ReasonUserCorrected        = "user_corrected"
	ReasonNoveltyAboveFloor    = "novelty_above_floor"
	ReasonLengthAboveFloor     = "length_above_floor"
)

// Evaluate returns (true, reasons) iff all four conjuncts are
// satisfied. The reasons slice always lists EVERY individual signal
// that fired, regardless of the conjunctive verdict — telemetry wants
// to know "we had 3 of 4 conjuncts" as well as "we had all 4".
//
// noveltyFloor is the cosine-distance threshold the caller picks;
// DefaultNoveltyFloor is a sane starting value.
//
// Pure function, no IO. Allocation budget: one reasons slice.
func Evaluate(s TriggerSignals, noveltyFloor float64) (fire bool, reasons []string) {
	reasons = make([]string, 0, 7)

	successOK := s.VerifiedSuccess || s.UserConfirmedSuccess
	if s.VerifiedSuccess {
		reasons = append(reasons, ReasonVerifiedSuccess)
	}
	if s.UserConfirmedSuccess {
		reasons = append(reasons, ReasonUserConfirmedSuccess)
	}

	patternOK := s.RepeatedToolPattern || s.ErrorRecoverySuccess || s.UserCorrected
	if s.RepeatedToolPattern {
		reasons = append(reasons, ReasonRepeatedToolPattern)
	}
	if s.ErrorRecoverySuccess {
		reasons = append(reasons, ReasonErrorRecoverySuccess)
	}
	if s.UserCorrected {
		reasons = append(reasons, ReasonUserCorrected)
	}

	noveltyOK := s.Novelty >= noveltyFloor
	if noveltyOK {
		reasons = append(reasons, ReasonNoveltyAboveFloor)
	}

	lengthOK := s.LengthAboveFloor
	if lengthOK {
		reasons = append(reasons, ReasonLengthAboveFloor)
	}

	fire = successOK && patternOK && noveltyOK && lengthOK
	return fire, reasons
}
