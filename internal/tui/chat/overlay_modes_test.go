package chat

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/frame"
)

// openModes wires a framed model with the given mode + SwitchMode hook
// and snaps the takeover open with the cursor on the active mode.
func openModes(t *testing.T, mode string, switchFn func(string) error) *Model {
	t.Helper()
	m := newFramedModel(t, FrameUI{
		Active:     "work",
		Glyph:      "▣",
		Accent:     "rust",
		Available:  []string{"personal", "work"},
		Mode:       mode,
		SwitchMode: switchFn,
	})
	m.openModeSwitcher()
	return m
}

func TestModeIndexFromName(t *testing.T) {
	cases := map[string]int{
		frame.ModeTight:        0,
		frame.ModeSolo:         1,
		frame.ModeOrchestrator: 2,
		"":                     1, // unknown falls to solo
		"bogus":                1,
	}
	for in, want := range cases {
		if got := modeIndexFromName(in); got != want {
			t.Errorf("modeIndexFromName(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestRenderModeSwitcher_ContainsAllThreeCards(t *testing.T) {
	ui := FrameUI{Active: "work", Mode: frame.ModeSolo}
	out := renderModeSwitcher(ui, 1, 120, 30, false)
	for _, want := range []string{"TIGHT", "SOLO", "ORCHESTRATOR"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing card title %q in output:\n%s", want, out)
		}
	}
	for _, want := range []string{
		"strictly single-thread",
		"the lone wanderer",
		"subagent-forward",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing tagline %q in output:\n%s", want, out)
		}
	}
}

func TestRenderModeSwitcher_ContainsArtForEachCard(t *testing.T) {
	ui := FrameUI{Active: "work", Mode: frame.ModeSolo}
	out := renderModeSwitcher(ui, 1, 120, 30, false)
	// One distinctive line from each art set.
	for _, want := range []string{
		"focus", // tight: "┌──[ focus ]──┐"
		"▣",     // orchestrator root node
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected art fragment %q in output:\n%s", want, out)
		}
	}
}

func TestRenderModeSwitcher_HeaderShowsCurrent(t *testing.T) {
	ui := FrameUI{Active: "work", Mode: frame.ModeOrchestrator}
	out := renderModeSwitcher(ui, 2, 120, 30, false)
	if !strings.Contains(out, "current: "+frame.ModeOrchestrator) {
		t.Errorf("header should echo current mode; got:\n%s", out)
	}
}

func TestRenderModeSwitcher_EmptyModeFallsBackToSolo(t *testing.T) {
	// FrameUI.Mode == "" must be treated the same as solo for both the
	// header echo and the active highlight - mirrors EffectiveMode in
	// internal/frame.
	ui := FrameUI{Active: "work", Mode: ""}
	out := renderModeSwitcher(ui, 1, 120, 30, false)
	if !strings.Contains(out, "current: "+frame.ModeSolo) {
		t.Errorf("empty mode should echo solo; got:\n%s", out)
	}
}

func TestRenderModeSwitcher_HelpFooterToggles(t *testing.T) {
	ui := FrameUI{Active: "work", Mode: frame.ModeSolo}
	base := renderModeSwitcher(ui, 1, 120, 30, false)
	if strings.Contains(base, "1-3") {
		t.Errorf("default footer should not show full help; got:\n%s", base)
	}
	help := renderModeSwitcher(ui, 1, 120, 30, true)
	if !strings.Contains(help, "1-3") {
		t.Errorf("help footer should mention number-jump; got:\n%s", help)
	}
}

func TestModeSwitcher_OpenSnapsToActive(t *testing.T) {
	m := openModes(t, frame.ModeOrchestrator, nil)
	if !m.showModeSwitcher {
		t.Fatal("openModeSwitcher should flip showModeSwitcher")
	}
	if m.modeSwitcherCursor != 2 {
		t.Errorf("cursor = %d, want 2 (orchestrator)", m.modeSwitcherCursor)
	}
}

func TestModeSwitcher_LeftRightNavClamps(t *testing.T) {
	m := openModes(t, frame.ModeSolo, nil)
	// solo is the middle; right → orchestrator
	m.handleModeSwitcherKey(key("right"))
	if m.modeSwitcherCursor != 2 {
		t.Errorf("after right, cursor = %d, want 2", m.modeSwitcherCursor)
	}
	// Further right is a no-op (no wrap).
	m.handleModeSwitcherKey(key("right"))
	if m.modeSwitcherCursor != 2 {
		t.Errorf("right at right edge should clamp; got %d", m.modeSwitcherCursor)
	}
	// Two lefts back to tight.
	m.handleModeSwitcherKey(key("left"))
	m.handleModeSwitcherKey(key("left"))
	if m.modeSwitcherCursor != 0 {
		t.Errorf("after two lefts, cursor = %d, want 0", m.modeSwitcherCursor)
	}
	// Further left clamps.
	m.handleModeSwitcherKey(key("left"))
	if m.modeSwitcherCursor != 0 {
		t.Errorf("left at left edge should clamp; got %d", m.modeSwitcherCursor)
	}
}

