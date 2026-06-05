package openai

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
)

// newTestServer returns an httptest.Server that responds to
// POST /v1/chat/completions with the supplied SSE script and a client
// pointed at it. headerHook (optional) captures inbound request headers.
// bodyHook (optional) captures the inbound request body for buildRequest
// assertions.
func newTestServer(t *testing.T, sseBody string, headerHook func(http.Header), bodyHook func([]byte)) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if headerHook != nil {
			headerHook(r.Header)
		}
		if bodyHook != nil {
			body, _ := io.ReadAll(r.Body)
			bodyHook(body)
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
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"content":"Hello, "}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"content":"Boss."}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, ``,
		`data: [DONE]`, ``, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)

	ch, err := c.Stream(context.Background(), providers.Request{
		Model:    "gpt-4o-mini",
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
	// finish_reason "stop" maps to canonical "end_turn".
	if stop != "end_turn" {
		t.Errorf("stop: want end_turn got %q", stop)
	}
}

func TestStream_ToolCallAccumulatesArgsAcrossChunks(t *testing.T) {
	// Reproduces the canonical OpenAI tool-call streaming shape:
	//   chunk 1: id + name (no args yet)
	//   chunk 2: args fragment "{"cmd":"ls"
	//   chunk 3: args fragment " /tmp\"}"
	//   chunk 4: finish_reason=tool_calls
	//   [DONE]
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant"}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"bash","arguments":""}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"ls"}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" /tmp\"}"}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, ``,
		`data: [DONE]`, ``, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)

	ch, err := c.Stream(context.Background(), providers.Request{Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)

	var start, end *providers.ToolUse
	var stop string
	var deltaConcat strings.Builder
	var sawEndBeforeStop bool
	stopSeen := false
	endSeen := false
	for _, ev := range got {
		switch ev.Kind {
		case providers.EventToolUseStart:
			start = ev.ToolUse
		case providers.EventToolUseDelta:
			if ev.ToolUse != nil {
				deltaConcat.Write(ev.ToolUse.Input)
			}
		case providers.EventToolUseEnd:
			end = ev.ToolUse
			endSeen = true
			if !stopSeen {
				sawEndBeforeStop = true
			}
		case providers.EventStopReason:
			stop = ev.Stop
			stopSeen = true
		case providers.EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if start == nil || start.ID != "call_abc" || start.Name != "bash" {
		t.Errorf("start: %+v", start)
	}
	if !endSeen || end == nil {
		t.Fatal("no EventToolUseEnd")
	}
	if string(end.Input) != `{"cmd":"ls /tmp"}` {
		t.Errorf("end.Input = %q, want %q", string(end.Input), `{"cmd":"ls /tmp"}`)
	}
	if deltaConcat.String() != `{"cmd":"ls /tmp"}` {
		t.Errorf("concatenated deltas = %q, want %q", deltaConcat.String(), `{"cmd":"ls /tmp"}`)
	}
	// finish_reason "tool_calls" → canonical "tool_use".
	if stop != "tool_use" {
		t.Errorf("stop: want tool_use got %q", stop)
	}
	// Ordering guarantee: the agent loop reads EventToolUseEnd before it
	// switches on EventStopReason. Lock this in.
	if !sawEndBeforeStop {
		t.Errorf("EventToolUseEnd must arrive BEFORE EventStopReason")
	}
}

func TestStream_ParallelToolCallsByIndex(t *testing.T) {
	// Two parallel tool_calls in the same turn (gpt-4o style). The deltas
	// for index=0 and index=1 are interleaved across chunks; the client
	// must reassemble each by index.
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant"}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"bash","arguments":""}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"ls\"}"}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"path\":\"/tmp\"}"}}]}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, ``,
		`data: [DONE]`, ``, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)
	ch, err := c.Stream(context.Background(), providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)

	ends := map[string]*providers.ToolUse{}
	for _, ev := range got {
		if ev.Kind == providers.EventToolUseEnd && ev.ToolUse != nil {
			ends[ev.ToolUse.ID] = ev.ToolUse
		}
	}
	if len(ends) != 2 {
		t.Fatalf("want 2 tool_use ends, got %d: %+v", len(ends), ends)
	}
	if ends["call_a"] == nil || ends["call_a"].Name != "bash" || string(ends["call_a"].Input) != `{"cmd":"ls"}` {
		t.Errorf("call_a: %+v", ends["call_a"])
	}
	if ends["call_b"] == nil || ends["call_b"].Name != "read_file" || string(ends["call_b"].Input) != `{"path":"/tmp"}` {
		t.Errorf("call_b: %+v", ends["call_b"])
	}
}

