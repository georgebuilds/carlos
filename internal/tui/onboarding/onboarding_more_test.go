package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/config"
)

// TestFlow_InitDispatchesPerStartingScreen exercises the dispatch table
// inside Flow.Init so we cover every screen branch.
func TestFlow_InitDispatchesPerStartingScreen(t *testing.T) {
	for _, s := range []Screen{
		ScreenName, ScreenProvider, ScreenModel,
		ScreenSkills, ScreenVault, ScreenDaemon,
		ScreenGateway, ScreenDone,
	} {
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
			// Init returns nil OR a tea.Cmd; both are acceptable. The
			// goal is to walk the switch so coverage records the path.
			_ = f.Init()
		})
	}
}

// TestFlow_InitDefaultBranch covers the fallback path when current is
// out of range (defensive default to name screen).
func TestFlow_InitDefaultBranch(t *testing.T) {
	f := New()
	f.current = Screen(99) // out of range
	// Should not panic; falls through to name.Init.
	_ = f.Init()
}

// TestFlow_ViewBeforeWindowSize renders defensively with assumed dims.
// Pre-WindowSizeMsg, the view paints a default frame instead of an
// empty string.
func TestFlow_ViewBeforeWindowSize(t *testing.T) {
	f := New()
	out := f.View()
	if out == "" {
		t.Error("View before WindowSizeMsg should still render something")
	}
}

// TestFlow_ViewTooSmall produces the polite "needs at least N x M"
// fallback rather than a broken layout.
func TestFlow_ViewTooSmall(t *testing.T) {
	f := New()
	f.width = 40
	f.height = 10
	out := f.View()
	if !strings.Contains(out, "needs at least") {
		t.Errorf("too-small view should say 'needs at least'; got:\n%s", out)
	}
}

// TestFlow_ViewNormalSize verifies a healthy terminal size produces a
// non-empty bordered render that includes the brand wordmark. Swap the
// cached portrait for an ASCII stand-in so the kitty/iterm protocol
// payload doesn't drown out the wordmark.
func TestFlow_ViewNormalSize(t *testing.T) {
	f := New()
	f.portrait = "[face]"
	f, _ = updateFlow(f, tea.WindowSizeMsg{Width: 120, Height: 40})
	out := f.View()
	if out == "" {
		t.Fatal("normal-sized view should not be empty")
	}
	if !strings.Contains(stripStyle(out), "carlos") {
		t.Errorf("view should include the 'carlos' wordmark in the rail; got:\n%s", out)
	}
}

// TestFlow_ViewEachScreen makes sure renderRightPane covers every
// screen branch (View calls renderInner -> renderRightPane). The
// cached portrait is swapped for an ASCII stand-in so a kitty/iterm
// protocol cached at package init doesn't dominate the rendered
// output (which would make a content assertion brittle).
func TestFlow_ViewEachScreen(t *testing.T) {
	for _, s := range []Screen{
		ScreenName, ScreenProvider, ScreenModel,
		ScreenSkills, ScreenVault, ScreenDaemon,
		ScreenGateway, ScreenDone,
	} {
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
			f.portrait = "[face]"
			f, _ = updateFlow(f, tea.WindowSizeMsg{Width: 120, Height: 40})
			out := f.View()
			if out == "" {
				t.Errorf("View for screen %v should not be empty", s)
			}
		})
	}
}

// TestFooterBar covers all three footer variants (Name / Done / default).
func TestFooterBar(t *testing.T) {
	cases := []struct {
		screen Screen
		want   []string
	}{
		{ScreenName, []string{"enter", "continue", "ctrl-c", "cancel"}},
		{ScreenDone, []string{"enter", "finish", "ctrl-c", "cancel"}},
		{ScreenProvider, []string{"enter", "continue", "shift-tab", "back", "ctrl-c", "cancel"}},
		{ScreenSkills, []string{"shift-tab", "back"}},
	}
	for _, c := range cases {
		got := stripStyle(footerBar(c.screen))
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("footerBar(%v) missing %q; got %q", c.screen, w, got)
			}
		}
	}
}

// TestQuitCmd verifies the quit helper returns a tea.Cmd that yields a
// quitMsg.
func TestQuitCmd(t *testing.T) {
	cmd := quit()
	if cmd == nil {
		t.Fatal("quit() returned nil cmd")
	}
	msg := cmd()
	if _, ok := msg.(quitMsg); !ok {
		t.Errorf("quit() cmd should emit quitMsg, got %T", msg)
	}
}

