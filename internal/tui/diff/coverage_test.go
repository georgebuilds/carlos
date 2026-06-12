package diff

import (
	"strings"
	"testing"
)

// TestStyleHeaderLine_classifies pins one case per branch of
// styleHeaderLine, including the two fall-through tails: a blank line
// (returned bare, no ANSI) and an unrecognized non-blank line (muted).
func TestStyleHeaderLine_classifies(t *testing.T) {
	r := Renderer{}
	cases := []struct {
		name     string
		in       string
		wantANSI bool
	}{
		{"diff-git", "diff --git a/x b/x", true},
		{"index", "index aaa..bbb 100644", true},
		{"old-path", "--- a/x", true},
		{"new-path", "+++ b/x", true},
		{"new-file-mode", "new file mode 100644", true},
		{"deleted-file-mode", "deleted file mode 100644", true},
		{"rename", "rename from old to new", true},
		{"similarity", "similarity index 95%", true},
		{"binary", "Binary files a/x and b/x differ", true},
		{"unknown-nonblank", "some junk header", true},
		{"blank", "", false},
		{"whitespace-only", "   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.styleHeaderLine(tc.in)
			// Body text must always survive.
			if !strings.Contains(got, strings.TrimSpace(tc.in)) && strings.TrimSpace(tc.in) != "" {
				t.Errorf("styleHeaderLine(%q) dropped body: %q", tc.in, got)
			}
			if has := containsANSI(got); has != tc.wantANSI {
				t.Errorf("styleHeaderLine(%q): ANSI=%v, want %v (got %q)", tc.in, has, tc.wantANSI, got)
			}
			// Blank/whitespace lines must pass through byte-for-byte.
			if !tc.wantANSI && got != tc.in {
				t.Errorf("blank line not passed through verbatim: got %q want %q", got, tc.in)
			}
		})
	}
}

// TestRender_binaryFile exercises the "Binary files ... differ" header
// path: parse keeps it as a header line (no hunk) and styleHeaderLine
// mutes it. The file still renders without a hunk body.
func TestRender_binaryFile(t *testing.T) {
	raw := []byte("diff --git a/logo.png b/logo.png\n" +
		"index aaaaaaa..bbbbbbb 100644\n" +
		"Binary files a/logo.png and b/logo.png differ\n")
	out := Renderer{}.Render(raw)
	if !strings.Contains(out, "Binary files a/logo.png and b/logo.png differ") {
		t.Errorf("binary render missing the differ line; got:\n%s", out)
	}
}

// TestRenderHunkInline_noNewlineAndBlankBody covers the '\' (no-newline
// marker) branch and the empty-line passthrough branch of
// renderHunkInline, plus the gutter cells for a marker (both zero).
func TestRenderHunkInline_noNewlineAndBlankBody(t *testing.T) {
	h := parsedHunk{
		header:   "@@ -1,2 +1,2 @@",
		oldStart: 1, newStart: 1,
		lines: []string{
			"-old",
			"+new",
			"",                             // blank body line → passthrough branch
			"\\ No newline at end of file", // backslash branch
		},
	}
	out, nAdd, nDel := Renderer{Gutter: true}.renderHunkInline(h, "b/eof.txt")
	if nAdd != 1 || nDel != 1 {
		t.Errorf("counts add=%d del=%d, want 1/1", nAdd, nDel)
	}
	if len(out) != 4 {
		t.Fatalf("want 4 output lines, got %d: %#v", len(out), out)
	}
	if out[2] != "" {
		t.Errorf("blank body line should pass through as empty, got %q", out[2])
	}
	if !strings.Contains(out[3], "No newline at end of file") {
		t.Errorf("no-newline marker missing from output: %q", out[3])
	}
}

// TestRenderHunkInline_unknownPrefix covers the default branch: a body
// line whose first byte is none of space/+/-/backslash is emitted as-is.
// Such lines only reach the renderer when a caller hand-builds a hunk;
// the parser normalizes them, but the renderer must still be total.
func TestRenderHunkInline_unknownPrefix(t *testing.T) {
	h := parsedHunk{
		header: "@@ -1 +1 @@",
		lines:  []string{"?weird"},
	}
	out, add, del := Renderer{}.renderHunkInline(h, "")
	if add != 0 || del != 0 {
		t.Errorf("unknown prefix should not count as add/del; got add=%d del=%d", add, del)
	}
	if len(out) != 1 || !strings.Contains(out[0], "?weird") {
		t.Errorf("unknown-prefix line not emitted verbatim: %#v", out)
	}
}

