package onboarding

import (
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
)

// TestDaemonChoiceForTest covers the cmd-level helper used by tests
// outside the package to peek at the daemon model's choice. Nil flow
// returns false; a fresh flow returns the model's default (false).
func TestDaemonChoiceForTest(t *testing.T) {
	if got := DaemonChoiceForTest(nil); got {
		t.Error("nil flow should report choice=false")
	}
	f := New()
	if got := DaemonChoiceForTest(f); got {
		t.Errorf("fresh flow should report daemon choice=false; got %v", got)
	}
	// Flipping the model's choice exposes the wiring.
	f.daemon.choice = true
	if got := DaemonChoiceForTest(f); !got {
		t.Error("after setting choice=true, helper should report true")
	}
}

// TestGatewayIsDecideStageForTest covers the per-flow stage peek used
// by cmd-level tests to verify --only / gateway-add skip the decide
// gate.
func TestGatewayIsDecideStageForTest(t *testing.T) {
	if got := GatewayIsDecideStageForTest(nil); got {
		t.Error("nil flow should not be on decide stage")
	}
	f := New()
	if got := GatewayIsDecideStageForTest(f); !got {
		t.Error("fresh flow should report decide stage")
	}
	f.PrimeGatewayStandalone()
	if got := GatewayIsDecideStageForTest(f); got {
		t.Error("after PrimeGatewayStandalone, decide stage should be skipped")
	}
}

// TestFlowCfgForTest hands tests the in-progress config pointer. Nil
// flow returns nil; a constructed flow returns its own cfg.
func TestFlowCfgForTest(t *testing.T) {
	if got := FlowCfgForTest(nil); got != nil {
		t.Error("nil flow should yield nil cfg")
	}
	cfg := &config.Config{UserName: "George"}
	f := NewWithOptions(Options{ExistingConfig: cfg})
	got := FlowCfgForTest(f)
	if got == nil {
		t.Fatal("constructed flow should expose its cfg")
	}
	if got.UserName != "George" {
		t.Errorf("FlowCfgForTest returned cfg with UserName=%q, want George", got.UserName)
	}
}
