package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// carlosClientName is the Implementation.Name advertised to every MCP
// server we connect to. Servers occasionally log this; keeping it
// constant lets users grep their server logs for carlos's connections.
const carlosClientName = "carlos"

// carlosClientVersion is a placeholder until cmd/carlos threads its own
// build version through. Bumped opportunistically; servers don't gate
// on it today.
const carlosClientVersion = "0.1.0"

// ToolDef is the serialized shape of an MCP tool, suitable for handing
// to a tools.Tool adapter. Schema is JSON bytes, not *jsonschema.Schema,
// because carlos's providers package expects raw bytes (see
// providers.ToolSpec.Schema) - keeping the conversion in one place
// avoids spreading SDK types through the rest of the tree.
type ToolDef struct {
	Name        string
	Description string
	Schema      []byte
}

// ErrToolResult marks a Server.Call error as "the MCP server returned a
// successful response with IsError=true" - distinct from a transport
// failure, a session-closed error, or a parse error. Callers that need
// to discriminate ("retry transport vs. surface tool failure to the
// model") match with errors.Is(err, ErrToolResult); the wrapped body
// stays in err.Error() for human-readable rendering.
var ErrToolResult = errors.New("mcp: tool returned error result")

// Session abstracts the subset of *sdk.ClientSession that Server uses,
// so unit tests can stub the upstream calls without spawning a real
// subprocess or wiring an in-memory transport. The official SDK exposes
// ClientSession as a struct (not an interface), so we adapt at the
// boundary.
type Session interface {
	ListTools(ctx context.Context, params *sdk.ListToolsParams) (*sdk.ListToolsResult, error)
	CallTool(ctx context.Context, params *sdk.CallToolParams) (*sdk.CallToolResult, error)
	Close() error
}

// Server is a live connection to one MCP server. It owns the spawned
// subprocess (cmd) and the SDK session; Close shuts both down in the
// right order (session close → stdin close → wait → SIGTERM → SIGKILL,
// per the SDK's CommandTransport pipeRWC.Close).
type Server struct {
	Name    string
	Session Session
	cmd     *exec.Cmd // the spawned subprocess; nil for tests that inject Session directly
}

// Connect spawns cfg.Command with cfg.Args, attaches a stdio transport,
// performs the MCP initialize handshake, and returns the live Server.
// Caller owns the returned *Server and MUST Close it on shutdown.
//
// Errors here are typically "command not found", "exec failed", or
// "initialize timed out" - the caller's policy (registry.ConnectAll) is
// to log + skip the server rather than abort boot.
func Connect(ctx context.Context, cfg ServerConfig) (*Server, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, errors.New("mcp: server name is empty")
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("mcp: server %q has empty command", cfg.Name)
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Env = expandEnv(cfg.Env)
	// Forward the server's stderr to the parent so MCP diagnostics
	// (startup banners, errors) land in the user's terminal alongside
	// carlos's own warnings. stdout is owned by the SDK's
	// CommandTransport (newline-delimited JSON) and must not be touched.
	cmd.Stderr = os.Stderr

	transport := &sdk.CommandTransport{Command: cmd}
	client := sdk.NewClient(&sdk.Implementation{
		Name:    carlosClientName,
		Version: carlosClientVersion,
	}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		// CommandTransport.Connect calls cmd.Start() before returning a
		// Connection. If the handshake then fails, the SDK usually calls
		// session.Close() (which closes the transport, which kills the
		// subprocess via pipeRWC.Close's stdin-close / SIGTERM / SIGKILL
		// staircase) - but a couple of error branches in client.Connect
		// return without closing (the unsupported-protocol-version branch
		// is the obvious one). exec.CommandContext only reaps the cmd
		// when the parent ctx is cancelled, and carlos's daemon ctx is
		// process-lifetime, so any leak here lingers until shutdown.
		//
		// Defensive: kill and reap. Both calls are no-ops if the SDK
		// already cleaned up - Kill on an exited process returns an
		// error we ignore, and Wait after the SDK already Wait'd
		// returns "Wait was already called" which we also ignore.
		killAndReap(cmd)
		return nil, fmt.Errorf("mcp: connect %q: %w", cfg.Name, err)
	}
	return &Server{
		Name:    cfg.Name,
		Session: session,
		cmd:     cmd,
	}, nil
}

// killAndReap force-terminates a subprocess and waits for it to exit so
// we don't leave a zombie. Safe to call on a process that has already
// exited or already been waited on - both Kill and Wait errors are
// intentionally discarded. Intended for the Connect-failed cleanup path
// where we can't tell whether the SDK has already torn cmd down.
func killAndReap(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// ListTools fetches the server's tool catalog and translates each entry
// into a ToolDef. The SDK's Tool.InputSchema is `any` (it's whatever the
// server marshalled), so we round-trip through json.Marshal to land on
// the []byte shape carlos's providers expect. A nil InputSchema becomes
// an empty `{"type":"object","properties":{}}` so providers that demand
// a schema (Gemini) don't reject the spec.
func (s *Server) ListTools(ctx context.Context) ([]ToolDef, error) {
	res, err := s.Session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: list tools %q: %w", s.Name, err)
	}
	out := make([]ToolDef, 0, len(res.Tools))
	for _, t := range res.Tools {
		if t == nil {
			continue
		}
		schema, err := marshalSchema(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("mcp: %s tool %q: marshal schema: %w", s.Name, t.Name, err)
		}
		out = append(out, ToolDef{
			Name:        t.Name,
			Description: t.Description,
			Schema:      schema,
		})
	}
	return out, nil
}

