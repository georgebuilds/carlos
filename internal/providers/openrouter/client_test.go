package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/oacompat"
)

// newTestServer returns an httptest.Server that responds to POST
// /chat/completions with the supplied SSE script and a client pointed at it.
// headerHook (optional) captures the inbound request headers for assertions;
// bodyHook (optional) captures the parsed request body.
func newTestServer(
	t *testing.T,
	sseBody string,
	headerHook func(http.Header),
	bodyHook func([]byte),
) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if headerHook != nil {
			headerHook(r.Header)
		}
		if bodyHook != nil {
			b, _ := io.ReadAll(r.Body)
			bodyHook(b)
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
		`data: {"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`, ``,
		`data: {"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello, "}}]}`, ``,
		`data: {"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Boss."}}]}`, ``,
		`data: {"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, ``,
		`data: [DONE]`, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)

	ch, err := c.Stream(context.Background(), providers.Request{
		Model:    "openai/gpt-4o",
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
	if stop != "stop" {
		t.Errorf("stop: want stop got %q", stop)
	}
}

func TestStream_ToolUseAccumulatesInput(t *testing.T) {
	// Tool call arrives in three argument chunks (the realistic shape) plus
	// a final finish_reason="tool_calls" chunk. Expect: one ToolUseStart,
	// multiple ToolUseDelta, one ToolUseEnd with the assembled JSON, then
	// StopReason="tool_use" (the canonical Anthropic name — confirms
	// finish-reason mapping).
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_42","type":"function","function":{"name":"bash","arguments":""}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"ls"}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" /tmp\"}"}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, ``,
		`data: [DONE]`, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)

	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)

	var start, end *providers.ToolUse
	var deltaCount int
	var stop string
	for _, ev := range got {
		switch ev.Kind {
		case providers.EventToolUseStart:
			start = ev.ToolUse
		case providers.EventToolUseDelta:
			deltaCount++
		case providers.EventToolUseEnd:
			end = ev.ToolUse
		case providers.EventStopReason:
			stop = ev.Stop
		case providers.EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if start == nil || start.ID != "call_42" || start.Name != "bash" {
		t.Errorf("start: %+v", start)
	}
	if deltaCount != 2 {
		t.Errorf("delta count: want 2, got %d", deltaCount)
	}
	if end == nil {
		t.Fatal("no EventToolUseEnd")
	}
	if string(end.Input) != `{"cmd":"ls /tmp"}` {
		t.Errorf("end.Input = %q, want %q", string(end.Input), `{"cmd":"ls /tmp"}`)
	}
	if end.ID != "call_42" || end.Name != "bash" {
		t.Errorf("end identity: id=%q name=%q", end.ID, end.Name)
	}
	if stop != "tool_use" {
		t.Errorf("stop: want tool_use (mapped from tool_calls) got %q", stop)
	}
	// EventToolUseEnd must arrive BEFORE EventStopReason so the agent loop
	// can collect tool_use blocks before deciding to dispatch.
	endIdx, stopIdx := -1, -1
	for i, ev := range got {
		if ev.Kind == providers.EventToolUseEnd {
			endIdx = i
		}
		if ev.Kind == providers.EventStopReason {
			stopIdx = i
		}
	}
	if endIdx == -1 || stopIdx == -1 || endIdx > stopIdx {
		t.Errorf("event ordering: ToolUseEnd@%d must precede StopReason@%d", endIdx, stopIdx)
	}
}

func TestStream_ParallelToolCalls(t *testing.T) {
	// Two parallel tool_calls (indices 0 and 1) interleaved across chunks.
	// Both must end up fully assembled with the correct id/name/input.
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"foo","arguments":""}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"bar","arguments":""}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":1}"}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"y\":2}"}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, ``,
		`data: [DONE]`, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)

	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)

	ends := map[string]*providers.ToolUse{}
	for _, ev := range got {
		if ev.Kind == providers.EventToolUseEnd {
			ends[ev.ToolUse.ID] = ev.ToolUse
		}
	}
	if len(ends) != 2 {
		t.Fatalf("want 2 ToolUseEnd events, got %d", len(ends))
	}
	if foo := ends["a"]; foo == nil || foo.Name != "foo" || string(foo.Input) != `{"x":1}` {
		t.Errorf("foo: %+v", foo)
	}
	if bar := ends["b"]; bar == nil || bar.Name != "bar" || string(bar.Input) != `{"y":2}` {
		t.Errorf("bar: %+v", bar)
	}
}

func TestStream_HeadersAndAuth(t *testing.T) {
	var captured http.Header
	body := "data: [DONE]\n\n"
	c, _ := newTestServer(t, body, func(h http.Header) { captured = h.Clone() }, nil)
	_, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := captured.Get("Authorization"); got != "Bearer test-api-key" {
		t.Errorf("Authorization = %q", got)
	}
	if got := captured.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	if got := captured.Get("HTTP-Referer"); got != httpReferer {
		t.Errorf("HTTP-Referer = %q, want %q", got, httpReferer)
	}
	if got := captured.Get("X-Title"); got != xTitle {
		t.Errorf("X-Title = %q, want %q", got, xTitle)
	}
}

func TestStream_RequestBodyShape(t *testing.T) {
	// Verify the canonical Request → OpenAI wire-format translation:
	// system prompt becomes a leading role=system message, tool_use blocks
	// fold into assistant.tool_calls, tool_results become role=tool messages,
	// and the model id passes through namespaced.
	var captured []byte
	body := "data: [DONE]\n\n"
	c, _ := newTestServer(t, body, nil, func(b []byte) { captured = b })

	req := providers.Request{
		Model:  "anthropic/claude-3.5-sonnet",
		System: "You are carlos.",
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Kind: "text", Text: "run ls"}}},
			{Role: "assistant", Content: []providers.Block{
				{Kind: "text", Text: "ok"},
				{Kind: "tool_use", ToolUseID: "tu-1", ToolName: "bash", ToolInput: []byte(`{"cmd":"ls"}`)},
			}},
			{Role: "user", Content: []providers.Block{
				{Kind: "tool_result", ToolUseID: "tu-1", ToolResult: []byte("file1\nfile2\n")},
			}},
		},
		Tools: []providers.ToolSpec{
			{Name: "bash", Description: "run a command", Schema: []byte(`{"type":"object"}`)},
		},
	}
	_, err := c.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var got oacompat.MessagesRequest
	if err := json.Unmarshal(captured, &got); err != nil {
		t.Fatalf("unmarshal sent body: %v\nbody: %s", err, captured)
	}
	if got.Model != "anthropic/claude-3.5-sonnet" {
		t.Errorf("model: %q", got.Model)
	}
	if !got.Stream {
		t.Errorf("stream flag not set")
	}
	if len(got.Messages) != 4 {
		t.Fatalf("want 4 messages (system+user+assistant+tool), got %d: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "system" || string(got.Messages[0].Content) != `"You are carlos."` {
		t.Errorf("system message: %+v", got.Messages[0])
	}
	if got.Messages[1].Role != "user" || string(got.Messages[1].Content) != `"run ls"` {
		t.Errorf("user message: %+v", got.Messages[1])
	}
	asst := got.Messages[2]
	if asst.Role != "assistant" || string(asst.Content) != `"ok"` {
		t.Errorf("assistant message: %+v", asst)
	}
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "tu-1" ||
		asst.ToolCalls[0].Function.Name != "bash" ||
		asst.ToolCalls[0].Function.Arguments != `{"cmd":"ls"}` {
		t.Errorf("assistant tool_calls: %+v", asst.ToolCalls)
	}
	tool := got.Messages[3]
	if tool.Role != "tool" || tool.ToolCallID != "tu-1" || string(tool.Content) != `"file1\nfile2\n"` {
		t.Errorf("tool message: %+v", tool)
	}
	if len(got.Tools) != 1 || got.Tools[0].Type != "function" ||
		got.Tools[0].Function.Name != "bash" ||
		got.Tools[0].Function.Description != "run a command" {
		t.Errorf("tools: %+v", got.Tools)
	}
}

func TestStream_NonOKErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, `{"error":{"code":401,"message":"No auth credentials found"}}`)
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

func TestStream_MidStreamErrorSurfaces(t *testing.T) {
	// OpenRouter sometimes returns errors mid-stream rather than as an HTTP
	// non-2xx (e.g., the upstream provider rejected the request after
	// OpenRouter accepted the connection). These must surface as EventError.
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"thinking..."}}]}`, ``,
		`data: {"error":{"code":"upstream_error","message":"provider unavailable"}}`, ``,
		`data: [DONE]`, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)
	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)
	var sawError bool
	for _, ev := range got {
		if ev.Kind == providers.EventError {
			sawError = true
			if !strings.Contains(ev.Err.Error(), "provider unavailable") {
				t.Errorf("error event message: %v", ev.Err)
			}
		}
	}
	if !sawError {
		t.Errorf("expected EventError; got %+v", got)
	}
}

