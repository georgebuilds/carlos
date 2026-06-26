// mcp_wire.go - the carlos-side glue between internal/mcp's discovery
// surface and the carlos tool registry. Lives here (not in internal/mcp)
// because internal/mcp can't import internal/tools without an import
// cycle through internal/config.
//
// Boot-time policy: a misconfigured MCP server doesn't block startup.
// Connect failures and tool-list failures are surfaced to stderr as
// "carlos: mcp: ..." warnings; the rest of the catalog still wires up.

package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/georgebuilds/carlos/internal/mcp"
	"github.com/georgebuilds/carlos/internal/tools"
	"github.com/georgebuilds/carlos/internal/tui/chat"
)

// wireMCP connects to every MCP server enabled for the active frame,
// registers their tools into reg, and returns the connected sessions
// plus a close func the caller should defer. Warnings (per-server
// connect / discover failures) are written to w in the same
// "carlos: mcp: ..." format the rest of the startup uses.
//
// A nil cfg.Servers list is a quick no-op: nothing connects, no
// warnings, the returned close func is also a no-op so callers can
// always defer without nil-checks.
func wireMCP(ctx context.Context, w io.Writer, cfg mcp.Config, frame string, reg *tools.Registry) ([]*mcp.Server, func(), int) {
	servers, warns := mcp.ConnectAll(ctx, cfg, frame)
	for _, msg := range warns {
		fmt.Fprintln(w, "carlos: mcp:", msg)
	}
	discovered, dWarns := mcp.DiscoverTools(ctx, servers)
	for _, msg := range dWarns {
		fmt.Fprintln(w, "carlos: mcp:", msg)
	}
	for _, t := range discovered {
		reg.Register(t)
	}
	closer := func() { mcp.CloseAll(servers) }
	return servers, closer, len(discovered)
}

// buildMCPStatusFn snapshots, for every MCP server configured in the
// active frame, whether it connected at boot and how many tools it
// contributed. The returned closure feeds chat.WithMCPStatus so the /mcp
// listing can show live state. nil is returned when no servers are
// configured for the frame (the /mcp empty state covers that case).
//
// Per-server tool counts are read back out of the registry by the
// "<server>__<tool>" namespace prefix, so there's no second round-trip to
// the servers. The snapshot is computed once: MCP sessions connect only
// at startup, so the state can't change for the life of the session.
func buildMCPStatusFn(cfg mcp.Config, frame string, servers []*mcp.Server, reg *tools.Registry) func() []chat.MCPServerStatus {
	configured := cfg.ForFrame(frame)
	if len(configured) == 0 {
		return nil
	}
	connected := make(map[string]bool, len(servers))
	for _, s := range servers {
		connected[s.Name] = true
	}
	counts := make(map[string]int)
	for _, t := range reg.All() {
		name := t.Name()
		if i := strings.Index(name, mcp.ToolNameSeparator); i > 0 {
			counts[name[:i]]++
		}
	}
	out := make([]chat.MCPServerStatus, 0, len(configured))
	for _, sc := range configured {
		out = append(out, chat.MCPServerStatus{
			Name:      sc.Name,
			Connected: connected[sc.Name],
			Tools:     counts[sc.Name],
		})
	}
	return func() []chat.MCPServerStatus { return out }
}
