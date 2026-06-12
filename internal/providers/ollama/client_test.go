package ollama

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

// newTestServer returns an httptest.Server that responds to POST /api/chat
// with the supplied JSONL body and a client pointed at it. The headerHook,
// when non-nil, receives a clone of the request headers so tests can
// assert on what we send.
func newTestServer(t *testing.T, jsonlBody string, headerHook func(http.Header), bodyHook func([]byte)) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
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
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			io.WriteString(w, jsonlBody)
			f.Flush()
		} else {
			io.WriteString(w, jsonlBody)
		}
	}))
	t.Cleanup(ts.Close)
	c := New(ts.URL)
	return c, ts
}

func collect(ch <-chan providers.Event) []providers.Event {
	var out []providers.Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// TestStream_TextDeltasAndStop covers the canonical happy path: a few
// content-bearing chunks followed by a done=true chunk with stop_reason=stop.
func TestStream_TextDeltasAndStop(t *testing.T) {
	body := strings.Join([]string{
		`{"model":"llama3.1:latest","created_at":"2026-06-04T00:00:00Z","message":{"role":"assistant","content":"Hello, "},"done":false}`,
		`{"model":"llama3.1:latest","created_at":"2026-06-04T00:00:00Z","message":{"role":"assistant","content":"Boss."},"done":false}`,
		`{"model":"llama3.1:latest","created_at":"2026-06-04T00:00:00Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`,
		``,
	}, "\n")

	c, _ := newTestServer(t, body, nil, nil)

	ch, err := c.Stream(context.Background(), providers.Request{
		Model:    "llama3.1:latest",
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
		t.Errorf("stop: want end_turn (mapped from 'stop') got %q", stop)
	}
}

// TestStream_ToolCallAtomic verifies Ollama's atomic tool_call shape:
// the entire tool_call (name + arguments object) arrives in one chunk,
// and we surface it as Start immediately followed by End. Also verifies
// stop_reason becomes "tool_use" even though Ollama reports "stop".
func TestStream_ToolCallAtomic(t *testing.T) {
	body := strings.Join([]string{
		`{"model":"llama3.1:latest","created_at":"2026-06-04T00:00:00Z","message":{"role":"assistant","content":""},"done":false}`,
		`{"model":"llama3.1:latest","created_at":"2026-06-04T00:00:00Z","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"bash","arguments":{"cmd":"ls /tmp"}}}]},"done":true,"done_reason":"stop"}`,
		``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)

	ch, err := c.Stream(context.Background(), providers.Request{Model: "llama3.1:latest"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)

	var start, end *providers.ToolUse
	var stop string
	for _, ev := range got {
		switch ev.Kind {
		case providers.EventToolUseStart:
			start = ev.ToolUse
		case providers.EventToolUseEnd:
			end = ev.ToolUse
		case providers.EventStopReason:
			stop = ev.Stop
		case providers.EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}

	if start == nil {
		t.Fatal("no EventToolUseStart emitted")
	}
	if end == nil {
		t.Fatal("no EventToolUseEnd emitted")
	}
	if start.Name != "bash" {
		t.Errorf("start.Name = %q, want %q", start.Name, "bash")
	}
	if start.ID == "" {
		t.Errorf("start.ID is empty (must synthesize)")
	}
	if !strings.HasPrefix(start.ID, "ollama-tu-") {
		t.Errorf("start.ID = %q, want prefix %q", start.ID, "ollama-tu-")
	}
	if end.ID != start.ID {
		t.Errorf("end.ID = %q, want = start.ID %q", end.ID, start.ID)
	}
	// Arguments must arrive as JSON bytes round-trippable to {"cmd":"ls /tmp"}.
	var parsed map[string]string
	if err := json.Unmarshal(end.Input, &parsed); err != nil {
		t.Fatalf("end.Input not JSON: %v (raw=%s)", err, end.Input)
	}
	if parsed["cmd"] != "ls /tmp" {
		t.Errorf("end.Input cmd = %q, want %q", parsed["cmd"], "ls /tmp")
	}
	if stop != "tool_use" {
		t.Errorf("stop = %q, want tool_use (tool_calls present overrides done_reason)", stop)
	}
}

// TestStream_MultipleToolCallsAtomic verifies that when a model emits
// multiple tool_calls in one chunk, each gets its own Start/End pair
// with a unique synthesized ID.
func TestStream_MultipleToolCallsAtomic(t *testing.T) {
	body := strings.Join([]string{
		`{"model":"llama3.1:latest","created_at":"2026-06-04T00:00:00Z","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"bash","arguments":{"cmd":"ls"}}},{"function":{"name":"bash","arguments":{"cmd":"pwd"}}}]},"done":true,"done_reason":"stop"}`,
		``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)

	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ch)

	var starts, ends []*providers.ToolUse
	for _, ev := range got {
		switch ev.Kind {
		case providers.EventToolUseStart:
			starts = append(starts, ev.ToolUse)
		case providers.EventToolUseEnd:
			ends = append(ends, ev.ToolUse)
		}
	}
	if len(starts) != 2 || len(ends) != 2 {
		t.Fatalf("want 2 starts + 2 ends, got %d/%d", len(starts), len(ends))
	}
	if starts[0].ID == starts[1].ID {
		t.Errorf("IDs collide: %q == %q", starts[0].ID, starts[1].ID)
	}
	if starts[0].ID != ends[0].ID || starts[1].ID != ends[1].ID {
		t.Errorf("start/end IDs mismatched across pairs")
	}
}

// TestStream_StopReasonMappings tabulates the done_reason → canonical
// stop translation.
func TestStream_StopReasonMappings(t *testing.T) {
	cases := []struct {
		doneReason string
		want       string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"", "end_turn"},           // omitted field
		{"unknown_x", "unknown_x"}, // pass-through
	}
	for _, tc := range cases {
		t.Run(tc.doneReason, func(t *testing.T) {
			line := fmt.Sprintf(
				`{"model":"x","created_at":"t","message":{"role":"assistant","content":""},"done":true,"done_reason":%q}`,
				tc.doneReason,
			)
			c, _ := newTestServer(t, line+"\n", nil, nil)
			ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
			if err != nil {
				t.Fatal(err)
			}
			var stop string
			for ev := range ch {
				if ev.Kind == providers.EventStopReason {
					stop = ev.Stop
				}
				if ev.Kind == providers.EventError {
					t.Fatalf("unexpected error event: %v", ev.Err)
				}
			}
			if stop != tc.want {
				t.Errorf("done_reason=%q → stop=%q, want %q", tc.doneReason, stop, tc.want)
			}
		})
	}
}

// TestStream_HeadersNoAuth asserts Content-Type is JSON and no
// Authorization header is sent (Ollama is local-only, no auth).
func TestStream_HeadersNoAuth(t *testing.T) {
	var captured http.Header
	body := `{"model":"x","created_at":"t","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}` + "\n"
	c, _ := newTestServer(t, body, func(h http.Header) { captured = h.Clone() }, nil)
	_, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the goroutine to actually read; cheap poll keeps the
	// test deterministic without a sync hook.
	if captured.Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %q, want application/json", captured.Get("Content-Type"))
	}
	if captured.Get("Authorization") != "" {
		t.Errorf("authorization should be unset, got %q", captured.Get("Authorization"))
	}
	// We do advertise the streaming format we accept.
	if accept := captured.Get("Accept"); accept != "application/x-ndjson" {
		t.Errorf("accept = %q, want application/x-ndjson", accept)
	}
	_ = c
}

// TestStream_NonOKError surfaces a non-200 status synchronously (before
// the channel opens) with the body included for diagnosis.
func TestStream_NonOKError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintln(w, `{"error":"model 'no-such-model' not found"}`)
	}))
	t.Cleanup(ts.Close)
	c := New(ts.URL)
	_, err := c.Stream(context.Background(), providers.Request{Model: "no-such-model"})
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention status: %v", err)
	}
	if !strings.Contains(err.Error(), "no-such-model") {
		t.Errorf("error should include server body: %v", err)
	}
}

