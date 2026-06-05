package diff

import (
	"strings"
	"testing"
)

// containsANSI returns true if s contains an ANSI escape sequence.
// We use it as a coarse "did styling happen" check — chasing exact
// escape strings is brittle across lipgloss/termenv versions.
func containsANSI(s string) bool { return strings.Contains(s, "\x1b[") }

func TestRender_empty(t *testing.T) {
	out := Renderer{}.Render(nil)
	// Empty input should produce the "no changes" placeholder, styled
	// or not. The text itself must be present.
	if !strings.Contains(out, emptyMsg) {
		t.Errorf("Render(nil) = %q, want it to contain %q", out, emptyMsg)
	}
}

func TestRender_simpleInline_colorsPlusMinus(t *testing.T) {
	out := Renderer{}.Render(mustReadTestdata(t, "simple.diff"))
	if out == "" {
		t.Fatal("empty render for simple.diff")
	}
	if !containsANSI(out) {
		t.Error("expected ANSI styling in rendered output")
	}
	// Sanity: every + and - line from the input must appear in the
	// rendered output (ANSI escapes around but body text preserved).
	for _, must := range []string{`import "fmt"`, `import "os"`, `package main`} {
		if !strings.Contains(out, must) {
			t.Errorf("rendered output missing %q", must)
		}
	}
	// Hunk header should be present.
	if !strings.Contains(out, "@@ -1,5 +1,6 @@") {
		t.Errorf("rendered output missing hunk header; got:\n%s", out)
	}
}

func TestRender_sideBySide_unevenColumns(t *testing.T) {
	r := Renderer{Mode: ModeSideBySide, Width: 80}
	out := r.Render(mustReadTestdata(t, "uneven.diff"))
	if out == "" {
		t.Fatal("empty side-by-side render")
	}
	// The separator must appear at least once per body row.
	if !strings.Contains(out, "│") {
		t.Errorf("side-by-side render missing column separator; got:\n%s", out)
	}
	// THREE / FOUR are added with no counterpart on the old side; their
	// rows should still render (with a blank left column).
	if !strings.Contains(out, "THREE") || !strings.Contains(out, "FOUR") {
		t.Errorf("rendered output missing uneven additions; got:\n%s", out)
	}
}

func TestRender_widthClampInline(t *testing.T) {
	// Force a narrow width and confirm that visible cells per line
	// never exceed it. We use a pathologically wide context line.
	raw := []byte("--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n-" + strings.Repeat("X", 200) + "\n+" + strings.Repeat("Y", 200) + "\n")
	r := Renderer{Width: 40}
	out := r.Render(raw)
	for _, line := range strings.Split(out, "\n") {
		if w := visibleWidth(line); w > 40 {
			t.Errorf("line exceeds clamp: width=%d, line=%q", w, line)
		}
	}
}

func TestRender_newFile(t *testing.T) {
	out := Renderer{}.Render(mustReadTestdata(t, "new_file.diff"))
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Errorf("new-file render missing added content; got:\n%s", out)
	}
	if !strings.Contains(out, "new file mode 100644") {
		t.Errorf("new-file render missing mode header; got:\n%s", out)
	}
}

func TestRender_deletedFile(t *testing.T) {
	out := Renderer{}.Render(mustReadTestdata(t, "deleted_file.diff"))
	if !strings.Contains(out, "deleted file mode 100644") {
		t.Errorf("deleted-file render missing mode header; got:\n%s", out)
	}
}

func TestRender_noNewlineMarker_styled(t *testing.T) {
	out := Renderer{}.Render(mustReadTestdata(t, "no_newline.diff"))
	if !strings.Contains(out, `No newline at end of file`) {
		t.Errorf("render missing no-newline marker; got:\n%s", out)
	}
}

func TestRender_gutter_lineNumbers(t *testing.T) {
	r := Renderer{Gutter: true}
	out := r.Render(mustReadTestdata(t, "simple.diff"))
	// First context line in simple.diff is "package main" at old=1 new=1.
	// With gutter enabled we expect a line that has both "   1    1"
	// (with padding) somewhere on it before "package main".
	if !strings.Contains(out, "package main") {
		t.Fatalf("output missing context line; got:\n%s", out)
	}
	// We won't assert exact spacing — that's brittle. We just confirm
	// the rendered output is wider than the no-gutter variant, which
	// is the observable signal that gutter columns landed.
	noGutter := Renderer{}.Render(mustReadTestdata(t, "simple.diff"))
	if len(out) <= len(noGutter) {
		t.Errorf("expected gutter render (%d) to be longer than no-gutter (%d)", len(out), len(noGutter))
	}
}

func TestRender_detectLanguage(t *testing.T) {
	cases := map[string]string{
		"a/foo.go":  "go",
		"b/foo.ts":  "typescript",
		"b/foo.tsx": "tsx",
		"b/foo.py":  "python",
		"b/UNKNOWN": "",
		"/dev/null": "",
		"":          "",
	}
	for in, want := range cases {
		if got := detectLanguage(in); got != want {
			t.Errorf("detectLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

// visibleWidth counts visible cells using the same lipgloss helper the
// width-clamp uses, so the test stays consistent with the clamp's
// definition of "width".
func visibleWidth(s string) int { return widthForTest(s) }
