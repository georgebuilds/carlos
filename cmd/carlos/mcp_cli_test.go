package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/mcp"
)

// TestParseMCPAdd_ClaudeForms exercises the full claude-compatible add
// grammar: positional + transport + env + header + frame + the `--`
// separator, in the orderings people actually paste from the internet.
func TestParseMCPAdd_ClaudeForms(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    mcp.ServerConfig
		warns   int
		wantErr bool
	}{
		{
			name: "stdio with env and dash-dash",
			args: []string{"github", "-e", "GITHUB_TOKEN=ghp_x", "--", "npx", "-y", "@modelcontextprotocol/server-github"},
			want: mcp.ServerConfig{
				Name:      "github",
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-github"},
				Env:       map[string]string{"GITHUB_TOKEN": "ghp_x"},
			},
		},
		{
			name: "stdio without dash-dash (no dashed args)",
			args: []string{"foo", "mybin", "subcmd"},
			want: mcp.ServerConfig{Name: "foo", Transport: "stdio", Command: "mybin", Args: []string{"subcmd"}},
		},
		{
			name: "stdio bare command",
			args: []string{"foo", "--", "mybin"},
			want: mcp.ServerConfig{Name: "foo", Transport: "stdio", Command: "mybin"},
		},
		{
			name: "sse by url",
			args: []string{"--transport", "sse", "linear", "https://mcp.linear.app/sse"},
			want: mcp.ServerConfig{Name: "linear", Transport: "sse", URL: "https://mcp.linear.app/sse"},
		},
		{
			name: "http with header after positionals",
			args: []string{"--transport", "http", "notion", "https://mcp.notion.com/mcp", "-H", "Authorization: Bearer xyz"},
			want: mcp.ServerConfig{
				Name:      "notion",
				Transport: "http",
				URL:       "https://mcp.notion.com/mcp",
				Headers:   map[string]string{"Authorization": "Bearer xyz"},
			},
		},
		{
			name: "transport via equals form",
			args: []string{"--transport=http", "n", "https://x.example/mcp"},
			want: mcp.ServerConfig{Name: "n", Transport: "http", URL: "https://x.example/mcp"},
		},
		{
			name: "short transport flag",
			args: []string{"-t", "sse", "n", "https://x.example/sse"},
			want: mcp.ServerConfig{Name: "n", Transport: "sse", URL: "https://x.example/sse"},
		},
		{
			name: "env with empty value",
			args: []string{"foo", "-e", "FLAG=", "--", "bin"},
			want: mcp.ServerConfig{Name: "foo", Transport: "stdio", Command: "bin", Env: map[string]string{"FLAG": ""}},
		},
		{
			name: "carlos-native frame flag",
			args: []string{"-f", "work", "foo", "--", "bin"},
			want: mcp.ServerConfig{Name: "foo", Transport: "stdio", Command: "bin", Frames: []string{"work"}},
		},
		{
			name:  "scope accepted but ignored",
			args:  []string{"--scope", "user", "foo", "--", "bin"},
			want:  mcp.ServerConfig{Name: "foo", Transport: "stdio", Command: "bin"},
			warns: 1,
		},
		{
			name:  "oauth flags accepted but ignored",
			args:  []string{"--client-id", "abc", "--client-secret", "--transport", "http", "foo", "https://x.example/mcp"},
			want:  mcp.ServerConfig{Name: "foo", Transport: "http", URL: "https://x.example/mcp"},
			warns: 2,
		},
		{
			name: "header value containing colons",
			args: []string{"-t", "http", "n", "https://x.example/mcp", "-H", "X-Time: 10:30:00"},
			want: mcp.ServerConfig{Name: "n", Transport: "http", URL: "https://x.example/mcp", Headers: map[string]string{"X-Time": "10:30:00"}},
		},
		{name: "no positionals", args: []string{"-e", "K=V"}, wantErr: true},
		{name: "stdio without command", args: []string{"foo"}, wantErr: true},
		{name: "sse without url", args: []string{"-t", "sse", "foo"}, wantErr: true},
		{name: "bad env no equals", args: []string{"foo", "-e", "NOTANENV", "--", "bin"}, wantErr: true},
		{name: "bad header no colon", args: []string{"-t", "http", "n", "https://x", "-H", "NoColon"}, wantErr: true},
		{name: "unknown flag", args: []string{"foo", "--bogus", "x", "--", "bin"}, wantErr: true},
		{name: "unknown transport", args: []string{"-t", "carrier-pigeon", "foo", "x"}, wantErr: true},
		{name: "transport flag missing value", args: []string{"-t"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warns, err := parseMCPAdd(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parse mismatch:\n got  %+v\n want %+v", got, tc.want)
			}
			if len(warns) != tc.warns {
				t.Errorf("warnings = %d (%v), want %d", len(warns), warns, tc.warns)
			}
		})
	}
}