func TestStream_HeadersAndAuth(t *testing.T) {
	var captured http.Header
	body := "data: [DONE]\n\n"
	c, _ := newTestServer(t, body, func(h http.Header) { captured = h.Clone() }, nil)
	_, err := c.Stream(context.Background(), providers.Request{Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if captured.Get("Authorization") != "Bearer test-api-key" {
		t.Errorf("Authorization = %q", captured.Get("Authorization"))
	}
	if captured.Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %q", captured.Get("Content-Type"))
	}
	if !strings.Contains(captured.Get("Accept"), "text/event-stream") {
		t.Errorf("accept = %q", captured.Get("Accept"))
	}
}

func TestStream_BuildRequestShape(t *testing.T) {
	// Lock in the wire-format conversion: system prompt → first message,
	// tools → tools[].function, stream:true, schema passes through.
	var bodyBytes []byte
	body := "data: [DONE]\n\n"
	c, _ := newTestServer(t, body, nil, func(b []byte) { bodyBytes = b })
	_, err := c.Stream(context.Background(), providers.Request{
		Model:  "gpt-4o-mini",
		System: "you are carlos",
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}},
		},
		Tools: []providers.ToolSpec{
			{
				Name:        "bash",
				Description: "run a shell command",
				Schema:      []byte(`{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}`),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		t.Fatalf("body not valid JSON: %v\n%s", err, bodyBytes)
	}
	if parsed["model"] != "gpt-4o-mini" {
		t.Errorf("model: %v", parsed["model"])
	}
	if parsed["stream"] != true {
		t.Errorf("stream: %v", parsed["stream"])
	}
	msgs, _ := parsed["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages: want 2 (system + user), got %d", len(msgs))
	}
	if m0 := msgs[0].(map[string]any); m0["role"] != "system" || m0["content"] != "you are carlos" {
		t.Errorf("messages[0]: %+v", m0)
	}
	if m1 := msgs[1].(map[string]any); m1["role"] != "user" || m1["content"] != "hi" {
		t.Errorf("messages[1]: %+v", m1)
	}
	tools, _ := parsed["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools: want 1, got %d", len(tools))
	}
	tool0 := tools[0].(map[string]any)
	if tool0["type"] != "function" {
		t.Errorf("tool.type: %v", tool0["type"])
	}
	fn := tool0["function"].(map[string]any)
	if fn["name"] != "bash" {
		t.Errorf("tool.function.name: %v", fn["name"])
	}
	// parameters must be the JSON schema verbatim (an object, not a string)
	if _, ok := fn["parameters"].(map[string]any); !ok {
		t.Errorf("tool.function.parameters is not an object: %T %v", fn["parameters"], fn["parameters"])
	}
}

func TestStream_BuildRequestToolResultFanOut(t *testing.T) {
	// A canonical user message carrying tool_result blocks must fan out
	// to role:"tool" messages with tool_call_id on the wire.
	var bodyBytes []byte
	body := "data: [DONE]\n\n"
	c, _ := newTestServer(t, body, nil, func(b []byte) { bodyBytes = b })
	_, err := c.Stream(context.Background(), providers.Request{
		Model: "gpt-4o-mini",
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Kind: "text", Text: "do it"}}},
			{Role: "assistant", Content: []providers.Block{
				{Kind: "tool_use", ToolUseID: "call_1", ToolName: "bash", ToolInput: []byte(`{"cmd":"ls"}`)},
			}},
			{Role: "user", Content: []providers.Block{
				{Kind: "tool_result", ToolUseID: "call_1", ToolResult: []byte("a.txt b.txt")},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		t.Fatal(err)
	}
	msgs := parsed["messages"].([]any)
	// user, assistant(tool_calls), tool(result) → 3 messages
	if len(msgs) != 3 {
		t.Fatalf("messages: want 3, got %d (%+v)", len(msgs), msgs)
	}
	a := msgs[1].(map[string]any)
	if a["role"] != "assistant" {
		t.Errorf("messages[1].role: %v", a["role"])
	}
	tcs, _ := a["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("assistant.tool_calls: want 1, got %d", len(tcs))
	}
	tc0 := tcs[0].(map[string]any)
	if tc0["id"] != "call_1" || tc0["type"] != "function" {
		t.Errorf("tool_call: %+v", tc0)
	}
	if fn := tc0["function"].(map[string]any); fn["name"] != "bash" || fn["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("tool_call.function: %+v", fn)
	}
	tmsg := msgs[2].(map[string]any)
	if tmsg["role"] != "tool" || tmsg["tool_call_id"] != "call_1" || tmsg["content"] != "a.txt b.txt" {
		t.Errorf("tool message: %+v", tmsg)
	}
}

