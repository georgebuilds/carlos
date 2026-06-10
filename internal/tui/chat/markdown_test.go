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

// TestStripVisibleLeadingMargin_PlainTwoSpaces is the baseline:
// a line that starts with raw "  content" gets the 2-space margin
// removed cleanly.
func TestStripVisibleLeadingMargin_PlainTwoSpaces(t *testing.T) {
	got := stripVisibleLeadingMargin("  hello", 2)
	if got != "hello" {
		t.Errorf("plain 2-space strip: got %q, want %q", got, "hello")
	}
}

// TestStripVisibleLeadingMargin_AnsiBeforeMargin is the regression
// test for the alignment field report. Glamour emits each row as
// "\x1b[...m\x1b[...m  <content>" — the visible 2-space margin sits
// AFTER the ANSI escape codes, not at the start of the raw line. A
// naive strings.TrimPrefix(ln, "  ") matches nothing and the body
// ends up double-indented (col 6, matching the bordered tool-card
// emojis, instead of col 4 matching the user-message body). The
// fix walks past leading CSI escapes and removes the 2 visible
// space cells.
func TestStripVisibleLeadingMargin_AnsiBeforeMargin(t *testing.T) {
	in := "\x1b[38;5;252m\x1b[0m\x1b[38;5;252m\x1b[0m  Yo! I'm carlos."
	got := stripVisibleLeadingMargin(in, 2)
	// The escape codes survive; the 2 visible margin spaces don't.
	want := "\x1b[38;5;252m\x1b[0m\x1b[38;5;252m\x1b[0mYo! I'm carlos."
	if got != want {
		t.Errorf("ansi-prefix strip:\n got %q\nwant %q", got, want)
	}
}

// TestStripVisibleLeadingMargin_NoMarginNoChange guards content
// that already starts flush-left (e.g. fenced code blocks where
// glamour drops the standard margin). Leave it alone.
func TestStripVisibleLeadingMargin_NoMarginNoChange(t *testing.T) {
	in := "no leading spaces here"
	if got := stripVisibleLeadingMargin(in, 2); got != in {
		t.Errorf("no-margin line should be unchanged: got %q", got)
	}
}

// TestStripVisibleLeadingMargin_TabIsNotConsumed pins that we only
// strip space bytes. A tab in the leading position is content and
// must survive (otherwise we'd silently eat indentation inside code
// blocks).
func TestStripVisibleLeadingMargin_TabIsNotConsumed(t *testing.T) {
	in := "\tcode line"
	if got := stripVisibleLeadingMargin(in, 2); got != in {
		t.Errorf("tab should not be consumed: got %q", got)
	}
}

// TestStripVisibleLeadingMargin_FewerSpacesThanMax handles the
// short-margin case: a line with only 1 leading visible space gets
// that one space removed but doesn't reach into content.
func TestStripVisibleLeadingMargin_FewerSpacesThanMax(t *testing.T) {
	got := stripVisibleLeadingMargin(" x", 2)
	if got != "x" {
		t.Errorf("1-space line: got %q, want %q", got, "x")
	}
}

// TestStripVisibleLeadingMargin_MalformedEscapeBails covers a
// truncated CSI sequence (no terminating 'm'). The helper must
// return the input untouched rather than silently truncating
// content past the bad escape.
func TestStripVisibleLeadingMargin_MalformedEscapeBails(t *testing.T) {
	in := "\x1b[38;5"
	if got := stripVisibleLeadingMargin(in, 2); got != in {
		t.Errorf("malformed escape: got %q, want %q (unchanged)", got, in)
	}
}

// TestFoldAvatarOntoMarkdown_ContinuationAlignsWithFirstLineBody is
// the end-to-end regression for the alignment bug. Before the fix
// the continuation rows landed two cells to the right of the first
// row's body (because the un-trimmed glamour margin stacked on top
// of our continuationPad). Assert that the visible column of the
// first body character on line 1 matches the visible column of the
// first body character on the wrapped second line.
func TestFoldAvatarOntoMarkdown_ContinuationAlignsWithFirstLineBody(t *testing.T) {
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(40),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	// Long paragraph that glamour will wrap to ≥2 lines.
	out := renderAssistantMarkdown(
		"This is a sufficiently long paragraph that glamour will be forced to wrap it across multiple visible lines so the test can compare indent columns.",
		80, r,
	)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected ≥2 rendered lines for wrapped paragraph; got %d:\n%s", len(lines), out)
	}
	// First-line body starts AFTER the avatar + ": " prefix (4 cells).
	first := visibleString(lines[0])
	if !strings.HasPrefix(first, "🧢: ") {
		t.Fatalf("line 1 should start with 🧢:\n%q", first)
	}
	firstBody := strings.TrimPrefix(first, "🧢: ")
	// Find the first wrapped continuation line that has visible text.
	var contBody string
	for _, ln := range lines[1:] {
		v := visibleString(ln)
		if strings.TrimSpace(v) == "" {
			continue
		}
		contBody = v
		break
	}
	if contBody == "" {
		t.Fatalf("no non-blank continuation line found in:\n%s", out)
	}
	// The continuation line's leading-space count must equal the
	// avatar-prefix width (4 cells). Anything more means the
	// un-trimmed glamour margin is leaking through and the body
	// has slid right by 2 cells.
	if got := leadingSpaceCount(contBody); got != 4 {
		t.Errorf("continuation indent = %d cells, want 4 (avatar-prefix width). Bodies misalign:\nline1 body %q\ncont body  %q",
			got, firstBody, contBody)
	}
}

// visibleString strips CSI escape codes from s so leading-space
// arithmetic counts visible cells, not raw bytes. Mirrors the
// stripper used by stripVisibleLeadingMargin's tests.
func visibleString(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func leadingSpaceCount(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		n++
	}
	return n
}
