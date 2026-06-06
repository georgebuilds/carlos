package gemini

import (
	"testing"
)

func TestNew_SetsBaseURL(t *testing.T) {
	c := New("test-key")
	if c.APIKey != "test-key" {
		t.Errorf("APIKey: want test-key got %q", c.APIKey)
	}
	if c.BaseURL != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Errorf("BaseURL: got %q", c.BaseURL)
	}
}

func TestName(t *testing.T) {
	if got := New("").Name(); got != "gemini" {
		t.Errorf("Name(): got %q, want gemini", got)
	}
}

func TestCapabilities(t *testing.T) {
	caps := New("").Capabilities()
	if !caps.ParallelToolUse || !caps.StructuredOut || !caps.Vision {
		t.Errorf("expected parallel tool use + structured out + vision; got %+v", caps)
	}
	if caps.PromptCaching {
		t.Error("Gemini prompt caching via OpenAI shim is not supported; capability should be false")
	}
}

func TestMapFinishReason(t *testing.T) {
	cases := map[string]string{
		"tool_calls":     "tool_use",
		"function_call":  "tool_use",
		"stop":           "stop",
		"length":         "length",
		"content_filter": "content_filter",
		"":               "",
	}
	for in, want := range cases {
		if got := mapFinishReason(in); got != want {
			t.Errorf("mapFinishReason(%q) = %q, want %q", in, got, want)
		}
	}
}
