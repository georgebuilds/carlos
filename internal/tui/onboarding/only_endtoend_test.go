package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/config"
)

// keyEnter is a small helper so the table tests stay readable.
func keyEnter() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }
func keyRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// driveFlow feeds a sequence of key messages into the Flow's Update,
// returning the final Flow + any error from the messages. Stops early
// if a quitMsg is emitted (Done screen finishes).
func driveFlow(t *testing.T, f *Flow, keys ...tea.Msg) *Flow {
	t.Helper()
	for _, msg := range keys {
		next, cmd := f.Update(msg)
		f = next.(*Flow)
		// Drain the cmd until it stops emitting more messages.
		for cmd != nil {
			msg := cmd()
			if msg == nil {
				break
			}
			if _, isQuit := msg.(quitMsg); isQuit {
				return f
			}
			next, cmd = f.Update(msg)
			f = next.(*Flow)
		}
	}
	return f
}

// --- daemon screen --only paths ---

func TestOnly_DaemonEnabledExisting_EnterKeepsEnabled(t *testing.T) {
	cfg := &config.Config{
		UserName: "George",
		Daemon:   config.DaemonConfig{Enabled: true},
	}
	f := NewWithOptions(Options{
		StartingScreen: ScreenDaemon,
		Only:           true,
		ExistingConfig: cfg,
	})
	if !f.daemon.choice {
		t.Errorf("daemon model should preload choice=true; got %v", f.daemon.choice)
	}
	if !f.daemon.preloaded {
		t.Error("daemon model should be marked preloaded")
	}
	driveFlow(t, f, keyEnter())
	if !f.cfg.Daemon.Enabled {
		t.Errorf("enter on a preloaded-enabled daemon should keep enabled; got %v", f.cfg.Daemon.Enabled)
	}
}

func TestOnly_DaemonDisabledExisting_EnterKeepsDisabled(t *testing.T) {
	cfg := &config.Config{UserName: "George", Daemon: config.DaemonConfig{Enabled: false}}
	f := NewWithOptions(Options{
		StartingScreen: ScreenDaemon,
		Only:           true,
		ExistingConfig: cfg,
	})
	if f.daemon.choice {
		t.Errorf("daemon model should preload choice=false; got %v", f.daemon.choice)
	}
	driveFlow(t, f, keyEnter())
	if f.cfg.Daemon.Enabled {
		t.Error("enter on a preloaded-disabled daemon should keep disabled")
	}
}

func TestOnly_DaemonExplicitYFlipsOn(t *testing.T) {
	cfg := &config.Config{UserName: "George", Daemon: config.DaemonConfig{Enabled: false}}
	f := NewWithOptions(Options{
		StartingScreen: ScreenDaemon,
		Only:           true,
		ExistingConfig: cfg,
	})
	driveFlow(t, f, keyRune('y'))
	if !f.cfg.Daemon.Enabled {
		t.Error("y on a disabled daemon should enable")
	}
}

func TestOnly_DaemonExplicitNFlipsOff(t *testing.T) {
	cfg := &config.Config{UserName: "George", Daemon: config.DaemonConfig{Enabled: true}}
	f := NewWithOptions(Options{
		StartingScreen: ScreenDaemon,
		Only:           true,
		ExistingConfig: cfg,
	})
	driveFlow(t, f, keyRune('n'))
	if f.cfg.Daemon.Enabled {
		t.Error("n on an enabled daemon should disable")
	}
}

func TestOnly_DaemonAdvancesToDoneAfterChoice(t *testing.T) {
	cfg := &config.Config{UserName: "George"}
	f := NewWithOptions(Options{
		StartingScreen: ScreenDaemon,
		Only:           true,
		ExistingConfig: cfg,
	})
	f = driveFlow(t, f, keyRune('y'))
	if f.current != ScreenDone {
		t.Errorf("daemon-only flow should land at Done; got %v", f.current)
	}
}

func TestOnly_DaemonPreloadedViewShowsKeepCurrent(t *testing.T) {
	cfg := &config.Config{UserName: "George", Daemon: config.DaemonConfig{Enabled: true}}
	f := NewWithOptions(Options{
		StartingScreen: ScreenDaemon,
		Only:           true,
		ExistingConfig: cfg,
	})
	view := f.daemon.View()
	if !strings.Contains(view, "currently enabled") {
		t.Errorf("preloaded view should say 'currently enabled'; got:\n%s", view)
	}
	if !strings.Contains(view, "keep current") {
		t.Errorf("preloaded view should say 'keep current'; got:\n%s", view)
	}
}

