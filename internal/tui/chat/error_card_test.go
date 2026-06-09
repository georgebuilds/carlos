package chat

import (
	"strings"
	"testing"
	"time"
)

// TestStripErrorPrefix_MarkerRecognized verifies the chatglue marker
// is picked up so applyEvent can route it to the error-card kind.
func TestStripErrorPrefix_MarkerRecognized(t *testing.T) {
	in := chatglueErrorPrefix + "openrouter: timeout"
	rest, ok := stripErrorPrefix(in)
	if !ok {
		t.Fatal("expected marker to be recognized")
	}
	if rest != "openrouter: timeout" {
		t.Errorf("rest = %q, want %q", rest, "openrouter: timeout")
	}
}

// TestStripErrorPrefix_PlainTextIgnored guards the false-positive
// path: a normal assistant turn that happens to start with "[" or
// looks vaguely similar must NOT be mistaken for an error event.
func TestStripErrorPrefix_PlainTextIgnored(t *testing.T) {
	if _, ok := stripErrorPrefix("hi there"); ok {
		t.Error("plain text should not match the marker")
	}
	if _, ok := stripErrorPrefix("[notes] something"); ok {
		t.Error("brackets alone should not match the marker")
	}
}

// TestSplitErrorHead_PicksLastTwoSegments confirms the colon-walk
// heuristic surfaces the closest source label + the actual error
// body. Wrapped errors like
//
//	loop: stream iter 0: openrouter: HTTP: Post …: http2: timeout …
//
// should land "http2" as the label and "timeout …" as the detail.
func TestSplitErrorHead_PicksLastTwoSegments(t *testing.T) {
	label, detail := splitErrorHead("loop: stream iter 0: openrouter: HTTP: Post xyz: http2: timeout awaiting response header")
	if label != "http2" {
		t.Errorf("label = %q, want %q", label, "http2")
	}
	if !strings.Contains(detail, "timeout awaiting response header") {
		t.Errorf("detail missing payload: %q", detail)
	}
}

// TestSplitErrorHead_FallbackForSingleSegment covers the simple
// single-clause path so a short error like "rate limited" still
// renders something.
func TestSplitErrorHead_FallbackForSingleSegment(t *testing.T) {
	label, detail := splitErrorHead("rate limited")
	if label != "rate limited" || detail != "" {
		t.Errorf("got label=%q detail=%q, want label=%q detail=%q", label, detail, "rate limited", "")
	}
}

// TestRenderErrorCard_ContainsLabelAndDetail proves the bordered
// card surfaces both the source label and the detail preview, with
// the ✗ glyph in place. We don't pin the box characters because
// lipgloss may swap them per terminal capability, but the content
// must be present.
func TestRenderErrorCard_ContainsLabelAndDetail(t *testing.T) {
	e := transcriptEntry{
		kind: entryError,
		ts:   time.Now(),
		text: "loop: stream iter 0: openrouter: http2: timeout awaiting response header",
	}
	out := renderErrorCard(e, 100)
	if !strings.Contains(out, "✗") {
		t.Errorf("card missing ✗ glyph:\n%s", out)
	}
	if !strings.Contains(out, "http2") {
		t.Errorf("card missing label:\n%s", out)
	}
	if !strings.Contains(out, "timeout") {
		t.Errorf("card missing detail:\n%s", out)
	}
}
