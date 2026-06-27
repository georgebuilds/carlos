package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/farewell"
	"github.com/georgebuilds/carlos/internal/mcp"
)

// seedMCPCLIConfig points CARLOS_CONFIG at a throwaway config seeded with the
// given servers, mirroring the TUI test helper so the CLI plan functions edit
// an isolated file rather than the user's real ~/.carlos/config.yaml.
func seedMCPCLIConfig(t *testing.T, servers ...mcp.ServerConfig) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{
		UserName:        "Boss",
		DefaultProvider: "openrouter",
		Providers: map[string]config.ProviderConfig{
			"openrouter": {APIKey: "x", DefaultModel: "anthropic/claude-opus-4-8"},
		},
	}
	cfg.MCP.Servers = servers
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Setenv("CARLOS_CONFIG", path)
	return path
}

// rowsText flattens a plan's rows into one searchable string.
func rowsText(rows []farewell.Message) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.Emoji + " " + r.Text + " " + r.Detail + "\n")
	}
	return b.String()
}

func TestMCPCLIPlan_AddStdioWithEnv(t *testing.T) {
	path := seedMCPCLIConfig(t)
	res := mcpCLIPlan([]string{
		"add", "digitalocean-mcp-local",
		"-e", "DIGITALOCEAN_API_TOKEN=dop_v1_secret",
		"--", "npx", "@digitalocean/mcp",
	})
	if !res.ok {
		t.Fatalf("add failed: %s", rowsText(res.rows))
	}
	if !strings.Contains(rowsText(res.rows), "added MCP server") {
		t.Errorf("box missing success line:\n%s", rowsText(res.rows))
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCP.Servers) != 1 {
		t.Fatalf("want 1 server persisted, got %d", len(cfg.MCP.Servers))
	}
	s := cfg.MCP.Servers[0]
	if s.Name != "digitalocean-mcp-local" || s.Command != "npx" {
		t.Errorf("persisted server wrong: %+v", s)
	}
	if s.Env["DIGITALOCEAN_API_TOKEN"] != "dop_v1_secret" {
		t.Errorf("env not persisted: %+v", s.Env)
	}
}

func TestMCPCLIPlan_AddInvalidSpecRendersErrorBox(t *testing.T) {
	seedMCPCLIConfig(t)
	res := mcpCLIPlan([]string{"add", "lonely"}) // stdio with no command
	if res.ok {
		t.Fatal("expected failure for a stdio server with no command")
	}
	body := rowsText(res.rows)
	if len(res.rows) == 0 || res.rows[0].Emoji != "⚠️" {
		t.Errorf("error box should lead with a warning glyph:\n%s", body)
	}
	if !strings.Contains(body, "needs a command") {
		t.Errorf("error box missing reason:\n%s", body)
	}
	if !strings.Contains(body, "carlos mcp add") {
		t.Errorf("error box should show usage:\n%s", body)
	}
}

func TestMCPCLIPlan_AddDuplicate(t *testing.T) {
	seedMCPCLIConfig(t, mcp.ServerConfig{Name: "github", Command: "x"})
	res := mcpCLIPlan(strings.Fields("add github -- npx server"))
	if res.ok {
		t.Fatal("duplicate add should fail")
	}
	if !strings.Contains(rowsText(res.rows), "already exists") {
		t.Errorf("box missing duplicate note:\n%s", rowsText(res.rows))
	}
}

func TestMCPCLIPlan_ListAndEmpty(t *testing.T) {
	// Empty config -> teaching box.
	seedMCPCLIConfig(t)
	res := mcpCLIPlan([]string{"list"})
	if !res.ok || !strings.Contains(rowsText(res.rows), "no MCP servers configured") {
		t.Errorf("empty list wrong:\n%s", rowsText(res.rows))
	}

	// Populated config -> one row per server, name-sorted.
	seedMCPCLIConfig(t,
		mcp.ServerConfig{Name: "zebra", Command: "z"},
		mcp.ServerConfig{Name: "alpha", Transport: mcp.TransportHTTP, URL: "https://a/mcp"},
	)
	res = mcpCLIPlan(nil) // bare `carlos mcp` lists
	if !res.ok {
		t.Fatalf("list failed: %s", rowsText(res.rows))
	}
	body := rowsText(res.rows)
	if !strings.Contains(body, "alpha") || !strings.Contains(body, "zebra") {
		t.Errorf("list missing a server:\n%s", body)
	}
	if !strings.Contains(body, "https://a/mcp") {
		t.Errorf("list missing http target:\n%s", body)
	}
	if strings.Index(body, "alpha") > strings.Index(body, "zebra") {
		t.Errorf("servers should be name-sorted:\n%s", body)
	}
}

func TestMCPCLIPlan_ListFrameScope(t *testing.T) {
	seedMCPCLIConfig(t,
		mcp.ServerConfig{Name: "everywhere", Command: "a"},
		mcp.ServerConfig{Name: "ludusonly", Command: "b", Frames: []string{"ludus"}},
	)
	res := mcpCLIPlan([]string{"list", "-f", "personal"})
	if !res.ok {
		t.Fatalf("list failed: %s", rowsText(res.rows))
	}
	body := rowsText(res.rows)
	if !strings.Contains(body, "everywhere") {
		t.Errorf("frameless server should show in every frame:\n%s", body)
	}
	if strings.Contains(body, "ludusonly") {
		t.Errorf("ludus-only server should not show in the personal frame:\n%s", body)
	}
}

func TestMCPCLIPlan_Remove(t *testing.T) {
	path := seedMCPCLIConfig(t, mcp.ServerConfig{Name: "github", Command: "x"})
	res := mcpCLIPlan([]string{"remove", "github"})
	if !res.ok || !strings.Contains(rowsText(res.rows), "removed MCP server") {
		t.Errorf("remove wrong:\n%s", rowsText(res.rows))
	}
	cfg, _ := config.Load(path)
	if len(cfg.MCP.Servers) != 0 {
		t.Errorf("server not removed: %+v", cfg.MCP.Servers)
	}

	// Removing a missing server fails with a helpful box.
	res = mcpCLIPlan([]string{"remove", "ghost"})
	if res.ok || !strings.Contains(rowsText(res.rows), "no server named") {
		t.Errorf("missing-server remove wrong:\n%s", rowsText(res.rows))
	}
}

func TestMCPCLIPlan_UnknownSubcommand(t *testing.T) {
	seedMCPCLIConfig(t)
	res := mcpCLIPlan([]string{"frobnicate"})
	if res.ok || !strings.Contains(rowsText(res.rows), "unknown subcommand") {
		t.Errorf("unknown subcommand wrong:\n%s", rowsText(res.rows))
	}
}

func TestMCPCLIPlan_Help(t *testing.T) {
	res := mcpCLIPlan([]string{"help"})
	if !res.ok {
		t.Fatal("help should succeed")
	}
	body := rowsText(res.rows)
	for _, want := range []string{"carlos mcp add", "--http", "-e KEY=VAL", "remove"} {
		if !strings.Contains(body, want) {
			t.Errorf("help missing %q:\n%s", want, body)
		}
	}
}

func TestRunMCP_FailureReturnsReportedSentinel(t *testing.T) {
	seedMCPCLIConfig(t)
	// runMCP renders to stdout and returns the silent sentinel on failure.
	err := runMCP([]string{"add", "lonely"})
	if !errors.Is(err, errMCPReported) {
		t.Fatalf("want errMCPReported, got %v", err)
	}
	if err := runMCP([]string{"help"}); err != nil {
		t.Fatalf("help should return nil, got %v", err)
	}
}
