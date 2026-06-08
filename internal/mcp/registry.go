package mcp

import (
	"context"
	"fmt"
)

// ConnectAll fan-outs Connect across every server enabled for the given
// frame, returning whatever sessions came up and a warning slice for
// the ones that didn't. A single misconfigured server (typo in the
// binary path, missing API key) does not block boot - carlos keeps
// going with the rest of the catalog and the user sees the warning on
// stderr.
//
// The caller owns the returned slice and MUST Close every entry on
// shutdown (the runtime wire-up does this in a defer).
//
// When cfg has no servers (the common case) ConnectAll is a quick
// no-op: nil slices, no warnings, no side effects. The boot path is
// hot enough that we don't want a syscall here when MCP isn't in use.
func ConnectAll(ctx context.Context, cfg Config, frame string) (servers []*Server, warnings []string) {
	enabled := cfg.ForFrame(frame)
	if len(enabled) == 0 {
		return nil, nil
	}
	servers = make([]*Server, 0, len(enabled))
	for _, sc := range enabled {
		srv, err := Connect(ctx, sc)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("connect %s: %v", sc.Name, err))
			continue
		}
		servers = append(servers, srv)
	}
	return servers, warnings
}

// DiscoverTools walks every connected server, fetches its tool catalog,
// and returns one *Tool adapter per discovered tool (ready to be
// registered into a carlos tools.Registry by the caller). A per-server
// discovery failure is logged as a warning and skipped - the user's
// other working servers still contribute their tools.
//
// This split (Discover here, Register in cmd/carlos) avoids the package
// cycle that would otherwise form: internal/tools imports internal/config
// for VaultConfig + ProviderSummary, internal/config imports internal/mcp
// for the top-level Config field, so internal/mcp cannot import
// internal/tools. The caller already lives in cmd/carlos, which sees
// both packages, and registration there is a two-line loop.
func DiscoverTools(ctx context.Context, servers []*Server) (toolsOut []*Tool, warnings []string) {
	if len(servers) == 0 {
		return nil, nil
	}
	for _, s := range servers {
		defs, err := s.ListTools(ctx)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("list tools %s: %v", s.Name, err))
			continue
		}
		for _, d := range defs {
			toolsOut = append(toolsOut, NewTool(s, d))
		}
	}
	return toolsOut, warnings
}

// CloseAll closes every server in the slice, ignoring individual close
// errors (best-effort shutdown - we're tearing down anyway). Provided
// as a convenience for the runtime wire-up's defer.
func CloseAll(servers []*Server) {
	for _, s := range servers {
		_ = s.Close()
	}
}
