package chat

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/mcp"
)

// withMCPConfig points CARLOS_CONFIG at a throwaway file seeded with the
// given MCP servers, so the /mcp handlers edit an isolated config instead
// of the user's real ~/.carlos/config.yaml. Returns the path. (The
// schedule tests have a sibling withTempConfig(t) for the no-server case.)
func withMCPConfig(t *testing.T, servers ...mcp.ServerConfig) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
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

// --- composeMCPList (pure formatter) ---------------------------------------

func TestComposeMCPList_Empty(t *testing.T) {
	body, summary := composeMCPList(nil, nil)
	if !strings.Contains(body, "No MCP servers configured") {
		t.Errorf("empty body missing teaching line:\n%s", body)
	}
	if !strings.Contains(body, "/mcp add") {
		t.Errorf("empty body should tell the user how to add a server:\n%s", body)
	}
	if !strings.Contains(summary, "no servers") {
		t.Errorf("summary = %q, want a no-servers note", summary)
	}
}

func TestComposeMCPList_NoLiveStatus(t *testing.T) {
	servers := []mcp.ServerConfig{
		{Name: "github", Command: "npx", Args: []string{"-y", "server-github"}},
		{Name: "docs", Transport: mcp.TransportHTTP, URL: "https://example.com/mcp"},
	}
	body, summary := composeMCPList(servers, nil)
	if !strings.Contains(body, "github") || !strings.Contains(body, "docs") {
		t.Errorf("body missing a server name:\n%s", body)
	}
	if !strings.Contains(body, "stdio npx -y server-github") {
		t.Errorf("body missing stdio target:\n%s", body)
	}
	if !strings.Contains(body, "http https://example.com/mcp") {
		t.Errorf("body missing http target:\n%s", body)
	}
	if !strings.Contains(body, "status unavailable") {
		t.Errorf("without a live snapshot the body should say status is unavailable:\n%s", body)
	}
	if strings.Contains(body, "●") || strings.Contains(body, "○") {
		t.Errorf("without a live snapshot, rows should use the neutral glyph, not connected/disconnected dots:\n%s", body)
	}
	if !strings.Contains(summary, "2 configured") {
		t.Errorf("summary = %q, want a 2-configured count", summary)
	}
}

func TestComposeMCPList_WithLiveStatus(t *testing.T) {
	servers := []mcp.ServerConfig{
		{Name: "github", Command: "gh-mcp"},
		{Name: "broken", Command: "nope"},
	}
	live := []MCPServerStatus{
		{Name: "github", Connected: true, Tools: 3},
		{Name: "broken", Connected: false},
	}
	body, summary := composeMCPList(servers, live)
	if !strings.Contains(body, "● github") {
		t.Errorf("connected server should get a filled dot:\n%s", body)
	}
	if !strings.Contains(body, "○ broken") {
		t.Errorf("disconnected server should get an open dot:\n%s", body)
	}
	if !strings.Contains(body, "3 tools") {
		t.Errorf("connected server should show its tool count:\n%s", body)
	}
	if !strings.Contains(body, "1/2 connected") {
		t.Errorf("body should summarize connected/total:\n%s", body)
	}
	if !strings.Contains(summary, "1 connected") {
		t.Errorf("summary = %q, want connected count", summary)
	}
}

func TestComposeMCPList_SortsByName(t *testing.T) {
	servers := []mcp.ServerConfig{
		{Name: "zebra", Command: "z"},
		{Name: "alpha", Command: "a"},
	}
	body, _ := composeMCPList(servers, nil)
	if strings.Index(body, "alpha") > strings.Index(body, "zebra") {
		t.Errorf("servers should be listed name-sorted:\n%s", body)
	}
}

func TestPluralTools(t *testing.T) {
	if got := pluralTools(1); got != "1 tool" {
		t.Errorf("pluralTools(1) = %q", got)
	}
	if got := pluralTools(0); got != "0 tools" {
		t.Errorf("pluralTools(0) = %q", got)
	}
	if got := pluralTools(5); got != "5 tools" {
		t.Errorf("pluralTools(5) = %q", got)
	}
}

// --- mcpAdd ----------------------------------------------------------------

func TestMCPAdd_Stdio(t *testing.T) {
	path := withTempConfig(t)
	m := &Model{}
	msg := m.mcpAdd("github npx -y @modelcontextprotocol/server-github")().(statusMsg)
	if msg.kind != statusInfo {
		t.Fatalf("add stdio: kind = %v, msg = %q", msg.kind, msg.text)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCP.Servers) != 1 {
		t.Fatalf("want 1 server persisted, got %d", len(cfg.MCP.Servers))
	}
	s := cfg.MCP.Servers[0]
	if s.Name != "github" || s.Command != "npx" {
		t.Errorf("persisted server wrong: %+v", s)
	}
	if want := []string{"-y", "@modelcontextprotocol/server-github"}; strings.Join(s.Args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", s.Args, want)
	}
	if s.TransportKind() != mcp.TransportStdio {
		t.Errorf("transport = %q, want stdio", s.TransportKind())
	}
}

