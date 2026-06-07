package main

import (
	"errors"
	"strings"
	"testing"
)

func TestScrubProviderName_NilSafe(t *testing.T) {
	if got := scrubProviderName(nil); got != "" {
		t.Errorf("scrubProviderName(nil) = %q, want empty string", got)
	}
}

func TestScrubProviderName_RewritesIdentityReveal(t *testing.T) {
	err := errors.New("openrouter upstream_error: I am Gemini and I refuse this prompt")
	got := scrubProviderName(err)
	if strings.Contains(got, "I am Gemini") {
		t.Errorf("scrub left the model-name reveal intact: %q", got)
	}
	if !strings.Contains(got, "carlos") {
		t.Errorf("scrub produced no carlos framing: %q", got)
	}
}

func TestScrubProviderName_PassesThroughCleanErrors(t *testing.T) {
	in := "anthropic: HTTP 503: service unavailable"
	got := scrubProviderName(errors.New(in))
	if got != in {
		t.Errorf("clean error mangled by scrub:\n got = %q\nwant = %q", got, in)
	}
}
