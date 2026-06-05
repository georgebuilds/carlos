package skills_test

import (
	"slices"
	"testing"

	"github.com/georgebuilds/carlos/internal/skills"
)

// Test the conjunctive truth table. For (Evaluate to fire = true), ALL
// FOUR conjuncts must hold:
//
//   - (verified || user-confirmed) success
//   - (repeated-tool || error-recovery || user-correction) pattern
//   - novelty >= floor
//   - length above floor
//
// We cover: the all-true happy path, each single-conjunct dropout, and
// a representative no-signal baseline. Reasons output is checked for
// presence of the fired conjuncts.

func TestTrigger_AllTrueFires(t *testing.T) {
	fire, reasons := skills.Evaluate(skills.TriggerSignals{
		VerifiedSuccess:     true,
		RepeatedToolPattern: true,
		Novelty:             0.9,
		LengthAboveFloor:    true,
	}, skills.DefaultNoveltyFloor)
	if !fire {
		t.Fatal("want fire=true")
	}
	if !slices.Contains(reasons, skills.ReasonVerifiedSuccess) {
		t.Errorf("missing verified_success in %v", reasons)
	}
	if !slices.Contains(reasons, skills.ReasonRepeatedToolPattern) {
		t.Errorf("missing repeated_tool_pattern in %v", reasons)
	}
	if !slices.Contains(reasons, skills.ReasonNoveltyAboveFloor) {
		t.Errorf("missing novelty_above_floor in %v", reasons)
	}
	if !slices.Contains(reasons, skills.ReasonLengthAboveFloor) {
		t.Errorf("missing length_above_floor in %v", reasons)
	}
}

func TestTrigger_NoSuccessNoFire(t *testing.T) {
	fire, _ := skills.Evaluate(skills.TriggerSignals{
		RepeatedToolPattern: true,
		Novelty:             0.9,
		LengthAboveFloor:    true,
	}, skills.DefaultNoveltyFloor)
	if fire {
		t.Error("want fire=false (no success)")
	}
}

func TestTrigger_NoPatternNoFire(t *testing.T) {
	fire, _ := skills.Evaluate(skills.TriggerSignals{
		VerifiedSuccess:  true,
		Novelty:          0.9,
		LengthAboveFloor: true,
	}, skills.DefaultNoveltyFloor)
	if fire {
		t.Error("want fire=false (no pattern)")
	}
}

func TestTrigger_NoveltyBelowFloorNoFire(t *testing.T) {
	fire, reasons := skills.Evaluate(skills.TriggerSignals{
		VerifiedSuccess:     true,
		RepeatedToolPattern: true,
		Novelty:             0.1,
		LengthAboveFloor:    true,
	}, skills.DefaultNoveltyFloor)
	if fire {
		t.Error("want fire=false (novelty below floor)")
	}
	if slices.Contains(reasons, skills.ReasonNoveltyAboveFloor) {
		t.Error("should NOT report novelty_above_floor")
	}
}

func TestTrigger_LengthBelowFloorNoFire(t *testing.T) {
	fire, _ := skills.Evaluate(skills.TriggerSignals{
		VerifiedSuccess:     true,
		RepeatedToolPattern: true,
		Novelty:             0.9,
	}, skills.DefaultNoveltyFloor)
	if fire {
		t.Error("want fire=false (length below floor)")
	}
}

// All three pattern sub-signals should each satisfy the pattern
// conjunct independently.
func TestTrigger_PatternErrorRecoveryAlone(t *testing.T) {
	fire, _ := skills.Evaluate(skills.TriggerSignals{
		VerifiedSuccess:      true,
		ErrorRecoverySuccess: true,
		Novelty:              0.9,
		LengthAboveFloor:     true,
	}, skills.DefaultNoveltyFloor)
	if !fire {
		t.Error("error_recovery alone should satisfy pattern conjunct")
	}
}

func TestTrigger_PatternUserCorrectedAlone(t *testing.T) {
	fire, _ := skills.Evaluate(skills.TriggerSignals{
		VerifiedSuccess:  true,
		UserCorrected:    true,
		Novelty:          0.9,
		LengthAboveFloor: true,
	}, skills.DefaultNoveltyFloor)
	if !fire {
		t.Error("user_corrected alone should satisfy pattern conjunct")
	}
}

// UserConfirmedSuccess alone (no VerifiedSuccess) satisfies the
// success conjunct.
func TestTrigger_SuccessUserConfirmedAlone(t *testing.T) {
	fire, reasons := skills.Evaluate(skills.TriggerSignals{
		UserConfirmedSuccess: true,
		RepeatedToolPattern:  true,
		Novelty:              0.9,
		LengthAboveFloor:     true,
	}, skills.DefaultNoveltyFloor)
	if !fire {
		t.Error("user_confirmed_success alone should satisfy success conjunct")
	}
	if !slices.Contains(reasons, skills.ReasonUserConfirmedSuccess) {
		t.Errorf("missing reason in %v", reasons)
	}
}

// No-signal baseline: empty signals → no fire, no reasons listed.
func TestTrigger_EmptyNoFire(t *testing.T) {
	fire, reasons := skills.Evaluate(skills.TriggerSignals{}, skills.DefaultNoveltyFloor)
	if fire {
		t.Error("want fire=false (no signals)")
	}
	if len(reasons) != 0 {
		t.Errorf("want empty reasons, got %v", reasons)
	}
}

// Custom novelty floor.
func TestTrigger_CustomNoveltyFloor(t *testing.T) {
	// 0.5 novelty meets a 0.4 floor but not a 0.6 floor.
	sigs := skills.TriggerSignals{
		VerifiedSuccess:     true,
		RepeatedToolPattern: true,
		Novelty:             0.5,
		LengthAboveFloor:    true,
	}
	fire, _ := skills.Evaluate(sigs, 0.4)
	if !fire {
		t.Error("expected fire at floor 0.4")
	}
	fire, _ = skills.Evaluate(sigs, 0.6)
	if fire {
		t.Error("expected no fire at floor 0.6")
	}
}
