package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// stubSession lets the unit tests exercise *Tool.Execute and Server.Call
// without spawning a subprocess or wiring an in-memory MCP transport.
// Only the three methods Server uses are implemented; everything else
// the SDK exposes is irrelevant to the adapter under test.
type stubSession struct {
	listFn func(context.Context, *sdk.ListToolsParams) (*sdk.ListToolsResult, error)
	callFn func(context.Context, *sdk.CallToolParams) (*sdk.CallToolResult, error)
	closed bool
}

func (s *stubSession) ListTools(ctx context.Context, p *sdk.ListToolsParams) (*sdk.ListToolsResult, error) {
	return s.listFn(ctx, p)
}
func (s *stubSession) CallTool(ctx context.Context, p *sdk.CallToolParams) (*sdk.CallToolResult, error) {
	return s.callFn(ctx, p)
}
func (s *stubSession) Close() error {
	s.closed = true
	return nil
}

// TestToolName_PrefixesWithServer pins the Claude Code-compatible
// "<server>__<tool>" convention so a model's muscle memory carries.
// Two MCP servers exposing the same tool name (e.g. "search") still
// produce distinct names ("github__search" vs "linear__search").
func TestToolName_PrefixesWithServer(t *testing.T) {
	srv := &Server{Name: "github", Session: &stubSession{}}
	tool := NewTool(srv, ToolDef{Name: "search", Description: "search issues", Schema: []byte(`{"type":"object"}`)})
	if got, want := tool.Name(), "github__search"; got != want {
		t.Errorf("Name(): want %q got %q", want, got)
	}
	if got, want := tool.RawName(), "search"; got != want {
		t.Errorf("RawName(): want %q got %q", want, got)
	}
	if got, want := tool.ServerName(), "github"; got != want {
		t.Errorf("ServerName(): want %q got %q", want, got)
	}
}

// TestToolSchema_PassesThrough verifies the JSON Schema bytes flow
// unchanged from the captured ToolDef to the providers boundary -
// no re-marshalling, no normalization.
func TestToolSchema_PassesThrough(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)
	tool := NewTool(&Server{Name: "x", Session: &stubSession{}}, ToolDef{Name: "t", Schema: schema})
	if got := string(tool.Schema()); got != string(schema) {
		t.Errorf("Schema() not pass-through: want %q got %q", string(schema), got)
	}
}