func TestStream_FinishReasonMapping(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"stop", "stop"},
		{"length", "length"},
		{"tool_calls", "tool_use"},
		{"function_call", "tool_use"},
		{"content_filter", "content_filter"},
	}
	for _, tc := range cases {
		if got := mapFinishReason(tc.in); got != tc.want {
			t.Errorf("mapFinishReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
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
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n")
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

func TestStream_DoneSentinelEndsStream(t *testing.T) {
	// Server emits text then [DONE] with NO finish_reason chunk in between.
	// Real OpenAI-compatible providers always send finish_reason, but if
	// one misbehaves the client must still close cleanly on [DONE].
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}`, ``,
		`data: [DONE]`, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)
	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)
	if len(got) == 0 {
		t.Fatal("no events received")
	}
	for _, ev := range got {
		if ev.Kind == providers.EventError {
			t.Errorf("unexpected error: %v", ev.Err)
		}
	}
}

func TestStream_PendingToolFlushedOnEarlyDone(t *testing.T) {
	// Pathological case: stream emits a tool_call, then [DONE] WITHOUT a
	// finish_reason chunk. The accumulator must still flush so the
	// tool_call isn't silently dropped.
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"orphan","type":"function","function":{"name":"x","arguments":"{}"}}]}}]}`, ``,
		`data: [DONE]`, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)
	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)
	var sawEnd bool
	for _, ev := range got {
		if ev.Kind == providers.EventToolUseEnd && ev.ToolUse.ID == "orphan" {
			sawEnd = true
			if string(ev.ToolUse.Input) != "{}" {
				t.Errorf("orphan tool input = %q", ev.ToolUse.Input)
			}
		}
	}
	if !sawEnd {
		t.Errorf("orphan tool_call was dropped instead of flushed: %+v", got)
	}
}

func TestCapabilities(t *testing.T) {
	c := New("k")
	caps := c.Capabilities()
	if !caps.ParallelToolUse {
		t.Error("ParallelToolUse should default true for OpenRouter")
	}
	if caps.PromptCaching {
		t.Error("PromptCaching should default false (varies by upstream model)")
	}
	if !caps.StructuredOut {
		t.Error("StructuredOut should default true")
	}
	if !caps.Vision {
		t.Error("Vision should default true")
	}
	if c.Name() != "openrouter" {
		t.Errorf("Name = %q", c.Name())
	}
}
