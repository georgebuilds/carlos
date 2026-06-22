package ccimport

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/georgebuilds/carlos/internal/mcp"
)

// writeFile writes content to path, creating parent dirs, failing the test on
// any error. Keeps the table cases focused on the JSON rather than plumbing.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// findByName returns the first Discovered with the given server name and
// whether it was found, so cases can assert on a specific server.
func findByName(got []Discovered, name string) (Discovered, bool) {
	for _, d := range got {
		if d.Server.Name == name {
			return d, true
		}
	}
	return Discovered{}, false
}

// TestMappingByTransport covers how each Claude Code transport shape maps onto
// a carlos ServerConfig, including the entries that must be skipped.
func TestMappingByTransport(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{
		"mcpServers": {
			"local":   {"command": "npx", "args": ["-y", "pkg"], "env": {"K": "V"}},
			"web":     {"type": "http", "url": "https://host/mcp", "headers": {"Authorization": "Bearer x"}},
			"stream":  {"type": "sse", "url": "https://host/sse", "headers": {"X-Token": "y"}},
			"pigeon":  {"type": "carrierpigeon", "url": "https://host/x"},
			"nocmd":   {"command": ""}
		}
	}`)

	got := Discover(home, "")

	// 4. unknown transport is dropped; 5. stdio with no command fails Validate.
	if _, ok := findByName(got, "pigeon"); ok {
		t.Error("server with unknown transport should be skipped")
	}
	if _, ok := findByName(got, "nocmd"); ok {
		t.Error("stdio server with empty command should be skipped (Validate)")
	}

	// 1. stdio (type absent) -> Command/Args/Env, Transport empty.
	local, ok := findByName(got, "local")
	if !ok {
		t.Fatal("expected stdio server 'local' to be discovered")
	}
	wantLocal := mcp.ServerConfig{
		Name:    "local",
		Command: "npx",
		Args:    []string{"-y", "pkg"},
		Env:     map[string]string{"K": "V"},
	}
	if !reflect.DeepEqual(local.Server, wantLocal) {
		t.Errorf("stdio mapping:\n got %#v\nwant %#v", local.Server, wantLocal)
	}

	// 2. http -> Transport=http, URL, Headers.
	web, ok := findByName(got, "web")
	if !ok {
		t.Fatal("expected http server 'web' to be discovered")
	}
	wantWeb := mcp.ServerConfig{
		Name:      "web",
		Transport: mcp.TransportHTTP,
		URL:       "https://host/mcp",
		Headers:   map[string]string{"Authorization": "Bearer x"},
	}
	if !reflect.DeepEqual(web.Server, wantWeb) {
		t.Errorf("http mapping:\n got %#v\nwant %#v", web.Server, wantWeb)
	}

	// 3. sse -> Transport=sse.
	stream, ok := findByName(got, "stream")
	if !ok {
		t.Fatal("expected sse server 'stream' to be discovered")
	}
	if stream.Server.Transport != mcp.TransportSSE {
		t.Errorf("sse transport = %q, want %q", stream.Server.Transport, mcp.TransportSSE)
	}
	if stream.Server.URL != "https://host/sse" {
		t.Errorf("sse url = %q, want %q", stream.Server.URL, "https://host/sse")
	}

	// Provenance for top-level servers is the global file.
	if local.Source != "~/.claude.json" {
		t.Errorf("top-level source = %q, want %q", local.Source, "~/.claude.json")
	}
}

// TestExplicitStdioType confirms an explicit "type":"stdio" maps the same way
// as an absent type: Transport stays empty (carlos's canonical stdio form).
func TestExplicitStdioType(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{
		"mcpServers": {"s": {"type": "stdio", "command": "run"}}
	}`)
	got := Discover(home, "")
	s, ok := findByName(got, "s")
	if !ok {
		t.Fatal("expected stdio server to be discovered")
	}
	if s.Server.Transport != "" {
		t.Errorf("explicit stdio Transport = %q, want empty", s.Server.Transport)
	}
}