// TestToolExecute_SuccessPath drives the happy path: input bytes get
// parsed into a JSON object, handed to the stub session, and the
// returned TextContent blocks are joined into a single body.
func TestToolExecute_SuccessPath(t *testing.T) {
	called := false
	stub := &stubSession{
		callFn: func(ctx context.Context, p *sdk.CallToolParams) (*sdk.CallToolResult, error) {
			called = true
			if p.Name != "search" {
				t.Errorf("tool name forwarded as %q, want %q", p.Name, "search")
			}
			args, ok := p.Arguments.(map[string]any)
			if !ok {
				t.Fatalf("Arguments not map[string]any: %T", p.Arguments)
			}
			if args["q"] != "hello" {
				t.Errorf("arg q=%v, want hello", args["q"])
			}
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: "line 1"},
					&sdk.TextContent{Text: "line 2"},
				},
			}, nil
		},
	}
	srv := &Server{Name: "github", Session: stub}
	tool := NewTool(srv, ToolDef{Name: "search"})

	got, err := tool.Execute(context.Background(), []byte(`{"q":"hello"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !called {
		t.Fatal("session.CallTool was not invoked")
	}
	if string(got) != "line 1\nline 2" {
		t.Errorf("joined body: want %q got %q", "line 1\nline 2", string(got))
	}
}

// TestToolExecute_EmptyInputIsTreatedAsEmptyObject covers the case
// where a model calls a no-arg tool with `null` or an empty string -
// the SDK rejects nil Arguments, so we normalize to "{}" before
// dispatching.
func TestToolExecute_EmptyInputIsTreatedAsEmptyObject(t *testing.T) {
	stub := &stubSession{
		callFn: func(ctx context.Context, p *sdk.CallToolParams) (*sdk.CallToolResult, error) {
			args, ok := p.Arguments.(map[string]any)
			if !ok {
				t.Fatalf("Arguments not map[string]any: %T", p.Arguments)
			}
			if len(args) != 0 {
				t.Errorf("Arguments not empty: %v", args)
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		},
	}
	tool := NewTool(&Server{Name: "s", Session: stub}, ToolDef{Name: "noop"})

	for _, in := range [][]byte{nil, []byte(""), []byte("   "), []byte("null")} {
		if _, err := tool.Execute(context.Background(), in); err != nil {
			t.Errorf("Execute(%q) returned error: %v", string(in), err)
		}
	}
}

// TestToolExecute_IsErrorWrappedAsError pins the IsError contract:
// the body is still returned so the agent loop can pass it to the
// model, but the error sentinel lets the dispatch tag the tool result
// with isError=true.
func TestToolExecute_IsErrorWrappedAsError(t *testing.T) {
	stub := &stubSession{
		callFn: func(ctx context.Context, p *sdk.CallToolParams) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{
				IsError: true,
				Content: []sdk.Content{&sdk.TextContent{Text: "permission denied"}},
			}, nil
		},
	}
	tool := NewTool(&Server{Name: "s", Session: stub}, ToolDef{Name: "delete"})

	body, err := tool.Execute(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected error wrapping IsError result")
	}
	if string(body) != "permission denied" {
		t.Errorf("body: want %q got %q", "permission denied", string(body))
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error message lost the body: %v", err)
	}
}

// TestToolExecute_SDKErrorPropagates verifies a protocol-level error
// (e.g. session died, transport closed) doesn't get swallowed: the
// agent loop needs to see this to escalate / retry.
func TestToolExecute_SDKErrorPropagates(t *testing.T) {
	sentinel := errors.New("connection closed")
	stub := &stubSession{
		callFn: func(ctx context.Context, p *sdk.CallToolParams) (*sdk.CallToolResult, error) {
			return nil, sentinel
		},
	}
	tool := NewTool(&Server{Name: "s", Session: stub}, ToolDef{Name: "x"})
	_, err := tool.Execute(context.Background(), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "connection closed") {
		t.Errorf("expected SDK error to bubble up; got %v", err)
	}
}

// TestServer_ListTools_TranslatesSchemas covers the *jsonschema.Schema
// or any-shaped InputSchema landing as JSON bytes carlos's providers
// can consume. A nil schema becomes the empty-object default so
// providers that demand a schema (Gemini) don't reject the spec.
func TestServer_ListTools_TranslatesSchemas(t *testing.T) {
	stub := &stubSession{
		listFn: func(ctx context.Context, p *sdk.ListToolsParams) (*sdk.ListToolsResult, error) {
			return &sdk.ListToolsResult{
				Tools: []*sdk.Tool{
					{Name: "with_schema", Description: "has one", InputSchema: map[string]any{
						"type":       "object",
						"properties": map[string]any{"q": map[string]any{"type": "string"}},
					}},
					{Name: "no_schema", Description: "nil schema", InputSchema: nil},
				},
			}, nil
		},
	}
	srv := &Server{Name: "stub", Session: stub}
	defs, err := srv.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("want 2 defs, got %d", len(defs))
	}
	if !strings.Contains(string(defs[0].Schema), `"type":"object"`) {
		t.Errorf("with_schema schema lost: %s", defs[0].Schema)
	}
	if !strings.Contains(string(defs[0].Schema), `"q"`) {
		t.Errorf("with_schema property lost: %s", defs[0].Schema)
	}
	if string(defs[1].Schema) != `{"type":"object","properties":{}}` {
		t.Errorf("no_schema not defaulted: %s", defs[1].Schema)
	}
}

// TestToolDescription_PassesThrough is the tiny but load-bearing
// assertion that we forward the MCP-side description verbatim.
// Mangling it (e.g. prepending the server name) was tried during
// prototyping and hurt tool-selection accuracy, so we pin the
// pass-through with a test.
func TestToolDescription_PassesThrough(t *testing.T) {
	tool := NewTool(&Server{Name: "s", Session: &stubSession{}},
		ToolDef{Name: "x", Description: "do the thing"})
	if got := tool.Description(); got != "do the thing" {
		t.Errorf("Description() = %q, want %q", got, "do the thing")
	}
}

// TestJoinContent_HandlesEveryKnownContentKind walks every Content
// variant the SDK ships and asserts the placeholder envelope we emit
// for non-text blocks. The model still sees one string back from the
// tool call - it just needs a hint that an image/audio/resource came
// over the wire.
func TestJoinContent_HandlesEveryKnownContentKind(t *testing.T) {
	blocks := []sdk.Content{
		&sdk.TextContent{Text: "header"},
		&sdk.ImageContent{MIMEType: "image/png"},
		&sdk.AudioContent{MIMEType: "audio/wav"},
		&sdk.ResourceLink{URI: "file:///x"},
		&sdk.EmbeddedResource{},
		&sdk.TextContent{Text: "footer"},
	}
	got := joinContent(blocks)
	for _, want := range []string{"header", "<image:image/png>", "<audio:audio/wav>", "<resource:file:///x>", "<embedded-resource>", "footer"} {
		if !strings.Contains(got, want) {
			t.Errorf("joinContent output missing %q:\n%s", want, got)
		}
	}
}

// TestJoinContent_EmptyReturnsEmpty pins the no-content branch so the
// caller's "non-empty body means tool ran" assumption holds.
func TestJoinContent_EmptyReturnsEmpty(t *testing.T) {
	if got := joinContent(nil); got != "" {
		t.Errorf("nil blocks should join to empty; got %q", got)
	}
	if got := joinContent([]sdk.Content{}); got != "" {
		t.Errorf("empty blocks should join to empty; got %q", got)
	}
}

// TestParseArgs_RejectsInvalidJSON guards the error path - a
// malformed input must surface so the tool dispatch can report it
// instead of silently calling with `{}`.
func TestParseArgs_RejectsInvalidJSON(t *testing.T) {
	if _, err := parseArgs([]byte(`{not json`)); err == nil {
		t.Error("expected parseArgs error on malformed input")
	}
}

// TestServer_Close_NilSafe covers the early-shutdown path where the
// supervisor closes a Server whose session was never bound (e.g. a
// connect that errored out very early).
func TestServer_Close_NilSafe(t *testing.T) {
	var s *Server
	if err := s.Close(); err != nil {
		t.Errorf("nil Server.Close: %v", err)
	}
	s = &Server{}
	if err := s.Close(); err != nil {
		t.Errorf("empty Server.Close: %v", err)
	}
}
