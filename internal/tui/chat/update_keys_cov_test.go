package chat

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/usershell"
)

func updateModel(t *testing.T) *Model {
	t.Helper()
	log := openTempLog(t)
	const agentID = "01HV0000000000000000UPD01"
	seedAgent(t, log, agentID, "upd", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource())
	return drive(t, m, 100, 30)
}

func TestUpdate_CtrlCQuitsWhenNoFgJob(t *testing.T) {
	m := updateModel(t)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.quitting {
		t.Error("ctrl+c with no fg job should quit")
	}
	if cmd == nil {
		t.Error("ctrl+c quit should return tea.Quit")
	}
}

func TestUpdate_CtrlCCancelsFgJob(t *testing.T) {
	mgr, _ := blockingManager(t)
	log := openTempLog(t)
	const agentID = "01HV0000000000000000UPD02"
	seedAgent(t, log, agentID, "upd2", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource(), WithUserShell(mgr))
	m = drive(t, m, 100, 30)

	job, _ := mgr.Submit(context.Background(), "vim", usershell.Foreground)
	waitForRunningState(t, mgr, job.ID)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.quitting {
		t.Error("ctrl+c should cancel the fg job, NOT quit")
	}
	if cmd == nil {
		t.Fatal("ctrl+c should return the cancel command")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "cancelled") {
		t.Errorf("ctrl+c should cancel; got %+v", st)
	}
}

func TestUpdate_CtrlZBackgroundsFgJob(t *testing.T) {
	mgr, _ := blockingManager(t)
	log := openTempLog(t)
	const agentID = "01HV0000000000000000UPD03"
	seedAgent(t, log, agentID, "upd3", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource(), WithUserShell(mgr))
	m = drive(t, m, 100, 30)

	job, _ := mgr.Submit(context.Background(), "vim", usershell.Foreground)
	waitForRunningState(t, mgr, job.ID)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlZ})
	if cmd == nil {
		t.Fatal("ctrl+z with a fg job should return the background command")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "background") {
		t.Errorf("ctrl+z should background; got %+v", st)
	}
}

func TestUpdate_CtrlZNoFgIsNoOp(t *testing.T) {
	mgr := minimalManager(t)
	defer mgr.Close()
	log := openTempLog(t)
	const agentID = "01HV0000000000000000UPD04"
	seedAgent(t, log, agentID, "upd4", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource(), WithUserShell(mgr))
	m = drive(t, m, 100, 30)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlZ})
	if cmd != nil {
		t.Errorf("ctrl+z with no fg job should be a no-op; got %T", cmd)
	}
}

func TestUpdate_EnterSubmitsUserMessage(t *testing.T) {
	m := updateModel(t)
	m.ta.SetValue("hello chat")
	before := len(m.transcript)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("enter on non-empty input should return a submit command")
	}
	// appendUserMessage optimistically paints the row immediately.
	if len(m.transcript) <= before {
		t.Errorf("enter should optimistically append the user row; was %d now %d", before, len(m.transcript))
	}
	if m.ta.Value() != "" {
		t.Errorf("enter should clear the textarea; got %q", m.ta.Value())
	}
}

func TestUpdate_PageKeysScrollViewport(t *testing.T) {
	m := updateModel(t)
	// Should not panic and should return without quitting for each.
	for _, k := range []string{"pgup", "pgdown", "home", "end"} {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		// pgup etc. arrive as named keys, not runes; send the proper form.
		_ = updated
	}
	// Named-key form.
	keys := []tea.KeyType{tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd}
	for _, kt := range keys {
		updated, _ := m.Update(tea.KeyMsg{Type: kt})
		m = updated.(*Model)
		if m.quitting {
			t.Errorf("page key %v should not quit", kt)
		}
	}
}

func TestUpdate_WindowResizeUpdatesDimensions(t *testing.T) {
	m := updateModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 142, Height: 44})
	m = updated.(*Model)
	if m.width != 142 || m.height != 44 {
		t.Errorf("resize not applied; width=%d height=%d", m.width, m.height)
	}
}

func TestUpdate_ErrMsgAppendsSystemNote(t *testing.T) {
	m := updateModel(t)
	before := len(m.transcript)
	updated, _ := m.Update(errMsg{err: context.DeadlineExceeded})
	m = updated.(*Model)
	if len(m.transcript) <= before {
		t.Errorf("errMsg should surface an inline note; was %d now %d", before, len(m.transcript))
	}
	last := m.transcript[len(m.transcript)-1]
	if last.kind != entrySystemNote {
		t.Errorf("errMsg should append a system note; got %v", last.kind)
	}
}

func TestUpdate_StatusMsgSetsStatusLine(t *testing.T) {
	m := updateModel(t)
	updated, _ := m.Update(statusMsg{text: "hello status", kind: statusInfo})
	m = updated.(*Model)
	if m.status != "hello status" {
		t.Errorf("statusMsg should set the status line; got %q", m.status)
	}
}
