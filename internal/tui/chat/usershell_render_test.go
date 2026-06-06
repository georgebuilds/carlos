package chat

import (
	"strings"
	"testing"
	"time"
)

func TestRenderUserShellEntry_RunningShowsHint(t *testing.T) {
	e := transcriptEntry{
		kind:         entryUserShell,
		shellJobID:   "01H123",
		shellCommand: "cargo test",
		shellRunning: true,
		shellOutput:  "running 12 tests\n",
	}
	out := renderUserShellEntry(e, 80)
	for _, want := range []string{"$", "cargo test", "running 12 tests", "running"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "⌃c") || !strings.Contains(out, "⌃z") {
		t.Errorf("missing keybind hints; got:\n%s", out)
	}
}

func TestRenderUserShellEntry_CompletedSuccess(t *testing.T) {
	e := transcriptEntry{
		kind:          entryUserShell,
		shellCommand:  "true",
		shellOutput:   "",
		shellExitCode: 0,
		shellDuration: 4500 * time.Millisecond,
	}
	out := renderUserShellEntry(e, 80)
	if !strings.Contains(out, "✓ 0") {
		t.Errorf("success badge missing: %s", out)
	}
	if !strings.Contains(out, "4.5s") {
		t.Errorf("duration suffix missing: %s", out)
	}
}

func TestRenderUserShellEntry_CompletedFailure(t *testing.T) {
	e := transcriptEntry{
		kind:          entryUserShell,
		shellCommand:  "false",
		shellExitCode: 1,
		shellDuration: 2 * time.Second,
	}
	out := renderUserShellEntry(e, 80)
	if !strings.Contains(out, "✗ 1") {
		t.Errorf("failure badge missing: %s", out)
	}
}

func TestRenderUserShellEntry_Cancelled(t *testing.T) {
	e := transcriptEntry{
		kind:           entryUserShell,
		shellCommand:   "sleep 10",
		shellCancelled: true,
		shellDuration:  100 * time.Millisecond,
	}
	out := renderUserShellEntry(e, 80)
	if !strings.Contains(out, "cancelled") {
		t.Errorf("cancelled badge missing: %s", out)
	}
	// Sub-second durations don't get a suffix.
	if strings.Contains(out, "0.1s") {
		t.Errorf("sub-second duration leaked into badge: %s", out)
	}
}

func TestRenderUserShellEntry_BackgroundedAnnotated(t *testing.T) {
	e := transcriptEntry{
		kind:              entryUserShell,
		shellCommand:      "tail -f /tmp/x",
		shellBackgrounded: true,
		shellRunning:      true,
	}
	out := renderUserShellEntry(e, 80)
	if !strings.Contains(out, "background") && !strings.Contains(out, "bg") {
		t.Errorf("backgrounded annotation missing: %s", out)
	}
}

func TestRenderUserShellEntry_TruncationNote(t *testing.T) {
	e := transcriptEntry{
		kind:           entryUserShell,
		shellCommand:   "spam",
		shellOutput:    "tail of output",
		shellExitCode:  0,
		shellTruncated: 50000,
	}
	out := renderUserShellEntry(e, 80)
	if !strings.Contains(out, "50000 more bytes") {
		t.Errorf("truncation hint missing: %s", out)
	}
}

func TestRenderUserShellEntry_FailErrSurfaced(t *testing.T) {
	e := transcriptEntry{
		kind:          entryUserShell,
		shellCommand:  "no-such-shell",
		shellExitCode: -1,
		shellFailErr:  "shell missing",
	}
	out := renderUserShellEntry(e, 80)
	if !strings.Contains(out, "shell missing") {
		t.Errorf("fail err missing: %s", out)
	}
}

func TestFormatDurationSuffix(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, ""},
		{500 * time.Millisecond, ""},
		{999 * time.Millisecond, ""},
		{time.Second, " · 1.0s"},
		{4200 * time.Millisecond, " · 4.2s"},
		{90 * time.Second, " · 1m30s"},
	}
	for _, tc := range cases {
		if got := formatDurationSuffix(tc.d); got != tc.want {
			t.Errorf("formatDurationSuffix(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestIndentEachLine(t *testing.T) {
	in := "alpha\nbeta\n\ngamma"
	got := indentEachLine(in, "  ")
	want := "  alpha\n  beta\n\n  gamma"
	if got != want {
		t.Errorf("indentEachLine:\nwant: %q\n got: %q", want, got)
	}
}

func TestAnnotatePaths_AddsHyperlink(t *testing.T) {
	in := "see main.go:42 for details"
	out := annotatePaths(in)
	if !strings.Contains(out, "\x1b]8;;") {
		t.Errorf("OSC 8 prefix missing: %q", out)
	}
	if !strings.Contains(out, "main.go:42") {
		t.Errorf("display text missing: %q", out)
	}
}

func TestAnnotatePaths_HandlesMultiple(t *testing.T) {
	in := "main.go:1 and pkg/foo.go:42:5"
	out := annotatePaths(in)
	// Two distinct hyperlinks.
	if strings.Count(out, "\x1b]8;;") != 4 { // each link = open + close
		t.Errorf("expected 2 OSC 8 links (4 prefix tokens): %q", out)
	}
}

func TestAnnotatePaths_PassesThroughNonPaths(t *testing.T) {
	in := "no paths here"
	if got := annotatePaths(in); got != in {
		t.Errorf("non-path text mutated: %q", got)
	}
}

func TestAnnotatePaths_AbsolutePath(t *testing.T) {
	in := "/Users/x/y/main.go"
	out := annotatePaths(in)
	if !strings.Contains(out, "file:///Users/x/y/main.go") {
		t.Errorf("absolute path URL missing: %q", out)
	}
}

func TestAnnotatePaths_RelativePath(t *testing.T) {
	in := "see ./pkg/foo.go"
	out := annotatePaths(in)
	if !strings.Contains(out, "file://./") {
		t.Errorf("relative path URL missing: %q", out)
	}
}

func TestIsNumericTail(t *testing.T) {
	cases := map[string]bool{
		"42":    true,
		"42:5":  true,
		"":      false,
		":5":    false,
		"abc":   false,
		"42a":   false,
		"42::5": false,
	}
	for in, want := range cases {
		if got := isNumericTail(in); got != want {
			t.Errorf("isNumericTail(%q) = %v want %v", in, got, want)
		}
	}
}

func TestRenderUserShellEntry_MinimumWidth(t *testing.T) {
	e := transcriptEntry{
		kind:         entryUserShell,
		shellCommand: "ls",
		shellOutput:  "a\nb\nc",
		shellRunning: true,
	}
	// Width below the floor (20) is clamped — should not panic.
	out := renderUserShellEntry(e, 5)
	if out == "" {
		t.Error("renderer should produce output at sub-minimum width")
	}
}
