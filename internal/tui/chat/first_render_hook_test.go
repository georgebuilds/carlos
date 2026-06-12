package chat

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// WithFirstRenderHook must fire the hook exactly once - on the first
// View() call - and never again on subsequent renders. cmd/carlos wires
// the slice-9f boot trace's first_frame checkpoint through this, and a
// double fire would double-print the trace line onto the alt screen.
func TestWithFirstRenderHook_FiresExactlyOnceOnFirstView(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000FRAME"
	seedAgent(t, log, agentID, "first-render", "claude-4.7-sonnet")

	fired := 0
	m := New(log, agentID, NewMemTextSource(), WithFirstRenderHook(func() { fired++ }))
	m = drive(t, m, 100, 30)

	if fired != 1 {
		// drive() pumps Update msgs; the hook must have fired on the
		// View call(s) it triggers - exactly once.
		_ = m.View()
		if fired != 1 {
			t.Fatalf("hook fired %d times, want exactly 1", fired)
		}
	}
	_ = m.View()
	_ = m.View()
	if fired != 1 {
		t.Fatalf("hook re-fired on later Views: %d times total, want 1", fired)
	}
	if m.firstRenderHook != nil {
		t.Error("hook must be consumed (nil) after firing")
	}
}

// The hook must fire even on the degenerate early-return View branches
// (tiny terminal), since the renderer still paints that output as the
// first frame.
func TestWithFirstRenderHook_FiresOnTinyTerminalBranch(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000TINY01"
	seedAgent(t, log, agentID, "tiny", "claude-4.7-sonnet")

	fired := 0
	m := New(log, agentID, NewMemTextSource(), WithFirstRenderHook(func() { fired++ }))
	// Below minTermWidth/minTermHeight: View returns the "needs at
	// least WxH" message - still a frame.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 10, Height: 4})
	m = updated.(*Model)
	_ = m.View()
	if fired != 1 {
		t.Fatalf("hook fired %d times on tiny-terminal View, want 1", fired)
	}
}

// A Model without the option (or with a nil fn) renders unchanged - the
// default path must stay a single nil-check.
func TestWithFirstRenderHook_NilIsNoOp(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000NIL001"
	seedAgent(t, log, agentID, "nil-hook", "claude-4.7-sonnet")

	bare := New(log, agentID, NewMemTextSource())
	if bare.firstRenderHook != nil {
		t.Fatal("default Model must have no hook")
	}
	_ = bare.View() // must not panic

	withNil := New(log, agentID, NewMemTextSource(), WithFirstRenderHook(nil))
	_ = withNil.View() // must not panic
}
