package oacompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
)

// TestBuildRequest_SystemAsFirstMessage pins the wire shape: a non-empty
// req.System becomes the first {role:"system"} message, then the rest
// of the canonical messages follow in order.
func TestBuildRequest_SystemAsFirstMessage(t *testing.T) {
	req := providers.Request{
		Model:  "gpt-test",
		System: "you are carlos",
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}},
		},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if !out.Stream {
		t.Errorf("Stream should always be true")
	}
	if out.Model != "gpt-test" {
		t.Errorf("Model: %q", out.Model)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("Messages: want 2 got %d", len(out.Messages))
	}
	if out.Messages[0].Role != "system" || out.Messages[0].Content != "you are carlos" {
		t.Errorf("system msg wrong: %+v", out.Messages[0])
	}
	if out.Messages[1].Role != "user" || out.Messages[1].Content != "hi" {
		t.Errorf("user msg wrong: %+v", out.Messages[1])
	}
}

// TestBuildRequest_NoSystemSkipsLeadingMessage verifies that an empty
// system prompt does NOT cause a stray empty role:system message.
func TestBuildRequest_NoSystemSkipsLeadingMessage(t *testing.T) {
	req := providers.Request{
		Model: "gpt-test",
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}},
		},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" {
		t.Errorf("expected single user msg; got %+v", out.Messages)
	}
}

// TestBuildRequest_ToolUseAndTextSameTurn covers the assistant message
// fan-in: a canonical assistant message containing both text and
// tool_use blocks should map to ONE wire message with content + tool_calls.
func TestBuildRequest_ToolUseAndTextSameTurn(t *testing.T) {
	req := providers.Request{
		Messages: []providers.Message{{
			Role: "assistant",
			Content: []providers.Block{
				{Kind: "text", Text: "let me check"},
				{Kind: "tool_use", ToolUseID: "call_1", ToolName: "read", ToolInput: []byte(`{"path":"a"}`)},
			},
		}},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("want 1 wire msg, got %d", len(out.Messages))
	}
	m := out.Messages[0]
	if m.Role != "assistant" || m.Content != "let me check" {
		t.Errorf("content/role wrong: %+v", m)
	}
	if len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != "call_1" || m.ToolCalls[0].Function.Name != "read" {
		t.Errorf("tool_calls wrong: %+v", m.ToolCalls)
	}
	if m.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Errorf("args not threaded: %q", m.ToolCalls[0].Function.Arguments)
	}
}

// TestBuildRequest_EmptyToolInputDefaultsToObject ensures a tool_use
// block with empty Input string becomes "{}" on the wire (Chat
// Completions requires Arguments to be valid JSON).
func TestBuildRequest_EmptyToolInputDefaultsToObject(t *testing.T) {
	req := providers.Request{
		Messages: []providers.Message{{
			Role: "assistant",
			Content: []providers.Block{
				{Kind: "tool_use", ToolUseID: "c", ToolName: "noop"},
			},
		}},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatal(err)
	}
	if out.Messages[0].ToolCalls[0].Function.Arguments != "{}" {
		t.Errorf("empty input should become '{}'; got %q", out.Messages[0].ToolCalls[0].Function.Arguments)
	}
}

// TestBuildRequest_ToolResultsFanOutToToolMessages covers the
// fan-out: a single canonical user message holding N tool_result blocks
// becomes N role:tool wire messages, each with the matching
// tool_call_id.
func TestBuildRequest_ToolResultsFanOutToToolMessages(t *testing.T) {
	req := providers.Request{
		Messages: []providers.Message{{
			Role: "user",
			Content: []providers.Block{
				{Kind: "tool_result", ToolUseID: "call_a", ToolResult: []byte("alpha-out")},
				{Kind: "tool_result", ToolUseID: "call_b", ToolResult: []byte("beta-out")},
			},
		}},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("expected 2 tool messages, got %d", len(out.Messages))
	}
	for i, want := range []struct{ id, body string }{{"call_a", "alpha-out"}, {"call_b", "beta-out"}} {
		got := out.Messages[i]
		if got.Role != "tool" {
			t.Errorf("msg[%d].Role = %q want tool", i, got.Role)
		}
		if got.ToolCallID != want.id {
			t.Errorf("msg[%d].ToolCallID = %q want %q", i, got.ToolCallID, want.id)
		}
		if got.Content != want.body {
			t.Errorf("msg[%d].Content = %q want %q", i, got.Content, want.body)
		}
	}
}