// Call invokes the named tool with raw JSON input. The input is
// unmarshaled into a map[string]any before being passed to the SDK, so
// the wire encoding matches what tool authors expect (a JSON object,
// not an opaque blob). A nil or empty input is normalized to "{}".
//
// The CallToolResult's Content slice is collapsed to a single string via
// joinContent: text blocks are concatenated, non-text blocks fall back to
// a "<image>" / "<audio>" / "<resource>" placeholder. The result is
// returned as raw bytes so the calling tools.Tool can hand it to the
// provider unchanged - carlos's existing tool surface returns []byte and
// MCP tools follow the same contract.
//
// When the server flags IsError, the joined string is still returned but
// wrapped in an error so the agent loop's tool-result handler can render
// it as a tool failure rather than a successful result.
func (s *Server) Call(ctx context.Context, toolName string, input []byte) ([]byte, error) {
	args, err := parseArgs(input)
	if err != nil {
		return nil, fmt.Errorf("mcp: %s call %q: parse input: %w", s.Name, toolName, err)
	}
	res, err := s.Session.CallTool(ctx, &sdk.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: %s call %q: %w", s.Name, toolName, err)
	}
	body := joinContent(res.Content)
	if res.IsError {
		// Surface as an error so the agent loop tags the tool result
		// with isError=true; the body is still the model-facing
		// content the server wanted to return. Wrap ErrToolResult so
		// callers can distinguish "tool reported failure" from
		// transport / parse errors via errors.Is.
		return []byte(body), fmt.Errorf("%w: %s", ErrToolResult, body)
	}
	return []byte(body), nil
}

// Close shuts down the session (which closes the transport, which
// closes the subprocess's stdin and waits for exit). Idempotent +
// concurrent-safe because the underlying *sdk.ClientSession.Close is.
func (s *Server) Close() error {
	if s == nil || s.Session == nil {
		return nil
	}
	return s.Session.Close()
}

// marshalSchema turns an opaque SDK schema (typically a map[string]any
// after JSON unmarshalling, occasionally a *jsonschema.Schema) into
// JSON bytes. A nil schema produces an empty object schema so downstream
// providers always see a valid JSON object.
func marshalSchema(schema any) ([]byte, error) {
	if schema == nil {
		return []byte(`{"type":"object","properties":{}}`), nil
	}
	return json.Marshal(schema)
}

// parseArgs decodes raw JSON tool input into a map[string]any. Nil,
// empty, or whitespace-only input is treated as an empty object so a
// model that calls a no-arg tool with `null` or `""` doesn't trip the
// JSON parser.
//
// Any other non-object shape (array, string, number, bool) is rejected
// with a typed error so a malformed model response surfaces as a tool
// failure instead of silently collapsing to {}, which would mask the
// real bug from the operator.
func parseArgs(input []byte) (map[string]any, error) {
	trimmed := bytesTrimSpace(input)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return map[string]any{}, nil
	}
	if kind := jsonKind(trimmed); kind != "object" {
		return nil, fmt.Errorf("expected JSON object, got %s", kind)
	}
	var args map[string]any
	if err := json.Unmarshal(trimmed, &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

// jsonKind classifies a trimmed JSON value by its first byte. Used to
// label a parseArgs rejection with the shape the model actually sent
// (e.g. "array", "string") instead of a generic unmarshal error.
func jsonKind(trimmed []byte) string {
	if len(trimmed) == 0 {
		return "empty"
	}
	switch c := trimmed[0]; {
	case c == '{':
		return "object"
	case c == '[':
		return "array"
	case c == '"':
		return "string"
	case c == '-' || (c >= '0' && c <= '9'):
		return "number"
	case c == 't' || c == 'f':
		return "bool"
	case c == 'n':
		return "null"
	default:
		return "unknown"
	}
}

// joinContent flattens a CallToolResult.Content slice into one string.
// Text content is concatenated verbatim; non-text content gets a
// "<image>" / "<audio>" / "<resource>" placeholder so the model at
// least knows something arrived. The model still sees a single string,
// which matches carlos's existing tools-return-text convention.
func joinContent(blocks []sdk.Content) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	first := true
	for _, c := range blocks {
		// Skip nil entries so a server that emits a sparse Content
		// slice (or a typed-nil block) doesn't panic the type switch.
		if c == nil {
			continue
		}
		if !first {
			b.WriteByte('\n')
		}
		first = false
		switch v := c.(type) {
		case *sdk.TextContent:
			b.WriteString(v.Text)
		case *sdk.ImageContent:
			b.WriteString("<image:")
			b.WriteString(v.MIMEType)
			b.WriteString(">")
		case *sdk.AudioContent:
			b.WriteString("<audio:")
			b.WriteString(v.MIMEType)
			b.WriteString(">")
		case *sdk.ResourceLink:
			b.WriteString("<resource:")
			b.WriteString(v.URI)
			b.WriteString(">")
		case *sdk.EmbeddedResource:
			b.WriteString("<embedded-resource>")
		default:
			// Unknown content kind: marshal as JSON so at least the
			// model sees the raw payload instead of an empty result.
			if v == nil {
				continue
			}
			if raw, err := json.Marshal(v); err == nil {
				b.Write(raw)
			} else {
				b.WriteString("<unknown-content>")
			}
		}
	}
	return b.String()
}

// bytesTrimSpace is a tiny dependency-free strings.TrimSpace for []byte.
// Keeps the import list short (no `bytes` for a one-liner).
func bytesTrimSpace(b []byte) []byte {
	start := 0
	for start < len(b) {
		switch b[start] {
		case ' ', '\t', '\n', '\r':
			start++
			continue
		}
		break
	}
	end := len(b)
	for end > start {
		switch b[end-1] {
		case ' ', '\t', '\n', '\r':
			end--
			continue
		}
		break
	}
	return b[start:end]
}
