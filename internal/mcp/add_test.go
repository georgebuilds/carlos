package mcp

import (
	"strings"
	"testing"
)

func TestParseAddSpec_StdioBare(t *testing.T) {
	// The legacy TUI form: name, command, args - no `--` separator.
	sc, err := ParseAddSpec(strings.Fields("github npx -y @modelcontextprotocol/server-github"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.Name != "github" || sc.Command != "npx" {
		t.Fatalf("name/command wrong: %+v", sc)
	}
	if got := strings.Join(sc.Args, " "); got != "-y @modelcontextprotocol/server-github" {
		t.Errorf("args = %q", got)
	}
	if sc.TransportKind() != TransportStdio {
		t.Errorf("transport = %q, want stdio", sc.TransportKind())
	}
}

func TestParseAddSpec_StdioWithEnvAndSeparator(t *testing.T) {
	// The Claude Code form the user actually typed.
	sc, err := ParseAddSpec([]string{
		"digitalocean-mcp-local",
		"-e", "DIGITALOCEAN_API_TOKEN=dop_v1_secret",
		"--", "npx", "@digitalocean/mcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.Command != "npx" || strings.Join(sc.Args, " ") != "@digitalocean/mcp" {
		t.Errorf("command/args wrong: %+v", sc)
	}
	if sc.Env["DIGITALOCEAN_API_TOKEN"] != "dop_v1_secret" {
		t.Errorf("env not parsed: %+v", sc.Env)
	}
}

func TestParseAddSpec_EnvValueWithEquals(t *testing.T) {
	// A value containing '=' must survive (only the first '=' splits).
	sc, err := ParseAddSpec([]string{"x", "--env", "KEY=a=b=c", "--", "run"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.Env["KEY"] != "a=b=c" {
		t.Errorf("env value = %q, want a=b=c", sc.Env["KEY"])
	}
}

func TestParseAddSpec_HTTPAndHeaders(t *testing.T) {
	sc, err := ParseAddSpec([]string{"docs", "--http", "https://example.com/mcp", "-H", "Authorization: Bearer tok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.TransportKind() != TransportHTTP || sc.URL != "https://example.com/mcp" {
		t.Errorf("transport/url wrong: %+v", sc)
	}
	if sc.Headers["Authorization"] != "Bearer tok" {
		t.Errorf("header not parsed: %+v", sc.Headers)
	}
}

func TestParseAddSpec_SSE(t *testing.T) {
	sc, err := ParseAddSpec(strings.Fields("events --sse https://example.com/sse"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.TransportKind() != TransportSSE || sc.URL != "https://example.com/sse" {
		t.Errorf("sse wrong: %+v", sc)
	}
}

func TestParseAddSpec_Frames(t *testing.T) {
	sc, err := ParseAddSpec([]string{"x", "--frame", "personal", "--frame", "ludus", "--", "run"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(sc.Frames, ",") != "personal,ludus" {
		t.Errorf("frames = %v", sc.Frames)
	}
}

// TestParseAddSpec_ClaudeCodeTransport covers Claude Code's grammar where
// `--transport`/`-t` selects the transport and the url is a positional after
// the name, with options allowed before OR after the name. This is the exact
// shape that previously failed ("first argument must be a server name, got
// flag").
func TestParseAddSpec_ClaudeCodeTransport(t *testing.T) {
	cases := []struct {
		name   string
		tokens []string
	}{
		{"transport before name", strings.Fields("--transport http home-tools http://192.168.0.201:5678/mcp/abc")},
		{"short -t before name", strings.Fields("-t http home-tools http://192.168.0.201:5678/mcp/abc")},
		{"transport after name", strings.Fields("home-tools --transport http http://192.168.0.201:5678/mcp/abc")},
		{"equals form", strings.Fields("--transport=http home-tools http://192.168.0.201:5678/mcp/abc")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := ParseAddSpec(tc.tokens)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sc.Name != "home-tools" {
				t.Errorf("name = %q, want home-tools", sc.Name)
			}
			if sc.TransportKind() != TransportHTTP {
				t.Errorf("transport = %q, want http", sc.TransportKind())
			}
			if sc.URL != "http://192.168.0.201:5678/mcp/abc" {
				t.Errorf("url = %q", sc.URL)
			}
		})
	}
}

func TestParseAddSpec_ClaudeCodeStdioAndScope(t *testing.T) {
	// -t stdio explicit, options before the name, -s scope accepted+ignored.
	sc, err := ParseAddSpec(strings.Fields("-s local -t stdio -e K=v gh npx -y @scope/pkg"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.Name != "gh" || sc.Command != "npx" {
		t.Errorf("name/command wrong: %+v", sc)
	}
	if strings.Join(sc.Args, " ") != "-y @scope/pkg" {
		t.Errorf("args = %v (command flags must pass through verbatim)", sc.Args)
	}
	if sc.Env["K"] != "v" {
		t.Errorf("env = %+v", sc.Env)
	}
	if sc.TransportKind() != TransportStdio {
		t.Errorf("transport = %q, want stdio", sc.TransportKind())
	}
}

func TestParseAddSpec_Errors(t *testing.T) {
	cases := []struct {
		name   string
		tokens []string
		want   string
	}{
		{"empty", nil, "usage"},
		{"only a flag, no name", []string{"--http", "https://x"}, "missing server name"},
		{"unknown flag", strings.Fields("docs --htttp https://x"), "unknown flag"},
		{"https typo", strings.Fields("docs --https https://x"), "unknown flag"},
		{"bad transport value", strings.Fields("-t ftp name url"), "unknown transport"},
		{"stdio no command", []string{"lonely"}, "needs a command"},
		{"env missing value", []string{"x", "-e"}, "needs a value"},
		{"env malformed", []string{"x", "-e", "NOEQUALS", "--", "run"}, "must be KEY=VALUE"},
		{"http missing url", []string{"x", "--http"}, "needs a value"},
		{"transport http missing url", strings.Fields("-t http x"), "needs a url"},
		{"http with extra positional", strings.Fields("x --http https://u extra cmd"), "takes only a url"},
		{"transport http with extra", strings.Fields("-t http x https://u extra"), "extra"},
		{"header on stdio", []string{"x", "-H", "A: b", "--", "run"}, "only applies to"},
		{"url as stdio command (misordered transport)", strings.Fields("home-tools http://host/mcp -t http"), "looks like a url"},
		{"transport conflict", strings.Fields("x --http https://u --sse https://v"), "conflicting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAddSpec(tc.tokens)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseAddSpec_EnvBeforeBareCommand(t *testing.T) {
	// Without an explicit `--`, env flags still apply and the first bare
	// token starts the command (its own flags become args).
	sc, err := ParseAddSpec(strings.Fields("x -e K=v npx -y pkg"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.Env["K"] != "v" {
		t.Errorf("env = %+v", sc.Env)
	}
	if sc.Command != "npx" || strings.Join(sc.Args, " ") != "-y pkg" {
		t.Errorf("command/args wrong: %+v", sc)
	}
}

func TestParseAddSpec_DoesNotMutateInput(t *testing.T) {
	in := []string{"x", "--", "run"}
	_, _ = ParseAddSpec(in)
	if in[0] != "x" || in[1] != "--" || in[2] != "run" {
		t.Errorf("input slice mutated: %v", in)
	}
}
