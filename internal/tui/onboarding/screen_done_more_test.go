package onboarding

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestDoneModel_Init returns nil (no work to do; the screen is purely
// a confirmation that exits on any key).
func TestDoneModel_Init(t *testing.T) {
	m := newDoneModel()
	if cmd := m.Init(); cmd != nil {
		t.Errorf("doneModel.Init() should be nil, got %v", cmd)
	}
}

// TestDoneModel_UpdateAnyKeyQuits proves any key press triggers the
// quit message via the returned cmd.
func TestDoneModel_UpdateAnyKeyQuits(t *testing.T) {
	m := newDoneModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("doneModel.Update on KeyMsg should return a cmd")
	}
	msg := cmd()
	if _, ok := msg.(quitMsg); !ok {
		t.Errorf("doneModel.Update key cmd should emit quitMsg, got %T", msg)
	}
}

// TestDoneModel_UpdateNonKeyIsNoop verifies non-key messages don't
// trigger a quit (so a stray tick won't blow the user out).
func TestDoneModel_UpdateNonKeyIsNoop(t *testing.T) {
	m := newDoneModel()
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if cmd != nil {
		t.Errorf("doneModel.Update on non-key should return nil cmd, got %v", cmd)
	}
}

// TestDoneModel_ViewIsEmpty pins the contract that View() returns "" -
// Flow.renderRightPane calls renderName directly so View() is unused.
func TestDoneModel_ViewIsEmpty(t *testing.T) {
	m := newDoneModel()
	if got := m.View(); got != "" {
		t.Errorf("doneModel.View() should be empty, got %q", got)
	}
}