// seedMCPCLIConfig points CARLOS_CONFIG at a temp file with a minimal
// valid config so the add/remove paths can Load + Save.
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

func TestRunMCPAdd_Persists(t *testing.T) {
	path := seedMCPCLIConfig(t)
	if err := runMCPAdd([]string{"github", "-e", "T=x", "--", "npx", "-y", "pkg"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCP.Servers) != 1 {
		t.Fatalf("want 1 server, got %d", len(cfg.MCP.Servers))
	}
	s := cfg.MCP.Servers[0]
	if s.Name != "github" || s.Command != "npx" || s.Env["T"] != "x" {
		t.Errorf("persisted wrong: %+v", s)
	}
}

func TestRunMCPAdd_DuplicateErrors(t *testing.T) {
	seedMCPCLIConfig(t, mcp.ServerConfig{Name: "github", Command: "x"})
	err := runMCPAdd([]string{"github", "--", "npx"})
	if err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestRunMCPAdd_NoConfigErrors(t *testing.T) {
	t.Setenv("CARLOS_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	if err := runMCPAdd([]string{"foo", "--", "bin"}); err == nil {
		t.Fatal("expected a 'run onboard first' error when config is absent")
	}
}

func TestRunMCPAddJSON_Persists(t *testing.T) {
	path := seedMCPCLIConfig(t)
	if err := runMCPAddJSON([]string{"weather", `{"type":"stdio","command":"npx","args":["-y","weather-mcp"]}`}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	s := cfg.MCP.Servers[0]
	if s.Name != "weather" || s.TransportKind() != mcp.TransportStdio || s.Command != "npx" {
		t.Errorf("add-json persisted wrong: %+v", s)
	}
}

func TestRunMCPAddJSON_SSEType(t *testing.T) {
	path := seedMCPCLIConfig(t)
	if err := runMCPAddJSON([]string{"docs", `{"type":"sse","url":"https://x.example/sse","headers":{"Authorization":"Bearer k"}}`}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	s := cfg.MCP.Servers[0]
	if s.TransportKind() != mcp.TransportSSE || s.URL != "https://x.example/sse" || s.Headers["Authorization"] != "Bearer k" {
		t.Errorf("add-json sse persisted wrong: %+v", s)
	}
}

func TestRunMCPAddJSON_BadJSON(t *testing.T) {
	seedMCPCLIConfig(t)
	if err := runMCPAddJSON([]string{"foo", "{not json"}); err == nil {
		t.Fatal("expected invalid-JSON error")
	}
}

func TestRunMCPRemove(t *testing.T) {
	path := seedMCPCLIConfig(t,
		mcp.ServerConfig{Name: "a", Command: "x"},
		mcp.ServerConfig{Name: "b", Command: "y"},
	)
	if err := runMCPRemove([]string{"a"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	if len(cfg.MCP.Servers) != 1 || cfg.MCP.Servers[0].Name != "b" {
		t.Errorf("after remove: %+v", cfg.MCP.Servers)
	}
	if err := runMCPRemove([]string{"nope"}); err == nil {
		t.Error("expected error removing a missing server")
	}
}

func TestRunMCP_Dispatch(t *testing.T) {
	seedMCPCLIConfig(t)
	if err := runMCP(nil); err != nil { // no subcommand -> usage, no error
		t.Errorf("bare mcp should print usage, got %v", err)
	}
	if err := runMCP([]string{"bogus"}); err == nil {
		t.Error("unknown subcommand should error")
	}
	if err := runMCP([]string{"list"}); err != nil {
		t.Errorf("mcp list should succeed on a seeded config, got %v", err)
	}
}

func TestRunMCPList_Populated(t *testing.T) {
	seedMCPCLIConfig(t,
		mcp.ServerConfig{Name: "zeb", Transport: mcp.TransportHTTP, URL: "https://z.example/mcp"},
		mcp.ServerConfig{Name: "alf", Command: "npx", Args: []string{"-y", "pkg"}, Frames: []string{"work"}},
	)
	// Exercises the populated path: name-sort, mcpTargetCLI for both
	// stdio and http, and the frames annotation.
	if err := runMCPList(nil); err != nil {
		t.Fatal(err)
	}
}

func TestRunMCPList_NoConfigEmptyState(t *testing.T) {
	t.Setenv("CARLOS_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	if err := runMCPList(nil); err != nil {
		t.Errorf("list with no config should print the empty state, not error: %v", err)
	}
}

func TestRunMCPAddJSON_DuplicateAndInvalid(t *testing.T) {
	seedMCPCLIConfig(t, mcp.ServerConfig{Name: "dup", Command: "x"})
	if err := runMCPAddJSON([]string{"dup", `{"type":"stdio","command":"y"}`}); err == nil {
		t.Error("expected duplicate error")
	}
	// stdio with no command fails Validate.
	if err := runMCPAddJSON([]string{"bad", `{"type":"stdio"}`}); err == nil {
		t.Error("expected validate error for stdio with no command")
	}
}

func TestRunMCP_RoutesEverySubcommand(t *testing.T) {
	seedMCPCLIConfig(t)
	steps := [][]string{
		{"add", "foo", "--", "bin"},
		{"add-json", "bar", `{"type":"stdio","command":"baz"}`},
		{"list"},
		{"ls"},
		{"get", "foo"},
		{"remove", "foo"},
		{"rm", "bar"},
		{"help"},
	}
	for _, s := range steps {
		if err := runMCP(s); err != nil {
			t.Errorf("runMCP %v: %v", s, err)
		}
	}
}

func TestRunMCPGet(t *testing.T) {
	seedMCPCLIConfig(t, mcp.ServerConfig{Name: "gh", Command: "npx", Args: []string{"pkg"}, Env: map[string]string{"T": "x"}})
	if err := runMCPGet([]string{"gh"}); err != nil {
		t.Fatal(err)
	}
	if err := runMCPGet([]string{"missing"}); err == nil {
		t.Error("expected error for missing server")
	}
}

// TestRunMCP_RedactsSecrets is the safety property: env values and auth
// header values must NEVER be printed; only their names. Guards against a
// future "print s.Env for debugging" regression that the err==nil checks
// would miss.
func TestRunMCP_RedactsSecrets(t *testing.T) {
	const (
		envName   = "GITHUB_TOKEN"
		envSecret = "ghp_SUPERSECRETVALUE"
		hdrName   = "Authorization"
		hdrSecret = "Bearer sk_live_TOPSECRET"
	)
	seedMCPCLIConfig(t,
		mcp.ServerConfig{Name: "gh", Command: "npx", Env: map[string]string{envName: envSecret}},
		mcp.ServerConfig{Name: "api", Transport: mcp.TransportHTTP, URL: "https://api.example/mcp", Headers: map[string]string{hdrName: hdrSecret}},
	)

	getEnv, err := captureStdout(t, func() error { return runMCPGet([]string{"gh"}) })
	if err != nil {
		t.Fatal(err)
	}
	getHdr, err := captureStdout(t, func() error { return runMCPGet([]string{"api"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(getEnv, envName) || !strings.Contains(getHdr, hdrName) {
		t.Errorf("get should print env/header NAMES; output:\n%s\n%s", getEnv, getHdr)
	}
	if strings.Contains(getEnv, envSecret) || strings.Contains(getHdr, hdrSecret) {
		t.Errorf("get LEAKED a secret value; output:\n%s\n%s", getEnv, getHdr)
	}

	list, err := captureStdout(t, func() error { return runMCPList(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(list, envSecret) || strings.Contains(list, hdrSecret) {
		t.Errorf("list LEAKED a secret value; output:\n%s", list)
	}
}

// TestParseMCPAdd_RejectsDroppedArgs covers the paste-mistake guards: a
// stray token before `--`, and too many tokens for a remote server.
func TestParseMCPAdd_RejectsDroppedArgs(t *testing.T) {
	cases := [][]string{
		{"foo", "stray", "--", "npx"},                          // arg before --
		{"-t", "http", "foo", "https://x.example/mcp", "junk"}, // extra after url
	}
	for _, args := range cases {
		if _, _, err := parseMCPAdd(args); err == nil {
			t.Errorf("parseMCPAdd(%v): expected error for dropped args", args)
		}
	}
}
