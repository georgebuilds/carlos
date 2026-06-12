package memory_test

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/memory"
	"github.com/georgebuilds/carlos/internal/providers"
)

// TestNaiveSummarizer_HappyPath verifies the v0 stub returns the
// documented "<N messages, last user said: ...>" shape so callers
// can exercise the storage path without a provider.
func TestNaiveSummarizer_HappyPath(t *testing.T) {
	msgs := []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}},
		{Role: "assistant", Content: []providers.Block{{Kind: "text", Text: "hello!"}}},
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "what is FTS5"}}},
	}
	text, tokens, err := memory.NaiveSummarizer{}.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !strings.HasPrefix(text, "<3 messages, last user said:") {
		t.Errorf("unexpected prefix: %q", text)
	}
	if !strings.Contains(text, "FTS5") {
		t.Errorf("last user content missing: %q", text)
	}
	if tokens == 0 {
		t.Errorf("token count should be > 0, got %d", tokens)
	}
}

// TestNaiveSummarizer_TruncatesLongLastUser verifies the rune-cap of
// 256 so a paste-bomb doesn't bloat one summary row.
func TestNaiveSummarizer_TruncatesLongLastUser(t *testing.T) {
	long := strings.Repeat("a", 1000)
	msgs := []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: long}}},
	}
	text, _, err := memory.NaiveSummarizer{}.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !strings.Contains(text, "…") {
		t.Errorf("expected truncation marker, got %q", text[:80])
	}
}

// TestNaiveSummarizer_EmptyRejected guards against silently
// inserting a 0-message summary.
func TestNaiveSummarizer_EmptyRejected(t *testing.T) {
	_, _, err := memory.NaiveSummarizer{}.Summarize(context.Background(), nil)
	if err == nil {
		t.Error("expected error on empty messages")
	}
}

// fakeProvider is the minimal providers.Provider implementation used
// to exercise LLMSummarizer without burning credits. It emits the
// canned response as a stream of EventTextDelta events and then
// closes the channel.
type fakeProvider struct {
	response string
	gotReq   providers.Request
}

func (f *fakeProvider) Name() string                         { return "fake" }
func (f *fakeProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (f *fakeProvider) Stream(_ context.Context, req providers.Request) (<-chan providers.Event, error) {
	f.gotReq = req
	ch := make(chan providers.Event, 2)
	ch <- providers.Event{Kind: providers.EventTextDelta, Text: f.response}
	close(ch)
	return ch, nil
}

// TestLLMSummarizer_HappyPath wires the fake provider through
// LLMSummarizer and verifies the summary text is what the provider
// emitted, plus the system prompt was forwarded.
func TestLLMSummarizer_HappyPath(t *testing.T) {
	fp := &fakeProvider{response: "  A short factual paragraph.  "}
	s := memory.LLMSummarizer{Provider: fp}
	text, tokens, err := s.Summarize(context.Background(), []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hello"}}},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if text != "A short factual paragraph." {
		t.Errorf("text: want trimmed, got %q", text)
	}
	if tokens == 0 {
		t.Errorf("tokens should be > 0")
	}
	if fp.gotReq.System == "" {
		t.Errorf("expected system prompt to be set on the request")
	}
	if len(fp.gotReq.Messages) != 1 || fp.gotReq.Messages[0].Role != "user" {
		t.Errorf("expected one user message in request, got %+v", fp.gotReq.Messages)
	}
}

// TestLLMSummarizer_NilProviderRejected guards against the easy
// misuse.
func TestLLMSummarizer_NilProviderRejected(t *testing.T) {
	_, _, err := memory.LLMSummarizer{}.Summarize(context.Background(), []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "x"}}},
	})
	if err == nil {
		t.Error("expected error with nil provider")
	}
}
