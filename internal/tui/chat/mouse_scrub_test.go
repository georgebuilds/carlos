package chat

import (
	"strings"
	"testing"
)

// TestScrubMouseReportEscapes_FieldReportPattern is the direct
// regression test for the observed leak: four chained press +
// release SGR mouse reports (button 64 wheel-up + button 65
// wheel-down) — the exact byte sequence the user posted from a
// live session.
func TestScrubMouseReportEscapes_FieldReportPattern(t *testing.T) {
	in := "[<64;96;7M[<64;96;7M[<65;96;7M[<65;96;7M"
	got := scrubMouseReportEscapes(in)
	if got != "" {
		t.Errorf("expected leak to be fully scrubbed; got %q", got)
	}
}

// TestScrubMouseReportEscapes_KeepsUserText pins that the
// scrubber doesn't touch legitimate composer content. Bracket
// characters are common in code-mention pastes ("see [link][1]")
// — the regex's three-numeric-segments + M/m terminator
// constraint keeps it strict enough that those survive.
func TestScrubMouseReportEscapes_KeepsUserText(t *testing.T) {
	cases := []string{
		"normal sentence",
		"see [link][1]",
		"array[0] = 1",
		"emoji 🧢: hi",
		"empty arg: [ ]",
		"keep [foo;bar;baz] alone",
		"and even [<1;2;3] without the M terminator",
	}
	for _, in := range cases {
		if got := scrubMouseReportEscapes(in); got != in {
			t.Errorf("scrubber damaged user text\n in: %q\nout: %q", in, got)
		}
	}
}

// TestScrubMouseReportEscapes_MixedInputCleansOnlyTheLeak covers
// the "the user typed some text and ALSO got a leak in the
// middle" case. Common after a quick alt+m toggle mid-compose:
// "hello [<64;96;7M world" → "hello  world" (note the double
// space — we don't try to collapse runs since the user might
// have wanted that spacing).
func TestScrubMouseReportEscapes_MixedInputCleansOnlyTheLeak(t *testing.T) {
	in := "hello [<64;96;7M world"
	want := "hello  world"
	if got := scrubMouseReportEscapes(in); got != want {
		t.Errorf("mixed input:\n got %q\nwant %q", got, want)
	}
}

// TestScrubMouseReportEscapes_ReleaseAndPressBoth pins that both
// the press ("M") and release ("m") terminators are stripped —
// the terminal emits a paired report for every click+release and
// the release leg leaks just as often as the press leg.
func TestScrubMouseReportEscapes_ReleaseAndPressBoth(t *testing.T) {
	in := "before [<0;1;2M after [<0;1;2m tail"
	got := scrubMouseReportEscapes(in)
	if strings.Contains(got, "[<") || strings.ContainsAny(got, "Mm") == false {
		// "Mm" check is the wrong invariant — we just want no
		// remaining "[<" sequences. The "Mm" half ensures the
		// natural text terminators in the tail survived.
	}
	if strings.Contains(got, "[<") {
		t.Errorf("scrubber missed a leak: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "tail") {
		t.Errorf("scrubber damaged surrounding text: %q", got)
	}
}

// TestScrubMouseReportEscapes_WithEscapeBytePrefix covers the
// alternate observed form where the literal "\x1b" lands ahead
// of the bracket. Some terminals emit the full CSI prefix when
// the mouse-mode toggle catches the report mid-flush.
func TestScrubMouseReportEscapes_WithEscapeBytePrefix(t *testing.T) {
	in := "\x1b[<64;96;7M\x1b[<64;96;7M"
	if got := scrubMouseReportEscapes(in); got != "" {
		t.Errorf("escape-prefixed leak should be fully scrubbed; got %q", got)
	}
}

// TestScrubMouseReportEscapes_NoLeakIsPassthrough is the
// hot-path assertion: every keystroke calls the scrubber, so a
// no-leak input must return unchanged (we rely on the regex's
// FindAll path being a no-op when no match is present).
func TestScrubMouseReportEscapes_NoLeakIsPassthrough(t *testing.T) {
	in := "this is a perfectly normal message"
	if got := scrubMouseReportEscapes(in); got != in {
		t.Errorf("no-leak path mutated input: %q → %q", in, got)
	}
}
