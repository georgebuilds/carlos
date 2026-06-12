package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/usershell"
	"github.com/georgebuilds/carlos/internal/workspace"
)

// framedUpdateModel wires a model with an active frame so the Ctrl+F /
// Ctrl+O / Ctrl+L branches in Update have something to act on, then
// drives it through Init + window size so View math is sane.
func framedUpdateModel(t *testing.T, ui FrameUI) *Model {
	t.Helper()
	log := openTempLog(t)
	const agentID = "01HV0000000000000000UBR001"
	seedAgent(t, log, agentID, "ubr", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource(), WithFrame(ui))
	return drive(t, m, 120, 36)
}

// TestUpdate_CtrlFOpensFrameSwitcher exercises the Ctrl+F branch in
// Update (chat.go) - distinct from the direct handler tests, this proves
// the keystroke routes through the top-level switch.
func TestUpdate_CtrlFOpensFrameSwitcher(t *testing.T) {
	m := framedUpdateModel(t, FrameUI{
		Active:    "work",
		Available: []string{"personal", "work"},
	})
	if m.showFrameSwitcher {
		t.Fatal("switcher should start closed")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = updated.(*Model)
	if !m.showFrameSwitcher {
		t.Error("ctrl+f with an active frame should open the switcher")
	}
	if cmd != nil {
		t.Errorf("ctrl+f open should not return a cmd; got %T", cmd)
	}
	// Cursor should snap to the active frame's index.
	if m.switcherCursor != 1 {
		t.Errorf("switcher cursor should snap to active; got %d", m.switcherCursor)
	}
}

// TestUpdate_CtrlFNoFrameIsNoOp confirms Ctrl+F is inert when frames
// aren't wired (Active == ""), falling through to the textarea route.
func TestUpdate_CtrlFNoFrameIsNoOp(t *testing.T) {
	m := updateModel(t)
	if m.frame.Active != "" {
		t.Skip("default model unexpectedly has an active frame")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = updated.(*Model)
	if m.showFrameSwitcher {
		t.Error("ctrl+f with no active frame must not open the switcher")
	}
}

// TestUpdate_CtrlOOpensModeSwitcher exercises the Ctrl+O mode-switcher
// branch through Update.
func TestUpdate_CtrlOOpensModeSwitcher(t *testing.T) {
	m := framedUpdateModel(t, FrameUI{
		Active:    "work",
		Available: []string{"personal", "work"},
		Mode:      "tight",
	})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(*Model)
	if !m.showModeSwitcher {
		t.Error("ctrl+o with an active frame should open the mode switcher")
	}
}

// TestUpdate_CtrlJTogglesJobsOverlay exercises the Ctrl+J jobs-overlay
// toggle and its reset of cursor/filter state.
func TestUpdate_CtrlJTogglesJobsOverlay(t *testing.T) {
	m := updateModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.jobsCursor = 5
	m.jobsFilter = "stale"
	m.jobsFilterMode = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = updated.(*Model)
	if !m.showJobs {
		t.Fatal("ctrl+j should open the jobs overlay")
	}
	if m.jobsCursor != 0 || m.jobsFilter != "" || m.jobsFilterMode {
		t.Errorf("opening jobs should reset cursor/filter; cursor=%d filter=%q mode=%v",
			m.jobsCursor, m.jobsFilter, m.jobsFilterMode)
	}
	// Second press closes it.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = updated.(*Model)
	if m.showJobs {
		t.Error("second ctrl+j should close the jobs overlay")
	}
}

// TestUpdate_CtrlJNoManagerIsNoOp confirms Ctrl+J is inert without a
// wired usershell manager.
func TestUpdate_CtrlJNoManagerIsNoOp(t *testing.T) {
	m := updateModel(t)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = updated.(*Model)
	if m.showJobs {
		t.Error("ctrl+j with no manager should not open the overlay")
	}
}

// TestUpdate_CtrlLMutesCwdHint walks the Ctrl+L branch: with a frame +
// MatchCwd wired, Ctrl+L should set the once-per-session lock and clear
// any footer hint.
func TestUpdate_CtrlLMutesCwdHint(t *testing.T) {
	m := framedUpdateModel(t, FrameUI{
		Active:    "work",
		Available: []string{"work"},
		MatchCwd:  func(string) string { return "work" },
	})
	m.footerHint = "you are in /tmp which matches frame `work`."
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = updated.(*Model)
	if !m.hintsLocked {
		t.Error("ctrl+l should lock cwd hints for the session")
	}
	if m.footerHint != "" {
		t.Errorf("ctrl+l should clear the footer hint; got %q", m.footerHint)
	}
}

// TestUpdate_CtrlLNoMatchCwdIsNoOp confirms Ctrl+L is inert when no
// MatchCwd resolver is wired (legacy single-shelf mode).
func TestUpdate_CtrlLNoMatchCwdIsNoOp(t *testing.T) {
	m := framedUpdateModel(t, FrameUI{
		Active:    "work",
		Available: []string{"work"},
	})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = updated.(*Model)
	if m.hintsLocked {
		t.Error("ctrl+l with no MatchCwd should not lock hints")
	}
}

// TestUpdate_ShowHelpDismissesOnNextKey exercises the showHelp early
// dismiss path: any non-ctrl+c keystroke closes the help panel without
// touching the textarea.
func TestUpdate_ShowHelpDismissesOnNextKey(t *testing.T) {
	m := updateModel(t)
	m.showHelp = true
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(*Model)
	if m.showHelp {
		t.Error("a keystroke should dismiss the help panel")
	}
	if cmd != nil {
		t.Errorf("help dismiss should be a clean no-op cmd; got %T", cmd)
	}
	if m.ta.Value() != "" {
		t.Errorf("help dismiss key must not reach the textarea; got %q", m.ta.Value())
	}
}

// TestUpdate_ReadOnlyEnterIsNoOp confirms a read-only viewer swallows
// Enter without submitting.
func TestUpdate_ReadOnlyEnterIsNoOp(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000UBR002"
	seedAgent(t, log, agentID, "ro", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource(), WithReadOnly())
	m = drive(t, m, 120, 30)
	before := len(m.transcript)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*Model)
	if cmd != nil {
		t.Errorf("read-only enter should be a no-op; got cmd %T", cmd)
	}
	if len(m.transcript) != before {
		t.Errorf("read-only enter should not append; was %d now %d", before, len(m.transcript))
	}
}

// TestUpdate_ReadOnlyArrowScrollsViewport confirms that in read-only
// mode a non-intercepted key routes to the viewport (the scroll path)
// rather than the textarea.
func TestUpdate_ReadOnlyArrowScrollsViewport(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000UBR003"
	seedAgent(t, log, agentID, "ro2", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource(), WithReadOnly())
	m = drive(t, m, 120, 30)
	// Should not panic and should not quit.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*Model)
	if m.quitting {
		t.Error("read-only down-arrow should not quit")
	}
}

// TestUpdate_TypingClearsStatusAndStartupNotices proves the textarea
// route clears a stale status echo and dismisses startup notices on the
// first keystroke.
func TestUpdate_TypingClearsStatusAndStartupNotices(t *testing.T) {
	m := updateModel(t)
	m.status = "stale echo"
	m.startupNotices = []string{"notice one"}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	m = updated.(*Model)
	if m.status != "" {
		t.Errorf("typing should clear the status echo; got %q", m.status)
	}
	if len(m.startupNotices) != 0 {
		t.Errorf("typing should dismiss startup notices; got %v", m.startupNotices)
	}
	if !strings.Contains(m.ta.Value(), "h") {
		t.Errorf("typed rune should reach the textarea; got %q", m.ta.Value())
	}
}

// TestUpdate_ApprovalAllowAlways exercises the pendingApproval 'a'
// branch through Update, which resolves the prompt with AllowAlways.
func TestUpdate_ApprovalAllowAlways(t *testing.T) {
	m := updateModel(t)
	m.approver = NewTUIApprover()
	defer m.approver.Close()
	reply := make(chan ApprovalDecision, 1)
	m.pendingApproval = &ApprovalRequest{
		Tool:  "bash",
		reply: reply,
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(*Model)
	if m.pendingApproval != nil {
		t.Error("'a' should clear the pending approval")
	}
	select {
	case d := <-reply:
		if d != ApprovalAllowAlways {
			t.Errorf("'a' should resolve AllowAlways; got %v", d)
		}
	default:
		t.Error("expected a decision on the reply channel")
	}
}

// TestUpdate_ApprovalSwallowsStrayKeys confirms an unrelated key while an
// approval prompt is up is swallowed (no textarea accumulation).
func TestUpdate_ApprovalSwallowsStrayKeys(t *testing.T) {
	m := updateModel(t)
	m.pendingApproval = &ApprovalRequest{Tool: "bash"}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	m = updated.(*Model)
	if cmd != nil {
		t.Errorf("stray key during approval should be swallowed; got cmd %T", cmd)
	}
	if m.ta.Value() != "" {
		t.Errorf("stray key must not reach the textarea while approving; got %q", m.ta.Value())
	}
}

// TestUpdate_StatusMsgKindStored confirms statusMsg sets both the text
// and the kind on the model.
func TestUpdate_StatusMsgKindStored(t *testing.T) {
	m := updateModel(t)
	updated, _ := m.Update(statusMsg{text: "warned", kind: statusWarn})
	m = updated.(*Model)
	if m.statusKind != statusWarn {
		t.Errorf("statusMsg should store kind; got %v", m.statusKind)
	}
}

// TestUpdate_UserShellSubscriptionClosedClearsChannel walks the
// userShellSubscriptionClosedMsg branch.
func TestUpdate_UserShellSubscriptionClosedClearsChannel(t *testing.T) {
	m := updateModel(t)
	ch := make(chan usershell.Update, 1)
	m.userShellSubCh = ch
	updated, cmd := m.Update(userShellSubscriptionClosedMsg{})
	m = updated.(*Model)
	if m.userShellSubCh != nil {
		t.Error("closed subscription should nil out the channel")
	}
	if cmd != nil {
		t.Errorf("closed subscription should not re-arm; got %T", cmd)
	}
}

// TestUpdate_ChildrenTickNoViewIsNoOp exercises the childrenTickMsg
// branch when no children view is wired.
func TestUpdate_ChildrenTickNoViewIsNoOp(t *testing.T) {
	m := updateModel(t)
	m.childrenView = nil
	updated, cmd := m.Update(childrenTickMsg{})
	m = updated.(*Model)
	if cmd != nil {
		t.Errorf("children tick with no view should be a no-op; got %T", cmd)
	}
}

// TestUpdate_CtrlAtBackgroundsShellSubmission walks the ctrl+@ branch:
// when the composer starts with "!", ctrl+@ submits it as a background
// shell job (and clears the textarea).
func TestUpdate_CtrlAtBackgroundsShellSubmission(t *testing.T) {
	m := updateModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.ta.SetValue("!echo hi")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlAt})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("ctrl+@ on a shell-prefixed composer should submit a background job")
	}
	if m.ta.Value() != "" {
		t.Errorf("ctrl+@ submission should clear the composer; got %q", m.ta.Value())
	}
}

