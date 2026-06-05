package oacompat

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
)

// drainEvents collects every Event from ch into a slice; used by the
// table-driven ProcessStream tests below.
func drainEvents(ch <-chan providers.Event) []providers.Event {
	var out []providers.Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// runProcessStream wraps ProcessStream in a goroutine with a buffered
// channel + ctx + nil finish-reason mapper (so values pass through),
// then drains. Mirrors how Stream() drives ProcessStream in production.
func runProcessStream(t *testing.T, body string) []providers.Event {
	t.Helper()
	ch := make(chan providers.Event, 64)
	go func() {
		defer close(ch)
		ProcessStream(context.Background(), strings.NewReader(body), ch, "test", nil)
	}()
	return drainEvents(ch)
}

func TestProcessStream_TextDeltas(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\", world\"}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	events := runProcessStream(t, body)
	var text string
	var stops []string
	for _, ev := range events {
		switch ev.Kind {
		case providers.EventTextDelta:
			text += ev.Text
		case providers.EventStopReason:
			stops = append(stops, ev.Stop)
		case providers.EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if text != "Hello, world" {
		t.Errorf("text = %q, want %q", text, "Hello, world")
	}
	if len(stops) != 1 || stops[0] != "stop" {
		t.Errorf("stops = %v, want [stop]", stops)
	}
}

func TestProcessStream_ToolCallAggregation(t *testing.T) {
	// First chunk carries id+name; subsequent chunks stream the JSON
	// arguments one piece at a time; finish_reason=tool_calls triggers
	// the End emit in ascending Index order.
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"id\":\"call_a\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\"}}" +
		"]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"function\":{\"arguments\":\"\\\"a.go\\\"}\"}}" +
		"]}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	events := runProcessStream(t, body)

	var sawStart, sawEnd bool
	var endInput string
	var stop string
	for _, ev := range events {
		switch ev.Kind {
		case providers.EventToolUseStart:
			if ev.ToolUse.ID != "call_a" || ev.ToolUse.Name != "read" {
				t.Errorf("Start id=%q name=%q, want call_a/read", ev.ToolUse.ID, ev.ToolUse.Name)
			}
			sawStart = true
		case providers.EventToolUseEnd:
			sawEnd = true
			endInput = string(ev.ToolUse.Input)
		case providers.EventStopReason:
			stop = ev.Stop
		case providers.EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("missing Start (%v) or End (%v)", sawStart, sawEnd)
	}
	if !strings.Contains(endInput, `"path":"a.go"`) {
		t.Errorf("End input = %q, missing assembled args", endInput)
	}
	if stop != "tool_calls" {
		t.Errorf("stop = %q, want tool_calls", stop)
	}
}

func TestProcessStream_MalformedFrameSurfacesError(t *testing.T) {
	body := "data: not-json\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	events := runProcessStream(t, body)
	var sawErr bool
	for _, ev := range events {
		if ev.Kind == providers.EventError {
			sawErr = true
			if !strings.Contains(ev.Err.Error(), "parse chunk") {
				t.Errorf("error message = %q, want parse-chunk wrapping", ev.Err.Error())
			}
		}
	}
	if !sawErr {
		t.Error("malformed frame did not surface an EventError")
	}
}

func TestProcessStream_DefensiveFlushOnEOF(t *testing.T) {
	// Server closes connection mid-tool-call without finish_reason or
	// [DONE]. Defensive flushAllTools should still emit End so the agent
	// loop doesn't hang waiting on it.
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"id\":\"x\",\"function\":{\"name\":\"y\",\"arguments\":\"{}\"}}" +
		"]}}]}\n\n"
	events := runProcessStream(t, body)
	var sawEnd bool
	for _, ev := range events {
		if ev.Kind == providers.EventToolUseEnd {
			sawEnd = true
		}
	}
	if !sawEnd {
		t.Error("EOF without finish_reason left tool_use pending — defensive flush failed")
	}
}
