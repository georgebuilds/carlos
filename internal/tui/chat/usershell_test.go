package chat

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/usershell"
)

func TestIsShellSubmission(t *testing.T) {
	cases := map[string]bool{
		"!ls":       true,
		"  !ls":     true,
		"\t!cargo":  true,
		"!":         false, // empty after the bang
		"! ":        false,
		"":          false,
		"ls":        false,
		"hello!":    false,
		"!\n":       false,
		"\t  ! ls":  true,
	}
	for in, want := range cases {
		if got := isShellSubmission(in); got != want {
			t.Errorf("isShellSubmission(%q) = %v want %v", in, got, want)
		}
	}
}

func TestHasShellPrefix(t *testing.T) {
	cases := map[string]bool{
		"!":     true,
		"! ":    true,
		"!ls":   true,
		"  !":   true,
		"ls":    false,
		"":      false,
		"hi!":   false,
	}
	for in, want := range cases {
		if got := hasShellPrefix(in); got != want {
			t.Errorf("hasShellPrefix(%q) = %v want %v", in, got, want)
		}
	}
}

func TestExtractShellCommand(t *testing.T) {
	cases := map[string]string{
		"!ls":         "ls",
		"  !ls":       "ls",
		"!  ls -la ":  "ls -la",
		"!cargo test": "cargo test",
		"ls":          "",
		"":            "",
	}
	for in, want := range cases {
		if got := extractShellCommand(in); got != want {
			t.Errorf("extractShellCommand(%q) = %q want %q", in, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0.0s"},
		{300 * time.Millisecond, "0.3s"},
		{1500 * time.Millisecond, "1.5s"},
		{45 * time.Second, "45.0s"},
		{90 * time.Second, "1m30s"},
		{2*time.Minute + 3*time.Second, "2m3s"},
		{-1 * time.Second, "0.0s"}, // negative clamps to zero
	}
	for _, tc := range cases {
		if got := formatDuration(tc.d); got != tc.want {
			t.Errorf("formatDuration(%v) = %q want %q", tc.d, got, tc.want)
		}
	}
}

func TestTruncateOneLine(t *testing.T) {
	if got := truncateOneLine("hello world", 5); got != "hell…" {
		t.Errorf("truncate basic: %q", got)
	}
	if got := truncateOneLine("ok", 10); got != "ok" {
		t.Errorf("under-cap pass-through: %q", got)
	}
	if got := truncateOneLine("foo\nbar", 7); got != "foo bar" {
		t.Errorf("newlines collapse to spaces: %q", got)
	}
	if got := truncateOneLine("x", 1); got != "x" {
		t.Errorf("len=cap edge: %q", got)
	}
	if got := truncateOneLine("xy", 1); got != "…" {
		t.Errorf("max=1 truncation: %q", got)
	}
}

// stubRunner is a minimal runner that emits a single output chunk
// then exits with the given code. Used by S4 tests that need a real
// Manager + Job lifecycle without depending on the package-internal
// fake from runner_fake_test.go.
type stubRunner struct {
	output string
	exit   int
	block  bool
	wait   chan struct{}
}

func (s *stubRunner) Start(ctx context.Context, command, cwd string) (any, func() (int, error), func(), error) {
	// Return a value satisfying usershell.runner via interface
	// unification at the import site. Since the runner interface is
	// package-private, S4 tests can't easily inject a fake — they
	// use the production PTY runner OR construct a Manager via the
	// public surface with no runner override. Here we degrade
	// gracefully when used in a test that doesn't actually start
	// jobs.
	return nil, nil, nil, errors.New("stubRunner is a compile-time placeholder; use the Manager's public methods instead")
}

// minimalManager constructs a real usershell.Manager wired with no
// log + tempdir output. Tests that don't actually spawn jobs can use
// this to drive the footer-state logic.
func minimalManager(t *testing.T) *usershell.Manager {
	t.Helper()
	return usershell.New(usershell.Options{
		Cwd:       t.TempDir(),
		OutputDir: t.TempDir(),
	})
}

// newTestModel builds a Model with a properly-initialized textarea so
// tests don't trip the zero-value panic. The textarea ships in chat.New
// pre-Focus()'d + with KeyMap tweaks; for footer-state tests we only
// need Value()/SetValue() to work.
func newTestModel(t *testing.T) *Model {
	t.Helper()
	return New(nil, "test-agent", NewMemTextSource())
}

func TestComputeUserShellFooterContext_NoManager(t *testing.T) {
	m := newTestModel(t)
	m.ta.SetValue("!ls")
	ctx := m.computeUserShellFooterContext()
	if ctx.state != userShellFooterIdle || ctx.hasShellMgr {
		t.Errorf("without manager: %+v", ctx)
	}
	// With no manager, render returns empty.
	if got := renderUserShellFooter(ctx); got != "" {
		t.Errorf("no-manager render: want empty got %q", got)
	}
}

func TestComputeUserShellFooterContext_IdleState(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.ta.SetValue("hello there")
	ctx := m.computeUserShellFooterContext()
	if ctx.state != userShellFooterIdle {
		t.Errorf("plain text input: want idle got %v", ctx.state)
	}
	// Idle state returns empty hint (preserves /help tip).
	if got := renderUserShellFooter(ctx); got != "" {
		t.Errorf("idle render: want empty got %q", got)
	}
}

func TestComputeUserShellFooterContext_TypingShell(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.ta.SetValue("!ls -la")
	ctx := m.computeUserShellFooterContext()
	if ctx.state != userShellFooterTypingShell {
		t.Errorf("typing-shell: %v", ctx.state)
	}
	out := renderUserShellFooter(ctx)
	for _, want := range []string{"shell", "enter", "background", "esc"} {
		if !strings.Contains(out, want) {
			t.Errorf("typing-shell hint missing %q in %q", want, out)
		}
	}
}

func TestComputeUserShellFooterContext_BareBangShowsTeaser(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.ta.SetValue("!")
	ctx := m.computeUserShellFooterContext()
	if ctx.state != userShellFooterTypingShell {
		t.Errorf("bare !: want typing-shell, got %v", ctx.state)
	}
	out := renderUserShellFooter(ctx)
	if !strings.Contains(out, "type a command") {
		t.Errorf("bare bang teaser missing: %q", out)
	}
}

func TestSubmitUserShellCmd_NilManager(t *testing.T) {
	m := newTestModel(t)
	cmd := m.submitUserShellCmd("ls", usershell.Foreground)
	if cmd == nil {
		t.Fatal("expected cmd that emits status")
	}
	msg := cmd()
	status, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", msg)
	}
	if !strings.Contains(status.text, "not wired") {
		t.Errorf("status text: %q", status.text)
	}
}

func TestCancelForegroundCmd_NoFg(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	if cmd := m.cancelForegroundCmd(); cmd != nil {
		t.Error("expected nil cmd when no fg job runs")
	}
}

func TestBackgroundRunningCmd_NoFg(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	if cmd := m.backgroundRunningCmd(); cmd != nil {
		t.Error("expected nil cmd when no fg job runs")
	}
}

func TestSubmit_RoutesBangToShell(t *testing.T) {
	// Use a Manager wired to a fake-runner so we don't actually
	// spawn a shell. We synthesize the Manager via the public
	// surface with a runner that just records calls.
	recorder := &recordingRunner{}
	mgr := usershell.New(usershell.Options{
		Runner:    recorder,
		OutputDir: t.TempDir(),
	})
	defer mgr.Close()
	m := newTestModel(t)
	m.usershell = mgr
	m.ta.SetValue("!echo hi")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("submit should have returned a cmd")
	}
	// The cmd dispatches a Submit; wait briefly for the recorder.
	msg := cmd()
	if _, ok := msg.(statusMsg); !ok {
		t.Errorf("expected statusMsg from submit, got %T", msg)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if recorder.calls() >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if recorder.calls() == 0 {
		t.Error("expected Runner.Start to have been called")
	}
	if m.ta.Value() != "" {
		t.Errorf("textarea should be cleared on submit; got %q", m.ta.Value())
	}
}

func TestSubmit_NonBangFallsThroughToModel(t *testing.T) {
	mgr := minimalManager(t)
	defer mgr.Close()
	m := newTestModel(t)
	m.usershell = mgr
	m.ta.SetValue("hello chat")
	// We can't easily assert the appendUserMessage path without a
	// real log, but we can verify the textarea cleared and the
	// returned cmd is non-nil.
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("submit returned nil")
	}
	if m.ta.Value() != "" {
		t.Errorf("textarea should clear; got %q", m.ta.Value())
	}
}

