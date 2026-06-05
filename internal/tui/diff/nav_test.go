package diff

import (
	"strings"
	"testing"
)

func TestRenderWithIndex_multiHunk_correctRanges(t *testing.T) {
	rendered, idx := Renderer{}.RenderWithIndex(mustReadTestdata(t, "multi_hunk.diff"))
	if len(idx) != 3 {
		t.Fatalf("want 3 hunks indexed, got %d", len(idx))
	}
	lines := strings.Split(rendered, "\n")
	for i, h := range idx {
		if h.StartLine < 0 || h.StartLine >= len(lines) {
			t.Errorf("idx[%d].StartLine %d out of range [0,%d)", i, h.StartLine, len(lines))
			continue
		}
		// The StartLine must point at a hunk-header line (`@@ ... @@`).
		if !strings.Contains(lines[h.StartLine], "@@") {
			t.Errorf("idx[%d].StartLine line is %q, want @@ header", i, lines[h.StartLine])
		}
		if h.EndLine < h.StartLine {
			t.Errorf("idx[%d] EndLine %d < StartLine %d", i, h.EndLine, h.StartLine)
		}
		if h.EndLine >= len(lines) {
			t.Errorf("idx[%d].EndLine %d out of range [0,%d)", i, h.EndLine, len(lines))
		}
	}
	// File ordering should be foo.go, foo.go, bar.go (two hunks in foo,
	// one in bar) — RenderWithIndex preserves document order.
	wantFiles := []string{"b/foo.go", "b/foo.go", "b/bar.go"}
	for i, want := range wantFiles {
		if idx[i].File != want {
			t.Errorf("idx[%d].File = %q, want %q", i, idx[i].File, want)
		}
	}
}

func TestRenderWithIndex_lineCounts(t *testing.T) {
	_, idx := Renderer{}.RenderWithIndex(mustReadTestdata(t, "multi_hunk.diff"))
	// foo.go first hunk: -line2 / +LINE2 → 1 del, 1 add
	if idx[0].DelLines != 1 || idx[0].NewLines != 1 {
		t.Errorf("idx[0] (foo.go hunk 1): del=%d add=%d, want 1/1", idx[0].DelLines, idx[0].NewLines)
	}
	// foo.go second hunk: +line11.5 → 0 del, 1 add
	if idx[1].DelLines != 0 || idx[1].NewLines != 1 {
		t.Errorf("idx[1] (foo.go hunk 2): del=%d add=%d, want 0/1", idx[1].DelLines, idx[1].NewLines)
	}
	// bar.go: -old / +new → 1 del, 1 add
	if idx[2].DelLines != 1 || idx[2].NewLines != 1 {
		t.Errorf("idx[2] (bar.go): del=%d add=%d, want 1/1", idx[2].DelLines, idx[2].NewLines)
	}
}

func TestRenderWithIndex_emptyInput(t *testing.T) {
	rendered, idx := Renderer{}.RenderWithIndex(nil)
	if idx != nil {
		t.Errorf("want nil index for empty input, got %v", idx)
	}
	if !strings.Contains(rendered, emptyMsg) {
		t.Errorf("want %q in rendered output, got %q", emptyMsg, rendered)
	}
}