// TestBuildRequest_ToolResultsPlusUserText covers the mixed case: a user
// message with both tool_result blocks AND a regular text block. Wire
// shape: N role:tool messages followed by ONE role:user with the text.
func TestBuildRequest_ToolResultsPlusUserText(t *testing.T) {
	req := providers.Request{
		Messages: []providers.Message{{
			Role: "user",
			Content: []providers.Block{
				{Kind: "tool_result", ToolUseID: "c1", ToolResult: []byte("res")},
				{Kind: "text", Text: "ok thanks"},
			},
		}},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 msgs, got %d", len(out.Messages))
	}
	if out.Messages[0].Role != "tool" || out.Messages[0].Content != "res" {
		t.Errorf("first should be tool result; got %+v", out.Messages[0])
	}
	if out.Messages[1].Role != "user" || out.Messages[1].Content != "ok thanks" {
		t.Errorf("second should be user text; got %+v", out.Messages[1])
	}
}

// TestBuildRequest_MultipleTextBlocksJoinedWithDoubleNewline verifies
// the rare case where one canonical message has multiple text blocks.
func TestBuildRequest_MultipleTextBlocksJoinedWithDoubleNewline(t *testing.T) {
	req := providers.Request{
		Messages: []providers.Message{{
			Role: "user",
			Content: []providers.Block{
				{Kind: "text", Text: "para1"},
				{Kind: "text", Text: "para2"},
				{Kind: "text", Text: "para3"},
			},
		}},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatal(err)
	}
	if out.Messages[0].Content != "para1\n\npara2\n\npara3" {
		t.Errorf("joined content: %q", out.Messages[0].Content)
	}
}

// TestBuildRequest_EmptyMessageEmitsRoleOnly guards against a fan-out
// that produces zero wire messages. The Spec says: emit a single empty
// role:user/assistant to preserve turn ordering.
func TestBuildRequest_EmptyMessageEmitsRoleOnly(t *testing.T) {
	req := providers.Request{
		Messages: []providers.Message{{Role: "user", Content: nil}},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" || out.Messages[0].Content != "" {
		t.Errorf("expected single empty user msg; got %+v", out.Messages)
	}
}

// TestBuildRequest_UnknownKindRejected ensures a junk Kind surfaces as
// an error rather than being silently dropped.
func TestBuildRequest_UnknownKindRejected(t *testing.T) {
	req := providers.Request{
		Messages: []providers.Message{{
			Role: "user",
			Content: []providers.Block{
				{Kind: "asteroid", Text: "uh"},
			},
		}},
	}
	_, err := BuildRequest(req, "mytest")
	if err == nil {
		t.Fatal("expected unknown-kind error")
	}
	if !strings.Contains(err.Error(), "asteroid") {
		t.Errorf("error should name the kind: %v", err)
	}
	// Provider prefix should appear in the wrap so the model sees the
	// right vendor label.
	if !strings.Contains(err.Error(), "mytest") {
		t.Errorf("error should include provider prefix; got %v", err)
	}
}

// TestBuildRequest_ToolsArrayWithMissingSchemaGetsDefault verifies that
// a ToolSpec with empty Schema gets `{"type":"object","properties":{}}`
// on the wire so the model never sees a schema-less tool.
func TestBuildRequest_ToolsArrayWithMissingSchemaGetsDefault(t *testing.T) {
	req := providers.Request{
		Tools: []providers.ToolSpec{
			{Name: "ping", Description: "say hi"},
		},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(out.Tools))
	}
	tool := out.Tools[0]
	if tool.Type != "function" {
		t.Errorf("type should be function; got %q", tool.Type)
	}
	if tool.Function.Name != "ping" {
		t.Errorf("name: %q", tool.Function.Name)
	}
	if !strings.Contains(string(tool.Function.Parameters), `"type"`) {
		t.Errorf("default schema should be a JSON object; got %s", tool.Function.Parameters)
	}
}

