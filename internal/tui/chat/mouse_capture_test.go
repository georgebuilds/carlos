package chat

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestMouseCapture_AltMTogglesState confirms the Alt+M toggle still
// works after the default flip: starting OFF (post-flip default), one
// Alt+M turns capture ON (wheel-scroll), a second Alt+M turns it back
// OFF (release for text selection).
func TestMouseCapture_AltMTogglesState(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000MOUSE0"
	seedAgent(t, log, agentID, "mouse toggle", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	// Simulate the Run-time default: capture starts OFF for
	// out-of-the-box text selection. (Run() sets m.mouseOff = true
	// before launching the program; we re-create that condition here
	// since drive() goes around Run().)
	m.mouseOff = true

	// First Alt+M turns capture ON.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
	m = updated.(*Model)
	if m.mouseOff {
		t.Errorf("first alt+m should turn capture ON; mouseOff still true")
	}
	if cmd == nil {
		t.Errorf("expected tea.EnableMouseCellMotion cmd; got nil")
	}

	// Second Alt+M turns capture OFF.
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
	m = updated.(*Model)
	if !m.mouseOff {
		t.Errorf("second alt+m should turn capture OFF; mouseOff = false")
	}
	if cmd == nil {
		t.Errorf("expected tea.DisableMouse cmd; got nil")
	}
}

// TestRenderFooter_HintShownWhenMouseCaptureOn is the discoverability
// regression test for the default flip. When the user has Alt+M'd
// capture ON (the unusual state, post-flip), the footer surfaces a
// dim line telling them how to release it for text selection.
// Otherwise users could end up stuck wondering why their text won't
// select without knowing the toggle exists.
func TestRenderFooter_HintShownWhenMouseCaptureOn(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000MOUSE1"
	seedAgent(t, log, agentID, "mouse hint", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	// Capture ON state.
	m.mouseOff = false
	out := m.renderFooter(120)
	if !strings.Contains(out, "alt+m") {
		t.Errorf("footer should mention alt+m hint when capture is on; got:\n%s", out)
	}
	if !strings.Contains(out, "text selection") {
		t.Errorf("footer should explain the toggle's effect; got:\n%s", out)
	}
}

// TestRenderFooter_NoHintWhenMouseCaptureOff guards the inverse:
// when capture is OFF (the default), the footer must NOT carry the
// mouse-capture hint — that's the expected state and adding a hint
// would just clutter every user's screen.
func TestRenderFooter_NoHintWhenMouseCaptureOff(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000MOUSE2"
	seedAgent(t, log, agentID, "mouse no hint", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.mouseOff = true
	out := m.renderFooter(120)
	if strings.Contains(out, "mouse capture") {
		t.Errorf("footer should not show capture hint when capture is off; got:\n%s", out)
	}
}
