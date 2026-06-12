package chat

import (
	"strings"
	"testing"
)

// TestOneLine_TrimsToFirstNewline confirms oneLine collapses a
// multi-line input to just its first line.
func TestOneLine_TrimsToFirstNewline(t *testing.T) {
	got := oneLine("ls -la /tmp\nsecond line\nthird", 80)
	if got != "ls -la /tmp" {
		t.Errorf("oneLine should keep only the first line; got %q", got)
	}
}

// TestOneLine_TruncatesWideLine confirms a single line wider than maxW
// is cut with an ellipsis.
func TestOneLine_TruncatesWideLine(t *testing.T) {
	long := strings.Repeat("x", 50)
	got := oneLine(long, 10)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("over-width line should end with ellipsis; got %q", got)
	}
	if len([]rune(got)) > 10 {
		t.Errorf("truncated line should not exceed maxW runes; got %d", len([]rune(got)))
	}
}

// TestOneLine_TooNarrowReturnsEmpty pins the maxW<4 guard.
func TestOneLine_TooNarrowReturnsEmpty(t *testing.T) {
	if got := oneLine("anything", 3); got != "" {
		t.Errorf("maxW<4 should return empty; got %q", got)
	}
}

// TestErrorCardSeparator_RepeatsRuleToWidth covers the separator
// renderer including the contentW<1 floor.
func TestErrorCardSeparator_RepeatsRuleToWidth(t *testing.T) {
	out := errorCardSeparator(8)
	if strings.Count(out, "─") != 8 {
		t.Errorf("separator should repeat the rule contentW times; got %q", out)
	}
	// Floor: contentW<1 still emits a single cell, not a panic / empty.
	floored := errorCardSeparator(0)
	if strings.Count(floored, "─") != 1 {
		t.Errorf("contentW<1 should floor to one rule cell; got %q", floored)
	}
}

// TestEnsureMarkdown_CachesRendererPerWidth proves ensureMarkdown
// rebuilds the renderer when the width changes and reuses it otherwise.
func TestEnsureMarkdown_CachesRendererPerWidth(t *testing.T) {
	m := innerModel(t)
	r1 := m.ensureMarkdown(80)
	if r1 == nil {
		t.Fatal("ensureMarkdown should build a renderer at width 80")
	}
	// Same width: returns the cached pointer.
	if r2 := m.ensureMarkdown(80); r2 != r1 {
		t.Error("same width should reuse the cached renderer")
	}
	// Different width: rebuilds.
	r3 := m.ensureMarkdown(120)
	if r3 == r1 {
		t.Error("width change should rebuild the renderer")
	}
	if m.markdownWidth != 120 {
		t.Errorf("markdownWidth should track the latest width; got %d", m.markdownWidth)
	}
}

// TestRenderFooter_StatusLineTakesPriority confirms a non-empty status
// renders above the keybind row.
func TestRenderFooter_StatusLineTakesPriority(t *testing.T) {
	m := innerModel(t)
	m.status = "saved frame config"
	m.statusKind = statusInfo
	out := m.renderFooter(120)
	if !strings.Contains(out, "saved frame config") {
		t.Errorf("footer should surface the status line; got:\n%s", out)
	}
	if !strings.Contains(out, "\n") {
		t.Errorf("status footer should stack status over the keybind row; got:\n%s", out)
	}
}

// TestRenderFooter_StartupNoticeBanner confirms the startup-notice
// banner renders above the keybind row when no status is set.
func TestRenderFooter_StartupNoticeBanner(t *testing.T) {
	m := innerModel(t)
	m.status = ""
	m.startupNotices = []string{"recovered 2 orphaned agents"}
	out := m.renderFooter(120)
	if !strings.Contains(out, "recovered 2 orphaned agents") {
		t.Errorf("footer should surface the startup notice; got:\n%s", out)
	}
}

// TestRenderFooter_CwdHintLine confirms the F-8 cwd-hint footer renders
// when set and not muted.
func TestRenderFooter_CwdHintLine(t *testing.T) {
	m := innerModel(t)
	m.status = ""
	m.startupNotices = nil
	m.footerHint = "you are in /repo which matches frame `work`."
	out := m.renderFooter(120)
	if !strings.Contains(out, "matches frame") {
		t.Errorf("footer should surface the cwd hint; got:\n%s", out)
	}
}

// TestRenderFooter_NarrowDropsTip confirms that at a width too small to
// fit both the keybind hints and the right-aligned tip, the tip is
// dropped (the default branch) rather than wrapped.
func TestRenderFooter_NarrowDropsTip(t *testing.T) {
	m := innerModel(t)
	m.status = ""
	m.startupNotices = nil
	m.footerHint = ""
	// Width 30 is narrower than hints+tip+2, so the tip is dropped.
	out := m.renderFooter(30)
	if strings.Contains(out, "/help") {
		t.Errorf("narrow footer should drop the /help tip; got:\n%s", out)
	}
	// The keybind hints themselves still render.
	if !strings.Contains(out, "send") {
		t.Errorf("narrow footer should still show keybind hints; got:\n%s", out)
	}
}

// TestView_TooSmallTerminalRefuses confirms View bails with the
// minimum-size notice below the cell budget.
func TestView_TooSmallTerminalRefuses(t *testing.T) {
	m := innerModel(t)
	m.width = 10
	m.height = 4
	out := m.View()
	if !strings.Contains(out, "at least") {
		t.Errorf("undersized terminal should print the minimum-size notice; got %q", out)
	}
}

// TestView_QuittingRendersBlank confirms a quitting model renders empty
// so bubbletea's final Print is clean.
func TestView_QuittingRendersBlank(t *testing.T) {
	m := innerModel(t)
	m.quitting = true
	if out := m.View(); out != "" {
		t.Errorf("quitting View should be blank; got %q", out)
	}
}

// TestView_ZeroSizeFallsBackToDefault confirms View applies the 100x30
// fallback when no WindowSizeMsg has landed yet.
func TestView_ZeroSizeFallsBackToDefault(t *testing.T) {
	m := innerModel(t)
	m.width = 0
	m.height = 0
	out := m.View()
	// 100x30 is above the minimum, so we render the box, not the notice.
	if strings.Contains(out, "at least") {
		t.Errorf("zero-size should fall back to 100x30 and render the box; got:\n%s", out)
	}
}
