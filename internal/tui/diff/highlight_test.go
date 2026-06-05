//go:build !nochroma

package diff

import (
	"strings"
	"testing"
)

func TestHighlight_goFile_addsTokenColor(t *testing.T) {
	// Build a small Go diff and render with Highlight=true. Chroma must
	// color at least one token (keyword `func` or string literal) on the
	// + line — we detect by counting ANSI escapes before vs after.
	raw := []byte("diff --git a/x.go b/x.go\nindex aaa..bbb 100644\n--- a/x.go\n+++ b/x.go\n@@ -1,1 +1,2 @@\n func main() {}\n+func helper() string { return \"hi\" }\n")

	plain := Renderer{}.Render(raw)
	hi := Renderer{Highlight: true}.Render(raw)

	if hi == plain {
		t.Errorf("expected Highlight=true to produce different output from plain")
	}
	if strings.Count(hi, "\x1b[") <= strings.Count(plain, "\x1b[") {
		t.Errorf("expected more ANSI escapes with highlight; plain=%d hi=%d",
			strings.Count(plain, "\x1b["), strings.Count(hi, "\x1b["))
	}
	// Source text must still be present.
	if !strings.Contains(hi, "helper") {
		t.Errorf("highlighted output dropped source text; got:\n%s", hi)
	}
}

func TestHighlight_unknownLanguage_noOp(t *testing.T) {
	raw := []byte("diff --git a/x.UNKNOWNEXT b/x.UNKNOWNEXT\n--- a/x.UNKNOWNEXT\n+++ b/x.UNKNOWNEXT\n@@ -1,1 +1,1 @@\n-old\n+new\n")
	hi := Renderer{Highlight: true}.Render(raw)
	// Output should still contain "new" and "old", and should not panic.
	if !strings.Contains(hi, "new") || !strings.Contains(hi, "old") {
		t.Errorf("expected old/new in output; got:\n%s", hi)
	}
}

func TestHighlightLine_emptyInput(t *testing.T) {
	if got := highlightLine("go", ""); got != "" {
		t.Errorf("highlightLine(go, \"\") = %q, want empty", got)
	}
}

func TestGetLexer_caches(t *testing.T) {
	// Just confirm the cache returns the same lexer (or both nil)
	// across calls — we don't introspect chroma's internals.
	a := getLexer("go")
	b := getLexer("go")
	if a != b {
		t.Errorf("lexer cache returned different instances: %p vs %p", a, b)
	}
	if a == nil {
		t.Skip("chroma has no 'go' lexer in this build; can't test caching further")
	}
}
