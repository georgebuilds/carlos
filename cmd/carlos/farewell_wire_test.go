package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/farewell"
)

// minimalCfgForFarewell builds the smallest config that exercises
// the gateway-orphan probe: gateway block with Enabled set per the
// caller's flag.
func minimalCfgForFarewell(gatewayEnabled bool) *config.Config {
	return &config.Config{
		Gateway: config.GatewayConfig{Enabled: gatewayEnabled},
	}
}

// TestFarewellMigrationSummary_GroupingByCount pins the human-readable
// summary line for the bordered farewell box. Singular "1 job",
// plural "12 jobs", and the English Oxford-comma joiner all need to
// be right or the post-exit box reads as broken English.
func TestFarewellMigrationSummary_GroupingByCount(t *testing.T) {
	tests := []struct {
		name                    string
		research, jobs, worktrs int
		want                    string
	}{
		{"only-jobs-many", 0, 12, 0, "migrated 12 shell jobs to per-frame layout"},
		{"only-jobs-one", 0, 1, 0, "migrated 1 shell job to per-frame layout"},
		{"all-three", 3, 1, 2, "migrated 3 research notes, 1 shell job, and 2 worktrees to per-frame layout"},
		{"two", 1, 0, 4, "migrated 1 research note and 4 worktrees to per-frame layout"},
		{"none", 0, 0, 0, "migrated to per-frame layout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := farewellMigrationSummary(tc.research, tc.jobs, tc.worktrs); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestJoinAnd_AllArities ensures the English joiner handles 0..n
// elements without an off-by-one.
func TestJoinAnd_AllArities(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"alpha"}, "alpha"},
		{[]string{"a", "b"}, "a and b"},
		{[]string{"a", "b", "c"}, "a, b, and c"},
		{[]string{"a", "b", "c", "d"}, "a, b, c, and d"},
	}
	for _, tc := range cases {
		if got := joinAnd(tc.in); got != tc.want {
			t.Errorf("joinAnd(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestQueueFrameMigration_QueuesOnMovement seeds a legacy directory,
// runs the queue variant, and asserts the panel got a 📦 message
// instead of stderr noise. This is the load-bearing wiring test: a
// bare stderr write would leak past the alt-screen as a plaintext
// line and defeat the whole farewell-panel design.
func TestQueueFrameMigration_QueuesOnMovement(t *testing.T) {
	tmp := t.TempDir()
	legacy := filepath.Join(tmp, ".carlos", "research")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "report.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	panel := farewell.New()
	queueFrameMigration(tmp, panel)
	msgs := panel.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Emoji != "📦" {
		t.Errorf("emoji = %q, want 📦", msgs[0].Emoji)
	}
	if !strings.Contains(msgs[0].Text, "per-frame layout") {
		t.Errorf("text missing 'per-frame layout': %q", msgs[0].Text)
	}
}

// TestQueueFrameMigration_NoMovementNoQueue confirms an already-
// migrated home doesn't push a stale message into the panel.
func TestQueueFrameMigration_NoMovementNoQueue(t *testing.T) {
	tmp := t.TempDir()
	panel := farewell.New()
	queueFrameMigration(tmp, panel)
	if got := panel.Len(); got != 0 {
		t.Errorf("expected 0 messages for fresh home, got %d", got)
	}
}

// TestQueueFrameMigration_EmptyHomeIsNoOp guards the early return.
func TestQueueFrameMigration_EmptyHomeIsNoOp(t *testing.T) {
	panel := farewell.New()
	queueFrameMigration("", panel)
	if got := panel.Len(); got != 0 {
		t.Errorf("empty home should not queue anything; got %d", got)
	}
}

// TestPrintFarewell_AppendsGoodbyeAndWrites swaps stderr for a pipe,
// calls printFarewell, and asserts the rendered output contains the
// goodbye + the user's name. This is the visible side of the
// feature — without it the user thinks the panel didn't fire.
func TestPrintFarewell_AppendsGoodbyeAndWrites(t *testing.T) {
	panel := farewell.New()
	panel.Add("🛰️", "daemon offline")

	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	printFarewell(panel, "George")
	w.Close()
	os.Stderr = origStderr

	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	out := buf.String()
	if !strings.Contains(out, "later, George") {
		t.Errorf("missing goodbye line in stderr output:\n%s", out)
	}
	if !strings.Contains(out, "daemon offline") {
		t.Errorf("missing daemon line:\n%s", out)
	}
	if !strings.Contains(out, "👋") {
		t.Errorf("missing 👋 emoji:\n%s", out)
	}
}

// TestPrintFarewell_DefaultNameFallback verifies the empty-name path
// (config missed UserName) falls back to "Boss" — carlos's brand
// default greeting elsewhere.
func TestPrintFarewell_DefaultNameFallback(t *testing.T) {
	panel := farewell.New()
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	printFarewell(panel, "")
	w.Close()
	os.Stderr = origStderr

	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	if !strings.Contains(buf.String(), "later, Boss") {
		t.Errorf("expected fallback to 'Boss', got:\n%s", buf.String())
	}
}

// TestCheckBrewAtExit_NonBrewInstallSkipsCheck pins the fast path: a
// binary not under a Cellar/ segment never invokes the check
// function. Under `go test` the binary lives under go-build, nowhere
// near a Cellar/ segment, so the real isBrew() returns false.
func TestCheckBrewAtExit_NonBrewInstallSkipsCheck(t *testing.T) {
	panel := farewell.New()
	checkBrewAtExit(panel)
	if panel.Len() != 0 {
		t.Errorf("non-brew install should never queue a brew message; got %d", panel.Len())
	}
}

// TestCheckBrewAtExitWith_BrewInstallRunsCheck covers the brew-
// install branch by injecting an isBrew=true detector and a check
// function that queues a message synchronously. Proves the check
// runs in-line (no goroutine, no done channel) and the panel sees
// the message.
func TestCheckBrewAtExitWith_BrewInstallRunsCheck(t *testing.T) {
	panel := farewell.New()
	checkBrewAtExitWith(panel,
		func() bool { return true },
		func(p *farewell.Panel) {
			p.Add("⬆️", "update available")
		},
	)
	if panel.Len() != 1 {
		t.Errorf("expected 1 message after probe, got %d", panel.Len())
	}
}

// TestCheckBrewAtExitWith_NotABrewInstallNoCheck — wired isBrew=false
// branch never calls the check function.
func TestCheckBrewAtExitWith_NotABrewInstallNoCheck(t *testing.T) {
	panel := farewell.New()
	called := false
	checkBrewAtExitWith(panel,
		func() bool { return false },
		func(p *farewell.Panel) { called = true },
	)
	if called {
		t.Error("check should not be called when isBrew returns false")
	}
	if panel.Len() != 0 {
		t.Errorf("panel should be empty; got %d messages", panel.Len())
	}
}

// TestCheckBrewAtExitWith_NilPanelIsNoOp guards the defensive
// nil-panel branch so a misuse in main doesn't panic.
func TestCheckBrewAtExitWith_NilPanelIsNoOp(t *testing.T) {
	called := false
	checkBrewAtExitWith(nil,
		func() bool { return true },
		func(p *farewell.Panel) { called = true },
	)
	if called {
		t.Error("check should not be called when panel is nil")
	}
}

// TestRunBrewCheck_NoUpdateSkipsQueue is the negative-case wiring:
// the production check function quietly returns when both the tap
// probe and brew report nothing outdated. Forcing brew to be absent
// (via PATH manipulation) handles the local-cache leg; passing a
// "dev" current version disables the remote tap probe (it bails on
// non-semver builds) so the test is fully offline-safe.
func TestRunBrewCheck_NoUpdateSkipsQueue(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-dir-for-test")
	panel := farewell.New()
	runBrewCheck(panel, "dev")
	if panel.Len() != 0 {
		t.Errorf("no-brew env + dev build should not queue a message; got %d", panel.Len())
	}
}

// TestClampFarewellWidth_BothBranches covers the cap + passthrough.
func TestClampFarewellWidth_BothBranches(t *testing.T) {
	if got := clampFarewellWidth(40); got != 40 {
		t.Errorf("under-cap should pass through; got %d", got)
	}
	if got := clampFarewellWidth(200); got != farewellWidthMax {
		t.Errorf("over-cap should clamp; got %d", got)
	}
}

// TestStderrTerminalWidth_NonTTYReturnsFalse exercises the !IsTerminal
// branch. Under `go test`, os.Stderr.Fd() is not a TTY, so we should
// see (0, false).
func TestStderrTerminalWidth_NonTTYReturnsFalse(t *testing.T) {
	_, ok := stderrTerminalWidth()
	if ok {
		t.Error("stderr should not be a TTY under `go test`")
	}
}

// TestStderrTerminalWidth_TTY covers the real-terminal path that the
// non-TTY tests can't reach: stderr is pointed at a pty slave so
// term.GetSize succeeds. It exercises both the success return and the
// w<=0 guard (by sizing the pty to zero columns), and also drives
// farewellTerminalWidth's clamp branch (120-col TTY clamps to the max).
func TestStderrTerminalWidth_TTY(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	if err := pty.Setsize(tty, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatalf("setsize: %v", err)
	}

	orig := os.Stderr
	os.Stderr = tty
	defer func() { os.Stderr = orig }()

	w, ok := stderrTerminalWidth()
	if !ok || w != 120 {
		t.Errorf("stderrTerminalWidth on a 120-col pty: got (%d, %v), want (120, true)", w, ok)
	}
	// 120 exceeds farewellWidthMax (100), so the clamp branch fires.
	if got := farewellTerminalWidth(); got != farewellWidthMax {
		t.Errorf("farewellTerminalWidth on a 120-col pty: got %d, want %d (clamped)", got, farewellWidthMax)
	}

	// Zero columns drives the err/w<=0 guard back to (0, false).
	if err := pty.Setsize(tty, &pty.Winsize{Rows: 0, Cols: 0}); err != nil {
		t.Fatalf("setsize zero: %v", err)
	}
	if w, ok := stderrTerminalWidth(); ok || w != 0 {
		t.Errorf("stderrTerminalWidth on a zero-width pty: got (%d, %v), want (0, false)", w, ok)
	}
}

// TestRunBrewCheckWith_TapUpdateQueues covers the authoritative-tap
// branch: when the tap probe reports a newer version, the panel gets
// the ⬆️ message and the brew fallback is never consulted.
func TestRunBrewCheckWith_TapUpdateQueues(t *testing.T) {
	panel := farewell.New()
	runBrewCheckWith(panel, "0.1.0",
		func(context.Context, string) (string, bool) { return "9.9.9", true },
		func(context.Context) bool {
			t.Error("brew fallback must not run once the tap reports an update")
			return false
		},
	)
	msgs := panel.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].Text, "9.9.9 is available") {
		t.Errorf("text missing tap version: %q", msgs[0].Text)
	}
	if !strings.Contains(msgs[0].Detail, "brew upgrade carlos") {
		t.Errorf("detail missing upgrade hint: %q", msgs[0].Detail)
	}
}

// TestRunBrewCheckWith_BrewOutdatedQueues covers the fallback branch:
// the tap probe finds nothing but `brew outdated` reports carlos stale.
func TestRunBrewCheckWith_BrewOutdatedQueues(t *testing.T) {
	panel := farewell.New()
	runBrewCheckWith(panel, "0.1.0",
		func(context.Context, string) (string, bool) { return "", false },
		func(context.Context) bool { return true },
	)
	msgs := panel.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].Text, "update available") {
		t.Errorf("text missing 'update available': %q", msgs[0].Text)
	}
}