// TestStream_MidStreamError surfaces an in-band {"error":"…"} object as
// an EventError without tearing down the stream prematurely.
func TestStream_MidStreamError(t *testing.T) {
	body := strings.Join([]string{
		`{"model":"x","created_at":"t","message":{"role":"assistant","content":"hello"},"done":false}`,
		`{"error":"context length exceeded"}`,
		``,
	}, "\n")
	c, _ := newTestServer(t, body, nil, nil)
	ch, err := c.Stream(context.Background(), providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var sawError bool
	var text strings.Builder
	for ev := range ch {
		switch ev.Kind {
		case providers.EventTextDelta:
			text.WriteString(ev.Text)
		case providers.EventError:
			sawError = true
			if !strings.Contains(ev.Err.Error(), "context length") {
				t.Errorf("error message missing detail: %v", ev.Err)
			}
		}
	}
	if text.String() != "hello" {
		t.Errorf("text before error: want %q got %q", "hello", text.String())
	}
	if !sawError {
		t.Error("expected EventError")
	}
}

// TestStream_CancellationStopsGoroutine verifies ctx cancellation closes
// the channel and reclaims the goroutine.
func TestStream_CancellationStopsGoroutine(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		// Emit one frame so the client transitions past the response-header
		// stage; then hold the connection open until the client cancels.
		io.WriteString(w, `{"model":"x","created_at":"t","message":{"role":"assistant","content":"."},"done":false}`+"\n")
		f.Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)
	c := New(ts.URL)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Stream(ctx, providers.Request{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // closed cleanly
			}
		case <-deadline:
			t.Fatal("stream did not close after ctx cancellation")
		}
	}
}