func TestSubmit_EmptyInputIsNoop(t *testing.T) {
	m := newTestModel(t)
	m.usershell = minimalManager(t)
	defer m.usershell.Close()
	m.ta.SetValue("   ")
	if cmd := m.submit(); cmd != nil {
		t.Error("whitespace-only input should produce no cmd")
	}
}

func TestSubmitBackgroundShell_Routes(t *testing.T) {
	recorder := &recordingRunner{}
	mgr := usershell.New(usershell.Options{
		Runner:    recorder,
		OutputDir: t.TempDir(),
	})
	defer mgr.Close()
	m := newTestModel(t)
	m.usershell = mgr
	m.ta.SetValue("!tail -f /tmp/x")
	cmd := m.submitBackgroundShell()
	if cmd == nil {
		t.Fatal("submitBackgroundShell returned nil")
	}
	msg := cmd()
	status, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", msg)
	}
	if !strings.Contains(status.text, "bg") {
		t.Errorf("status should announce bg: %q", status.text)
	}
}

func TestPumpUserShellCmd_DeliversUpdate(t *testing.T) {
	ch := make(chan usershell.Update, 1)
	cmd := pumpUserShellCmd(ch)
	go func() {
		ch <- usershell.Update{JobID: "j-1", State: usershell.StateRunning}
	}()
	msg := cmd()
	u, ok := msg.(userShellUpdateMsg)
	if !ok {
		t.Fatalf("expected userShellUpdateMsg, got %T", msg)
	}
	if u.u.JobID != "j-1" {
		t.Errorf("update payload mismatch: %+v", u.u)
	}
}

