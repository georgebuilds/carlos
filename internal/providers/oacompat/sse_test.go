package oacompat

import (
	"strings"
	"testing"
)

// SSE parser tests are unified here from the original per-provider files
// (`openai/sse_test.go` + `openrouter/sse_test.go`) when those parsers
// merged into oacompat. Each case preserved the intent of its original.

func TestParseSSE_SingleDataFrame(t *testing.T) {
	// OpenAI's typical shape: no `event:` line, just `data: {json}\n\n`.
	input := `data: {"id":"x","choices":[{"delta":{"content":"hi"}}]}` + "\n\n"
	var got []SSEFrame
	err := ParseSSE(strings.NewReader(input), func(f SSEFrame) error {
		got = append(got, f)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("frames: want 1 got %d", len(got))
	}
	if got[0].Event != "" {
		t.Errorf("event = %q, want empty (OpenAI default)", got[0].Event)
	}
	if !strings.Contains(got[0].Data, `"content":"hi"`) {
		t.Errorf("data does not contain payload: %q", got[0].Data)
	}
}

func TestParseSSE_DoneSentinelIsADataFrame(t *testing.T) {
	// The parser doesn't special-case [DONE]; it passes it through as a
	// data frame and the caller treats it as end-of-stream.
	input := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: [DONE]\n\n"
	var frames []SSEFrame
	_ = ParseSSE(strings.NewReader(input), func(f SSEFrame) error {
		frames = append(frames, f)
		return nil
	})
	if len(frames) != 2 {
		t.Fatalf("frames: want 2 got %d", len(frames))
	}
	if frames[1].Data != "[DONE]" {
		t.Errorf("[DONE] frame: got data %q", frames[1].Data)
	}
}

func TestParseSSE_MultipleFramesAndKeepalive(t *testing.T) {
	// OpenRouter sends ": OPENROUTER PROCESSING" keepalives; OpenAI uses
	// generic ": " comments. Both must be ignored, not dispatched.
	input := ": OPENROUTER PROCESSING\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		": keepalive\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n" +
		"data: [DONE]\n\n"
	var frames []SSEFrame
	if err := ParseSSE(strings.NewReader(input), func(f SSEFrame) error {
		frames = append(frames, f)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("frames: want 3 got %d", len(frames))
	}
	if frames[2].Data != "[DONE]" {
		t.Errorf("last frame data: want [DONE] got %q", frames[2].Data)
	}
}

func TestParseSSE_TrailingFrameWithoutBlankLine(t *testing.T) {
	// Defensive: some proxies omit the trailing blank line before EOF.
	input := "data: {\"x\":1}\n"
	var count int
	_ = ParseSSE(strings.NewReader(input), func(SSEFrame) error { count++; return nil })
	if count != 1 {
		t.Errorf("want 1 trailing frame dispatched, got %d", count)
	}
}

func TestParseSSE_LeadingSpaceAfterColonIsStripped(t *testing.T) {
	input := "data: {\"k\":1}\n\n"
	var f SSEFrame
	_ = ParseSSE(strings.NewReader(input), func(g SSEFrame) error { f = g; return nil })
	if f.Data != `{"k":1}` {
		t.Errorf("data = %q (leading space not stripped)", f.Data)
	}
}

func TestParseSSE_EventNameRoundTrips(t *testing.T) {
	// OpenRouter / certain gateways emit named events; round-trip them.
	input := "event: chunk\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"
	var f SSEFrame
	_ = ParseSSE(strings.NewReader(input), func(g SSEFrame) error { f = g; return nil })
	if f.Event != "chunk" {
		t.Errorf("event = %q, want chunk", f.Event)
	}
}

func TestParseSSE_MultilineDataConcatWithNewline(t *testing.T) {
	// Per SSE spec, multiple `data:` lines for one frame are joined with
	// "\n". OpenAI doesn't emit this, but the parser supports it.
	input := "data: line1\ndata: line2\n\n"
	var f SSEFrame
	_ = ParseSSE(strings.NewReader(input), func(g SSEFrame) error { f = g; return nil })
	if f.Data != "line1\nline2" {
		t.Errorf("multi-line data = %q, want line1\\nline2", f.Data)
	}
}
