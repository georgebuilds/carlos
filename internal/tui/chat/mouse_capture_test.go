package chat

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestMouseCapture_AltMTogglesState confirms the Alt+M toggle still
// works: starting in the default (capture ON for trackpad/wheel
// scroll), one Alt+M releases capture so the terminal can do
// text selection; a second Alt+M restores capture for scroll.
func TestMouseCapture_AltMTogglesState(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000MOUSE0"
	seedAgent(t, log, agentID, "mouse toggle", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	// Default: capture ON. (Run() leaves m.mouseOff at its zero
	// value (false) so the bubbletea program starts with
	// WithMouseCellMotion.)

	// First Alt+M releases capture (mouseOff = true).
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
	m = updated.(*Model)
	if !m.mouseOff {
		t.Errorf("first alt+m should release capture; mouseOff still false")
	}
	if cmd == nil {
		t.Errorf("expected tea.DisableMouse cmd; got nil")
	}

	// Second Alt+M restores capture (mouseOff = false).
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
	m = updated.(*Model)
	if m.mouseOff {
		t.Errorf("second alt+m should restore capture; mouseOff still true")
	}
	if cmd == nil {
		t.Errorf("expected tea.EnableMouseCellMotion cmd; got nil")
	}
}

// TestRenderFooter_AltMHintAlwaysPresent is the discoverability
// regression test: the keybind row always carries an "alt+m" hint
// (with a label that tracks the current state) so users discover
// the toggle without having to read docs or stumble onto it.
func TestRenderFooter_AltMHintAlwaysPresent(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000MOUSE1"
	seedAgent(t, log, agentID, "mouse hint", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	for _, state := range []struct {
		mouseOff bool
		want     string
	}{
		{mouseOff: false, want: "select"}, // capture on → press alt+m to select
		{mouseOff: true, want: "scroll"},  // capture off → press alt+m to scroll
	} {
		m.mouseOff = state.mouseOff
		out := m.renderFooter(120)
		if !strings.Contains(out, "alt+m") {
			t.Errorf("mouseOff=%v: footer missing alt+m hint:\n%s", state.mouseOff, out)
		}
		if !strings.Contains(out, state.want) {
			t.Errorf("mouseOff=%v: footer hint label should mention %q:\n%s",
				state.mouseOff, state.want, out)
		}
	}
}

// TestMouseHintLabel_LabelTracksState pins the pure label helper so
// the keymap and the visible label can't drift apart in a future
// refactor.
func TestMouseHintLabel_LabelTracksState(t *testing.T) {
	if got := mouseHintLabel(false); !strings.Contains(got, "select") {
		t.Errorf("capture on (mouseOff=false) → hint should say 'select'; got %q", got)
	}
	if got := mouseHintLabel(true); !strings.Contains(got, "scroll") {
		t.Errorf("capture off (mouseOff=true) → hint should say 'scroll'; got %q", got)
	}
}
