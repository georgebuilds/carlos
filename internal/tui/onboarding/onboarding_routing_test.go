package onboarding

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/config"
)

// TestFlow_UpdateRoutesToActiveChild drives a key message into the Flow
// on each screen and confirms the corresponding child model handled it
// (the Flow's per-screen Update arms). We use a type assertion on the
// returned model to make sure the Flow stays the outer model.
func TestFlow_UpdateRoutesToActiveChild(t *testing.T) {
	screens := []Screen{
		ScreenName, ScreenProvider, ScreenModel,
		ScreenSkills, ScreenVault, ScreenDaemon,
		ScreenGateway, ScreenDone,
	}
	for _, s := range screens {
		t.Run(screenTitle(s), func(t *testing.T) {
			f := NewWithOptions(Options{
				StartingScreen: s,
				ExistingConfig: &config.Config{
					UserName: "Tester",
					Providers: map[string]config.ProviderConfig{
						"openai": {APIKey: "sk-x"},
					},
				},
			})
			// A benign keypress that no screen treats as advance.
			out, _ := f.Update(tea.KeyMsg{Runes: []rune{'z'}, Type: tea.KeyRunes})
			if _, ok := out.(*Flow); !ok {
				t.Fatalf("Update should return the *Flow; got %T", out)
			}
		})
	}
}

// TestFlow_PulseTickChainsThenSettles drives the pulse animation through
// its full 1→2→3→0 walk via repeated pulseTickMsg, covering both the
// chaining arm and the wrap-to-zero terminal arm.
func TestFlow_PulseTickChainsThenSettles(t *testing.T) {
	f := New()
	f.pulseFrame = 1
	// Frames 2 and 3 should keep chaining (non-nil cmd).
	for want := 2; want <= 3; want++ {
		out, cmd := f.Update(pulseTickMsg{})
		f = out.(*Flow)
		if f.pulseFrame != want {
			t.Fatalf("pulseFrame: want %d got %d", want, f.pulseFrame)
		}
		if cmd == nil {
			t.Fatalf("frame %d should chain another tick", want)
		}
	}
	// Next tick wraps past 3 → settles to 0 with no further tick.
	out, cmd := f.Update(pulseTickMsg{})
	f = out.(*Flow)
	if f.pulseFrame != 0 {
		t.Errorf("pulse should settle to 0; got %d", f.pulseFrame)
	}
	if cmd != nil {
		t.Error("settled pulse should not schedule another tick")
	}
}

// TestFlow_QuitMsgRequestsQuit covers the quitMsg arm of Flow.Update.
func TestFlow_QuitMsgRequestsQuit(t *testing.T) {
	f := New()
	_, cmd := f.Update(quitMsg{})
	if cmd == nil {
		t.Fatal("quitMsg should produce a tea.Quit cmd")
	}
}

// TestFlow_NextScreenMsgAdvancesAndPulses covers the nextScreenMsg arm:
// it applies the payload, advances, and kicks off the pulse.
func TestFlow_NextScreenMsgAdvancesAndPulses(t *testing.T) {
	f := New()
	f.current = ScreenName
	out, cmd := f.Update(nextScreenMsg{payload: nameResult{name: "Ada"}})
	f = out.(*Flow)
	if f.cfg.UserName != "Ada" {
		t.Errorf("payload should be applied; UserName = %q", f.cfg.UserName)
	}
	if f.current != ScreenProvider {
		t.Errorf("should advance to ScreenProvider; got %v", f.current)
	}
	if f.pulseFrame != 1 {
		t.Errorf("advance should arm the pulse (frame 1); got %d", f.pulseFrame)
	}
	if cmd == nil {
		t.Error("advance should schedule the first pulse tick")
	}
}

// TestNewWithOptions_OpenRouterPrefetch covers the constructor branch
// that kicks off the OpenRouter catalog fetch when an existing config
// already wires openrouter with a key.
func TestNewWithOptions_OpenRouterPrefetch(t *testing.T) {
	f := NewWithOptions(Options{
		ExistingConfig: &config.Config{
			UserName: "Tester",
			Providers: map[string]config.ProviderConfig{
				"openrouter": {APIKey: "sk-or-x"},
			},
		},
	})
	if f.model.orFuture == nil {
		t.Error("existing openrouter key should prime the model screen's fetch future")
	}
}

// TestNewWithOptions_OutOfRangeStartingScreenClampsToName verifies the
// starting-screen clamp guard.
func TestNewWithOptions_OutOfRangeStartingScreenClampsToName(t *testing.T) {
	f := NewWithOptions(Options{StartingScreen: Screen(99)})
	if f.current != ScreenName {
		t.Errorf("out-of-range starting screen should clamp to ScreenName; got %v", f.current)
	}
}

// TestNewWithOptions_BlankUserNameGetsDefault covers the blank-username
// substitution arm.
func TestNewWithOptions_BlankUserNameGetsDefault(t *testing.T) {
	f := NewWithOptions(Options{
		ExistingConfig: &config.Config{UserName: "   "},
	})
	if f.cfg.UserName != config.DefaultUserName {
		t.Errorf("blank username should default to %q; got %q", config.DefaultUserName, f.cfg.UserName)
	}
}

// TestNewWithOptions_NilProvidersInitialized covers the nil-map guard.
func TestNewWithOptions_NilProvidersInitialized(t *testing.T) {
	f := NewWithOptions(Options{
		ExistingConfig: &config.Config{UserName: "X", Providers: nil},
	})
	if f.cfg.Providers == nil {
		t.Error("nil Providers map should be initialized")
	}
}