// TestBuildRequest_ToolsArrayHonorsProvidedSchema lets the caller's
// Schema flow through verbatim, even custom JSON.
func TestBuildRequest_ToolsArrayHonorsProvidedSchema(t *testing.T) {
	req := providers.Request{
		Tools: []providers.ToolSpec{
			{Name: "search", Description: "web search", Schema: []byte(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)},
		},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out.Tools[0].Function.Parameters), `"required":["q"]`) {
		t.Errorf("schema not threaded: %s", out.Tools[0].Function.Parameters)
	}
}

// TestErrorTag_PrefersType picks Type when present, falls back to Code.
func TestErrorTag(t *testing.T) {
	if got := errorTag(&StreamError{Type: "server_error", Code: "x"}); got != "server_error" {
		t.Errorf("type preferred; got %q", got)
	}
	if got := errorTag(&StreamError{Code: "upstream_error"}); got != "upstream_error" {
		t.Errorf("code fallback; got %q", got)
	}
	if got := errorTag(&StreamError{}); got != "" {
		t.Errorf("empty -> empty; got %q", got)
	}
}

// TestIsContextCancellation reports true when ctx.Err is set OR the
// error itself is one of the cancellation sentinels.
func TestIsContextCancellation(t *testing.T) {
	if isContextCancellation(context.Background(), nil) {
		t.Errorf("bg + nil should not be cancellation")
	}
	if !isContextCancellation(context.Background(), context.Canceled) {
		t.Errorf("explicit canceled err should count")
	}
	if !isContextCancellation(context.Background(), context.DeadlineExceeded) {
		t.Errorf("deadline err should count")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !isContextCancellation(ctx, errors.New("other")) {
		t.Errorf("ctx.Err set -> always cancellation")
	}
}

// TestProcessStream_ErrorEnvelopeMidStream verifies that an `error`
// payload inside a chunk surfaces as EventError without tearing down
// the stream.
func TestProcessStream_ErrorEnvelopeMidStream(t *testing.T) {
	body := `data: {"error":{"type":"server_error","message":"upstream timed out"}}` + "\n\n" +
		`data: [DONE]` + "\n\n"
	events := runProcessStream(t, body)
	var sawErr bool
	for _, ev := range events {
		if ev.Kind == providers.EventError {
			sawErr = true
			if !strings.Contains(ev.Err.Error(), "server_error") {
				t.Errorf("error should carry type tag: %v", ev.Err)
			}
		}
	}
	if !sawErr {
		t.Errorf("expected EventError from stream-error envelope")
	}
}

// TestProcessStream_StopReasonMappingApplied - when a MapFinishReason
// is provided, the canonical Stop value is what the mapper returns.
func TestProcessStream_StopReasonMappingApplied(t *testing.T) {
	body := "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	ch := make(chan providers.Event, 16)
	go func() {
		defer close(ch)
		ProcessStream(context.Background(), strings.NewReader(body), ch, "test", func(s string) string {
			if s == "stop" {
				return "end_turn"
			}
			return s
		})
	}()
	var stop string
	for ev := range ch {
		if ev.Kind == providers.EventStopReason {
			stop = ev.Stop
		}
	}
	if stop != "end_turn" {
		t.Errorf("mapped stop: want end_turn got %q", stop)
	}
}

// TestProcessStream_FunctionCallFinishReasonFlushes covers the legacy
// "function_call" finish reason: it must also flush the tool_use
// accumulator before emitting the Stop event.
func TestProcessStream_FunctionCallFinishReasonFlushes(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"id\":\"x\",\"function\":{\"name\":\"y\",\"arguments\":\"{}\"}}" +
		"]}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"function_call\"}]}\n\n" +
		"data: [DONE]\n\n"
	events := runProcessStream(t, body)
	var sawStart, sawEnd bool
	var endBeforeStop bool
	for i, ev := range events {
		if ev.Kind == providers.EventToolUseStart {
			sawStart = true
		}
		if ev.Kind == providers.EventToolUseEnd {
			sawEnd = true
			for _, later := range events[i+1:] {
				if later.Kind == providers.EventStopReason {
					endBeforeStop = true
					break
				}
			}
		}
	}
	if !sawStart || !sawEnd {
		t.Errorf("Start/End not emitted: start=%v end=%v", sawStart, sawEnd)
	}
	if !endBeforeStop {
		t.Errorf("End must precede StopReason on function_call finish")
	}
}

// TestProcessStream_StartDeferredUntilIDAndNameKnown - when chunk 1
// carries name only and chunk 2 carries id, Start fires once both are
// present.
func TestProcessStream_StartDeferredUntilIDAndNameKnown(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"function\":{\"name\":\"early\"}}" +
		"]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"id\":\"late\",\"function\":{\"arguments\":\"{\\\"a\\\":1}\"}}" +
		"]}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	events := runProcessStream(t, body)
	var startEv providers.Event
	for _, ev := range events {
		if ev.Kind == providers.EventToolUseStart {
			startEv = ev
			break
		}
	}
	if startEv.ToolUse == nil {
		t.Fatal("Start never fired")
	}
	if startEv.ToolUse.ID != "late" || startEv.ToolUse.Name != "early" {
		t.Errorf("Start payload: id=%q name=%q", startEv.ToolUse.ID, startEv.ToolUse.Name)
	}
}

// TestProcessStream_DeltaEventsAfterStart - incremental arg fragments
// should emit EventToolUseDelta only after Start (id+name both known).
func TestProcessStream_DeltaEventsAfterStart(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"id\":\"call_x\",\"function\":{\"name\":\"f\",\"arguments\":\"{\\\"a\\\":\"}}" +
		"]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"function\":{\"arguments\":\"1}\"}}" +
		"]}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	events := runProcessStream(t, body)
	deltaCount := 0
	for _, ev := range events {
		if ev.Kind == providers.EventToolUseDelta {
			deltaCount++
			if ev.ToolUse.ID == "" || ev.ToolUse.Name == "" {
				t.Errorf("delta missing id/name: %+v", ev.ToolUse)
			}
		}
	}
	if deltaCount < 1 {
		t.Errorf("expected at least one delta event; got %d", deltaCount)
	}
}

// TestProcessStream_EmitsInAscendingIndexOrder - parallel tool_use with
// indexes 0 and 1: End events come out in 0,1 order regardless of input
// chunk arrival.
func TestProcessStream_EmitsInAscendingIndexOrder(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":1,\"id\":\"b\",\"function\":{\"name\":\"f1\",\"arguments\":\"{}\"}}," +
		"{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"f0\",\"arguments\":\"{}\"}}" +
		"]}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	events := runProcessStream(t, body)
	var ends []string
	for _, ev := range events {
		if ev.Kind == providers.EventToolUseEnd {
			ends = append(ends, ev.ToolUse.ID)
		}
	}
	if len(ends) != 2 || ends[0] != "a" || ends[1] != "b" {
		t.Errorf("End order: want [a b], got %v", ends)
	}
}

// TestStream_HTTPHappyPath spins up an httptest server emitting a tiny
// SSE stream and verifies the Stream function exposes the canonical
// events through the returned channel.
func TestStream_HTTPHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Auth header should arrive on the request.
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header missing: %q", got)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Name:       "test",
		BaseURL:    srv.URL,
		Path:       "/v1/chat/completions",
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		ExtraHeaders: map[string]string{
			"X-Custom": "v1",
		},
	}
	ch, err := Stream(context.Background(), cfg, providers.Request{
		Model:    "m",
		System:   "be brief",
		Messages: []providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hello"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var text string
	var sawStop bool
	for ev := range ch {
		switch ev.Kind {
		case providers.EventTextDelta:
			text += ev.Text
		case providers.EventStopReason:
			sawStop = true
		case providers.EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if text != "hi" {
		t.Errorf("text = %q want hi", text)
	}
	if !sawStop {
		t.Errorf("stop event missing")
	}
}

// TestStream_HTTPNon2xxSurfacesError verifies that a non-200 response
// is wrapped with the status code and trimmed body.
func TestStream_HTTPNon2xxSurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, "  bad api key\n")
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Name:       "test",
		BaseURL:    srv.URL,
		Path:       "/v1/chat/completions",
		APIKey:     "wrong",
		HTTPClient: srv.Client(),
	}
	_, err := Stream(context.Background(), cfg, providers.Request{Model: "m"})
	if err == nil {
		t.Fatal("expected non-2xx error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401; got %v", err)
	}
	if !strings.Contains(err.Error(), "bad api key") {
		t.Errorf("error should include body; got %v", err)
	}
}