func TestStream_NonOKErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, `{"error":{"message":"Incorrect API key","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(ts.Close)
	c := New("nope")
	c.BaseURL = ts.URL
	_, err := c.Stream(context.Background(), providers.Request{Model: "gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestStream_EmbeddedStreamErrorSurfacesAsEvent(t *testing.T) {
	// Some gateways embed errors in 200-OK streams rather than tearing down
	// the connection. Make sure those surface as EventError.
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}`, ``,
		`data: {"error":{"type":"server_error","message":"upstream timeout"}}`, ``,
		`data: [DONE]`, ``, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)
	ch, err := c.Stream(context.Background(), providers.Request{Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	var sawErr bool
	for ev := range ch {
		if ev.Kind == providers.EventError {
			sawErr = true
			if !strings.Contains(ev.Err.Error(), "upstream timeout") {
				t.Errorf("error message: %v", ev.Err)
			}
		}
	}
	if !sawErr {
		t.Error("expected EventError from embedded stream error")
	}
}

func TestStream_MalformedFrameDoesNotTearDownStream(t *testing.T) {
	// One garbage frame should emit EventError + continue, NOT close the
	// channel prematurely.
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"a"}}]}`, ``,
		`data: not-json-at-all`, ``,
		`data: {"choices":[{"index":0,"delta":{"content":"b"}}]}`, ``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, ``,
		`data: [DONE]`, ``, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)
	ch, err := c.Stream(context.Background(), providers.Request{Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var sawErr bool
	for ev := range ch {
		switch ev.Kind {
		case providers.EventTextDelta:
			text.WriteString(ev.Text)
		case providers.EventError:
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("expected EventError on malformed frame")
	}
	// Both a and b should have made it through.
	if text.String() != "ab" {
		t.Errorf("text after recovery: %q", text.String())
	}
}

func TestStream_FlushesToolCallOnDoneWithoutFinishReason(t *testing.T) {
	// Defensive path: a compatible server emits tool_calls and [DONE] but
	// forgets to set finish_reason. We must still flush EventToolUseEnd.
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`, ``,
		`data: [DONE]`, ``, ``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)
	ch, err := c.Stream(context.Background(), providers.Request{Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	var end *providers.ToolUse
	for ev := range ch {
		if ev.Kind == providers.EventToolUseEnd {
			end = ev.ToolUse
		}
	}
	if end == nil {
		t.Fatal("EventToolUseEnd was not flushed on [DONE] without finish_reason")
	}
	if end.ID != "call_x" || end.Name != "bash" || string(end.Input) != `{"cmd":"ls"}` {
		t.Errorf("end: %+v", end)
	}
}

func TestStream_FinishReasonMapping(t *testing.T) {
	cases := []struct {
		fr       string
		wantStop string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"content_filter", "content_filter"},
	}
	for _, tc := range cases {
		t.Run(tc.fr, func(t *testing.T) {
			body := fmt.Sprintf(`data: {"choices":[{"index":0,"delta":{"content":"x"}}]}`+"\n\n"+
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":%q}]}`+"\n\n"+
				`data: [DONE]`+"\n\n", tc.fr)
			c, _ := newTestServer(t, body, nil, nil)
			ch, err := c.Stream(context.Background(), providers.Request{Model: "gpt-4o-mini"})
			if err != nil {
				t.Fatal(err)
			}
			var stop string
			for ev := range ch {
				if ev.Kind == providers.EventStopReason {
					stop = ev.Stop
				}
			}
			if stop != tc.wantStop {
				t.Errorf("finish_reason %q → stop %q, want %q", tc.fr, stop, tc.wantStop)
			}
		})
	}
}

func TestStream_CancellationStopsGoroutine(t *testing.T) {
	// Server emits one frame to push past response-header stage, then holds
	// the connection until the client cancels ctx.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}`+"\n\n")
		f.Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)
	c := New("k")
	c.BaseURL = ts.URL

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Stream(ctx, providers.Request{Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
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

func TestCapabilities(t *testing.T) {
	c := New("k")
	caps := c.Capabilities()
	if !caps.ParallelToolUse {
		t.Error("ParallelToolUse must be true (gpt-4o family)")
	}
	if !caps.PromptCaching {
		t.Error("PromptCaching must be true (gpt-4o automatic caching)")
	}
	if !caps.StructuredOut {
		t.Error("StructuredOut must be true (response_format)")
	}
	if !caps.Vision {
		t.Error("Vision must be true (gpt-4o image_url)")
	}
}

func TestName(t *testing.T) {
	if got := New("k").Name(); got != "openai" {
		t.Errorf("Name() = %q, want openai", got)
	}
}