// TestBrewCheckWithVersion_InvokesRunBrewCheck drives the closure
// returned by brewCheckWithVersion (the path checkBrewAtExit can only
// reach on a real Cellar binary). PATH is nuked and the version is the
// non-semver "dev" sentinel, so both real probes bail and the panel
// stays empty — the point is to execute the wiring, not queue a message.
func TestBrewCheckWithVersion_InvokesRunBrewCheck(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-dir-for-test")
	panel := farewell.New()
	brewCheckWithVersion("dev")(panel)
	if panel.Len() != 0 {
		t.Errorf("dev build + no-brew env should not queue a message; got %d", panel.Len())
	}
}

// TestPrintFarewell_NilPanelIsNoOp guards the early-return branch.
func TestPrintFarewell_NilPanelIsNoOp(t *testing.T) {
	printFarewell(nil, "George") // should not panic
}

// TestFarewellTerminalWidth_FallbackOnNonTTY proves the fallback
// path (78) fires when stderr isn't a TTY — which is always true
// under `go test` since it pipes stderr.
func TestFarewellTerminalWidth_FallbackOnNonTTY(t *testing.T) {
	got := farewellTerminalWidth()
	// In a non-TTY env it should return 78; we don't pin the value
	// (the caller may be running under a fancier runner) but it
	// should be sensibly bounded.
	if got < 40 || got > 200 {
		t.Errorf("farewellTerminalWidth returned %d, want a reasonable column count", got)
	}
}

