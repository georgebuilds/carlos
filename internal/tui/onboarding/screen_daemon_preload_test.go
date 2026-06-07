package onboarding

import (
	"strings"
	"testing"
)

// TestDaemonScreen_PreloadedEnabledRendersKeepCurrent pins the preload
// View copy. The user sees "currently enabled" + "keep current" so they
// know what `enter` will do.
func TestDaemonScreen_PreloadedEnabledRendersKeepCurrent(t *testing.T) {
	m := newDaemonModelWithInitial(true)
	view := m.View()
	for _, want := range []string{"currently enabled", "keep current"} {
		if !strings.Contains(view, want) {
			t.Errorf("preloaded-enabled view missing %q; got:\n%s", want, view)
		}
	}
}

func TestDaemonScreen_PreloadedDisabledRendersKeepCurrent(t *testing.T) {
	m := newDaemonModelWithInitial(false)
	view := m.View()
	for _, want := range []string{"currently disabled", "keep current"} {
		if !strings.Contains(view, want) {
			t.Errorf("preloaded-disabled view missing %q; got:\n%s", want, view)
		}
	}
}

func TestDaemonScreen_FreshViewLacksPreloadLanguage(t *testing.T) {
	m := newDaemonModel()
	view := m.View()
	for _, no := range []string{"currently enabled", "currently disabled", "keep current"} {
		if strings.Contains(view, no) {
			t.Errorf("fresh view should not include preload language; found %q in:\n%s", no, view)
		}
	}
}

// TestPrimeGatewayStandalone_Idempotent ensures double-prime is safe.
func TestPrimeGatewayStandalone_Idempotent(t *testing.T) {
	f := NewWithOptions(Options{StartingScreen: ScreenGateway, Only: true})
	f.PrimeGatewayStandalone()
	stage1 := f.gateway.stage
	f.PrimeGatewayStandalone()
	stage2 := f.gateway.stage
	if stage1 != stage2 {
		t.Errorf("double-prime changed stage: %v -> %v", stage1, stage2)
	}
	if stage1 == gwStageDecide {
		t.Errorf("primed flow should not be on gwStageDecide; got %v", stage1)
	}
}
