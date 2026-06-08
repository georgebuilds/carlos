package mcp

import (
	"context"
)

// ToolNameSeparator is the double-underscore convention Claude Code uses
// for MCP-backed tool names ("<server>__<tool>"). Keeping the same
// shape means a user's model muscle-memory carries: a model that's
// learned "github__list_issues" from Claude Code calls the same name
// against carlos.
const ToolNameSeparator = "__"

// Tool is the tools.Tool adapter for a single MCP-discovered tool. The
// adapter is intentionally thin: Name/Description/Schema are read off
// the captured ToolDef, Execute delegates straight to Server.Call.
//
// Per the v1 scope, MCP tools inherit the standard approval path
// (LayeredApprover). They are NOT auto-approved by the read-only
// allowlist, which lets the user gate them by tool name from the
// approval prompt.
type Tool struct {
	server *Server
	def    ToolDef
}

// NewTool wraps a discovered ToolDef so it can be registered into a
// tools.Registry. The server pointer is captured so Execute can route
// the call back over the same MCP session.
func NewTool(server *Server, def ToolDef) *Tool {
	return &Tool{server: server, def: def}
}

// Name returns the carlos-side tool name. The "<server>__<tool>"
// prefix disambiguates between two MCP servers exposing the same tool
// name (common: both a "github" and a "gitlab" server defining "list_issues")
// and avoids collisions with carlos's built-in tool names (bash, read, …).
func (t *Tool) Name() string {
	return t.server.Name + ToolNameSeparator + t.def.Name
}

// Description forwards the server's tool description verbatim. Models
// rely on this for tool selection - mangling it (e.g. prepending the
// server name) hurt more than it helped during prototyping.
func (t *Tool) Description() string {
	return t.def.Description
}

// Schema returns the JSON Schema bytes the MCP server advertised. The
// shape matches providers.ToolSpec.Schema, so no further translation
// is needed at the agent-loop boundary.
func (t *Tool) Schema() []byte {
	return t.def.Schema
}

// Execute proxies the call through the captured Server. Errors from
// Server.Call are returned unchanged - including the IsError-flag case
// where the body and a non-nil error arrive together, which the agent
// loop's tool dispatch already handles (the body is fed to the model
// as the tool result and the error tags isError=true).
func (t *Tool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	return t.server.Call(ctx, t.def.Name, input)
}

// RawName returns the original MCP-side tool name (without the
// "<server>__" prefix). Exposed for diagnostics + the /mcp slash
// surface - the runtime never strips the prefix when calling out.
func (t *Tool) RawName() string {
	return t.def.Name
}

// ServerName returns the local nickname of the MCP server that owns
// this tool. Useful for grouping tools in UI surfaces (the /mcp
// slash, the permissions overlay) without re-splitting Name().
func (t *Tool) ServerName() string {
	return t.server.Name
}