func TestModeSwitcher_NumberJumps(t *testing.T) {
	m := openModes(t, frame.ModeSolo, nil)
	m.handleModeSwitcherKey(key("3"))
	if m.modeSwitcherCursor != 2 {
		t.Errorf("3 should jump to orchestrator; got %d", m.modeSwitcherCursor)
	}
	m.handleModeSwitcherKey(key("1"))
	if m.modeSwitcherCursor != 0 {
		t.Errorf("1 should jump to tight; got %d", m.modeSwitcherCursor)
	}
}

func TestModeSwitcher_EscCloses(t *testing.T) {
	m := openModes(t, frame.ModeSolo, nil)
	m.handleModeSwitcherKey(key("esc"))
	if m.showModeSwitcher {
		t.Error("esc should close the switcher")
	}
}

func TestModeSwitcher_HelpToggle(t *testing.T) {
	m := openModes(t, frame.ModeSolo, nil)
	if m.modeSwitcherHelp {
		t.Fatal("help should start off")
	}
	m.handleModeSwitcherKey(key("?"))
	if !m.modeSwitcherHelp {
		t.Error("? should toggle help on")
	}
	m.handleModeSwitcherKey(key("?"))
	if m.modeSwitcherHelp {
		t.Error("? should toggle help off")
	}
}

func TestModeSwitcher_EnterSwitchesMode(t *testing.T) {
	var switched string
	m := openModes(t, frame.ModeSolo, func(mode string) error {
		switched = mode
		return nil
	})
	m.handleModeSwitcherKey(key("right")) // → orchestrator
	_, cmd, _ := m.handleModeSwitcherKey(key("enter"))
	if switched != frame.ModeOrchestrator {
		t.Errorf("SwitchMode hook got %q, want %q", switched, frame.ModeOrchestrator)
	}
	if m.frame.Mode != frame.ModeOrchestrator {
		t.Errorf("Model.frame.Mode = %q, want %q", m.frame.Mode, frame.ModeOrchestrator)
	}
	if m.showModeSwitcher {
		t.Error("enter should close the switcher")
	}
	s := runStatusCmd(t, cmd)
	if !strings.Contains(s.text, frame.ModeOrchestrator) {
		t.Errorf("status echo should mention new mode; got %q", s.text)
	}
}

func TestModeSwitcher_EnterOnActiveModeIsNoOp(t *testing.T) {
	// Enter on the already-active card should report "already <mode>"
	// without calling the SwitchMode hook (mirrors modeSlash semantics).
	called := false
	m := openModes(t, frame.ModeSolo, func(mode string) error {
		called = true
		return nil
	})
	// Cursor already on solo (index 1) from openModes.
	_, cmd, _ := m.handleModeSwitcherKey(key("enter"))
	if called {
		t.Error("SwitchMode should not fire when picking the active mode")
	}
	s := runStatusCmd(t, cmd)
	if !strings.Contains(s.text, "already") {
		t.Errorf("status echo should say 'already'; got %q", s.text)
	}
}

func TestModeSwitcher_EnterReportsSwitchFailure(t *testing.T) {
	m := openModes(t, frame.ModeSolo, func(mode string) error {
		return errors.New("disk full")
	})
	m.handleModeSwitcherKey(key("right"))
	_, cmd, _ := m.handleModeSwitcherKey(key("enter"))
	s := runStatusCmd(t, cmd)
	if s.kind != statusWarn {
		t.Errorf("failure should be statusWarn; got %v", s.kind)
	}
	if !strings.Contains(s.text, "disk full") {
		t.Errorf("status text should surface error; got %q", s.text)
	}
	if m.frame.Mode != frame.ModeSolo {
		t.Errorf("frame.Mode should be unchanged on failure; got %q", m.frame.Mode)
	}
}

func TestModeSwitcher_EnterWithoutHookEchoesNotWired(t *testing.T) {
	m := openModes(t, frame.ModeSolo, nil)
	m.handleModeSwitcherKey(key("right"))
	_, cmd, _ := m.handleModeSwitcherKey(key("enter"))
	s := runStatusCmd(t, cmd)
	if s.kind != statusWarn || !strings.Contains(s.text, "not wired") {
		t.Errorf("expected 'not wired' warn; got %v %q", s.kind, s.text)
	}
}

// TestCtrlO_OpensModeSwitcher pins the chat.Update wiring: a ctrl+o
// keypress on the top-level Update path flips showModeSwitcher when a
// frame is wired. The case label is "ctrl+o" - chosen because ctrl+m
// would shadow Enter (KeyCtrlM == keyCR in Bubbletea).
func TestCtrlO_OpensModeSwitcher(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:    "work",
		Available: []string{"personal", "work"},
		Mode:      frame.ModeSolo,
	})
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	mm := out.(*Model)
	if !mm.showModeSwitcher {
		t.Error("ctrl+o should open the mode switcher")
	}
}

func TestCtrlO_NoOpWhenFramesUnwired(t *testing.T) {
	// Legacy single-shelf mode (frame.Active == "") should not open
	// the picker - same gating as ctrl+f.
	m := newFramedModel(t, FrameUI{})
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	mm := out.(*Model)
	if mm.showModeSwitcher {
		t.Error("ctrl+o should be a no-op without a wired frame")
	}
}