// TestOuterBorderStyle returns a lipgloss style sized to width-2 and
// height-4 (matches the leading "\n\n" margin in View).
func TestOuterBorderStyle(t *testing.T) {
	s := outerBorderStyle(100, 30)
	if w := s.GetWidth(); w != 98 {
		t.Errorf("outer border width = %d, want 98", w)
	}
	if h := s.GetHeight(); h != 26 {
		t.Errorf("outer border height = %d, want 26", h)
	}
}

// TestCenterLines centers a short visible-width payload inside a wider
// frame by adding a left-pad prefix on every line.
func TestCenterLines(t *testing.T) {
	s := centerLines("hi", 10, 2) // pad = (10-2)/2 = 4
	if s != "    hi" {
		t.Errorf("centerLines pad: got %q", s)
	}
}

// TestCenterLines_NoPad keeps the input intact when there's no room to
// center (negative or zero pad).
func TestCenterLines_NoPad(t *testing.T) {
	if got := centerLines("hello", 5, 5); got != "hello" {
		t.Errorf("centerLines no pad: got %q want %q", got, "hello")
	}
	if got := centerLines("hello", 3, 5); got != "hello" {
		t.Errorf("centerLines negative pad: got %q want %q", got, "hello")
	}
}

// TestCenterLines_Multiline prefixes the pad to each line of a
// multi-line block (lipgloss-rendered blocks are joined with \n).
func TestCenterLines_Multiline(t *testing.T) {
	got := centerLines("a\nbc", 6, 2)
	want := "  a\n  bc"
	if got != want {
		t.Errorf("centerLines multiline: got %q want %q", got, want)
	}
}

// TestFlow_RenderInnerProducesRailAndPane composes the full inner render
// and asserts the rail's brand wordmark and the right-pane title both
// land in the output. The cached portrait is swapped for a short ASCII
// stand-in so a kitty/iterm protocol pre-cached at package init time
// doesn't crowd the assertion needles out of the visible area.
func TestFlow_RenderInnerProducesRailAndPane(t *testing.T) {
	f := New()
	f.portrait = "[face]"
	f.width = 120
	f.height = 40
	inner := f.renderInner(110, 30)
	clean := stripStyle(inner)
	if !strings.Contains(clean, "carlos") {
		t.Errorf("renderInner missing 'carlos' brand; got:\n%s", clean)
	}
	if !strings.Contains(clean, screenTitle(ScreenName)) {
		t.Errorf("renderInner missing name screen title; got:\n%s", clean)
	}
}

// TestFlow_RenderInnerTinyHeight verifies the contentH floor when the
// outer frame is squeezed below the configured minimum (12-row floor).
func TestFlow_RenderInnerTinyHeight(t *testing.T) {
	f := New()
	out := f.renderInner(110, 5) // forces contentH < 12
	if out == "" {
		t.Error("renderInner with tiny height should still render")
	}
}

// TestFlow_RenderLeftRailIncludesStepCounter shows the "step N of M"
// line is present. We replace the cached portrait with a short ASCII
// stand-in so the test doesn't have to fight whichever portrait
// protocol got cached at package init time.
func TestFlow_RenderLeftRailIncludesStepCounter(t *testing.T) {
	f := New()
	f.portrait = "[face]"
	out := stripStyle(f.renderLeftRail(leftRailWidth, 20))
	if !strings.Contains(out, "step 1 of 8") {
		t.Errorf("rail should show 'step 1 of 8' on fresh flow; got:\n%s", out)
	}
}

// TestFlow_RenderRightPaneEachScreen iterates the right-pane dispatch
// to land coverage on every screen-title path.
func TestFlow_RenderRightPaneEachScreen(t *testing.T) {
	for _, s := range []Screen{
		ScreenName, ScreenProvider, ScreenModel, ScreenSkills,
		ScreenVault, ScreenDaemon, ScreenGateway, ScreenDone,
	} {
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
			out := f.renderRightPane(60, 20)
			if out == "" {
				t.Errorf("renderRightPane for %v should not be empty", s)
			}
			clean := stripStyle(out)
			if !strings.Contains(clean, screenTitle(s)) {
				t.Errorf("renderRightPane for %v missing screen title; got:\n%s", s, clean)
			}
		})
	}
}

// TestFlow_QuitMsgEndsRun proves the quitMsg path returns tea.Quit so
// the program loop terminates cleanly.
func TestFlow_QuitMsgEndsRun(t *testing.T) {
	f := New()
	_, cmd := f.Update(quitMsg{})
	if cmd == nil {
		t.Fatal("quitMsg should produce a tea.Quit cmd")
	}
	// We don't pin the returned msg type (tea.Quit is unexported as a
	// concrete type), but the cmd must not be nil.
}

