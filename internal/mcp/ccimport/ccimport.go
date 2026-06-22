// Package ccimport discovers Model Context Protocol servers that the user
// has already configured for Claude Code and maps them onto carlos's own
// mcp.ServerConfig shape so they can be offered for import.
//
// carlos is strictly read-only against Claude Code's configuration: we read
// the same files Claude Code reads, but never write them back. The point is
// convenience - a machine that already runs the GitHub or Playwright MCP
// server under Claude Code shouldn't have to re-type the same command, env,
// and headers into ~/.carlos/config.yaml.
//
// Discovery is best-effort by design. A machine with no Claude Code install,
// an unreadable file, or a malformed JSON blob yields an empty result rather
// than an error: import is an optional nicety, so a missing source must never
// block carlos from booting or surface a scary diagnostic.
package ccimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/georgebuilds/carlos/internal/mcp"
)

// Discovered is one Claude Code MCP server, already mapped to carlos's
// ServerConfig, paired with a human-readable Source describing where it came
// from. Source is meant for display (e.g. in an import picker) so the user can
// tell a global server apart from a project-scoped one before accepting it.
type Discovered struct {
	Server mcp.ServerConfig
	Source string
}

// ccEntry is the on-disk shape of a single Claude Code MCP server entry. It is
// a superset of all three transports: stdio reads Command/Args/Env, while
// http/sse read URL/Headers. Type is the discriminator; an absent or "stdio"
// type means stdio. We decode into one struct and let the mapping step pick
// the relevant fields so we only describe Claude Code's schema in one place.
type ccEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// ccRoot is the part of ~/.claude.json we care about. The real file is large
// and carries a lot of unrelated UI/session state; json.Unmarshal ignores the
// fields we don't name, so we only declare the two MCP-bearing branches:
// top-level servers and per-project servers.
type ccRoot struct {
	MCPServers map[string]ccEntry `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]ccEntry `json:"mcpServers"`
	} `json:"projects"`
}

// mcpFile is the shape of a project's .mcp.json: just the server map.
type mcpFile struct {
	MCPServers map[string]ccEntry `json:"mcpServers"`
}

// Discover reads every Claude Code MCP source reachable from home and
// projectDir, maps each server to a carlos mcp.ServerConfig, and returns the
// importable ones deduplicated by name (first occurrence wins).
//
// Sources are read in a fixed order so the "first wins" dedup is meaningful
// and the output is stable across runs: the global ~/.claude.json top-level
// servers first, then its per-project servers (projects sorted by path), then
// the project-local .mcp.json. Within every server map the names are sorted,
// because Go randomizes map iteration and we want deterministic output.
//
// Best-effort: missing, unreadable, or malformed files are skipped silently
// and never produce an error. Servers whose mapping fails Validate (unknown
// transport, stdio with no command, http/sse with no url) are dropped.
func Discover(home, projectDir string) []Discovered {
	var out []Discovered
	seen := make(map[string]bool)

	// add appends a mapped, validated, not-yet-seen server. Centralizing the
	// map -> validate -> dedup pipeline here keeps every source consistent.
	add := func(name string, entry ccEntry, source string) {
		if seen[name] {
			return
		}
		cfg, ok := mapEntry(name, entry)
		if !ok {
			return
		}
		if err := cfg.Validate(); err != nil {
			return
		}
		seen[name] = true
		out = append(out, Discovered{Server: cfg, Source: source})
	}

	// addMap drains one server map in sorted-name order through add.
	addMap := func(servers map[string]ccEntry, source string) {
		for _, name := range sortedKeys(servers) {
			add(name, servers[name], source)
		}
	}

	if home != "" {
		if root, ok := readJSON[ccRoot](filepath.Join(home, ".claude.json")); ok {
			addMap(root.MCPServers, "~/.claude.json")
			for _, proj := range sortedKeys(root.Projects) {
				source := "~/.claude.json [" + proj + "]"
				addMap(root.Projects[proj].MCPServers, source)
			}
		}
	}

	if projectDir != "" {
		if f, ok := readJSON[mcpFile](filepath.Join(projectDir, ".mcp.json")); ok {
			addMap(f.MCPServers, ".mcp.json")
		}
	}

	return out
}

// mapEntry translates one Claude Code entry into a carlos ServerConfig. The
// bool is false when the transport is unrecognized, signaling the caller to
// skip the server entirely (as opposed to producing an invalid config that
// Validate would later reject for a less specific reason).
func mapEntry(name string, e ccEntry) (mcp.ServerConfig, bool) {
	cfg := mcp.ServerConfig{Name: name}
	switch strings.ToLower(strings.TrimSpace(e.Type)) {
	case "", mcp.TransportStdio:
		// Leave Transport empty: carlos treats empty as stdio, which keeps
		// the imported config in the same canonical form a hand-written one
		// would take.
		cfg.Command = e.Command
		cfg.Args = e.Args
		cfg.Env = e.Env
	case mcp.TransportHTTP:
		cfg.Transport = mcp.TransportHTTP
		cfg.URL = e.URL
		cfg.Headers = e.Headers
	case mcp.TransportSSE:
		cfg.Transport = mcp.TransportSSE
		cfg.URL = e.URL
		cfg.Headers = e.Headers
	default:
		return mcp.ServerConfig{}, false
	}
	return cfg, true
}

// readJSON reads and unmarshals a JSON file into T. The bool is false for any
// failure (missing file, permission error, malformed JSON); callers treat a
// false as "this source contributes nothing" so discovery stays best-effort.
func readJSON[T any](path string) (T, bool) {
	var zero T
	data, err := os.ReadFile(path)
	if err != nil {
		return zero, false
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, false
	}
	return v, true
}

// sortedKeys returns the keys of m in ascending order. Discovery sorts every
// map it iterates because Go's map iteration order is randomized, and we want
// both deterministic output and a stable "first occurrence wins" dedup.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
