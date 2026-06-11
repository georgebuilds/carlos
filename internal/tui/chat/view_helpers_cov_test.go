package chat

import (
	"strings"
	"testing"
)

func TestPlural(t *testing.T) {
	if got := plural(1); got != "" {
		t.Errorf("plural(1) = %q want \"\"", got)
	}
	for _, n := range []int{0, 2, 5, 100} {
		if got := plural(n); got != "s" {
			t.Errorf("plural(%d) = %q want \"s\"", n, got)
		}
	}
}

func TestPreviewLines_EmptyAndTrim(t *testing.T) {
	if got := previewLines("", 80, 5); got != "" {
		t.Errorf("empty input should preview empty; got %q", got)
	}
	if got := previewLines("\n\n\n", 80, 5); got != "" {
		t.Errorf("whitespace-only should preview empty; got %q", got)
	}
}

func TestPreviewLines_TruncatesWithMoreFooter(t *testing.T) {
	in := "a\nb\nc\nd\ne\nf\ng"
	got := previewLines(in, 80, 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("want 3 rows + footer; got %d:\n%s", len(lines), got)
	}
	if !strings.Contains(lines[3], "more line") {
		t.Errorf("footer should report remaining lines; got %q", lines[3])
	}
	// 7 total - 3 shown = 4 remaining.
	if !strings.Contains(lines[3], "4 more lines") {
		t.Errorf("footer count wrong; got %q", lines[3])
	}
}

func TestPreviewLines_WrapsLongLineAndClampsWidth(t *testing.T) {
	long := strings.Repeat("x", 50)
	got := previewLines(long, 10, 3)
	for _, ln := range strings.Split(got, "\n") {
		if len(ln) > 10 && !strings.Contains(ln, "more line") {
			t.Errorf("wrapped line exceeds width: %q", ln)
		}
	}
}

func TestPreviewLines_WrapHittingMaxRowsEmitsFooter(t *testing.T) {
	// A single very long line that wraps past maxRows must terminate
	// with the "… N more line(s)" footer (the inner-loop break path).
	long := strings.Repeat("y", 200)
	got := previewLines(long, 10, 2)
	if !strings.Contains(got, "more line") {
		t.Errorf("expected mid-wrap footer; got:\n%s", got)
	}
}

func TestPreviewLines_NarrowWidthFloor(t *testing.T) {
	// width < 4 is floored to 4; just confirm no panic + bounded rows.
	got := previewLines("abcdefgh\nij", 1, 2)
	if got == "" {
		t.Error("expected non-empty preview")
	}
}

func TestClampLines_TruncatesAtMaxRows(t *testing.T) {
	in := "1\n2\n3\n4\n5"
	got := clampLines(in, 80, 2)
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected truncation footer; got:\n%s", got)
	}
	if strings.Count(got, "\n") != 2 { // 2 rows + footer
		t.Errorf("row count off; got:\n%s", got)
	}
}

func TestClampLines_WrapTruncation(t *testing.T) {
	long := strings.Repeat("z", 100)
	got := clampLines(long, 10, 2)
	if !strings.Contains(got, "truncated") {
		t.Errorf("wrap past maxRows should truncate; got:\n%s", got)
	}
}

func TestClampLines_WidthFloorAndPassthrough(t *testing.T) {
	// width 0 floors to 1, so "ab" wraps to two single-char rows.
	if got := clampLines("ab", 0, 5); got != "a\nb" {
		t.Errorf("width floor should wrap to width 1; got %q", got)
	}
	if got := clampLines("short", 80, 5); got != "short" {
		t.Errorf("under-cap passthrough; got %q", got)
	}
}

func TestStatusColor(t *testing.T) {
	if got := statusColor(statusError); string(got) != string(colorWarn) {
		t.Errorf("statusError color: %q", got)
	}
	if got := statusColor(statusWarn); string(got) != string(colorWarn) {
		t.Errorf("statusWarn color: %q", got)
	}
	if got := statusColor(statusInfo); string(got) != string(colorAccent) {
		t.Errorf("statusInfo color: %q", got)
	}
}

func TestFooterTip(t *testing.T) {
	if got := footerTip(true); got != "read-only" {
		t.Errorf("read-only tip: %q", got)
	}
	if got := footerTip(false); !strings.Contains(got, "/help") {
		t.Errorf("input-mode tip should mention /help: %q", got)
	}
}

func TestIsNoColor(t *testing.T) {
	// We don't control the theme here; just confirm the predicate
	// agrees with the accent color it inspects (no panic, consistent).
	want := colorAccent == ""
	if got := isNoColor(); got != want {
		t.Errorf("isNoColor() = %v want %v (accent=%q)", got, want, colorAccent)
	}
}