// TestProjectScopedServers covers servers nested under projects[path] and the
// project-qualified Source label they get (case 6).
func TestProjectScopedServers(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{
		"projects": {
			"/home/me/proj": {"mcpServers": {"scoped": {"command": "tool"}}}
		}
	}`)
	got := Discover(home, "")
	scoped, ok := findByName(got, "scoped")
	if !ok {
		t.Fatal("expected project-scoped server to be discovered")
	}
	wantSource := "~/.claude.json [/home/me/proj]"
	if scoped.Source != wantSource {
		t.Errorf("project source = %q, want %q", scoped.Source, wantSource)
	}
}

// TestMCPJSONFile covers the project-local .mcp.json: discovered when
// projectDir is set, and never read when projectDir is empty (case 7).
func TestMCPJSONFile(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{}`)
	writeFile(t, filepath.Join(proj, ".mcp.json"), `{
		"mcpServers": {"projlocal": {"command": "tool"}}
	}`)

	withProj := Discover(home, proj)
	d, ok := findByName(withProj, "projlocal")
	if !ok {
		t.Fatal("expected .mcp.json server to be discovered when projectDir set")
	}
	if d.Source != ".mcp.json" {
		t.Errorf(".mcp.json source = %q, want %q", d.Source, ".mcp.json")
	}

	noProj := Discover(home, "")
	if _, ok := findByName(noProj, "projlocal"); ok {
		t.Error(".mcp.json must not be read when projectDir is empty")
	}
}

// TestDedupAcrossSources confirms a name present in multiple sources appears
// once, with the first source (top-level) winning over later ones (case 8).
func TestDedupAcrossSources(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{
		"mcpServers": {"dup": {"command": "global-cmd"}},
		"projects": {
			"/a": {"mcpServers": {"dup": {"command": "proj-a-cmd"}}},
			"/b": {"mcpServers": {"dup": {"command": "proj-b-cmd"}}}
		}
	}`)
	got := Discover(home, "")

	count := 0
	for _, d := range got {
		if d.Server.Name == "dup" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("dup appeared %d times, want exactly 1", count)
	}
	d, _ := findByName(got, "dup")
	if d.Server.Command != "global-cmd" {
		t.Errorf("first occurrence should win: command = %q, want %q", d.Server.Command, "global-cmd")
	}
	if d.Source != "~/.claude.json" {
		t.Errorf("winning source = %q, want top-level", d.Source)
	}
}

// TestMissingAndMalformedFiles covers the best-effort contract (case 9): a
// machine with no Claude Code install, an absent .mcp.json, and a malformed
// JSON blob all yield an empty slice with no panic.
func TestMissingAndMalformedFiles(t *testing.T) {
	t.Run("no files at all", func(t *testing.T) {
		got := Discover(t.TempDir(), t.TempDir())
		if len(got) != 0 {
			t.Errorf("expected empty result, got %d servers", len(got))
		}
	})

	t.Run("empty home string", func(t *testing.T) {
		if got := Discover("", ""); len(got) != 0 {
			t.Errorf("expected empty result for empty home, got %d", len(got))
		}
	})

	t.Run("malformed claude.json", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".claude.json"), `{not valid json`)
		if got := Discover(home, ""); len(got) != 0 {
			t.Errorf("malformed file should be skipped, got %d servers", len(got))
		}
	})

	t.Run("malformed .mcp.json with valid home", func(t *testing.T) {
		home := t.TempDir()
		proj := t.TempDir()
		writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"ok":{"command":"c"}}}`)
		writeFile(t, filepath.Join(proj, ".mcp.json"), `garbage`)
		got := Discover(home, proj)
		if len(got) != 1 {
			t.Fatalf("expected only the valid home server, got %d", len(got))
		}
		if got[0].Server.Name != "ok" {
			t.Errorf("got server %q, want %q", got[0].Server.Name, "ok")
		}
	})
}

// TestDeterministicOrdering asserts the full output order: top-level servers
// first (sorted), then projects (sorted by path) each sorted by name, then
// .mcp.json (case 10). Two runs must produce identical ordering despite Go's
// randomized map iteration.
func TestDeterministicOrdering(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{
		"mcpServers": {
			"bravo": {"command": "c"},
			"alpha": {"command": "c"}
		},
		"projects": {
			"/zeta":  {"mcpServers": {"zulu": {"command": "c"}}},
			"/delta": {"mcpServers": {"echo": {"command": "c"}, "delta": {"command": "c"}}}
		}
	}`)
	writeFile(t, filepath.Join(proj, ".mcp.json"), `{
		"mcpServers": {"local": {"command": "c"}}
	}`)

	want := []string{"alpha", "bravo", "delta", "echo", "zulu", "local"}

	names := func(ds []Discovered) []string {
		out := make([]string, len(ds))
		for i, d := range ds {
			out[i] = d.Server.Name
		}
		return out
	}

	first := names(Discover(home, proj))
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("ordering = %v, want %v", first, want)
	}
	// Run again to catch any reliance on map iteration order.
	for i := 0; i < 5; i++ {
		if got := names(Discover(home, proj)); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d ordering = %v, want %v", i, got, want)
		}
	}
}