// TestUpdate_CtrlAtNonShellFallsThrough confirms ctrl+@ on a plain
// (non-"!") composer does not submit a background job.
func TestUpdate_CtrlAtNonShellFallsThrough(t *testing.T) {
	m := updateModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.ta.SetValue("just text")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlAt})
	m = updated.(*Model)
	// The value should be untouched (no submission happened).
	if m.ta.Value() != "just text" {
		t.Errorf("ctrl+@ on non-shell text should not submit; got %q", m.ta.Value())
	}
}

// TestUpdate_UpWalksChatHistory exercises the up-arrow chat-history
// branch inside Update (distinct from the direct chatHistoryUp tests).
func TestUpdate_UpWalksChatHistory(t *testing.T) {
	m := newHistoryModel(t, "first message", "second message")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*Model)
	if m.ta.Value() != "second message" {
		t.Errorf("up should recall the most recent message; got %q", m.ta.Value())
	}
}

// TestUpdate_UpShellHistoryWhenShellPrefixed confirms that in "!" mode
// the up-arrow walks shell history, not chat history.
func TestUpdate_UpShellHistoryWhenShellPrefixed(t *testing.T) {
	m := updateModel(t)
	hist := usershell.NewHistory(t.TempDir() + "/sh-history")
	_ = hist.Add("ls -la")
	m.shellHistory = hist
	m.ta.SetValue("!")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*Model)
	if m.ta.Value() != "!ls -la" {
		t.Errorf("up in shell mode should recall shell history with bang prefix; got %q", m.ta.Value())
	}
}

