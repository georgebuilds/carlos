package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"

	"github.com/georgebuilds/carlos/internal/theme"
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

// TestGlamourStyleFor_VariantMatrix pins the variant→style mapping
// the boot-time renderer uses. Field bug: leaving WithAutoStyle in
// place caused glamour to invoke termenv's OSC 11 background-color
// query against the live terminal; in tabbed Ghostty the response
// landed inside the textarea as a literal "]11;rgb:..." escape.
// Pinning the style from carlos's pre-detected theme variant cuts
// that query off entirely.
func TestGlamourStyleFor_VariantMatrix(t *testing.T) {
	cases := []struct {
		variant theme.Variant
		want    string
	}{
		{theme.Dark, "dark"},
		{theme.Light, "light"},
		{theme.Variant(99), "notty"}, // unknown → safe fallback
	}
	for _, tc := range cases {
		if got := glamourStyleFor(tc.variant); got != tc.want {
			t.Errorf("glamourStyleFor(%v) = %q, want %q", tc.variant, got, tc.want)
		}
	}
}

// TestApplyPalette_UpdatesGlamourStyle locks the chat package's
// boot wire-up: calling ApplyPalette with a dark palette pins the
// glamour style to "dark", and a light palette flips it to "light".
// Without this regression test a future ApplyPalette refactor could
// silently re-introduce the WithAutoStyle bug.
func TestApplyPalette_UpdatesGlamourStyle(t *testing.T) {
	// Stash + restore so the test doesn't leak into siblings.
	saved := glamourStyle
	t.Cleanup(func() { glamourStyle = saved })

	ApplyPalette(theme.Palette{Variant: theme.Dark})
	if glamourStyle != "dark" {
		t.Errorf("dark variant: got %q", glamourStyle)
	}
	ApplyPalette(theme.Palette{Variant: theme.Light})
	if glamourStyle != "light" {
		t.Errorf("light variant: got %q", glamourStyle)
	}
}

// TestNewMarkdownRenderer_DoesNotInvokeAutoStyle is a smoke gate
// over the boot path that ensures we build without surfacing the
// auto-style code path. We can't directly assert "termenv was not
// queried" in-process, but we CAN verify the renderer builds
// successfully under a zeroed environment (no $TERM, no
// $COLORTERM) — WithAutoStyle would have nothing to detect and
// would fall back to notty; our pinned style still produces a
// styled output.
func TestNewMarkdownRenderer_DoesNotInvokeAutoStyle(t *testing.T) {
	saved := glamourStyle
	t.Cleanup(func() { glamourStyle = saved })
	glamourStyle = "dark"

	t.Setenv("TERM", "")
	t.Setenv("COLORTERM", "")

	r, err := newMarkdownRenderer(80)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	out, err := r.Render("**bold**")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out, "**bold**") {
		t.Errorf("dark style should have rendered bold markdown; got raw asterisks:\n%q", out)
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
