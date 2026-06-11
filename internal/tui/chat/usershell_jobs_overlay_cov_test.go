package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/usershell"
)

// jobsModel builds a model with a real usershell manager and the jobs
// overlay open, plus N running background jobs so the cursor has rows
// to walk.
func jobsModel(t *testing.T, bgJobs int) *Model {
	t.Helper()
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	t.Cleanup(func() { m.usershell.Close() })
	m.showJobs = true
	for i := 0; i < bgJobs; i++ {
		job, err := m.usershell.Submit(context.Background(), "sleep 5", usershell.Background)
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		waitForRunning(t, m.usershell, job.ID)
	}
	return m
}

func TestJobsOverlay_DownUpClamp(t *testing.T) {
	m := jobsModel(t, 3)
	// Down past the end clamps at len-1 == 2.
	for i := 0; i < 10; i++ {
		_, _, handled := m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		if !handled {
			t.Fatal("j should be handled by the overlay")
		}
	}
	if m.jobsCursor != 2 {
		t.Errorf("cursor should clamp at 2; got %d", m.jobsCursor)
	}
	// Up past the top clamps at 0.
	for i := 0; i < 10; i++ {
		_, _, _ = m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	}
	if m.jobsCursor != 0 {
		t.Errorf("cursor should clamp at 0; got %d", m.jobsCursor)
	}
}

func TestJobsOverlay_GAndShiftGJump(t *testing.T) {
	m := jobsModel(t, 3)
	_, _, _ = m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if m.jobsCursor != 2 {
		t.Errorf("G should jump to last row (2); got %d", m.jobsCursor)
	}
	_, _, _ = m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.jobsCursor != 0 {
		t.Errorf("g should jump to first row; got %d", m.jobsCursor)
	}
}

func TestJobsOverlay_EscClosesAndResets(t *testing.T) {
	m := jobsModel(t, 1)
	m.jobsFilter = "x"
	m.jobsCursor = 0
	_, _, handled := m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !handled {
		t.Fatal("esc should be handled")
	}
	if m.showJobs || m.jobsFilter != "" || m.jobsFilterMode {
		t.Errorf("esc should close + reset; showJobs=%v filter=%q", m.showJobs, m.jobsFilter)
	}
}

func TestJobsOverlay_CtrlCFallsThrough(t *testing.T) {
	m := jobsModel(t, 1)
	_, _, handled := m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if handled {
		t.Error("ctrl+c must fall through so the user can still quit")
	}
}

func TestJobsOverlay_FilterModeTyping(t *testing.T) {
	m := jobsModel(t, 1)
	// '/' enters filter mode.
	_, _, _ = m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !m.jobsFilterMode {
		t.Fatal("'/' should enter filter mode")
	}
	// Type "sl" then space then "e".
	_, _, _ = m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sl")})
	_, _, _ = m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeySpace})
	_, _, _ = m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if m.jobsFilter != "sl e" {
		t.Errorf("filter buffer = %q want 'sl e'", m.jobsFilter)
	}
	// Backspace trims one rune.
	_, _, _ = m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.jobsFilter != "sl " {
		t.Errorf("backspace failed; got %q", m.jobsFilter)
	}
	// Esc exits filter mode WITHOUT closing the overlay.
	_, _, _ = m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.jobsFilterMode {
		t.Error("esc in filter mode should leave filter mode")
	}
	if !m.showJobs {
		t.Error("esc in filter mode must NOT close the overlay")
	}
}

func TestJobsOverlay_FilterEnterCommits(t *testing.T) {
	m := jobsModel(t, 1)
	m.jobsFilterMode = true
	_, cmd, handled := m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled {
		t.Fatal("enter in filter mode should be handled")
	}
	if m.jobsFilterMode {
		t.Error("enter should exit filter mode")
	}
	// commit emits a status command for the highlighted bg job.
	if cmd == nil {
		t.Fatal("commit should issue a status command")
	}
	if _, ok := cmd().(statusMsg); !ok {
		t.Error("commit command should produce a statusMsg")
	}
}

func TestJobsOverlay_UnknownKeySwallowed(t *testing.T) {
	m := jobsModel(t, 1)
	_, _, handled := m.handleJobsOverlayKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Z")})
	if !handled {
		t.Error("overlay should swallow unknown keys so they don't reach the textarea")
	}
}

func TestCommitJobsOverlay_EmptyIsNoOp(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.showJobs = true
	_, cmd, handled := m.commitJobsOverlay()
	if !handled || cmd != nil {
		t.Errorf("empty job list commit should be a no-op; cmd=%v", cmd)
	}
}

func TestCommitJobsOverlay_BackgroundForegrounds(t *testing.T) {
	m := jobsModel(t, 1)
	_, cmd, _ := m.commitJobsOverlay()
	if cmd == nil {
		t.Fatal("commit on a bg job should issue a command")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "foreground") {
		t.Errorf("bg commit should report foreground; got %+v", st)
	}
}

func TestCancelHighlightedJob_RunningJob(t *testing.T) {
	m := jobsModel(t, 1)
	_, cmd, handled := m.cancelHighlightedJob()
	if !handled || cmd == nil {
		t.Fatal("cancel on a running job should issue a command")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "cancelled") {
		t.Errorf("cancel should report cancellation; got %+v", st)
	}
}

func TestCancelHighlightedJob_EmptyIsNoOp(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.showJobs = true
	_, cmd, handled := m.cancelHighlightedJob()
	if !handled || cmd != nil {
		t.Errorf("cancel with no rows should be a no-op; cmd=%v", cmd)
	}
}

func TestCancelHighlightedJob_TerminalIsNoOp(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.showJobs = true
	// Run a fast job to completion so it lands in a terminal state.
	job, err := m.usershell.Submit(context.Background(), "true", usershell.Background)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTerminal(t, m.usershell, job.ID)
	_, cmd, handled := m.cancelHighlightedJob()
	if !handled || cmd == nil {
		t.Fatal("cancel on terminal job should still emit an informational status")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "already complete") {
		t.Errorf("terminal cancel should report already-complete; got %+v", st)
	}
}

func TestJobsOverlay_CtrlJTogglesViaUpdate(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000JOBS1"
	seedAgent(t, log, agentID, "jobs", "claude-4.7-sonnet")
	mgr := minimalManager(t)
	defer mgr.Close()
	m := New(log, agentID, NewMemTextSource(), WithUserShell(mgr))
	m = drive(t, m, 100, 30)

	if m.showJobs {
		t.Fatal("precondition: jobs overlay starts closed")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = updated.(*Model)
	if !m.showJobs {
		t.Error("ctrl+j should open the jobs overlay")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = updated.(*Model)
	if m.showJobs {
		t.Error("ctrl+j again should close the jobs overlay")
	}
}

// waitForTerminal polls until the job reaches a terminal state.
func waitForTerminal(t *testing.T, mgr *usershell.Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := mgr.Get(id)
		if err == nil && s.State.IsTerminal() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never reached terminal state", id)
}