// TestRenderHunkSideBySide_markerAndBlank covers the '\' (no-newline)
// branch and the empty-line skip branch of side-by-side rendering.
func TestRenderHunkSideBySide_markerAndBlank(t *testing.T) {
	h := parsedHunk{
		header: "@@ -1 +1 @@",
		lines: []string{
			"-old",
			"+new",
			"",                             // empty → skipped
			"\\ No newline at end of file", // marker → flush + paired note row
			" context",
		},
	}
	out, add, del := Renderer{Mode: ModeSideBySide, Width: 80}.renderHunkSideBySide(h)
	if add != 1 || del != 1 {
		t.Errorf("counts add=%d del=%d, want 1/1", add, del)
	}
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "No newline at end of file") {
		t.Errorf("side-by-side missing no-newline marker; got:\n%s", joined)
	}
	if !strings.Contains(joined, "context") {
		t.Errorf("side-by-side missing context line; got:\n%s", joined)
	}
}

// TestHalfWidth_edges covers the two clamp branches of halfWidth:
// Width <= 4 (the tiny-terminal floor) and a Width whose half falls
// below the 8-cell minimum.
func TestHalfWidth_edges(t *testing.T) {
	cases := []struct {
		width, want int
	}{
		{0, 8},   // unset → floor
		{4, 8},   // <= 4 → floor
		{10, 8},  // (10-1)/2 = 4 < 8 → floor
		{18, 8},  // (18-1)/2 = 8 → exactly the minimum
		{81, 40}, // (81-1)/2 = 40 → above minimum, used as-is
	}
	for _, tc := range cases {
		if got := (Renderer{Width: tc.width}).halfWidth(); got != tc.want {
			t.Errorf("halfWidth(Width=%d) = %d, want %d", tc.width, got, tc.want)
		}
	}
}

// TestItoaPad_overflow covers the len(s) >= width early return: a number
// wider than the requested pad is returned unpadded rather than clipped.
func TestItoaPad_overflow(t *testing.T) {
	if got := itoaPad(123456, 4); got != "123456" {
		t.Errorf("itoaPad(123456,4) = %q, want unpadded 123456", got)
	}
	if got := itoaPad(7, 4); got != "   7" {
		t.Errorf("itoaPad(7,4) = %q, want padded '   7'", got)
	}
	if got := itoaPad(42, 2); got != "42" {
		t.Errorf("itoaPad(42,2) = %q, want exact-fit 42", got)
	}
}

// TestPadOrClip_exactWidthPassthrough covers the w == cells early return.
func TestPadOrClip_exactWidthPassthrough(t *testing.T) {
	in := "abcde"
	if got := padOrClip(in, 5); got != in {
		t.Errorf("padOrClip exact-fit = %q, want passthrough %q", got, in)
	}
}

// TestDetectLanguage_allMappedExtensions walks every extension the
// mapping knows so the long switch is fully exercised, plus the a/ b/
// prefix-strip and the dot-case insensitivity.
func TestDetectLanguage_allMappedExtensions(t *testing.T) {
	cases := map[string]string{
		"a/x.go":        "go",
		"b/x.ts":        "typescript",
		"x.tsx":         "tsx",
		"x.js":          "javascript",
		"x.jsx":         "jsx",
		"x.py":          "python",
		"x.rs":          "rust",
		"x.rb":          "ruby",
		"x.java":        "java",
		"x.c":           "c",
		"x.h":           "c",
		"x.cpp":         "cpp",
		"x.cc":          "cpp",
		"x.cxx":         "cpp",
		"x.hpp":         "cpp",
		"x.sh":          "bash",
		"x.bash":        "bash",
		"x.json":        "json",
		"x.yaml":        "yaml",
		"x.yml":         "yaml",
		"x.toml":        "toml",
		"x.md":          "markdown",
		"x.html":        "html",
		"x.css":         "css",
		"x.sql":         "sql",
		"x.GO":          "go", // case-insensitive on the extension
		"b/nested/y.py": "python",
		"x.unknownext":  "",
		"noext":         "",
	}
	for in, want := range cases {
		if got := detectLanguage(in); got != want {
			t.Errorf("detectLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStyleBodyLine_highlightFallback covers styleBodyLine's Highlight
// path for a language whose highlightLine returns "" (empty body): it
// must fall through to the plain base.Render rather than emit nothing.
func TestStyleBodyLine_highlightFallback(t *testing.T) {
	r := Renderer{Highlight: true}
	// Empty body → highlightLine returns "" → fall through to base.Render("").
	got := r.styleBodyLine("", styleAdded, "b/x.go")
	if got != styleAdded.Render("") {
		t.Errorf("empty-body highlight fallback = %q, want plain base render", got)
	}
	// Unknown language (no lexer) → detectLanguage returns "" → plain.
	plain := r.styleBodyLine("payload", styleAdded, "b/x.unknownext")
	if !strings.Contains(plain, "payload") {
		t.Errorf("unknown-lang body dropped text: %q", plain)
	}
}
