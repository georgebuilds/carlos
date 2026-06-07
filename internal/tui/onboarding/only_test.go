package onboarding

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/config"
)

// TestNewWithOptions_StartsAtRequestedScreen proves the StartingScreen
// option lands the user at the right slot.
func TestNewWithOptions_StartsAtRequestedScreen(t *testing.T) {
	f := NewWithOptions(Options{
		StartingScreen: ScreenModel,
		Only:           true,
	})
	if f.current != ScreenModel {
		t.Errorf("starting screen: want ScreenModel got %v", f.current)
	}
	if !f.only {
		t.Error("only flag not propagated")
	}
	if f.onlyStart != ScreenModel {
		t.Errorf("onlyStart: want ScreenModel got %v", f.onlyStart)
	}
}

// TestOnlyAdvanceSkipsToDone confirms that advancing out of the
// requested screen jumps directly to ScreenDone, bypassing every
// downstream screen.
func TestOnlyAdvanceSkipsToDone(t *testing.T) {
	f := NewWithOptions(Options{
		StartingScreen: ScreenSkills,
		Only:           true,
	})
	f.advance()
	if f.current != ScreenDone {
		t.Errorf("after advance from skills in only mode: want ScreenDone got %v", f.current)
	}
}

// TestOnlyBackNavBlockedAtEntry shift-tab from the only-entry should
// not back-nav out of the requested sub-flow.
func TestOnlyBackNavBlockedAtEntry(t *testing.T) {
	f := NewWithOptions(Options{
		StartingScreen: ScreenVault,
		Only:           true,
	})
	// Simulate shift-tab.
	next, _ := f.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	ff := next.(*Flow)
	if ff.current != ScreenVault {
		t.Errorf("shift-tab in only mode should stay put; got %v", ff.current)
	}
}

// TestExistingConfigSeedsFlow proves a pre-loaded config is honored
// (so `--only models` for an existing setup keeps the user's name).
func TestExistingConfigSeedsFlow(t *testing.T) {
	cfg := &config.Config{
		UserName:  "George",
		Providers: map[string]config.ProviderConfig{"openai": {APIKey: "x"}},
	}
	f := NewWithOptions(Options{
		StartingScreen: ScreenModel,
		Only:           true,
		ExistingConfig: cfg,
	})
	if f.cfg.UserName != "George" {
		t.Errorf("existing user name not preserved; got %q", f.cfg.UserName)
	}
	if _, ok := f.cfg.Providers["openai"]; !ok {
		t.Errorf("existing provider not preserved; got %+v", f.cfg.Providers)
	}
}

// TestOnlyAdvanceFromGatewayLandsAtDone is a focused check for the
// gateway sub-flow used by `carlos gateway add`.
func TestOnlyAdvanceFromGatewayLandsAtDone(t *testing.T) {
	f := NewWithOptions(Options{
		StartingScreen: ScreenGateway,
		Only:           true,
	})
	f.advance()
	if f.current != ScreenDone {
		t.Errorf("gateway-only advance: want ScreenDone got %v", f.current)
	}
}