// TestStream_HTTPNetworkErrorSurfaces - pointing at an unbindable host
// triggers the HTTP error wrap.
func TestStream_HTTPNetworkErrorSurfaces(t *testing.T) {
	cfg := Config{
		Name:       "test",
		BaseURL:    "http://127.0.0.1:1", // unreachable
		Path:       "/x",
		APIKey:     "k",
		HTTPClient: &http.Client{},
	}
	_, err := Stream(context.Background(), cfg, providers.Request{Model: "m"})
	if err == nil {
		t.Fatal("expected network error")
	}
	if !strings.Contains(err.Error(), "test:") && !strings.Contains(err.Error(), "HTTP:") {
		t.Errorf("error should be wrapped with provider tag; got %v", err)
	}
}

// TestStream_BuildRequestErrorPropagates ensures Stream returns a
// builder-level error early without dialling the network.
func TestStream_BuildRequestErrorPropagates(t *testing.T) {
	cfg := Config{
		Name:       "test",
		BaseURL:    "http://127.0.0.1:1",
		Path:       "/x",
		HTTPClient: &http.Client{},
	}
	// Unknown block.Kind triggers BuildRequest error.
	_, err := Stream(context.Background(), cfg, providers.Request{
		Messages: []providers.Message{{
			Role:    "user",
			Content: []providers.Block{{Kind: "bogus"}},
		}},
	})
	if err == nil {
		t.Fatal("expected BuildRequest error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should mention bogus kind; got %v", err)
	}
}

