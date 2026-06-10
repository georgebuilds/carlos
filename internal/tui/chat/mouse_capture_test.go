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

// TestRenderFooter_ShiftDragHintAlwaysPresent is the
// discoverability regression test: the keybind row always carries
// a "shift+drag select" hint so users learn the universal terminal
// override (Ghostty, iTerm2, WezTerm, macOS Terminal all pass
// Shift+drag through any mouse-capture mode as a force-selection
// gesture). No state-tracking trailer — the hint is the same
// regardless of whether capture is on or off, because shift+drag
// works in both directions.
func TestRenderFooter_ShiftDragHintAlwaysPresent(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000MOUSE1"
	seedAgent(t, log, agentID, "mouse hint", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	for _, mouseOff := range []bool{false, true} {
		m.mouseOff = mouseOff
		out := m.renderFooter(120)
		if !strings.Contains(out, "shift+drag") {
			t.Errorf("mouseOff=%v: footer missing shift+drag hint:\n%s", mouseOff, out)
		}
		if !strings.Contains(out, "select") {
			t.Errorf("mouseOff=%v: footer should label the shift+drag affordance:\n%s", mouseOff, out)
		}
		// alt+m is intentionally NOT advertised in the footer — it
		// stays a working keybinding (the universal fallback for any
		// terminal that doesn't pass shift+drag through) but the
		// surface area is the shift+drag tip.
		if strings.Contains(out, "alt+m") {
			t.Errorf("mouseOff=%v: footer should no longer mention alt+m:\n%s", mouseOff, out)
		}
	}
}