// TestUpdate_EventMsgAppliesAndRepumps walks the eventMsg branch: the
// event is folded into the transcript and a batched repump/flush cmd is
// returned.
func TestUpdate_EventMsgAppliesAndRepumps(t *testing.T) {
	m := updateModel(t)
	before := len(m.transcript)
	payload, err := json.Marshal(agent.MessagePayload{Text: "streamed in"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ev := agent.Event{
		AgentID: m.agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtUserMessage,
		Payload: payload,
	}
	updated, _ := m.Update(eventMsg{ev: ev})
	m = updated.(*Model)
	if len(m.transcript) <= before {
		t.Errorf("eventMsg should fold the event into the transcript; was %d now %d", before, len(m.transcript))
	}
}

// TestUpdate_UserShellOutputAppendsToEntry walks the userShellUpdateMsg
// output path: a chunk for a known job appends to that entry's buffer.
func TestUpdate_UserShellOutputAppendsToEntry(t *testing.T) {
	m := updateModel(t)
	m.transcript = append(m.transcript, transcriptEntry{
		kind:       entryUserShell,
		shellJobID: "job-X",
	})
	idx := len(m.transcript) - 1
	updated, _ := m.Update(userShellUpdateMsg{u: usershell.Update{
		JobID:  "job-X",
		Output: []byte("hello output"),
	}})
	m = updated.(*Model)
	if !strings.Contains(m.transcript[idx].shellOutput, "hello output") {
		t.Errorf("output chunk should append to the matching entry; got %q", m.transcript[idx].shellOutput)
	}
}

// TestUpdate_TextTickPicksUpFirstChildSnapshot walks the textTickMsg
// branch that promotes to the faster children tick when a fresh child
// appears in the snapshot.
func TestUpdate_TextTickPicksUpFirstChildSnapshot(t *testing.T) {
	m := updateModel(t)
	m.childrenView = ChildrenViewFunc(func() []ChildSnapshot {
		return []ChildSnapshot{{AgentID: "child-1", LastEvent: "spawned"}}
	})
	m.childrenSnap = nil
	updated, cmd := m.Update(textTickMsg{})
	m = updated.(*Model)
	if len(m.childrenSnap) != 1 {
		t.Errorf("text tick should pick up the first child snapshot; got %d", len(m.childrenSnap))
	}
	if cmd == nil {
		t.Error("text tick that found a child should re-arm the tick loop")
	}
}

// TestUpdate_ChildrenTickStopsOnEmptySnapshot walks the childrenTickMsg
// branch that stops the loop when the snapshot drains to empty.
func TestUpdate_ChildrenTickStopsOnEmptySnapshot(t *testing.T) {
	m := updateModel(t)
	m.childrenSnap = []ChildSnapshot{{AgentID: "old"}}
	m.childrenView = ChildrenViewFunc(func() []ChildSnapshot { return nil })
	updated, cmd := m.Update(childrenTickMsg{})
	m = updated.(*Model)
	if len(m.childrenSnap) != 0 {
		t.Errorf("children tick should refresh to the empty snapshot; got %d", len(m.childrenSnap))
	}
	if cmd != nil {
		t.Errorf("an empty snapshot should stop the tick loop; got %T", cmd)
	}
}

// TestUpdate_DownShellHistoryNext walks the down-arrow shell-history
// branch in "!" mode, including the empty-next "!" reset.
func TestUpdate_DownShellHistoryNext(t *testing.T) {
	m := updateModel(t)
	hist := usershell.NewHistory(t.TempDir() + "/sh-history")
	_ = hist.Add("first")
	_ = hist.Add("second")
	m.shellHistory = hist
	m.ta.SetValue("!")
	// Walk up twice to reach an older entry, then down to come back.
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*Model)
	if !strings.HasPrefix(m.ta.Value(), "!") {
		t.Errorf("down in shell mode should keep the bang prefix; got %q", m.ta.Value())
	}
}

// TestUpdate_CtrlCDuringApprovalDeniesAndQuits walks the ctrl+c branch
// while an approval prompt is active: it resolves Deny and quits.
func TestUpdate_CtrlCDuringApprovalDeniesAndQuits(t *testing.T) {
	m := updateModel(t)
	m.approver = NewTUIApprover()
	defer m.approver.Close()
	reply := make(chan ApprovalDecision, 1)
	m.pendingApproval = &ApprovalRequest{Tool: "bash", reply: reply}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(*Model)
	if !m.quitting {
		t.Error("ctrl+c during approval should set quitting")
	}
	if cmd == nil {
		t.Error("ctrl+c during approval should return tea.Quit")
	}
	select {
	case d := <-reply:
		if d != ApprovalDeny {
			t.Errorf("ctrl+c during approval should Deny; got %v", d)
		}
	default:
		t.Error("expected a Deny on the reply channel")
	}
}

// TestUpdate_FirstTrustYesDrainsQueuedCmds drives the showFirstTrust
// path through Update: pressing 'y' queues the trust cmd, which Update
// drains into a batched tea.Cmd.
func TestUpdate_FirstTrustYesDrainsQueuedCmds(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	// Make cwd look like a project so the prompt logic + trust make sense.
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := workspace.NewStore(filepath.Join(dir, "trust.json"))
	policy := workspace.NewPolicy(store, cwd)

	log := openTempLog(t)
	const agentID = "01HV0000000000000000UBR005"
	seedAgent(t, log, agentID, "ft", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource(), WithWorkspacePolicy(policy))
	m = drive(t, m, 120, 30)
	m.showFirstTrust = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(*Model)
	if m.showFirstTrust {
		t.Error("'y' should dismiss the first-trust prompt")
	}
	if !m.firstTrustDismissed {
		t.Error("'y' should mark the prompt dismissed for the session")
	}
	if cmd == nil {
		t.Error("'y' should drain the queued trust command into a batched cmd")
	}
}

// TestUpdate_FirstTrustNoDismisses walks the n/esc dismissal branch.
func TestUpdate_FirstTrustNoDismisses(t *testing.T) {
	m := updateModel(t)
	m.showFirstTrust = true
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(*Model)
	if m.showFirstTrust {
		t.Error("'n' should dismiss the first-trust prompt")
	}
}

// TestUpdate_FrameSwitcherKeyRoutedWhileOpen confirms that while the
// frame switcher is open, a nav key routes through its handler (and the
// composer never sees it).
func TestUpdate_FrameSwitcherKeyRoutedWhileOpen(t *testing.T) {
	m := framedUpdateModel(t, FrameUI{
		Active:    "work",
		Available: []string{"personal", "work", "research"},
	})
	m.openFrameSwitcher()
	before := m.switcherCursor
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(*Model)
	if m.switcherCursor == before {
		t.Errorf("right-arrow should move the switcher cursor while open; stayed at %d", before)
	}
	if m.ta.Value() != "" {
		t.Errorf("switcher nav key must not reach the composer; got %q", m.ta.Value())
	}
}

// TestUpdate_ModeSwitcherKeyRoutedWhileOpen confirms a nav key routes
// through the mode-switcher handler while it's open (Update branch).
func TestUpdate_ModeSwitcherKeyRoutedWhileOpen(t *testing.T) {
	m := framedUpdateModel(t, FrameUI{
		Active:    "work",
		Available: []string{"work"},
		Mode:      "solo",
	})
	m.openModeSwitcher()
	before := m.modeSwitcherCursor
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*Model)
	if m.modeSwitcherCursor == before && m.ta.Value() != "" {
		t.Errorf("mode switcher should consume the nav key; cursor %d, composer %q",
			m.modeSwitcherCursor, m.ta.Value())
	}
	if m.ta.Value() != "" {
		t.Errorf("mode switcher nav key must not reach the composer; got %q", m.ta.Value())
	}
}