// TestBuildRequest_RequestShape exercises the full canonical → wire
// adapter: system prompt prepends as role=system, assistant tool_use
// blocks collapse into tool_calls, user tool_result blocks fan out to
// role=tool wire messages, and tools format mirrors OpenAI's.
func TestBuildRequest_RequestShape(t *testing.T) {
	var sent []byte
	body := `{"model":"x","created_at":"t","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}` + "\n"
	c, _ := newTestServer(t, body, nil, func(b []byte) { sent = b })

	req := providers.Request{
		Model:  "llama3.1:latest",
		System: "you are helpful",
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}},
			{Role: "assistant", Content: []providers.Block{
				{Kind: "text", Text: "running:"},
				{Kind: "tool_use", ToolUseID: "tu-1", ToolName: "bash", ToolInput: []byte(`{"cmd":"ls"}`)},
			}},
			{Role: "user", Content: []providers.Block{
				{Kind: "tool_result", ToolUseID: "tu-1", ToolResult: []byte("a\nb\n")},
			}},
		},
		Tools: []providers.ToolSpec{
			{Name: "bash", Description: "run a shell command", Schema: []byte(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)},
		},
	}
	_, err := c.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) == 0 {
		t.Fatal("server received no body")
	}

	var parsed struct {
		Model    string         `json:"model"`
		Stream   bool           `json:"stream"`
		Messages []apiMsg       `json:"messages"`
		Tools    []apiTool      `json:"tools"`
		Options  map[string]any `json:"options"`
	}
	if err := json.Unmarshal(sent, &parsed); err != nil {
		t.Fatalf("unmarshal sent body: %v\n%s", err, sent)
	}

	if parsed.Model != "llama3.1:latest" {
		t.Errorf("model = %q", parsed.Model)
	}
	if !parsed.Stream {
		t.Error("stream must be true")
	}
	if got := parsed.Options["num_predict"]; got == nil {
		t.Error("options.num_predict not set")
	}

	// Expected wire messages in order:
	//   0: system        (prepended from req.System)
	//   1: user text
	//   2: assistant text+tool_calls
	//   3: tool          (fanned out from tool_result)
	if len(parsed.Messages) != 4 {
		t.Fatalf("messages count = %d, want 4\n%+v", len(parsed.Messages), parsed.Messages)
	}
	if parsed.Messages[0].Role != "system" || parsed.Messages[0].Content != "you are helpful" {
		t.Errorf("messages[0] = %+v", parsed.Messages[0])
	}
	if parsed.Messages[1].Role != "user" || parsed.Messages[1].Content != "hi" {
		t.Errorf("messages[1] = %+v", parsed.Messages[1])
	}
	if parsed.Messages[2].Role != "assistant" {
		t.Errorf("messages[2].role = %q", parsed.Messages[2].Role)
	}
	if parsed.Messages[2].Content != "running:" {
		t.Errorf("messages[2].content = %q", parsed.Messages[2].Content)
	}
	if len(parsed.Messages[2].ToolCalls) != 1 {
		t.Fatalf("messages[2].tool_calls = %+v", parsed.Messages[2].ToolCalls)
	}
	if parsed.Messages[2].ToolCalls[0].Function.Name != "bash" {
		t.Errorf("tool_call name = %q", parsed.Messages[2].ToolCalls[0].Function.Name)
	}
	// arguments must be a JSON OBJECT on the wire (not a string).
	if !strings.HasPrefix(string(parsed.Messages[2].ToolCalls[0].Function.Arguments), "{") {
		t.Errorf("tool_call arguments not an object: %s", parsed.Messages[2].ToolCalls[0].Function.Arguments)
	}
	if parsed.Messages[3].Role != "tool" {
		t.Errorf("messages[3].role = %q, want tool", parsed.Messages[3].Role)
	}
	if parsed.Messages[3].ToolCallID != "tu-1" {
		t.Errorf("messages[3].tool_call_id = %q", parsed.Messages[3].ToolCallID)
	}
	if parsed.Messages[3].Content != "a\nb\n" {
		t.Errorf("messages[3].content = %q", parsed.Messages[3].Content)
	}

	// Tools: OpenAI-style {type:function, function:{name,description,parameters}}.
	if len(parsed.Tools) != 1 {
		t.Fatalf("tools count = %d", len(parsed.Tools))
	}
	if parsed.Tools[0].Type != "function" {
		t.Errorf("tool[0].type = %q, want function", parsed.Tools[0].Type)
	}
	if parsed.Tools[0].Function.Name != "bash" {
		t.Errorf("tool[0].function.name = %q", parsed.Tools[0].Function.Name)
	}
}

