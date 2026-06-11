package tools

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCodeSearch_RejectsNonTextContentType — a service returning a binary
// content type lands an error in its hit envelope (not an excerpt), and
// the overall response carries the "no indexer hit" note.
func TestCodeSearch_RejectsNonTextContentType(t *testing.T) {
	endpoints, stop := fakeIndexer(t, map[string]http.HandlerFunc{
		"deepwiki": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG\r\n"))
		},
	})
	defer stop()
	tool := NewCodeSearchTool()
	tool.Endpoints = endpoints
	resp := runCodeSearch(t, tool, codeSearchInput{Services: []string{"deepwiki"}})
	if len(resp.Services) != 1 {
		t.Fatalf("want 1 service hit, got %d", len(resp.Services))
	}
	if resp.Services[0].Error == "" {
		t.Errorf("non-text content type should set an error: %+v", resp.Services[0])
	}
	if resp.Services[0].Excerpt != "" {
		t.Errorf("rejected hit should carry no excerpt: %q", resp.Services[0].Excerpt)
	}
	if resp.Note == "" {
		t.Error("response should note that no indexer returned content")
	}
}

// TestCodeSearch_EmptyContentTypeDefaultsToHTML — a 200 with no
// Content-Type header is treated as text/html and parsed as a hit.
func TestCodeSearch_EmptyContentTypeDefaultsToHTML(t *testing.T) {
	endpoints, stop := fakeIndexer(t, map[string]http.HandlerFunc{
		"deepwiki": func(w http.ResponseWriter, r *http.Request) {
			// Explicitly clear any auto-detected content type.
			w.Header()["Content-Type"] = nil
			_, _ = w.Write([]byte("<html><head><title>t</title></head><body>hello</body></html>"))
		},
	})
	defer stop()
	tool := NewCodeSearchTool()
	tool.Endpoints = endpoints
	resp := runCodeSearch(t, tool, codeSearchInput{Services: []string{"deepwiki"}})
	if len(resp.Services) != 1 {
		t.Fatalf("want 1 service hit, got %d", len(resp.Services))
	}
	if resp.Services[0].Error != "" {
		t.Errorf("empty content-type should default to html, not error: %q", resp.Services[0].Error)
	}
	if resp.Services[0].Excerpt == "" {
		t.Error("expected an excerpt from the html body")
	}
}

// TestFilterServices_EmptyReturnsAllSorted — an empty filter returns every
// configured endpoint name in sorted order.
func TestFilterServices_EmptyReturnsAllSorted(t *testing.T) {
	eps := map[string]string{"deepwiki": "x", "codewiki": "y", "context7": "z"}
	got := filterServices(nil, eps)
	want := []string{"codewiki", "context7", "deepwiki"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("filterServices[%d] = %q, want %q (sorted)", i, got[i], want[i])
		}
	}
}

// TestAnyHitOK — true only when a hit has no error AND a non-empty excerpt.
func TestAnyHitOK(t *testing.T) {
	if anyHitOK([]codeSearchServiceHit{{Error: "boom"}, {Excerpt: ""}}) {
		t.Error("no usable hit should report false")
	}
	if !anyHitOK([]codeSearchServiceHit{{Excerpt: "content"}}) {
		t.Error("a clean excerpt should report true")
	}
}

// TestFillCodeSearchTemplate_Empty — an empty template returns "".
func TestFillCodeSearchTemplate_Empty(t *testing.T) {
	if got := fillCodeSearchTemplate("", "o", "r"); got != "" {
		t.Errorf("empty template should yield empty string; got %q", got)
	}
}

// TestExcerpt_RuneBoundaryBacktrack — when the byte cap lands in the
// middle of a multibyte rune, excerpt backtracks to a rune boundary so the
// output stays valid UTF-8.
func TestExcerpt_RuneBoundaryBacktrack(t *testing.T) {
	// "é" is 2 bytes (0xC3 0xA9). Build a string where the cap falls inside
	// a multibyte rune.
	s := strings.Repeat("é", 50) // 100 bytes
	got := excerpt(s, 51)        // cap inside the 26th rune's bytes
	if !strings.HasSuffix(got, "(... excerpt truncated)") {
		t.Errorf("expected truncation marker; got %q", got)
	}
	body := strings.TrimSuffix(got, "\n\n(... excerpt truncated)")
	if !utf8.ValidString(body) {
		t.Errorf("excerpt should backtrack to a rune boundary; got invalid UTF-8: %q", body)
	}
}

// TestExcerpt_ZeroMaxPassesThrough — a non-positive max returns the input
// unchanged.
func TestExcerpt_ZeroMaxPassesThrough(t *testing.T) {
	if got := excerpt("anything", 0); got != "anything" {
		t.Errorf("max<=0 should pass through; got %q", got)
	}
}
