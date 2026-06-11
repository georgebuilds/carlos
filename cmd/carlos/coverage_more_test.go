package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/daemon"
	"github.com/georgebuilds/carlos/internal/farewell"
	"github.com/georgebuilds/carlos/internal/skills"
)

// --- please_status.go: cheap message-driven branches -------------------

// TestPleaseStatus_InitReturnsTickCmd pins that Init kicks off the
// spinner tick. Without it the panel would render a frozen spinner.
func TestPleaseStatus_InitReturnsTickCmd(t *testing.T) {
	m := newPleaseStatusModel("x", "openrouter", "m", testPleasePalette())
	if m.Init() == nil {
		t.Fatal("Init should return a tick cmd, got nil")
	}
}

// TestPleaseStatus_TickAdvancesSpinnerAndReschedules walks the live
// branch: a tick bumps the frame and returns another tick so the
// animation keeps going while !done.
func TestPleaseStatus_TickAdvancesSpinnerAndReschedules(t *testing.T) {
	m := newPleaseStatusModel("x", "openrouter", "m", testPleasePalette())
	start := m.spinnerFrame
	next, cmd := m.Update(pleaseTickMsg(time.Now()))
	m = next.(pleaseStatusModel)
	if m.spinnerFrame == start {
		t.Errorf("tick should advance spinnerFrame from %d", start)
	}
	if cmd == nil {
		t.Error("tick while running should reschedule another tick")
	}
}

// TestPleaseStatus_TickAfterDoneStops confirms the spinner keeps
// animating visually but stops rescheduling once done — otherwise the
// program would never settle after quit.
func TestPleaseStatus_TickAfterDoneStops(t *testing.T) {
	m := newPleaseStatusModel("x", "openrouter", "m", testPleasePalette())
	m.done = true
	_, cmd := m.Update(pleaseTickMsg(time.Now()))
	if cmd != nil {
		t.Error("tick after done should not reschedule")
	}
}

// TestPleaseStatus_WindowSizeSetsWidth covers the resize branch that
// drives the box width clamps in View.
func TestPleaseStatus_WindowSizeSetsWidth(t *testing.T) {
	m := newPleaseStatusModel("x", "openrouter", "m", testPleasePalette())
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(pleaseStatusModel)
	if m.width != 120 {
		t.Errorf("width = %d, want 120", m.width)
	}
}

// TestPleaseStatus_CtrlCQuits covers the keyboard interrupt branch:
// ctrl+c marks done and asks bubbletea to quit.
func TestPleaseStatus_CtrlCQuits(t *testing.T) {
	m := newPleaseStatusModel("x", "openrouter", "m", testPleasePalette())
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(pleaseStatusModel)
	if !m.done {
		t.Error("ctrl+c should set done")
	}
	if cmd == nil {
		t.Error("ctrl+c should issue a quit cmd")
	}
}

// TestPleaseStatus_UnknownKeyIsNoop covers the default key fall-through
// (any key that isn't ctrl+c leaves the model untouched).
func TestPleaseStatus_UnknownKeyIsNoop(t *testing.T) {
	m := newPleaseStatusModel("x", "openrouter", "m", testPleasePalette())
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd != nil {
		t.Error("unknown key should not issue a cmd")
	}
	if next.(pleaseStatusModel).done {
		t.Error("unknown key should not set done")
	}
}

// TestPleaseStatus_ToolDoneWithErrorStashesMessage covers the errMsg
// branch of pleaseToolDoneMsg: a failed tool result records lastError
// so a later done-frame can surface it.
func TestPleaseStatus_ToolDoneWithErrorStashesMessage(t *testing.T) {
	m := newPleaseStatusModel("x", "openrouter", "m", testPleasePalette())
	next, _ := m.Update(pleaseToolStartMsg{name: "bash", inputJSON: "{}", t: time.Now()})
	m = next.(pleaseStatusModel)
	next, _ = m.Update(pleaseToolDoneMsg{name: "bash", errMsg: "exit status 1"})
	m = next.(pleaseStatusModel)
	if m.lastError != "exit status 1" {
		t.Errorf("lastError = %q, want 'exit status 1'", m.lastError)
	}
	if m.toolsDone != 1 {
		t.Errorf("toolsDone = %d, want 1", m.toolsDone)
	}
}

// TestPleaseStatus_ToolDoneForOtherToolKeepsFocus covers the branch
// where the completed tool's name doesn't match the currently focused
// tool (concurrent tool calls): the focus row stays put.
func TestPleaseStatus_ToolDoneForOtherToolKeepsFocus(t *testing.T) {
	m := newPleaseStatusModel("x", "openrouter", "m", testPleasePalette())
	next, _ := m.Update(pleaseToolStartMsg{name: "bash", inputJSON: "{}", t: time.Now()})
	m = next.(pleaseStatusModel)
	next, _ = m.Update(pleaseToolDoneMsg{name: "read"})
	m = next.(pleaseStatusModel)
	if m.currentTool != "bash" {
		t.Errorf("currentTool = %q, want bash to stay focused", m.currentTool)
	}
}

