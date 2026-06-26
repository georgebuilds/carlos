package main

import (
	"testing"

	"github.com/georgebuilds/carlos/internal/mcp"
	"github.com/georgebuilds/carlos/internal/tools"
)

// TestBuildMCPStatusFn_ConnectedAndCounts confirms the snapshot reports
// per-server connection state and derives tool counts from the registry's
// "<server>__<tool>" namespacing, with disconnected configured servers
// surfaced as Connected=false.
func TestBuildMCPStatusFn_ConnectedAndCounts(t *testing.T) {
	cfg := mcp.Config{Servers: []mcp.ServerConfig{
		{Name: "github", Command: "x"},
		{Name: "docs", Transport: mcp.TransportHTTP, URL: "https://e.com"},
	}}
	// Only github came up at boot.
	gh := &mcp.Server{Name: "github"}
	servers := []*mcp.Server{gh}

	reg := tools.NewRegistry()
	reg.Register(mcp.NewTool(gh, mcp.ToolDef{Name: "list_issues", Schema: []byte("{}")}))
	reg.Register(mcp.NewTool(gh, mcp.ToolDef{Name: "create_pr", Schema: []byte("{}")}))

	fn := buildMCPStatusFn(cfg, "personal", servers, reg)
	if fn == nil {
		t.Fatal("expected a non-nil status fn when servers are configured")
	}
	got := fn()
	byName := map[string]struct {
		connected bool
		tools     int
	}{}
	for _, s := range got {
		byName[s.Name] = struct {
			connected bool
			tools     int
		}{s.Connected, s.Tools}
	}
	if g := byName["github"]; !g.connected || g.tools != 2 {
		t.Errorf("github status = %+v, want connected with 2 tools", g)
	}
	if d := byName["docs"]; d.connected || d.tools != 0 {
		t.Errorf("docs status = %+v, want disconnected with 0 tools", d)
	}
}

// TestBuildMCPStatusFn_NilWhenNoServers confirms the snapshot is nil when
// the active frame has no configured servers, so the chat falls back to
// the config-only /mcp listing (which renders the empty state).
func TestBuildMCPStatusFn_NilWhenNoServers(t *testing.T) {
	fn := buildMCPStatusFn(mcp.Config{}, "personal", nil, tools.NewRegistry())
	if fn != nil {
		t.Errorf("expected nil status fn for an empty config, got non-nil")
	}
}