// TestParseSSE_HandlesScannerError ensures a read error mid-stream
// surfaces with the oacompat/sse wrap prefix.
func TestParseSSE_HandlesScannerError(t *testing.T) {
	r := &errReader{err: errors.New("boom")}
	err := ParseSSE(r, func(SSEFrame) error { return nil })
	if err == nil {
		t.Fatal("expected scanner error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("wrap should include underlying err; got %v", err)
	}
}

type errReader struct{ err error }

func (e *errReader) Read(p []byte) (int, error) { return 0, e.err }

// TestParseSSE_OnFrameErrorAborts - when the callback returns an
// error, parsing stops and that error is returned.
func TestParseSSE_OnFrameErrorAborts(t *testing.T) {
	input := "data: a\n\ndata: b\n\n"
	count := 0
	abort := errors.New("stop")
	err := ParseSSE(strings.NewReader(input), func(SSEFrame) error {
		count++
		return abort
	})
	if err != abort {
		t.Errorf("got %v, want abort sentinel", err)
	}
	if count != 1 {
		t.Errorf("should stop after first frame; processed %d", count)
	}
}

// TestParseSSE_OnFrameErrorOnTrailingFrame - the trailing-frame path
// also propagates onFrame errors.
func TestParseSSE_OnFrameErrorOnTrailingFrame(t *testing.T) {
	abort := errors.New("stop")
	err := ParseSSE(strings.NewReader("data: x\n"), func(SSEFrame) error { return abort })
	if err != abort {
		t.Errorf("trailing onFrame error not propagated; got %v", err)
	}
}

// TestParseSSE_LineWithoutColonIgnored - defensive against a non-SSE
// line; should not dispatch a frame.
func TestParseSSE_LineWithoutColonIgnored(t *testing.T) {
	input := "garbage\ndata: ok\n\n"
	var frames []SSEFrame
	if err := ParseSSE(strings.NewReader(input), func(f SSEFrame) error {
		frames = append(frames, f)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].Data != "ok" {
		t.Errorf("garbage line should not dispatch; frames=%+v", frames)
	}
}

// TestMessagesRequest_JSONMarshalShape pins the on-the-wire JSON layout
// so future struct-tag drift breaks loudly.
func TestMessagesRequest_JSONMarshalShape(t *testing.T) {
	req := &MessagesRequest{
		Model:    "m",
		Stream:   true,
		Messages: []APIMsg{{Role: "user", Content: "hi"}},
		Tools: []APITool{{
			Type: "function",
			Function: APIToolFnDecl{
				Name:       "f",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		}},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"model":"m"`, `"stream":true`, `"messages":`, `"tools":`, `"function"`} {
		if !strings.Contains(s, key) {
			t.Errorf("wire shape missing %q in %s", key, s)
		}
	}
}