// TestPleaseStatus_ViewNarrowWidthClamps drives the small-terminal
// branches in View: widths below the minimums clamp to the floor so
// the box never renders degenerate.
func TestPleaseStatus_ViewNarrowWidthClamps(t *testing.T) {
	m := newPleaseStatusModel(strings.Repeat("long prompt ", 10), "openrouter", "gemini", testPleasePalette())
	m.width = 20 // below the boxW<50 floor
	v := m.View()
	if !strings.Contains(v, "working on:") {
		t.Errorf("narrow view should still render headline:\n%s", v)
	}
}

// TestPleaseStatus_ViewZeroWidthFallback covers the w<=0 default-width
// branch (90) when no WindowSizeMsg has arrived yet.
func TestPleaseStatus_ViewZeroWidthFallback(t *testing.T) {
	m := newPleaseStatusModel("hi", "openrouter", "m", testPleasePalette())
	// width left at 0
	if !strings.Contains(m.View(), "working on:") {
		t.Error("zero-width view should fall back and still render")
	}
}

// TestPleaseStatus_ViewClampsWideWidth covers the w>100 cap branch.
func TestPleaseStatus_ViewClampsWideWidth(t *testing.T) {
	m := newPleaseStatusModel("hi", "openrouter", "m", testPleasePalette())
	m.width = 400
	v := m.View()
	// The rendered border width is capped at 100-2; assert no line
	// runs absurdly long (sanity that the cap engaged).
	for _, ln := range strings.Split(v, "\n") {
		if len([]rune(ln)) > 200 {
			t.Errorf("line exceeded clamp expectation: %d runes", len([]rune(ln)))
		}
	}
}

// wrappedCancel wraps context.Canceled so the Unwrap branch of
// isContextCanceled actually runs.
type wrappedCancel struct{ inner error }

func (w wrappedCancel) Error() string { return "wrapped: " + w.inner.Error() }
func (w wrappedCancel) Unwrap() error { return w.inner }

// TestIsContextCanceled_UnwrapsWrappedError covers the Unwrap loop:
// a non-leaf error that wraps context.Canceled is still reported as
// canceled, while a wrapper around an unrelated error is not.
func TestIsContextCanceled_UnwrapsWrappedError(t *testing.T) {
	if !isContextCanceled(wrappedCancel{inner: context.Canceled}) {
		t.Error("wrapped context.Canceled should be reported")
	}
	if isContextCanceled(wrappedCancel{inner: errors.New("nope")}) {
		t.Error("wrapper around unrelated err should not be reported")
	}
	// fmt.Errorf %w wrapping should also resolve.
	if !isContextCanceled(fmt.Errorf("loop: %w", context.Canceled)) {
		t.Error("%%w-wrapped context.Canceled should be reported")
	}
}

// --- main.go: stripResumeMode -----------------------------------------

