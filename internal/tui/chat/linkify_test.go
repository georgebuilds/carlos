package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// withFakeHome pins userHomeDir for one test.
func withFakeHome(t *testing.T, home string, err error) {
	t.Helper()
	prev := userHomeDir
	userHomeDir = func() (string, error) { return home, err }
	t.Cleanup(func() { userHomeDir = prev })
}

// TestOSC8_SequenceShape pins the exact escape layout the shipped
// annotatePaths established: ESC]8;;url ESC\ text ESC]8;; ESC\.
func TestOSC8_SequenceShape(t *testing.T) {
	got := osc8("file:///a/b.go", "/a/b.go")
	want := "\x1b]8;;file:///a/b.go\x1b\\/a/b.go\x1b]8;;\x1b\\"
	if got != want {
		t.Fatalf("osc8 = %q, want %q", got, want)
	}
	// lipgloss must see the wrap as zero-width (layout invariant the
	// whole slice rests on).
	if w := lipgloss.Width(got); w != len("/a/b.go") {
		t.Fatalf("visible width = %d, want %d", w, len("/a/b.go"))
	}
}

func TestFileURL(t *testing.T) {
	withFakeHome(t, "/home/george", nil)
	cases := []struct{ in, want string }{
		{"/Users/g/x.go", "file:///Users/g/x.go"},
		{"~/notes.md", "file:///home/george/notes.md"},
		{"~/a/b.go", "file:///home/george/a/b.go"},
		{"rel/path.go", "file://./rel/path.go"}, // annotatePaths convention
	}
	for _, tc := range cases {
		if got := fileURL(tc.in); got != tc.want {
			t.Errorf("fileURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFileURL_HomeUnresolvable(t *testing.T) {
	withFakeHome(t, "", os.ErrNotExist)
	if got := fileURL("~/x.go"); got != "file://~/x.go" {
		t.Errorf("degraded ~ URL = %q, want literal passthrough", got)
	}
}

// TestLinkifyText_Table covers the plain-text matching rules.
func TestLinkifyText_Table(t *testing.T) {
	withFakeHome(t, "/home/george", nil)
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"no slash passthrough",
			"hello world",
			"hello world",
		},
		{
			"absolute path",
			"see /Users/g/foo.go now",
			"see " + osc8("file:///Users/g/foo.go", "/Users/g/foo.go") + " now",
		},
		{
			"path at start of text",
			"/etc/hosts is readable",
			osc8("file:///etc/hosts", "/etc/hosts") + " is readable",
		},
		{
			"line suffix links the file, display keeps it",
			"at /Users/g/foo.go:42",
			"at " + osc8("file:///Users/g/foo.go", "/Users/g/foo.go:42"),
		},
		{
			"line and column suffix",
			"at /Users/g/foo.go:42:7",
			"at " + osc8("file:///Users/g/foo.go", "/Users/g/foo.go:42:7"),
		},
		{
			"tilde path expands against home",
			"open ~/notes/today.md",
			"open " + osc8("file:///home/george/notes/today.md", "~/notes/today.md"),
		},
		{
			"tilde path with one segment",
			"open ~/todo.md",
			"open " + osc8("file:///home/george/todo.md", "~/todo.md"),
		},
		{
			"https URL",
			"docs at https://carlos.dev/docs/frames",
			"docs at " + osc8("https://carlos.dev/docs/frames", "https://carlos.dev/docs/frames"),
		},
		{
			"http URL",
			"http://localhost:8080/healthz up",
			osc8("http://localhost:8080/healthz", "http://localhost:8080/healthz") + " up",
		},
		{
			"trailing sentence punctuation stays outside the link",
			"read /Users/g/a/b.md.",
			"read " + osc8("file:///Users/g/a/b.md", "/Users/g/a/b.md") + ".",
		},
		{
			"URL trailing comma stays outside",
			"see https://example.com/x, then run it",
			"see " + osc8("https://example.com/x", "https://example.com/x") + ", then run it",
		},
		{
			"single-segment absolute token is a slash command, not a path",
			"type /help for commands",
			"type /help for commands",
		},
		{
			"relative paths are not linkified in the transcript",
			"edit internal/tui/chat/view.go",
			"edit internal/tui/chat/view.go",
		},
		{
			"slash inside a word never links its suffix",
			"the foo/bar/baz module",
			"the foo/bar/baz module",
		},
		{
			"parenthesized path",
			"(see /tmp/x/y.txt)",
			"(see " + osc8("file:///tmp/x/y.txt", "/tmp/x/y.txt") + ")",
		},
		{
			"two tokens in one line",
			"/a/b and https://c.dev/d",
			osc8("file:///a/b", "/a/b") + " and " + osc8("https://c.dev/d", "https://c.dev/d"),
		},
	}
	for _, tc := range cases {
		if got := linkifyText(tc.in); got != tc.want {
			t.Errorf("%s:\nlinkifyText(%q)\n got %q\nwant %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestLinkifyText_ANSIInterleaved: SGR styling around a path survives
// untouched and the injected link nests inside it.
func TestLinkifyText_ANSIInterleaved(t *testing.T) {
	in := "\x1b[1m/Users/g/a.go\x1b[0m rest"
	want := "\x1b[1m" + osc8("file:///Users/g/a.go", "/Users/g/a.go") + "\x1b[0m rest"
	if got := linkifyText(in); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// TestLinkifyText_NoMatchInsideEscapes: path-shaped strings inside an
// OSC (window title) or any other escape body must not be wrapped.
func TestLinkifyText_NoMatchInsideEscapes(t *testing.T) {
	cases := []string{
		"\x1b]0;/Users/g/title.go\x07 plain tail",   // OSC title, BEL-terminated
		"\x1b]0;/Users/g/title.go\x1b\\ plain tail", // OSC title, ST-terminated
		"\x1b[38;2;1;2;3m no path here \x1b[0m",     // SGR params
	}
	for _, in := range cases {
		if got := linkifyText(in); got != in {
			t.Errorf("escape body was modified:\n in %q\nout %q", in, got)
		}
	}
}

// TestLinkifyText_Idempotent: re-running the pass over its own output
// changes nothing - already-linked text is copied verbatim.
func TestLinkifyText_Idempotent(t *testing.T) {
	in := "mix \x1b[1m/Users/g/a.go\x1b[0m and https://x.dev/y plus /tmp/z/w.txt."
	once := linkifyText(in)
	twice := linkifyText(once)
	if once != twice {
		t.Fatalf("not idempotent:\nonce  %q\ntwice %q", once, twice)
	}
	// And a hand-built pre-existing link is preserved as-is.
	pre := "see " + osc8("file:///already", "/Users/g/linked.go") + " here"
	if got := linkifyText(pre); got != pre {
		t.Fatalf("existing link rewrapped:\n in %q\nout %q", pre, got)
	}
	// Same when the linked text itself carries SGR styling - the
	// scanner skips through interior escapes to the link close.
	styled := osc8("file:///s", "\x1b[1m/Users/g/styled.go\x1b[0m") + " /a/b/c.go"
	want := osc8("file:///s", "\x1b[1m/Users/g/styled.go\x1b[0m") + " " + osc8("file:///a/b/c.go", "/a/b/c.go")
	if got := linkifyText(styled); got != want {
		t.Fatalf("styled link body:\n got %q\nwant %q", got, want)
	}
}

// TestTrimTrailingPunct_AllPunct covers the degenerate all-punctuation
// token (unreachable through linkifyToken, pinned for safety).
func TestTrimTrailingPunct_AllPunct(t *testing.T) {
	head, tail := trimTrailingPunct("...")
	if head != "" || tail != "..." {
		t.Fatalf("got (%q, %q), want (%q, %q)", head, tail, "", "...")
	}
}

// TestLinkifyText_TruncatedEscapeNeverPanics: malformed input (escape
// cut off mid-sequence) passes through without panicking or matching.
func TestLinkifyText_TruncatedEscapeNeverPanics(t *testing.T) {
	// Every case carries a '/' so the fast path doesn't skip the
	// scanner entirely.
	cases := []string{
		"/a/b \x1b",                  // ESC as final byte
		"/a/b \x1b[1",                // CSI with no final byte
		"\x1b]8;;file:///x",          // open link, no terminator, no close
		"\x1b]0;/a/b unterminated",   // OSC with no BEL/ST
		"\x1bP /a/b two-byte escape", // neither CSI nor OSC
		"tail /a/b \x1b[",
	}
	for _, in := range cases {
		got := linkifyText(in) // must not panic
		if !strings.Contains(got, "\x1b") {
			t.Errorf("escape bytes lost from %q: %q", in, got)
		}
	}
}

// ----- injection points ----------------------------------------------------

// TestRenderAssistantMarkdown_LinksSurviveGlamour: golden-ish check
// that the post-glamour injection emits working OSC 8 sequences for
// both paths and URLs in a sealed assistant turn.
func TestRenderAssistantMarkdown_LinksSurviveGlamour(t *testing.T) {
	md, err := newMarkdownRenderer(100)
	if err != nil {
		t.Fatalf("newMarkdownRenderer: %v", err)
	}
	out := renderAssistantMarkdown("Open /Users/george/code/foo.go and https://example.com/docs today.", 100, md)

	if !strings.Contains(out, "\x1b]8;;file:///Users/george/code/foo.go\x1b\\") {
		t.Errorf("path link missing from glamour output:\n%q", out)
	}
	if !strings.Contains(out, "\x1b]8;;https://example.com/docs") {
		t.Errorf("URL link missing from glamour output:\n%q", out)
	}
	// Open/close discipline: every open has a close, nothing dangles.
	opens := strings.Count(out, "\x1b]8;;")
	if opens == 0 || opens%2 != 0 {
		t.Errorf("unbalanced OSC 8 sequences (%d) in:\n%q", opens, out)
	}
}

// TestRenderAssistantMarkdown_PlainFallbackLinkified: the no-glamour
// path (renderer nil) gets links too, injected after the wrap.
func TestRenderAssistantMarkdown_PlainFallbackLinkified(t *testing.T) {
	out := renderAssistantMarkdown("see /Users/g/a.go", 100, nil)
	if !strings.Contains(out, "\x1b]8;;file:///Users/g/a.go\x1b\\") {
		t.Errorf("plain fallback missing link:\n%q", out)
	}
}

// TestRenderEntry_UserMessageLinkified: paths the user typed become
// links in the sealed transcript row.
func TestRenderEntry_UserMessageLinkified(t *testing.T) {
	e := transcriptEntry{kind: entryUserMessage, text: "look at /Users/g/a.go please"}
	out := renderEntry(e, nil, nil, 100)
	if !strings.Contains(out, "\x1b]8;;file:///Users/g/a.go\x1b\\") {
		t.Errorf("user message missing link:\n%q", out)
	}
}

// TestRenderAssistantLive_NeverLinkified: the streaming buffer is
// explicitly out of scope - only sealed turns get links.
func TestRenderAssistantLive_NeverLinkified(t *testing.T) {
	out := renderAssistantLive("streaming /Users/g/a.go and https://x.dev/y", 100)
	if strings.Contains(out, "\x1b]8;;") {
		t.Errorf("live text must not carry OSC 8 links:\n%q", out)
	}
}

// ----- mention seam ----------------------------------------------------------

// TestMentionLinkText_WrapsOSC8: the slice-9l seam is no longer the
// identity function.
func TestMentionLinkText_WrapsOSC8(t *testing.T) {
	if got, want := mentionLinkText("/a/b.go"), osc8("file:///a/b.go", "/a/b.go"); got != want {
		t.Errorf("absolute: got %q, want %q", got, want)
	}
	if got, want := mentionLinkText("a/b.go"), osc8("file://./a/b.go", "a/b.go"); got != want {
		t.Errorf("relative: got %q, want %q", got, want)
	}
}

// TestMentionLinkDisplay_TruncationSafe: the band/peek pattern -
// truncate the plain text, keep the full path in the URL - never cuts
// an escape and never leaves the link unclosed.
func TestMentionLinkDisplay_TruncationSafe(t *testing.T) {
	path := "/very/long/path/to/some/deeply/nested/file.go"
	out := mentionLinkDisplay(path, truncateRight(path, 12))

	if !strings.Contains(out, "file://"+path) {
		t.Errorf("URL must keep the full path: %q", out)
	}
	if !strings.HasSuffix(out, "\x1b]8;;\x1b\\") {
		t.Errorf("link left unclosed: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("display text should be truncated: %q", out)
	}
	if w := lipgloss.Width(out); w != 12 {
		t.Errorf("visible width = %d, want 12", w)
	}
}

// TestRenderMentionPeekCard_LinkSurvivesNarrowWidths: integration of
// the truncation-safety contract - at any width, the rendered card
// contains only complete escape sequences (stripANSI removes them all,
// leaving zero ESC bytes).
func TestRenderMentionPeekCard_LinkSurvivesNarrowWidths(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deeply", "nested")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(p, "linked.go")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	att := agent.Attachment{Kind: agent.AttachmentMention, Nickname: "linked.go", Path: f}
	for _, w := range []int{120, 40, 24, 10} {
		out := renderMentionPeekCard(att, w)
		if !strings.Contains(out, "\x1b]8;;file://"+f+"\x1b\\") {
			t.Errorf("w=%d: full-path link missing:\n%q", w, out)
		}
		if residue := stripANSI(out); strings.ContainsRune(residue, 0x1b) {
			t.Errorf("w=%d: broken escape survives stripANSI:\n%q", w, residue)
		}
	}
}

// TestAnnotatePaths_SharesOSC8Emitter pins the refactor: shell-output
// path annotation emits the exact same sequence shape as the shared
// helper, so the two link paths can never drift apart.
func TestAnnotatePaths_SharesOSC8Emitter(t *testing.T) {
	got := annotatePaths("ok main.go:42 done")
	want := "ok " + osc8("file://./main.go", "main.go:42") + " done"
	if got != want {
		t.Fatalf("annotatePaths = %q, want %q", got, want)
	}
}
