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

	"github.com/georgebuilds/carlos/internal/mcp"
	"github.com/georgebuilds/carlos/internal/tools"
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