func TestPumpUserShellCmd_DetectsClosedChannel(t *testing.T) {
	ch := make(chan usershell.Update)
	close(ch)
	cmd := pumpUserShellCmd(ch)
	msg := cmd()
	if _, ok := msg.(userShellSubscriptionClosedMsg); !ok {
		t.Errorf("expected userShellSubscriptionClosedMsg, got %T", msg)
	}
}

// recordingRunner satisfies the usershell.runner interface by
// surfacing through the Options.Runner seam. The runner package-
// internal interface is small enough that this duck-typed struct
// matches it; if the signature ever changes, this compiles-error
// loudly and tests are forced to update.
type recordingRunner struct {
	mu     sync.Mutex
	starts int
}

func (r *recordingRunner) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

func (r *recordingRunner) Start(ctx context.Context, command, cwd string) (io.Reader, func() (int, error), func(), error) {
	r.mu.Lock()
	r.starts++
	r.mu.Unlock()
	reader := strings.NewReader("")
	wait := func() (int, error) { return 0, nil }
	kill := func() {}
	return reader, wait, kill, nil
}

// Compile-time check that Model's Update doesn't panic when handed
// the user-shell messages without a wired manager. Defensive against
// future Update refactors that forget the nil-channel branch.
func TestUpdate_UserShellMsg_NilManagerIsSafe(t *testing.T) {
	m := newTestModel(t)
	// userShellUpdateMsg should be a no-op (and not panic) without a
	// subscription channel.
	model, cmd := m.Update(userShellUpdateMsg{u: usershell.Update{}})
	if cmd != nil {
		t.Error("expected nil cmd when channel not subscribed")
	}
	if model == nil {
		t.Error("Update returned nil model")
	}
}

func TestUpdate_UserShellClosed_ClearsSubCh(t *testing.T) {
	ch := make(chan usershell.Update, 1)
	m := newTestModel(t)
	m.userShellSubCh = ch
	model, _ := m.Update(userShellSubscriptionClosedMsg{})
	mm := model.(*Model)
	if mm.userShellSubCh != nil {
		t.Error("subscription channel should be cleared")
	}
}

var _ tea.Msg = userShellUpdateMsg{}
var _ tea.Msg = userShellSubscriptionClosedMsg{}