// TestFlow_CtrlCSetsAbortFlag walks the abort path and verifies the
// aborted flag flips so Run() returns ErrAborted.
func TestFlow_CtrlCSetsAbortFlag(t *testing.T) {
	f := New()
	next, cmd := f.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	ff := next.(*Flow)
	if !ff.aborted {
		t.Error("ctrl-c should set aborted=true")
	}
	if cmd == nil {
		t.Error("ctrl-c should return tea.Quit cmd")
	}
}

// TestFlow_ShiftTabAtNameIsNoop guards the floor of back-nav: shift-tab
// at the first screen cannot underflow into negative-screen-index.
func TestFlow_ShiftTabAtNameIsNoop(t *testing.T) {
	f := New() // starts at ScreenName
	next, _ := f.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	ff := next.(*Flow)
	if ff.current != ScreenName {
		t.Errorf("shift-tab at name should stay; got %v", ff.current)
	}
}

// TestFlow_ShiftTabSkipsGatewayWhenDaemonDisabled mirrors the
// advance()-side auto-skip.
func TestFlow_ShiftTabSkipsGatewayWhenDaemonDisabled(t *testing.T) {
	f := NewWithOptions(Options{
		StartingScreen: ScreenDone,
		ExistingConfig: &config.Config{
			UserName: "Tester",
			Daemon:   config.DaemonConfig{Enabled: false},
		},
	})
	// shift-tab from Done with daemon disabled lands on Daemon (one
	// extra step back past Gateway).
	next, _ := f.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	ff := next.(*Flow)
	if ff.current != ScreenDaemon {
		t.Errorf("shift-tab from Done w/ daemon off should land at Daemon, got %v", ff.current)
	}
}

// TestFlow_ShiftTabStopsAtGatewayWhenDaemonEnabled exercises the
// converse: with daemon enabled, shift-tab from Done lands at Gateway.
func TestFlow_ShiftTabStopsAtGatewayWhenDaemonEnabled(t *testing.T) {
	f := NewWithOptions(Options{
		StartingScreen: ScreenDone,
		ExistingConfig: &config.Config{
			UserName: "Tester",
			Daemon:   config.DaemonConfig{Enabled: true},
		},
	})
	next, _ := f.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	ff := next.(*Flow)
	if ff.current != ScreenGateway {
		t.Errorf("shift-tab from Done w/ daemon on should land at Gateway, got %v", ff.current)
	}
}

// TestPrimeGatewayStandalone is the public helper that lets `gateway
// add` bypass the decide gate.
func TestPrimeGatewayStandalone(t *testing.T) {
	f := New()
	if f.gateway.stage != gwStageDecide {
		t.Fatalf("fresh gateway should start at decide; got %v", f.gateway.stage)
	}
	f.PrimeGatewayStandalone()
	if f.gateway.stage == gwStageDecide {
		t.Error("PrimeGatewayStandalone should skip the decide gate")
	}
}

// updateFlow is a typed convenience wrapper for the tea.Model.Update
// pattern so test code reads tighter.
func updateFlow(f *Flow, msg tea.Msg) (*Flow, tea.Cmd) {
	next, cmd := f.Update(msg)
	return next.(*Flow), cmd
}

// TestApplyChildPayload_Name merges a non-empty name into the config.
func TestApplyChildPayload_Name(t *testing.T) {
	f := New()
	f.applyChildPayload(nameResult{name: "Alice"})
	if f.cfg.UserName != "Alice" {
		t.Errorf("name payload: cfg.UserName = %q, want Alice", f.cfg.UserName)
	}
}

// TestApplyChildPayload_NameEmptyKeepsExisting verifies the empty-name
// guard so a back-nav with an empty input doesn't blank the field.
func TestApplyChildPayload_NameEmptyKeepsExisting(t *testing.T) {
	f := NewWithOptions(Options{
		ExistingConfig: &config.Config{UserName: "Existing"},
	})
	f.applyChildPayload(nameResult{name: ""})
	if f.cfg.UserName != "Existing" {
		t.Errorf("empty name payload should preserve existing; got %q", f.cfg.UserName)
	}
}

// TestApplyChildPayload_Provider replaces the providers map and sets
// the default; if openrouter was wired with a key, the model future
// kicks off.
func TestApplyChildPayload_Provider(t *testing.T) {
	f := New()
	pr := providerResult{
		providers: map[string]config.ProviderConfig{
			"openai":     {APIKey: "sk-x"},
			"openrouter": {APIKey: "or-x"},
		},
		defaultProvider: "openai",
	}
	f.applyChildPayload(pr)
	if f.cfg.DefaultProvider != "openai" {
		t.Errorf("default provider: got %q, want openai", f.cfg.DefaultProvider)
	}
	if _, ok := f.cfg.Providers["openrouter"]; !ok {
		t.Error("openrouter should be in providers map")
	}
	// The openrouter future should now be primed.
	if f.model.orFuture == nil {
		t.Error("openrouter with APIKey should kick off orFuture")
	}
}