// TestUpdate_PermsKeyRoutedWhileOpen confirms a key routes through the
// permissions overlay handler while it's open (Update branch).
func TestUpdate_PermsKeyRoutedWhileOpen(t *testing.T) {
	m := updateModel(t)
	m.showPerms = true
	// Tab cycles the perms tab; it should be consumed by the overlay.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*Model)
	if m.ta.Value() != "" {
		t.Errorf("perms overlay key must not reach the composer; got %q", m.ta.Value())
	}
}

// TestUpdate_ResumeKeyRoutedWhileOpen confirms a nav key routes through
// the resume handler while the picker is open (Update branch).
func TestUpdate_ResumeKeyRoutedWhileOpen(t *testing.T) {
	m := updateModel(t)
	m.showResume = true
	m.resumeSessions = []resumeSession{
		{ID: "01HV0000000000000000RES010", Preview: "a"},
		{ID: "01HV0000000000000000RES011", Preview: "b"},
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*Model)
	if m.ta.Value() != "" {
		t.Errorf("resume overlay key must not reach the composer; got %q", m.ta.Value())
	}
}

// TestUpdate_NewFrameKeyRoutedWhileOpen confirms typing routes through
// the new-frame wizard handler while it's open (Update branch).
func TestUpdate_NewFrameKeyRoutedWhileOpen(t *testing.T) {
	m := framedUpdateModel(t, FrameUI{
		Active:    "work",
		Available: []string{"work"},
		AddFrame:  func(frame.Frame) error { return nil },
	})
	m.openNewFrameWizard("")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(*Model)
	// The wizard's name field should capture the keystroke, not the composer.
	if m.ta.Value() != "" {
		t.Errorf("wizard key must not reach the composer; got %q", m.ta.Value())
	}
	if !strings.Contains(m.newFrame.Name, "x") {
		t.Errorf("wizard should capture the typed rune into the name field; got %q", m.newFrame.Name)
	}
}

