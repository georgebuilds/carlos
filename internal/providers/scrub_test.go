package providers

import (
	"errors"
	"strings"
	"testing"
)

func TestScrubModelNameString_IdentityReveals(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"i_am_gemini", "I am Gemini, a large language model trained by Google.", "I am carlos, a large language model trained by Google."},
		{"im_gemini_lowercase", "i'm gemini and happy to help.", "I am carlos and happy to help."},
		{"i_am_claude", "I am Claude, made by Anthropic.", "I am carlos, made by Anthropic."},
		{"i_am_chatgpt", "I am ChatGPT, here to assist.", "I am carlos, here to assist."},
		{"i_am_gpt4", "I am GPT-4 and can help with that.", "I am carlos and can help with that."},
		{"my_name_is_gemini", "My name is Gemini and I work for Google.", "My name is carlos and I work for Google."},
		{"my_name_is_claude_punct", "Hi! My name is Claude.", "Hi! My name is carlos."},
		{"bare_gemini_prefix", "Gemini: quota exceeded", "carlos: quota exceeded"},
		{"bare_chatgpt_speaking", "ChatGPT speaking, ready to help.", "carlos speaking, ready to help."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScrubModelNameString(tc.in)
			if got != tc.want {
				t.Errorf("ScrubModelNameString(%q)\n got = %q\nwant = %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestScrubModelNameString_LeavesLegitContentAlone(t *testing.T) {
	cases := []string{
		// Model IDs inside larger identifiers — the operator legitimately
		// wants these to round-trip.
		"anthropic: HTTP 503: claude-sonnet-4-6 unavailable",
		"openrouter route anthropic/claude-3.5-sonnet failed",
		"gpt-4o-mini latency exceeded",
		// A status line that names the provider without claiming
		// identity — not an "I am" reveal, must pass through.
		"talking to Anthropic's API failed: connection reset",
		// A code review snippet mentioning a model name — the scrub
		// must not damage prose.
		"the SDK exports a 'claude' constant",
	}
	for _, s := range cases {
		got := ScrubModelNameString(s)
		if got != s {
			t.Errorf("scrub mangled legit message:\n in  = %q\n got = %q", s, got)
		}
	}
}

func TestScrubModelNameString_MultiLine(t *testing.T) {
	in := "first line is fine\nI am Gemini and the second line reveals.\nthird line is fine"
	want := "first line is fine\nI am carlos and the second line reveals.\nthird line is fine"
	got := ScrubModelNameString(in)
	if got != want {
		t.Errorf("multi-line scrub mismatch:\n got = %q\nwant = %q", got, want)
	}
}

func TestScrubModelName_PreservesUnderlyingErrorViaErrorsIs(t *testing.T) {
	sentinel := errors.New("anthropic HTTP 500: I am Claude and something went wrong")
	wrapped := ScrubModelName(sentinel)
	if wrapped == nil {
		t.Fatal("ScrubModelName returned nil for non-nil input")
	}
	if !errors.Is(wrapped, sentinel) {
		t.Error("scrubbed error does not unwrap to the original via errors.Is")
	}
	if strings.Contains(wrapped.Error(), "I am Claude") {
		t.Errorf("wrapped error still contains the model name: %q", wrapped.Error())
	}
	if !strings.Contains(wrapped.Error(), "carlos") {
		t.Errorf("wrapped error missing carlos framing: %q", wrapped.Error())
	}
}

func TestScrubModelName_PassesThroughWhenNoMatch(t *testing.T) {
	orig := errors.New("anthropic: HTTP 503: service unavailable")
	got := ScrubModelName(orig)
	if got != orig {
		t.Errorf("clean error should be returned unchanged; got %v", got)
	}
}

func TestScrubModelName_NilSafe(t *testing.T) {
	if got := ScrubModelName(nil); got != nil {
		t.Errorf("ScrubModelName(nil) = %v, want nil", got)
	}
}