func TestOnly_DaemonFreshOnboardingNotPreloaded(t *testing.T) {
	// Fresh onboarding (no Only flag) keeps the legacy daemon View.
	f := New()
	if f.daemon.preloaded {
		t.Error("fresh onboarding should not preload daemon")
	}
	view := f.daemon.View()
	if strings.Contains(view, "currently enabled") || strings.Contains(view, "currently disabled") {
		t.Errorf("fresh view should NOT reference current state; got:\n%s", view)
	}
}

// --- gateway screen --only paths ---

func TestOnly_GatewaySkipsDecideGate(t *testing.T) {
	cfg := &config.Config{UserName: "George", Gateway: config.GatewayConfig{Enabled: false}}
	f := NewWithOptions(Options{
		StartingScreen: ScreenGateway,
		Only:           true,
		ExistingConfig: cfg,
	})
	if f.gateway.stage == gwStageDecide {
		t.Errorf("gateway --only should auto-prime past gwStageDecide; got stage %v", f.gateway.stage)
	}
}

func TestOnly_GatewayFreshOnboardingShowsDecideGate(t *testing.T) {
	// Without --only, the gateway flow keeps the gwStageDecide gate so
	// the user can opt for "set up later" during the regular walk.
	f := New()
	if f.gateway.stage != gwStageDecide {
		t.Errorf("fresh onboarding should land on gwStageDecide; got %v", f.gateway.stage)
	}
}

func TestOnly_GatewayPreservesExistingConfig(t *testing.T) {
	cfg := &config.Config{
		UserName: "George",
		Gateway: config.GatewayConfig{
			Enabled: true,
			Ntfy: config.NtfyGatewayConfig{
				Enabled: true,
				Server:  "https://ntfy.sh",
				Topic:   "carlos-test",
			},
		},
	}
	f := NewWithOptions(Options{
		StartingScreen: ScreenGateway,
		Only:           true,
		ExistingConfig: cfg,
	})
	// The cfg pointer is the same one driving the flow; existing gateway
	// fields should still be there before the user touches anything.
	if !f.cfg.Gateway.Enabled {
		t.Error("existing gateway enabled flag dropped")
	}
	if f.cfg.Gateway.Ntfy.Topic != "carlos-test" {
		t.Errorf("existing ntfy topic dropped; got %q", f.cfg.Gateway.Ntfy.Topic)
	}
}

func TestOnly_GatewayAdvanceLandsAtDone(t *testing.T) {
	cfg := &config.Config{UserName: "George"}
	f := NewWithOptions(Options{
		StartingScreen: ScreenGateway,
		Only:           true,
		ExistingConfig: cfg,
	})
	f.advance()
	if f.current != ScreenDone {
		t.Errorf("gateway --only advance should land at Done; got %v", f.current)
	}
}

// --- empty-config edge cases ---

func TestOnly_DaemonWithNilExistingConfigSilentlyUsesDefaults(t *testing.T) {
	f := NewWithOptions(Options{
		StartingScreen: ScreenDaemon,
		Only:           true,
		// ExistingConfig nil — runOnboard handles this when config doesn't
		// exist; the flow still constructs cleanly.
	})
	if f.daemon.preloaded {
		t.Error("nil existing config should NOT trigger preload")
	}
	// Should behave like fresh onboarding's daemon screen (default false,
	// no "keep current" framing).
	if f.daemon.choice {
		t.Error("nil existing config should leave choice=false")
	}
}

func TestOnly_GatewayWithNilExistingConfigKeepsDecideGate(t *testing.T) {
	f := NewWithOptions(Options{
		StartingScreen: ScreenGateway,
		Only:           true,
	})
	// Without an existing config the user is essentially fresh-onboarding
	// just this screen; the decide gate is still meaningful.
	if f.gateway.stage != gwStageDecide {
		t.Errorf("nil-existing gateway --only should keep decide gate; got %v", f.gateway.stage)
	}
}
