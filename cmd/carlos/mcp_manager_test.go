package main

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/mcp"
	"github.com/georgebuilds/carlos/internal/tools"
	"github.com/georgebuilds/carlos/internal/tui/chat"
)

func mgrFixture() (*mcpManager, *config.Config) {
	reg := tools.NewRegistry()
	for _, n := range []string{"read", "digitalocean__droplet_list", "digitalocean__database_create", "home-tools__ping"} {
		reg.Register(ftool{n})
	}
	cfg := &config.Config{
		Frames: frame.Config{Active: "personal", List: []frame.Frame{{Name: "personal"}}},
		MCP: mcp.Config{Servers: []mcp.ServerConfig{
			{Name: "digitalocean", Command: "x", Tools: []string{"droplet_list"}}, // only one exposed
			{Name: "home-tools", Command: "y"},
		}},
	}
	mm := &mcpManager{
		cfg:       cfg,
		reg:       reg,
		connected: map[string]bool{"digitalocean": true, "home-tools": true},
		dispatch:  func() (string, string) { return "openrouter", "x-ai/grok-4" },
		save:      func() error { return nil },
	}
	return mm, cfg
}

func countEnabled(ts []chat.MCPManagedTool) int {
	n := 0
	for _, t := range ts {
		if t.Enabled {
			n++
		}
	}
	return n
}

func TestMCPManager_ServersReportsToolsAndEnabled(t *testing.T) {
	mm, _ := mgrFixture()
	servers := mm.Servers()
	if len(servers) != 2 {
		t.Fatalf("servers=%d want 2", len(servers))
	}
	var tools, enabled int
	for _, s := range servers {
		if s.Name == "digitalocean" {
			tools, enabled = len(s.Tools), countEnabled(s.Tools)
		}
	}
	if tools != 2 {
		t.Errorf("digitalocean tools=%d want 2 (full catalog listed)", tools)
	}
	if enabled != 1 {
		t.Errorf("digitalocean enabled=%d want 1 (allowlist of one)", enabled)
	}
}

func TestMCPManager_SetAllowAndAutoApprovePersist(t *testing.T) {
	mm, cfg := mgrFixture()
	if err := mm.SetAllow("digitalocean", []string{"droplet_list", "database_create"}); err != nil {
		t.Fatal(err)
	}
	if got := cfg.MCP.Find("digitalocean").Tools; strings.Join(got, ",") != "droplet_list,database_create" {
		t.Errorf("allowlist not set: %v", got)
	}
	if err := mm.SetAutoApprove("digitalocean", true); err != nil {
		t.Fatal(err)
	}
	if !cfg.MCP.Find("digitalocean").AutoApprove {
		t.Error("auto-approve not set")
	}
	if err := mm.SetAllow("nope", nil); err == nil {
		t.Error("unknown server should error")
	}
}

func TestMCPManager_CapStatus(t *testing.T) {
	mm, _ := mgrFixture()
	model, cap, exposed := mm.CapStatus()
	if model != "x-ai/grok-4" || cap != 200 {
		t.Errorf("CapStatus model=%q cap=%d want grok/200", model, cap)
	}
	// exposed = read + digitalocean__droplet_list + home-tools__ping = 3
	if exposed != 3 {
		t.Errorf("exposed=%d want 3", exposed)
	}
}

func TestMCPManager_SetAutoApproveCallsReapprove(t *testing.T) {
	mm, _ := mgrFixture()
	called := false
	mm.reapprove = func() { called = true }
	if err := mm.SetAutoApprove("digitalocean", true); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("reapprove should fire so the change applies this session")
	}
}

func TestConnectedSet(t *testing.T) {
	got := connectedSet([]*mcp.Server{{Name: "a"}, {Name: "b"}})
	if !got["a"] || !got["b"] || len(got) != 2 {
		t.Errorf("connectedSet=%v", got)
	}
}

func TestMCPAutoApproveSet(t *testing.T) {
	c := mcp.Config{Servers: []mcp.ServerConfig{
		{Name: "do", AutoApprove: true},
		{Name: "home", AutoApprove: false},
	}}
	got := mcpAutoApproveSet(c)
	if !got["do"] || got["home"] || len(got) != 1 {
		t.Errorf("mcpAutoApproveSet=%v want only do", got)
	}
	if mcpAutoApproveSet(mcp.Config{}) != nil {
		t.Error("empty config => nil set")
	}
}