// TestStripResumeMode covers every arm of the leading resume-flag
// parser: both short+long forms of -c/-r, the no-flag pass-through,
// the empty-args guard, and the rule that a resume flag is only
// consumed in the leading position (a verb after it is left intact).
func TestStripResumeMode(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantMode string
		wantRest []string
	}{
		{"empty", nil, "", nil},
		{"continue short", []string{"-c"}, "continue", []string{}},
		{"continue long", []string{"--continue"}, "continue", []string{}},
		{"resume short", []string{"-r"}, "resume", []string{}},
		{"resume long", []string{"--resume"}, "resume", []string{}},
		{"flag then verb", []string{"-c", "onboard"}, "continue", []string{"onboard"}},
		{"no flag verb", []string{"version"}, "", []string{"version"}},
		{"flag not leading", []string{"onboard", "-c"}, "", []string{"onboard", "-c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, rest := stripResumeMode(tc.in)
			if mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", mode, tc.wantMode)
			}
			if strings.Join(rest, " ") != strings.Join(tc.wantRest, " ") {
				t.Errorf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

// --- picker_inline.go: cardsPerRow direct unit ------------------------

// TestCardsPerRow_Branches pins every branch of the layout math: zero
// frames, a width too narrow for even one card (floors to 1), the
// per-row formula, and the cap at the actual frame count.
func TestCardsPerRow_Branches(t *testing.T) {
	if got := cardsPerRow(200, 0); got != 0 {
		t.Errorf("zero total: got %d, want 0", got)
	}
	if got := cardsPerRow(5, 4); got != 1 {
		t.Errorf("too-narrow width: got %d, want 1 (floor)", got)
	}
	// Very wide: capped at total frame count, not the geometric fit.
	if got := cardsPerRow(10_000, 3); got != 3 {
		t.Errorf("wide width with 3 frames: got %d, want 3 (capped)", got)
	}
	// A width that fits more than one but fewer than total exercises
	// the formula's middle case. cardW=13, gap=2: 13 + 15n.
	// avail = w-2; pick w so two fit but not three.
	if got := cardsPerRow(2+13+15, 8); got != 2 {
		t.Errorf("two-card width: got %d, want 2", got)
	}
}

// --- daemon.go: queueGatewayOrphaned ----------------------------------

// TestQueueGatewayOrphaned_NilConfigIsNoop guards the early return.
func TestQueueGatewayOrphaned_NilConfigIsNoop(t *testing.T) {
	panel := farewell.New()
	queueGatewayOrphaned(nil, panel)
	if panel.Len() != 0 {
		t.Errorf("nil cfg should not queue; got %d", panel.Len())
	}
}

// TestQueueGatewayOrphaned_NilPanelIsNoop guards the panel==nil arm.
func TestQueueGatewayOrphaned_NilPanelIsNoop(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.Enabled = true
	// Must not panic on a nil panel even with gateway enabled.
	queueGatewayOrphaned(cfg, nil)
}

// TestQueueGatewayOrphaned_DisabledIsNoop covers the !Enabled arm.
func TestQueueGatewayOrphaned_DisabledIsNoop(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.Enabled = false
	panel := farewell.New()
	queueGatewayOrphaned(cfg, panel)
	if panel.Len() != 0 {
		t.Errorf("disabled gateway should not queue; got %d", panel.Len())
	}
}

// TestQueueGatewayOrphaned_EnabledNoDaemonQueues covers the load-
// bearing branch: gateway on + no daemon reachable -> the farewell
// panel gets the "daemon offline" note (instead of a bare stderr line
// that would leak past the alt-screen).
func TestQueueGatewayOrphaned_EnabledNoDaemonQueues(t *testing.T) {
	isolateHome(t) // points CARLOS_DAEMON_SOCKET at a dead path
	cfg := &config.Config{}
	cfg.Gateway.Enabled = true
	panel := farewell.New()
	queueGatewayOrphaned(cfg, panel)
	if panel.Len() != 1 {
		t.Fatalf("expected 1 queued message, got %d", panel.Len())
	}
	msg := panel.Messages()[0]
	if !strings.Contains(msg.Text, "daemon offline") {
		t.Errorf("message text = %q, want 'daemon offline'", msg.Text)
	}
}

// TestRunGateway_AddArmRoutesToAddWizard covers the `add` dispatch
// arm of runGateway. In an isolated home with no config, runGatewayAdd
// returns the "no config" error before any TUI launch, so we get a
// clean, deterministic assertion that the arm wired through.
func TestRunGateway_AddArmRoutesToAddWizard(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CARLOS_CONFIG", "") // force DefaultPath() under the temp HOME
	err := runGateway([]string{"add"})
	if err == nil {
		t.Fatal("expected the add wizard to error on a config-less home")
	}
	if !strings.Contains(err.Error(), "carlos onboard") {
		t.Errorf("error should come from runGatewayAdd: %v", err)
	}
}

// --- main.go: summariseSkills nil-skill skip --------------------------

// TestSummariseSkills_SkipsNilSkillEntries covers the `s == nil`
// continue inside the projection loop: a defensively-nil entry in the
// active set is dropped rather than panicking on s.Name.
func TestSummariseSkills_SkipsNilSkillEntries(t *testing.T) {
	lib := &skills.Library{
		Active: []*skills.Skill{
			{Name: "real", Description: "d"},
			nil,
		},
	}
	got := summariseSkills(lib, "personal")
	if len(got) != 1 {
		t.Fatalf("nil entry should be skipped; got %d: %+v", len(got), got)
	}
	if got[0].Name != "real" {
		t.Errorf("surviving skill = %q, want 'real'", got[0].Name)
	}
}

// TestQueueGatewayOrphaned_DaemonRunningSilent covers the success arm:
// a reachable daemon means nothing gets queued.
func TestQueueGatewayOrphaned_DaemonRunningSilent(t *testing.T) {
	sock := shortSockDaemon(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)
	stop := fakeDaemonD(t, sock, func(_ daemon.Request) daemon.Response {
		return daemon.Response{Ok: true}
	})
	defer stop()

	cfg := &config.Config{}
	cfg.Gateway.Enabled = true
	panel := farewell.New()
	queueGatewayOrphaned(cfg, panel)
	if panel.Len() != 0 {
		t.Errorf("daemon up should silence the panel; got %d", panel.Len())
	}
}
