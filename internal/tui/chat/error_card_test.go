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

// TestRenderErrorCardGroup_GroupsConsecutiveEntries pins the v0.7.6
// behavior: 2+ error entries collapse into a single rounded-border
// card with internal `─` separators (Bootstrap list-group style),
// shrinking the vertical real estate consumed by a flurry of
// back-to-back errors. The user explicitly asked for this layout
// over the previous N×3-row stack of independent boxes.
func TestRenderErrorCardGroup_GroupsConsecutiveEntries(t *testing.T) {
	es := []transcriptEntry{
		{kind: entryError, ts: time.Now(), text: "openrouter: HTTP 400: No models provided"},
		{kind: entryError, ts: time.Now(), text: "supervisor: spawn refused, frame mode 'solo'"},
		{kind: entryError, ts: time.Now(), text: "loop: budget exceeded"},
	}
	out := renderErrorCardGroup(es, 100)

	// Each row's label must survive into the group output.
	for _, want := range []string{"HTTP 400", "spawn refused", "budget exceeded"} {
		if !strings.Contains(out, want) {
			t.Errorf("group missing %q:\n%s", want, out)
		}
	}
	// The ✗ glyph appears N times (one per row) — not just once.
	if got := strings.Count(out, "✗"); got != len(es) {
		t.Errorf("✗ count = %d, want %d (one per row)", got, len(es))
	}
	// Output shape: 2 border rows (top, bottom) + N content rows +
	// (N-1) internal separator rows. A separator row looks like
	// "│ ───…─── │"; we identify it by the `│` edges paired with a
	// long dash run AND no glyph-bearing payload (✗ would mark a
	// content row).
	lines := strings.Split(out, "\n")
	wantLines := 2 + len(es) + (len(es) - 1)
	if got := len(lines); got != wantLines {
		t.Errorf("output line count = %d, want %d (2 borders + %d content + %d separators):\n%s",
			got, wantLines, len(es), len(es)-1, out)
	}
	separatorRows := 0
	for _, ln := range lines {
		stripped := stripChatANSI(ln)
		if !strings.Contains(stripped, "│") {
			continue
		}
		if strings.Contains(stripped, "✗") {
			continue
		}
		if strings.Contains(stripped, "──") {
			separatorRows++
		}
	}
	if want := len(es) - 1; separatorRows != want {
		t.Errorf("internal separator rows = %d, want %d", separatorRows, want)
	}
}

// TestRenderErrorCardGroup_SingleEntryMatchesLegacyCard guards the
// "one error looks the same" contract: a 1-element group must
// produce the identical output as the legacy single-card render so
// the user sees zero visual change for solo errors.
func TestRenderErrorCardGroup_SingleEntryMatchesLegacyCard(t *testing.T) {
	e := transcriptEntry{
		kind: entryError,
		ts:   time.Now(),
		text: "openrouter: rate limited",
	}
	group := renderErrorCardGroup([]transcriptEntry{e}, 100)
	solo := renderErrorCard(e, 100)
	if group != solo {
		t.Errorf("solo group differs from single-card render:\ngroup:\n%s\nsolo:\n%s", group, solo)
	}
}

// stripChatANSI removes ANSI escapes for snapshot-style assertions.
// Local to this test file; the chat package already has multiple
// strippers but they live in test files we don't share state with.
func stripChatANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				j++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