func TestMCPAdd_HTTP(t *testing.T) {
	path := withTempConfig(t)
	m := &Model{}
	msg := m.mcpAdd("docs --http https://example.com/mcp")().(statusMsg)
	if msg.kind != statusInfo {
		t.Fatalf("add http: kind = %v, msg = %q", msg.kind, msg.text)
	}
	cfg, _ := config.Load(path)
	s := cfg.MCP.Servers[0]
	if s.TransportKind() != mcp.TransportHTTP || s.URL != "https://example.com/mcp" {
		t.Errorf("persisted http server wrong: %+v", s)
	}
}

func TestMCPAdd_SSE(t *testing.T) {
	path := withTempConfig(t)
	m := &Model{}
	_ = m.mcpAdd("events --sse https://example.com/sse")()
	cfg, _ := config.Load(path)
	s := cfg.MCP.Servers[0]
	if s.TransportKind() != mcp.TransportSSE || s.URL != "https://example.com/sse" {
		t.Errorf("persisted sse server wrong: %+v", s)
	}
}

func TestMCPAdd_StdioWithEnvAndSeparator(t *testing.T) {
	// The TUI add path shares mcp.ParseAddSpec with the CLI, so `-e` env
	// overrides and the `--` command separator work from `/mcp add` too.
	path := withTempConfig(t)
	m := &Model{}
	msg := m.mcpAdd("do -e TOKEN=secret -- npx @digitalocean/mcp")().(statusMsg)
	if msg.kind != statusInfo {
		t.Fatalf("add stdio+env: kind = %v, msg = %q", msg.kind, msg.text)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.MCP.Servers[0]
	if s.Command != "npx" || strings.Join(s.Args, " ") != "@digitalocean/mcp" {
		t.Errorf("command/args wrong: %+v", s)
	}
	if s.Env["TOKEN"] != "secret" {
		t.Errorf("env not persisted: %+v", s.Env)
	}
}

func TestMCPAdd_Duplicate(t *testing.T) {
	withMCPConfig(t, mcp.ServerConfig{Name: "github", Command: "x"})
	m := &Model{}
	msg := m.mcpAdd("github npx server")().(statusMsg)
	if msg.kind != statusWarn || !strings.Contains(msg.text, "already exists") {
		t.Errorf("duplicate add: got kind=%v text=%q", msg.kind, msg.text)
	}
}

func TestMCPAdd_RejectsTransportTypo(t *testing.T) {
	withTempConfig(t)
	m := &Model{}
	for _, in := range []string{"docs --htttp https://x", "docs --https https://x"} {
		msg := m.mcpAdd(in)().(statusMsg)
		if msg.kind != statusWarn || !strings.Contains(msg.text, "unknown flag") {
			t.Errorf("mcpAdd(%q): want unknown-flag warn, got kind=%v text=%q", in, msg.kind, msg.text)
		}
	}
}

func TestMCPList_NoConfigShowsEmptyState(t *testing.T) {
	t.Setenv("CARLOS_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	m := &Model{}
	msg := m.mcpSlash("list")().(statusMsg)
	if msg.kind != statusInfo {
		t.Fatalf("missing config: want info empty-state, got kind=%v text=%q", msg.kind, msg.text)
	}
	echo := m.transcript[len(m.transcript)-1].text
	if !strings.Contains(echo, "No MCP servers configured") {
		t.Errorf("missing config should render the empty state, got:\n%s", echo)
	}
}

func TestMCPList_FrameScoped(t *testing.T) {
	withMCPConfig(t, mcp.ServerConfig{Name: "ludusonly", Command: "x", Frames: []string{"ludus"}})
	// Active frame "personal" should not see a ludus-scoped server.
	m := &Model{frame: FrameUI{Active: "personal"}}
	_ = m.mcpSlash("list")()
	echo := m.transcript[len(m.transcript)-1].text
	if strings.Contains(echo, "ludusonly") {
		t.Errorf("personal frame should not list a ludus-scoped server:\n%s", echo)
	}
	// The ludus frame should see it.
	m2 := &Model{frame: FrameUI{Active: "ludus"}}
	_ = m2.mcpSlash("list")()
	echo2 := m2.transcript[len(m2.transcript)-1].text
	if !strings.Contains(echo2, "ludusonly") {
		t.Errorf("ludus frame should list the ludus-scoped server:\n%s", echo2)
	}
}

func TestMCPAdd_InvalidNoURL(t *testing.T) {
	withTempConfig(t)
	m := &Model{}
	msg := m.mcpAdd("docs --http")().(statusMsg)
	if msg.kind != statusWarn {
		t.Errorf("http with no url should warn, got kind=%v text=%q", msg.kind, msg.text)
	}
}

func TestMCPAdd_Usage(t *testing.T) {
	withTempConfig(t)
	m := &Model{}
	// Each malformed input warns with guidance specific to what's wrong:
	// an empty add shows the full usage; a name with no command says so.
	cases := map[string]string{
		"":         "usage",
		"onlyname": "needs a command",
	}
	for in, want := range cases {
		msg := m.mcpAdd(in)().(statusMsg)
		if msg.kind != statusWarn || !strings.Contains(msg.text, want) {
			t.Errorf("mcpAdd(%q): want warn containing %q, got kind=%v text=%q", in, want, msg.kind, msg.text)
		}
	}
}

// --- mcpRemove -------------------------------------------------------------

func TestMCPRemove(t *testing.T) {
	path := withMCPConfig(t,
		mcp.ServerConfig{Name: "github", Command: "x"},
		mcp.ServerConfig{Name: "docs", Transport: mcp.TransportHTTP, URL: "https://e.com"},
	)
	m := &Model{}
	msg := m.mcpRemove("github")().(statusMsg)
	if msg.kind != statusInfo {
		t.Fatalf("remove: kind=%v text=%q", msg.kind, msg.text)
	}
	got, _ := config.Load(path)
	if len(got.MCP.Servers) != 1 || got.MCP.Servers[0].Name != "docs" {
		t.Errorf("after remove, servers = %+v", got.MCP.Servers)
	}
}

func TestMCPRemove_Missing(t *testing.T) {
	withMCPConfig(t, mcp.ServerConfig{Name: "github", Command: "x"})
	m := &Model{}
	msg := m.mcpRemove("nope")().(statusMsg)
	if msg.kind != statusWarn || !strings.Contains(msg.text, "no server") {
		t.Errorf("remove missing: got kind=%v text=%q", msg.kind, msg.text)
	}
}

func TestMCPRemove_Usage(t *testing.T) {
	withTempConfig(t)
	m := &Model{}
	msg := m.mcpRemove("")().(statusMsg)
	if msg.kind != statusWarn {
		t.Errorf("remove no-arg: want warn, got kind=%v text=%q", msg.kind, msg.text)
	}
}

// --- mcpSlash routing + list integration -----------------------------------

func TestMCPSlash_Routing(t *testing.T) {
	withTempConfig(t)
	m := &Model{}
	cases := []struct {
		args     string
		wantKind statusKind
	}{
		{"help", statusInfo},
		{"bogus", statusWarn},
	}
	for _, tc := range cases {
		msg := m.mcpSlash(tc.args)().(statusMsg)
		if msg.kind != tc.wantKind {
			t.Errorf("mcpSlash(%q): kind=%v text=%q, want %v", tc.args, msg.kind, msg.text, tc.wantKind)
		}
	}
}

// TestMCPSlash_RoutesAddAndRemove confirms the verb dispatch reaches the
// add/remove handlers (and the rm/delete aliases) end-to-end, not just
// the handlers in isolation.
func TestMCPSlash_RoutesAddAndRemove(t *testing.T) {
	path := withTempConfig(t)
	m := &Model{}
	if msg := m.mcpSlash("add github npx server-github")().(statusMsg); msg.kind != statusInfo {
		t.Fatalf("route add: kind=%v text=%q", msg.kind, msg.text)
	}
	cfg, _ := config.Load(path)
	if len(cfg.MCP.Servers) != 1 {
		t.Fatalf("add via mcpSlash did not persist: %+v", cfg.MCP.Servers)
	}
	for _, alias := range []string{"rm", "delete", "remove"} {
		// Re-add so each alias has something to remove.
		_ = m.mcpSlash("add github npx server-github")()
		if msg := m.mcpSlash(alias + " github")().(statusMsg); msg.kind != statusInfo {
			t.Errorf("route %q: kind=%v text=%q", alias, msg.kind, msg.text)
		}
	}
}

func TestMCPSlash_ListAppendsTranscript(t *testing.T) {
	withMCPConfig(t, mcp.ServerConfig{Name: "github", Command: "x"})
	m := &Model{}
	before := len(m.transcript)
	msg := m.mcpSlash("")().(statusMsg)
	if msg.kind != statusInfo {
		t.Fatalf("list: kind=%v text=%q", msg.kind, msg.text)
	}
	if len(m.transcript) != before+1 {
		t.Fatalf("list should append one transcript echo, got %d new", len(m.transcript)-before)
	}
	echo := m.transcript[len(m.transcript)-1]
	if echo.kind != entrySlashEcho || !strings.Contains(echo.text, "github") {
		t.Errorf("transcript echo wrong: kind=%v text=%q", echo.kind, echo.text)
	}
}

func TestMCPSlash_ListUsesLiveStatus(t *testing.T) {
	withMCPConfig(t, mcp.ServerConfig{Name: "github", Command: "x"})
	m := &Model{
		mcpStatus: func() []MCPServerStatus {
			return []MCPServerStatus{{Name: "github", Connected: true, Tools: 2}}
		},
	}
	_ = m.mcpSlash("list")()
	echo := m.transcript[len(m.transcript)-1].text
	if !strings.Contains(echo, "● github") || !strings.Contains(echo, "2 tools") {
		t.Errorf("live status not reflected in listing:\n%s", echo)
	}
}
