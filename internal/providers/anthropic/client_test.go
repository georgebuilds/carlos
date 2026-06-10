package anthropic

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
)

// newTestServer returns an httptest.Server that responds to POST /v1/messages
// with the supplied SSE script and a client pointed at it.
func newTestServer(t *testing.T, sseBody string, headerHook func(http.Header)) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		if headerHook != nil {
			headerHook(r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			io.WriteString(w, sseBody)
			f.Flush()
		} else {
			io.WriteString(w, sseBody)
		}
	}))
	t.Cleanup(ts.Close)
	c := New("test-api-key")
	c.BaseURL = ts.URL
	return c, ts
}

func collect(ch <-chan providers.Event) []providers.Event {
	var out []providers.Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestStream_TextDeltasAndStop(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`, `data: {"type":"message_start"}`, ``,
		`event: content_block_start`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello, "}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Boss."}}`, ``,
		`event: content_block_stop`, `data: {"type":"content_block_stop","index":0}`, ``,
		`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, ``,
		`event: message_stop`, `data: {"type":"message_stop"}`, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil)

	ch, err := c.Stream(context.Background(), providers.Request{
		Model:    "claude-3-5-sonnet-latest",
		Messages: []providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)

	var text strings.Builder
	var stop string
	for _, ev := range got {
		switch ev.Kind {
		case providers.EventTextDelta:
			text.WriteString(ev.Text)
		case providers.EventStopReason:
			stop = ev.Stop
		case providers.EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if text.String() != "Hello, Boss." {
		t.Errorf("text: want %q got %q", "Hello, Boss.", text.String())
	}
	if stop != "end_turn" {
		t.Errorf("stop: want end_turn got %q", stop)
	}
}

func TestStream_ToolUseAccumulatesInput(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`, `data: {"type":"message_start"}`, ``,
		`event: content_block_start`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu-1","name":"bash","input":{}}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls"}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" /tmp\"}"}}`, ``,
		`event: content_block_stop`, `data: {"type":"content_block_stop","index":0}`, ``,
		`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`, ``,
		`event: message_stop`, `data: {"type":"message_stop"}`, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil)

	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)

	var start, end *providers.ToolUse
	for _, ev := range got {
		switch ev.Kind {
		case providers.EventToolUseStart:
			start = ev.ToolUse
		case providers.EventToolUseEnd:
			end = ev.ToolUse
		}
	}
	if start == nil || start.ID != "tu-1" || start.Name != "bash" {
		t.Errorf("start: %+v", start)
	}
	if end == nil {
		t.Fatal("no EventToolUseEnd")
	}
	if string(end.Input) != `{"cmd":"ls /tmp"}` {
		t.Errorf("end.Input = %q, want %q", string(end.Input), `{"cmd":"ls /tmp"}`)
	}
}

// TestStream_ToolUseStopWithoutDeltasDoesNotPanic covers the defensive
// branch in content_block_stop: when a tool_use block is opened and
// immediately stopped without any input_json_delta in between, the
// accumulator buffer is empty (or, under a protocol skew where the start
// frame's index doesn't match the stop frame's buffer registration, nil).
// The handler must not panic and must emit EventToolUseEnd with Input "{}"
// so downstream consumers see a well-formed tool_use block.
func TestStream_ToolUseStopWithoutDeltasDoesNotPanic(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`, `data: {"type":"message_start"}`, ``,
		`event: content_block_start`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu-empty","name":"noop","input":{}}}`, ``,
		// No input_json_delta - go straight to stop.
		`event: content_block_stop`, `data: {"type":"content_block_stop","index":0}`, ``,
		`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`, ``,
		`event: message_stop`, `data: {"type":"message_stop"}`, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil)

	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)

	var end *providers.ToolUse
	for _, ev := range got {
		if ev.Kind == providers.EventError {
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
		if ev.Kind == providers.EventToolUseEnd {
			end = ev.ToolUse
		}
	}
	if end == nil {
		t.Fatal("no EventToolUseEnd emitted")
	}
	if end.ID != "tu-empty" || end.Name != "noop" {
		t.Errorf("tool identity: %+v", end)
	}
	if string(end.Input) != "{}" {
		t.Errorf("Input = %q, want %q", string(end.Input), "{}")
	}
}

// TestStream_ToolUseStopForUnknownIndexIsIgnored covers the other edge of
// the same defense: a content_block_stop for an index that was never
// opened (corrupt frame ordering, flaky proxy reordering frames, future
// stream format change). The handler must not panic and must not emit a
// spurious EventToolUseEnd - the unknown index simply has no associated
// tool_use to close.
func TestStream_ToolUseStopForUnknownIndexIsIgnored(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`, `data: {"type":"message_start"}`, ``,
		// Stop for index 7 with no prior start - must be a no-op.
		`event: content_block_stop`, `data: {"type":"content_block_stop","index":7}`, ``,
		`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, ``,
		`event: message_stop`, `data: {"type":"message_stop"}`, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil)

	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)

	for _, ev := range got {
		if ev.Kind == providers.EventToolUseEnd {
			t.Errorf("unexpected EventToolUseEnd for unknown index: %+v", ev.ToolUse)
		}
		if ev.Kind == providers.EventError {
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
}

func TestStream_HeadersAndAuth(t *testing.T) {
	var captured http.Header
	body := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	c, _ := newTestServer(t, body, func(h http.Header) { captured = h.Clone() })
	_, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if captured.Get("x-api-key") != "test-api-key" {
		t.Errorf("x-api-key = %q", captured.Get("x-api-key"))
	}
	if captured.Get("anthropic-version") != apiVersion {
		t.Errorf("anthropic-version = %q", captured.Get("anthropic-version"))
	}
	if captured.Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %q", captured.Get("Content-Type"))
	}
}

func TestStream_NonOKErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, `{"error":{"type":"authentication_error","message":"bad key"}}`)
	}))
	t.Cleanup(ts.Close)
	c := New("nope")
	c.BaseURL = ts.URL
	_, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestStream_CancellationStopsGoroutine(t *testing.T) {
	// Server hangs forever; ctx cancellation must close the channel.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		// Emit one frame so the client transitions past the response-header
		// stage; then hold the connection open until the client cancels.
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
		f.Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)
	c := New("k")
	c.BaseURL = ts.URL

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Stream(ctx, providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// Cancel shortly after; ensure the channel closes within a deadline.
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed → goroutine returned cleanly
			}
		case <-deadline:
			t.Fatal("stream did not close after ctx cancellation")
		}
	}
}
