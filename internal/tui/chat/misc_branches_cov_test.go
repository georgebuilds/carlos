package chat

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/usershell"
)

// TestHandleResumeKey_CtrlCFallsThrough covers the ctrl+c branch that
// returns handled=false so the global quit path can fire.
func TestHandleResumeKey_CtrlCFallsThrough(t *testing.T) {
	m := &Model{showResume: true, resumeSessions: []resumeSession{{ID: "a"}}}
	_, _, handled := m.handleResumeKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if handled {
		t.Error("ctrl+c should fall through (handled=false) so the quit path runs")
	}
}

// TestHandleResumeKey_EmptyListNavIsNoOp covers the empty-list up/down
// guards.
func TestHandleResumeKey_EmptyListNavIsNoOp(t *testing.T) {
	m := &Model{showResume: true}
	if _, _, handled := m.handleResumeKey(tea.KeyMsg{Type: tea.KeyUp}); !handled {
		t.Error("up on an empty list should still be handled")
	}
	if _, _, handled := m.handleResumeKey(tea.KeyMsg{Type: tea.KeyDown}); !handled {
		t.Error("down on an empty list should still be handled")
	}
	if m.resumeCursor != 0 {
		t.Errorf("empty-list nav should leave the cursor at 0; got %d", m.resumeCursor)
	}
}

// TestHandleResumeKey_EnterEmptyClosesPicker covers the enter branch
// that closes the picker when there's nothing to pick.
func TestHandleResumeKey_EnterEmptyClosesPicker(t *testing.T) {
	m := &Model{showResume: true}
	_, _, handled := m.handleResumeKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled {
		t.Error("enter on an empty picker should be handled")
	}
	if m.showResume {
		t.Error("enter on an empty picker should close it")
	}
	if m.resumeSelected != "" {
		t.Error("enter on an empty picker must not commit a pick")
	}
}

// TestCommitJobsOverlay_RunningFgIsAlreadyForeground covers the
// jobsSectionRunning commit branch.
func TestCommitJobsOverlay_RunningFgIsAlreadyForeground(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.showJobs = true
	job, err := m.usershell.Submit(context.Background(), "sleep 30", usershell.Foreground)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForRunning(t, m.usershell, job.ID)
	m.jobsCursor = 0
	_, cmd, handled := m.commitJobsOverlay()
	if !handled || cmd == nil {
		t.Fatal("commit on a running fg job should echo a status")
	}
	st := cmd().(statusMsg)
	if !strings.Contains(st.text, "already foreground") {
		t.Errorf("running fg commit should report already-foreground; got %q", st.text)
	}
}

// TestModeSlash_SwitchHookErrorEchoesWarn covers the SwitchMode-error
// branch of modeSlash.
func TestModeSlash_SwitchHookErrorEchoesWarn(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:     "work",
		Available:  []string{"work"},
		Mode:       "solo",
		SwitchMode: func(string) error { return fakeErr("config locked") },
	})
	cmd := m.modeSlash("tight")
	if cmd == nil {
		t.Fatal("expected an error echo cmd")
	}
	st := cmd().(statusMsg)
	if !strings.Contains(st.text, "config locked") {
		t.Errorf("mode switch failure should surface the error; got %q", st.text)
	}
	if m.frame.Mode != "solo" {
		t.Errorf("mode should not change on hook error; got %q", m.frame.Mode)
	}
}

// TestResolveApproval_NilPendingIsNoOp covers the defensive guard in
// resolveApproval (no pending request, no approver).
func TestResolveApproval_NilPendingIsNoOp(t *testing.T) {
	m := newTestModel(t)
	if cmd := m.resolveApproval(ApprovalAllow); cmd != nil {
		t.Errorf("resolveApproval with no pending request should be a no-op; got %T", cmd)
	}
}

// TestRepumpCmd_NilChannelReturnsNil covers the subCh==nil guard in
// repumpCmd; the wired path is exercised by the subscription tests.
func TestRepumpCmd_NilChannelReturnsNil(t *testing.T) {
	m := newTestModel(t)
	m.subCh = nil
	if cmd := m.repumpCmd(); cmd != nil {
		t.Errorf("repumpCmd with no subscription should return nil; got %T", cmd)
	}
}

// TestPermsTab_StringExhaustive covers the permsTab.String() default
// branch (an out-of-range tab returns "").
func TestPermsTab_StringExhaustive(t *testing.T) {
	if got := permsTabBuiltin.String(); got != "Built-in" {
		t.Errorf("builtin tab label = %q", got)
	}
	if got := permsTabWorkspace.String(); got != "Workspace" {
		t.Errorf("workspace tab label = %q", got)
	}
	// permsTabCount is the sentinel past the real tabs; default => "".
	if got := permsTabCount.String(); got != "" {
		t.Errorf("out-of-range tab should stringify empty; got %q", got)
	}
}

// TestNewFrame_BackspaceEmptyNameNoOp covers the len(runes)==0 guard in
// newFrameBackspace (backspace on an empty name field is inert).
func TestNewFrame_BackspaceEmptyNameNoOp(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openNewFrameWizard("")
	if m.newFrame.Name != "" {
		t.Fatal("expected empty name on a no-prefill open")
	}
	m.newFrameBackspace()
	if m.newFrame.Name != "" {
		t.Errorf("backspace on empty name should stay empty; got %q", m.newFrame.Name)
	}
}