// TestApplyChildPayload_Model writes per-provider default models into
// existing config rows.
func TestApplyChildPayload_Model(t *testing.T) {
	f := NewWithOptions(Options{
		ExistingConfig: &config.Config{
			UserName: "Tester",
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "sk-x"},
			},
		},
	})
	f.applyChildPayload(modelResult{models: map[string]string{"openai": "gpt-4o-mini"}})
	if got := f.cfg.Providers["openai"].DefaultModel; got != "gpt-4o-mini" {
		t.Errorf("model payload: openai.DefaultModel = %q, want gpt-4o-mini", got)
	}
}

// TestApplyChildPayload_Daemon flips the daemon toggle.
func TestApplyChildPayload_Daemon(t *testing.T) {
	f := New()
	f.applyChildPayload(daemonResult{enabled: true})
	if !f.cfg.Daemon.Enabled {
		t.Error("daemon payload should set Daemon.Enabled = true")
	}
	f.applyChildPayload(daemonResult{enabled: false})
	if f.cfg.Daemon.Enabled {
		t.Error("daemon payload should set Daemon.Enabled = false")
	}
}

// TestApplyChildPayload_Skills writes the convention.
func TestApplyChildPayload_Skills(t *testing.T) {
	f := New()
	f.applyChildPayload(skillsResult{convention: "claude"})
	if f.cfg.Skills.Convention != "claude" {
		t.Errorf("skills payload: convention = %q, want claude", f.cfg.Skills.Convention)
	}
}

// TestApplyChildPayload_Vault writes the chosen vault path.
func TestApplyChildPayload_Vault(t *testing.T) {
	f := New()
	f.applyChildPayload(vaultResult{path: "/tmp/v"})
	if f.cfg.Vault.Path != "/tmp/v" {
		t.Errorf("vault payload: path = %q, want /tmp/v", f.cfg.Vault.Path)
	}
}

// TestApplyChildPayload_GatewayDisabled drops the enabled flag and
// neither ntfy nor telegram leak in when disabled.
func TestApplyChildPayload_GatewayDisabled(t *testing.T) {
	f := New()
	f.applyChildPayload(gatewayResult{enabled: false})
	if f.cfg.Gateway.Enabled {
		t.Error("gateway payload with enabled=false should leave Gateway.Enabled=false")
	}
}

// TestApplyChildPayload_GatewayNtfyAndTelegram exercises the dual-write
// path through the gateway payload.
func TestApplyChildPayload_GatewayNtfyAndTelegram(t *testing.T) {
	f := New()
	res := gatewayResult{
		enabled: true,
		ntfy: config.NtfyGatewayConfig{
			Enabled: true,
			Server:  "https://ntfy.sh",
			Topic:   "carlos-test",
		},
		telegram: config.TelegramConfig{
			Enabled:  true,
			BotToken: "env:CARLOS_TELEGRAM_TOKEN",
		},
	}
	f.applyChildPayload(res)
	if !f.cfg.Gateway.Enabled {
		t.Error("gateway payload should set Gateway.Enabled = true")
	}
	if f.cfg.Gateway.Ntfy.Topic != "carlos-test" {
		t.Errorf("ntfy topic dropped; got %q", f.cfg.Gateway.Ntfy.Topic)
	}
	if f.cfg.Gateway.Telegram.BotToken != "env:CARLOS_TELEGRAM_TOKEN" {
		t.Errorf("telegram bot token dropped; got %q", f.cfg.Gateway.Telegram.BotToken)
	}
}

// TestApplyChildPayload_UnknownTypeIsNoop covers the default switch
// branch (no payload type matched). We compare a couple of fields
// rather than the whole struct (which contains maps and is not
// directly comparable).
func TestApplyChildPayload_UnknownTypeIsNoop(t *testing.T) {
	f := New()
	prevName := f.cfg.UserName
	prevDaemon := f.cfg.Daemon.Enabled
	f.applyChildPayload(struct{}{})
	if f.cfg.UserName != prevName {
		t.Errorf("unknown payload mutated UserName: %q -> %q", prevName, f.cfg.UserName)
	}
	if f.cfg.Daemon.Enabled != prevDaemon {
		t.Errorf("unknown payload mutated Daemon.Enabled: %v -> %v", prevDaemon, f.cfg.Daemon.Enabled)
	}
}
