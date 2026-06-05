package anthropic

import (
	"strings"
	"testing"
)

func TestParseSSE_SingleFrame(t *testing.T) {
	input := "event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n" +
		"\n"
	var got []sseFrame
	err := parseSSE(strings.NewReader(input), func(f sseFrame) error {
		got = append(got, f)
		return nil
	})
	if err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("frames: want 1 got %d", len(got))
	}
	if got[0].Event != "content_block_delta" {
		t.Errorf("event = %q", got[0].Event)
	}
	if !strings.Contains(got[0].Data, "Hi") {
		t.Errorf("data does not contain payload: %q", got[0].Data)
	}
}

func TestParseSSE_MultipleFramesAndKeepalive(t *testing.T) {
	input := ": ping\n" +
		"event: message_start\n" +
		"data: {\"type\":\"message_start\"}\n" +
		"\n" +
		": ping\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n" +
		"\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n" +
		"\n"
	var frames []sseFrame
	if err := parseSSE(strings.NewReader(input), func(f sseFrame) error {
		frames = append(frames, f)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("frames: want 3 got %d", len(frames))
	}
	wantEvents := []string{"message_start", "content_block_delta", "message_stop"}
	for i, w := range wantEvents {
		if frames[i].Event != w {
			t.Errorf("frame %d event: want %s got %s", i, w, frames[i].Event)
		}
	}
}

func TestParseSSE_TrailingFrameWithoutBlankLine(t *testing.T) {
	// Some servers omit the trailing blank line before EOF. The parser
	// should still dispatch the last frame.
	input := "event: message_stop\n" + "data: {\"type\":\"message_stop\"}\n"
	var count int
	_ = parseSSE(strings.NewReader(input), func(sseFrame) error { count++; return nil })
	if count != 1 {
		t.Errorf("want 1 trailing frame dispatched, got %d", count)
	}
}

func TestParseSSE_LeadingSpaceAfterColonIsStripped(t *testing.T) {
	input := "event: foo\n" + "data: {\"k\":1}\n\n"
	var f sseFrame
	_ = parseSSE(strings.NewReader(input), func(g sseFrame) error { f = g; return nil })
	if f.Data != `{"k":1}` {
		t.Errorf("data = %q (leading space not stripped)", f.Data)
	}
}