// TestBuildRequest_UnknownBlockKindRejected mirrors Anthropic's
// behavior: unknown block kinds bubble out of Stream synchronously
// rather than silently dropping content the model needs to see.
func TestBuildRequest_UnknownBlockKindRejected(t *testing.T) {
	c := New("")
	_, err := c.Stream(context.Background(), providers.Request{
		Model: "x",
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Kind: "asteroid", Text: "nope"}}},
		},
	})
	if err == nil {
		t.Fatal("expected error on unknown block kind")
	}
	if !strings.Contains(err.Error(), "asteroid") {
		t.Errorf("error should name the unknown kind: %v", err)
	}
}

// TestBuildRequest_ImageBlockDegradesToPlaceholder: ollama advertises
// Vision=false, so an image block (e.g. history replayed after a
// provider switch) must NOT error the turn - it degrades to a visible
// text placeholder, keeping the rest of the message intact.
func TestBuildRequest_ImageBlockDegradesToPlaceholder(t *testing.T) {
	out, err := buildRequest(providers.Request{
		Model: "x",
		Messages: []providers.Message{{
			Role: "user",
			Content: []providers.Block{
				{Kind: "text", Text: "look at this:"},
				providers.ImageBlock("image/png", []byte{0x89, 'P', 'N', 'G'}),
				{Kind: "text", Text: "what is it?"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("image block must degrade, not error: %v", err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("want 1 wire message, got %d", len(out.Messages))
	}
	got := out.Messages[0].Content
	if !strings.Contains(got, "[image attachment omitted") {
		t.Errorf("placeholder missing from content: %q", got)
	}
	if !strings.Contains(got, "look at this:") || !strings.Contains(got, "what is it?") {
		t.Errorf("surrounding text lost: %q", got)
	}
	if strings.Contains(got, "PNG") {
		t.Errorf("raw image bytes leaked into text content: %q", got)
	}
}

// TestParseJSONL_BlankLinesAndTrailingNoNewline checks the parser's
// defensive posture against framing quirks: stray blank lines and a
// final line lacking a trailing newline.
func TestParseJSONL_BlankLinesAndTrailingNoNewline(t *testing.T) {
	input := "\n" +
		`{"k":1}` + "\n" +
		"\n" +
		`{"k":2}` + "\n" +
		`{"k":3}` // no trailing newline
	var got []string
	err := parseJSONL(strings.NewReader(input), func(line string) error {
		got = append(got, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 lines, got %d: %+v", len(got), got)
	}
	if got[0] != `{"k":1}` || got[1] != `{"k":2}` || got[2] != `{"k":3}` {
		t.Errorf("lines = %+v", got)
	}
}

// TestCapabilitiesShape locks in the capability advertisement so any
// future change is a conscious decision.
func TestCapabilitiesShape(t *testing.T) {
	c := New("")
	caps := c.Capabilities()
	if caps.ParallelToolUse {
		t.Error("ParallelToolUse should be false")
	}
	if caps.PromptCaching {
		t.Error("PromptCaching should be false")
	}
	if !caps.StructuredOut {
		t.Error("StructuredOut should be true (format:json/schema supported)")
	}
	if caps.Vision {
		t.Error("Vision should be false (images field punted to a later slice)")
	}
}

// TestSynthesizeToolUseIDIsUnique guards against any future regression
// to deterministic IDs that would collide within a single turn.
func TestSynthesizeToolUseIDIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := synthesizeToolUseID()
		if id == "" {
			t.Fatal("synthesized ID is empty")
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("ID collision after %d iterations: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestNewDefaultsToLocalhost guards the documented default endpoint.
func TestNewDefaultsToLocalhost(t *testing.T) {
	c := New("")
	if c.BaseURL != "http://localhost:11434" {
		t.Errorf("default BaseURL = %q, want http://localhost:11434", c.BaseURL)
	}
	c2 := New("http://example:1234")
	if c2.BaseURL != "http://example:1234" {
		t.Errorf("explicit BaseURL clobbered: %q", c2.BaseURL)
	}
}