// TestNewFrame_BackspaceGlyphClearsAndPinsEdit covers the glyph-field
// branch of newFrameBackspace: it zeroes the glyph and flips the
// "glyph edited" flag so name edits stop auto-tracking.
func TestNewFrame_BackspaceGlyphClearsAndPinsEdit(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openNewFrameWizard("work")
	// Move focus to the glyph field.
	m.handleNewFrameKey(namedKey(tea.KeyTab))
	if m.newFrameField != newFrameFieldGlyph {
		t.Fatalf("expected glyph field after one tab; got %d", m.newFrameField)
	}
	m.newFrameBackspace()
	if m.newFrame.Glyph != "" {
		t.Errorf("glyph backspace should clear the glyph; got %q", m.newFrame.Glyph)
	}
	if !m.newFrameGlyphEd {
		t.Error("glyph backspace should pin the glyph-edited flag")
	}
}

// TestNewFrameToggleText_TemplateWiredShowsCopy covers the
// PersonalTemplate != nil branch of newFrameToggleText (the "copy
// personal" option is offered as active rather than "(not wired)").
func TestNewFrameToggleText_TemplateWiredShowsCopy(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, func() frame.Frame {
		return frame.Frame{Provider: "anthropic"}
	})
	m.openNewFrameWizard("")
	out := newFrameToggleText(m)
	if !strings.Contains(out, "copy personal") {
		t.Errorf("template-wired toggle should offer copy personal; got %q", out)
	}
	if strings.Contains(out, "not wired") {
		t.Errorf("template-wired toggle should NOT say 'not wired'; got %q", out)
	}
}

// TestNewFrameToggleText_TemplateUnwiredShowsNotWired covers the nil
// PersonalTemplate branch.
func TestNewFrameToggleText_TemplateUnwiredShowsNotWired(t *testing.T) {
	m := newWizardModel(t, []string{"personal"}, func(frame.Frame) error { return nil }, nil)
	m.openNewFrameWizard("")
	out := newFrameToggleText(m)
	if !strings.Contains(out, "not wired") {
		t.Errorf("unwired toggle should say 'not wired'; got %q", out)
	}
}

// TestSubmitUserShellCmd_SuccessRecordsHistory drives the happy path of
// submitUserShellCmd with a real manager AND a wired shell history: the
// command is recorded (Add + Reset) and the returned cmd reports the
// queued job.
func TestSubmitUserShellCmd_SuccessRecordsHistory(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	hist := usershell.NewHistory(t.TempDir() + "/sh-history")
	m.shellHistory = hist

	cmd := m.submitUserShellCmd("echo hello", usershell.Foreground)
	if cmd == nil {
		t.Fatal("expected a submit cmd")
	}
	st, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg; got %T", cmd())
	}
	if !strings.Contains(st.text, "queued") || !strings.Contains(st.text, "echo hello") {
		t.Errorf("submit status should report the queued job + command; got %q", st.text)
	}
	// History should have recorded the command.
	if got := hist.Prev(); got != "echo hello" {
		t.Errorf("submit should record the command in history; Prev() = %q", got)
	}
}

// TestSubmitUserShellCmd_BackgroundModeWord confirms the bg modeWord
// branch is taken for a background submission.
func TestSubmitUserShellCmd_BackgroundModeWord(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	cmd := m.submitUserShellCmd("sleep 1", usershell.Background)
	st := cmd().(statusMsg)
	if !strings.Contains(st.text, "(bg)") {
		t.Errorf("background submit should label the job (bg); got %q", st.text)
	}
}

// TestBackgroundRunningCmd_BackgroundsRunningJob drives the ^Z path
// with a genuinely running foreground job.
func TestBackgroundRunningCmd_BackgroundsRunningJob(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	job, err := m.usershell.Submit(context.Background(), "sleep 30", usershell.Foreground)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForRunning(t, m.usershell, job.ID)
	cmd := m.backgroundRunningCmd()
	if cmd == nil {
		t.Fatal("expected a background cmd for a running fg job")
	}
	st := cmd().(statusMsg)
	if !strings.Contains(st.text, "background") {
		t.Errorf("background should report the move; got %q", st.text)
	}
}

// TestChatHistoryDown_ShrunkTranscriptRestoresDraft covers the
// shrink-under-us guard in chatHistoryDown: when the cursor points past
// a transcript that shrank mid-walk, the draft is restored instead of an
// out-of-bounds index.
func TestChatHistoryDown_ShrunkTranscriptRestoresDraft(t *testing.T) {
	m := newHistoryModel(t, "one", "two", "three")
	m.ta.SetValue("draft")
	// Walk up to the oldest entry (cursor 2).
	m.chatHistoryUp()
	m.chatHistoryUp()
	m.chatHistoryUp()
	if m.chatHistoryCursor != 2 {
		t.Fatalf("expected cursor at 2 after three ups; got %d", m.chatHistoryCursor)
	}
	// Now simulate the transcript shrinking (e.g. /clear mid-walk) so the
	// cursor (about to become 1) still points past the new tail.
	m.transcript = m.transcript[:0]
	if !m.chatHistoryDown() {
		t.Fatal("down should still return handled")
	}
	if m.ta.Value() != "draft" {
		t.Errorf("shrunk-transcript down should restore the draft; got %q", m.ta.Value())
	}
	if m.chatHistoryCursor != -1 {
		t.Errorf("walk should reset after the shrink guard; cursor %d", m.chatHistoryCursor)
	}
}

// TestCancelForegroundCmd_CancelsRunningJob drives the cancel path with
// a genuinely running foreground job.
func TestCancelForegroundCmd_CancelsRunningJob(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	job, err := m.usershell.Submit(context.Background(), "sleep 30", usershell.Foreground)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForRunning(t, m.usershell, job.ID)
	cmd := m.cancelForegroundCmd()
	if cmd == nil {
		t.Fatal("expected a cancel cmd for a running fg job")
	}
	st := cmd().(statusMsg)
	if !strings.Contains(st.text, "cancelled") {
		t.Errorf("cancel should report cancelled; got %q", st.text)
	}
}