// TestApplyEvent_SteeringAppendsEntry covers the EvtSteering branch in
// applyEvent.
func TestApplyEvent_SteeringAppendsEntry(t *testing.T) {
	m := updateModel(t)
	payload, err := json.Marshal(agent.MessagePayload{Text: "steer left"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	before := len(m.transcript)
	m.applyEvent(agent.Event{
		AgentID: m.agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtSteering,
		Payload: payload,
	})
	if len(m.transcript) != before+1 {
		t.Fatalf("steering event should append one entry; was %d now %d", before, len(m.transcript))
	}
	last := m.transcript[len(m.transcript)-1]
	if last.kind != entrySteering {
		t.Errorf("steering entry kind wrong; got %v", last.kind)
	}
	if last.text != "steer left" {
		t.Errorf("steering text = %q, want 'steer left'", last.text)
	}
}

// TestUpdate_BackfillReplaysEvents confirms a backfillMsg drains all
// events through applyEvent.
func TestUpdate_BackfillReplaysEvents(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000UBR004"
	seedAgent(t, log, agentID, "bf", "claude-4.7-sonnet")
	seedUserMessage(t, log, agentID, "from backfill")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	found := false
	for _, e := range m.transcript {
		if e.kind == entryUserMessage && strings.Contains(e.text, "from backfill") {
			found = true
		}
	}
	if !found {
		t.Error("backfill should replay the seeded user message into the transcript")
	}
}