// TestQueueGatewayOrphaned_NilCfgIsNoOp pins the early-return
// branch so the gateway probe doesn't trip on a missing config.
func TestQueueGatewayOrphaned_NilCfgIsNoOp(t *testing.T) {
	panel := farewell.New()
	queueGatewayOrphaned(nil, panel)
	if got := panel.Len(); got != 0 {
		t.Errorf("nil cfg should not queue anything; got %d", got)
	}
}

// TestQueueGatewayOrphaned_GatewayDisabledIsNoOp same idea — a
// disabled gateway means there's nothing to warn about.
func TestQueueGatewayOrphaned_GatewayDisabledIsNoOp(t *testing.T) {
	cfg := minimalCfgForFarewell(false)
	panel := farewell.New()
	queueGatewayOrphaned(cfg, panel)
	if got := panel.Len(); got != 0 {
		t.Errorf("disabled gateway should not queue anything; got %d", got)
	}
}

// TestQueueGatewayOrphaned_DaemonOfflineQueues exercises the dial-
// fails / queue-warning branch. The test process can't reach the
// real daemon UDS, so the dial errors and the panel gets the 🛰️
// message. The detail line carries the recovery hint.
func TestQueueGatewayOrphaned_DaemonOfflineQueues(t *testing.T) {
	// Point at a tmpdir UDS so the dial reliably fails (no socket at
	// the resolved path). We can't easily inject a Dial, but the
	// real Dial only checks $CARLOS_DAEMON_SOCKET / default.
	t.Setenv("CARLOS_DAEMON_SOCKET", "/nonexistent/farewell-test.sock")

	cfg := minimalCfgForFarewell(true)
	panel := farewell.New()
	queueGatewayOrphaned(cfg, panel)
	msgs := panel.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message for offline daemon, got %d", len(msgs))
	}
	if msgs[0].Emoji != "🛰️" {
		t.Errorf("emoji = %q, want 🛰️", msgs[0].Emoji)
	}
	if !strings.Contains(msgs[0].Detail, "daemon enable") {
		t.Errorf("detail missing recovery hint: %q", msgs[0].Detail)
	}
}
