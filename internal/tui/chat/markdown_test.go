package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
)

// TestRenderAssistantMarkdown_BoldAndCode is the headline behavior:
// raw asterisks should be gone from the rendered output, replaced with
// ANSI styles. Inline backticks the same. This is the screenshot bug
// the user flagged that motivated wiring glamour in.
//
// We pin to the "dark" style explicitly because go test runs without
// a TTY, where AutoStyle picks "notty" which preserves raw markdown -
// fine for production users (they'd be on a styled terminal) but
// the wrong renderer to assert against here.
func TestRenderAssistantMarkdown_BoldAndCode(t *testing.T) {
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(78),
		glamour.WithEmoji(),
	)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	out := renderAssistantMarkdown("Here is **bold** and `inline code` together.", 80, r)

	if strings.Contains(out, "**bold**") {
		t.Errorf("output still contains raw '**bold**' markdown:\n%q", out)
	}
	if strings.Contains(out, "`inline code`") {
		t.Errorf("output still contains raw '`inline code`' markdown:\n%q", out)
	}
	if !strings.Contains(out, "bold") || !strings.Contains(out, "inline code") {
		t.Errorf("rendered output lost the actual words:\n%q", out)
	}
	if !strings.Contains(out, "🧢") {
		t.Errorf("rendered output missing the 🧢 avatar header:\n%q", out)
	}
}

// TestRenderAssistantMarkdown_NilFallback proves the fallback path
// engages when the renderer is unavailable - critical because the
// Model's lazy init returns nil on glamour setup failure and we don't
// want assistant messages to vanish.
func TestRenderAssistantMarkdown_NilFallback(t *testing.T) {
	out := renderAssistantMarkdown("plain text", 80, nil)
	if !strings.Contains(out, "plain text") {
		t.Errorf("nil renderer should fall back to plain text, got:\n%s", out)
	}
	if !strings.Contains(out, "🧢") {
		t.Errorf("plain fallback should still show the avatar:\n%s", out)
	}
}

// TestRenderAssistantMarkdown_NarrowFallback guards the small-width
// branch: glamour wraps too aggressively in <24 columns, so we drop
// to plain rendering rather than smear the content across many tiny
// lines.
func TestRenderAssistantMarkdown_NarrowFallback(t *testing.T) {
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(78),
	)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	out := renderAssistantMarkdown("**bold**", 10, r)
	// Plain fallback retains the raw asterisks since it's not running
	// the markdown parser. That's the signal of the fallback path.
	if !strings.Contains(out, "**bold**") {
		t.Errorf("narrow viewport should NOT have markdown rendered; got:\n%q", out)
	}
}

// TestNewMarkdownRenderer_NarrowWidthClampsToFloor covers the
// width-floor branch so the renderer never gets handed a 5-column
// wrap width that produces garbage output.
func TestNewMarkdownRenderer_NarrowWidthClampsToFloor(t *testing.T) {
	r, err := newMarkdownRenderer(5) // below mdMinWidth (24)
	if err != nil {
		t.Fatalf("renderer should still build at narrow width: %v", err)
	}
	if r == nil {
		t.Fatal("renderer should be non-nil even when input width is tiny")
	}
}

// TestRenderAssistantMarkdown_EmptyTextFallbackToPlain pins the
// post-render "empty body" branch: glamour rendered nothing useful,
// so we surface the avatar block instead of an emoji with no body.
func TestRenderAssistantMarkdown_EmptyTextFallbackToPlain(t *testing.T) {
	r, err := newMarkdownRenderer(80)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	out := renderAssistantMarkdown("", 80, r)
	if !strings.Contains(out, "🧢") {
		t.Errorf("empty text should still render the avatar:\n%q", out)
	}
}

// TestTrimPerLineRight verifies the glamour-background-fill trimmer.
// Without this, every transcript line ends in dozens of spaces, which
// breaks mouse copy-paste and inflates the rendered transcript width.
func TestTrimPerLineRight(t *testing.T) {
	in := "first line    \nsecond  \n\tthird\t  \n"
	want := "first line\nsecond\n\tthird\n"
	if got := trimPerLineRight(in); got != want {
		t.Errorf("trimPerLineRight = %q, want %q", got, want)
	}
}
