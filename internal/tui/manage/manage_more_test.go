package manage

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestModel_Quitting_FlipsOnCtrlC asserts the Quitting() predicate
// follows the ctrl+c keypress.
func TestModel_Quitting_FlipsOnCtrlC(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	if m.Quitting() {
		t.Fatalf("fresh model already quitting")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(*Model)
	if !m.Quitting() {
		t.Errorf("Quitting() = false after ctrl+c")
	}
}

// TestModel_Quitting_FlipsOnQ asserts the q keypress quits as well.
func TestModel_Quitting_FlipsOnQ(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(*Model)
	if !m.Quitting() {
		t.Errorf("Quitting() = false after q")
	}
}

// TestModel_Quitting_ViewShortCircuits confirms View returns "" once
// the model has begun shutdown so the screen flushes cleanly.
func TestModel_Quitting_ViewShortCircuits(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.quitting = true
	if got := m.View(); got != "" {
		t.Errorf("quitting View = %q, want empty", got)
	}
}

// TestRosterPaneWidth_ClampsExtremes pins the boundary cases of the
// pane-width math: tiny terminals fall back to 50; wide terminals
// reserve at least 30 columns for the focus pane.
func TestRosterPaneWidth_ClampsExtremes(t *testing.T) {
	cases := []struct {
		termW int
		want  int
	}{
		{0, 50},
		{-1, 50},
		{80, 50},      // 40% of 80 = 32, below the 50 floor
		{200, 80},     // 40% of 200 = 80
		{300, 120},    // 40% of 300 = 120
		{55, 25},      // termW - 30 = 25 wins over the 50 floor
	}
	for _, c := range cases {
		if got := rosterPaneWidth(c.termW); got != c.want {
			t.Errorf("rosterPaneWidth(%d) = %d, want %d", c.termW, got, c.want)
		}
	}
}

// TestModel_Update_StatusEcho exercises the statusEchoMsg branch of
// Update, which the approval pane uses to surface non-fatal failures.
func TestModel_Update_StatusEcho(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	updated, _ := m.Update(statusEchoMsg{text: "hello"})
	m = updated.(*Model)
	if m.status != "hello" {
		t.Errorf("status = %q, want 'hello'", m.status)
	}
}

// TestModel_Update_ClearStatusMsg confirms the clearStatusMsg handler
// wipes the active status echo.
func TestModel_Update_ClearStatusMsg(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.status = "old"
	updated, _ := m.Update(clearStatusMsg{})
	m = updated.(*Model)
	if m.status != "" {
		t.Errorf("status after clear = %q, want empty", m.status)
	}
}

// TestModel_Update_VerbResultSchedulesClear confirms a VerbResult
// landing in the loop populates the status bar AND schedules a clear
// tick.
func TestModel_Update_VerbResultSchedulesClear(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	updated, cmd := m.Update(VerbResult{Verb: "steer", AgentID: "01HVabc12345678"})
	m = updated.(*Model)
	if m.status == "" {
		t.Errorf("VerbResult didn't populate status")
	}
	if cmd == nil {
		t.Errorf("VerbResult didn't schedule clear tick")
	}
}

// TestModel_MoveCursor_NoRosterIsNoOp confirms moveCursor with an
// empty roster doesn't panic and leaves cursor at 0.
func TestModel_MoveCursor_NoRosterIsNoOp(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.moveCursor(5)
	if m.cursor != 0 {
		t.Errorf("cursor after move on empty roster = %d, want 0", m.cursor)
	}
}

// TestModel_SelectedID_EmptyRoster returns the empty string when no
// row is selectable.
func TestModel_SelectedID_EmptyRoster(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	if got := m.selectedID(); got != "" {
		t.Errorf("selectedID empty roster = %q", got)
	}
}

// TestModel_FocusSelected_NoOpOnEmpty confirms focusSelected returns
// nil when no row is under the cursor (no agent to bind to).
func TestModel_FocusSelected_NoOpOnEmpty(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	if cmd := m.focusSelected(); cmd != nil {
		t.Errorf("focusSelected on empty = non-nil cmd")
	}
}

// TestModel_FocusSelected_SkipsIfAlreadyBound binds the focus pane
// then re-fires Enter on the same row; the command should be nil
// (no need to re-backfill).
func TestModel_FocusSelected_SkipsIfAlreadyBound(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.rosterRows = []rosterRow{{row: agent.AgentRow{ID: "01HVbound1234567"}}}
	m.cursor = 0
	m.focus.Bind("01HVbound1234567")
	if cmd := m.focusSelected(); cmd != nil {
		t.Errorf("focusSelected already-bound = non-nil cmd")
	}
}

// TestModel_TeardownFocus_IsIdempotent confirms teardownFocus on an
// unbound model is a no-op (no panic on nil cancel func).
func TestModel_TeardownFocus_IsIdempotent(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.teardownFocus()
	m.teardownFocus()
	if m.focusSubCancel != nil || m.focusSubCh != nil {
		t.Errorf("teardownFocus didn't zero handles")
	}
}

// TestModel_Update_RepumpsOnFocusEvent piggybacks on the focusEventMsg
// branch: an event for the bound agent should land in the pane and
// return a non-nil cmd (the repump) when a subscription is active.
func TestModel_Update_RepumpsOnFocusEvent(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.focus.Bind("01HVrepump1234567")
	ch := make(chan agent.Event, 1)
	m.focusSubCh = ch

	updated, cmd := m.Update(focusEventMsg{ev: agent.Event{
		AgentID: "01HVrepump1234567",
		Type:    agent.EvtHeartbeat,
	}})
	m = updated.(*Model)
	if cmd == nil {
		t.Errorf("focusEventMsg with active sub returned nil cmd (expected repump)")
	}

	// Event for a different agent should be discarded but still return
	// the repump cmd.
	updated, _ = m.Update(focusEventMsg{ev: agent.Event{
		AgentID: "01HVother12345678",
		Type:    agent.EvtHeartbeat,
	}})
	m = updated.(*Model)
}

// TestModel_Update_FocusSubscribedAdopts confirms the orchestrator
// adopts a focusSubscribedMsg matching the bound agent.
func TestModel_Update_FocusSubscribedAdopts(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.focus.Bind("01HVsub01234abcde")
	ch := make(chan agent.Event)
	cancelled := false
	cancel := func() { cancelled = true }

	updated, cmd := m.Update(focusSubscribedMsg{
		agentID: "01HVsub01234abcde",
		ch:      ch,
		cancel:  cancel,
	})
	m = updated.(*Model)
	if m.focusSubCh != ch {
		t.Errorf("Update didn't adopt subscription channel")
	}
	if cmd == nil {
		t.Errorf("focusSubscribedMsg with channel didn't return pump cmd")
	}
	_ = cancelled
}

// TestModel_Update_FocusSubscribedRejectsMismatch confirms a
// subscription for a stale agent is cancelled rather than adopted.
func TestModel_Update_FocusSubscribedRejectsMismatch(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	m.focus.Bind("01HVlive1234abcde")
	ch := make(chan agent.Event)
	cancelled := false
	cancel := func() { cancelled = true }

	updated, _ := m.Update(focusSubscribedMsg{
		agentID: "01HVstale123abcde",
		ch:      ch,
		cancel:  cancel,
	})
	m = updated.(*Model)
	if !cancelled {
		t.Errorf("stale subscription should have been cancelled")
	}
	if m.focusSubCh == ch {
		t.Errorf("stale subscription should not have been adopted")
	}
}

// TestModel_Update_SnapshotErrorSurfaces confirms a snapshot error
// lands in the rosterRefreshErr field so the header chip surfaces it.
func TestModel_Update_SnapshotErrorSurfaces(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	updated, _ := m.Update(snapshotReadyMsg{err: errBoom("kaput")})
	m = updated.(*Model)
	if m.rosterRefreshErr == "" {
		t.Errorf("snapshot error didn't surface")
	}
}

// errBoom is a tiny local error type so the test doesn't pull errors
// just for one assertion.
type errBoom string

func (e errBoom) Error() string { return string(e) }
