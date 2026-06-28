// Package mcp wires Model Context Protocol servers into carlos's tool
// registry. At boot the configured servers are connected (stdio servers are
// spawned as subprocesses; http/sse servers are dialed over HTTP), their
// tools are discovered, each one is wrapped in a tools.Tool adapter, and the
// adapter is registered under a "<server>__<tool>" name so the provider sees
// them alongside the built-in tools.
//
// Out of scope (tracked as TODOs):
//   - Resources, prompts, sampling. Tool calls only.
//   - Per-tool approval categories. MCP tools inherit the standard
//     LayeredApprover path: anything not in the built-in read-only
//     allowlist falls through to the user-prompt approver.
//   - Dynamic server reload. Config changes pick up on the next restart.
//   - Mid-session /mcp restart <name>.
package mcp

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Transport identifiers for a ServerConfig. An empty Transport normalizes
// to stdio so configs written before transport support round-trip
// unchanged and the common "spawn a subprocess" case stays the default.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
	TransportSSE   = "sse"
)

// Config is the top-level on-disk shape of the `mcp:` block in
// ~/.carlos/config.yaml. A zero value is fine (the rest of carlos boots
// untouched); a non-empty Servers list triggers boot-time fan-out.
//
// Forward-compat: new fields land here, get a YAML round-trip test, and
// then are read by the boot wire-up. Older configs without the field
// load forward without rewriting.
type Config struct {
	Servers []ServerConfig `json:"servers,omitempty"`
}

// ServerConfig captures a single MCP server's connection parameters.
//
// Name is a local nickname used for tool prefixing (e.g. "github" yields
// "github__list_issues").
//
// Transport selects how carlos reaches the server: "stdio" (default; spawn
// a subprocess and talk over its stdio), "http" (Streamable HTTP), or "sse"
// (HTTP server-sent events). An empty Transport means stdio.
//
// Stdio fields: Command is the executable; if it's just a bare binary name
// (no slash) the OS PATH is searched at spawn time. Args are the literal
// argv after Command. Env is merged onto os.Environ() with `${VAR}`
// expansion via os.ExpandEnv, so a config can pin a secret via
// `${GITHUB_TOKEN}` without inlining the value into the YAML. Empty values
// still override (the same semantics as `KEY=` in a unix env list).
//
// HTTP/SSE fields: URL is the server endpoint. Headers are sent on every
// request (e.g. {"Authorization": "Bearer ${TOKEN}"}); values get the same
// `${VAR}` expansion as Env so secrets stay out of the YAML.
//
// Frames gates the server to a subset of frames. Empty (the common case)
// means "available in every frame" - the same convention skills use.
//
// Tools is a per-server availability allowlist of RAW tool names (without the
// "<server>__" prefix). Empty (the default) exposes every tool the server
// advertises. A non-empty list exposes only those tools to the model - the
// lever for trimming a huge MCP catalog (e.g. DigitalOcean's hundreds of
// tools) down to the handful actually used, which also keeps the request
// under provider tool caps. Hidden tools stay connected and executable; they
// are simply not advertised, so re-enabling is instant.
//
// AutoApprove, when true, makes every tool from this server run without the
// approval prompt (the per-server analog of trusting a workspace). Off by
// default: MCP tools prompt like any other non-read-only tool.
type ServerConfig struct {
	Name        string            `json:"name"`
	Transport   string            `json:"transport,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Frames      []string          `json:"frames,omitempty"`
	Tools       []string          `json:"tools,omitempty"`
	AutoApprove bool              `json:"auto_approve,omitempty"`
}

// TransportKind returns the normalized transport for the server: an empty
// or whitespace-only Transport field means stdio, and the value is
// lower-cased so "HTTP" and "http" are the same transport.
func (s ServerConfig) TransportKind() string {
	t := strings.ToLower(strings.TrimSpace(s.Transport))
	if t == "" {
		return TransportStdio
	}
	return t
}

// Validate reports whether the server config is coherent for its transport:
// a name is always required, stdio needs a Command, and http/sse need a URL.
// An unrecognized transport is rejected so a typo'd `transport:` surfaces at
// load/import time rather than as a confusing connect failure later.
func (s ServerConfig) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("mcp: server name is empty")
	}
	switch s.TransportKind() {
	case TransportStdio:
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("mcp: server %q (stdio) has empty command", s.Name)
		}
	case TransportHTTP, TransportSSE:
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("mcp: server %q (%s) has empty url", s.Name, s.TransportKind())
		}
	default:
		return fmt.Errorf("mcp: server %q has unknown transport %q", s.Name, s.Transport)
	}
	return nil
}

// ForFrame returns the subset of servers available in the given frame.
// A server with no Frames list is always returned (skills use the same
// "empty == all frames" convention). An empty frame name returns every
// server, which mirrors the legacy single-shelf mode.
func (c Config) ForFrame(frame string) []ServerConfig {
	if len(c.Servers) == 0 {
		return nil
	}
	out := make([]ServerConfig, 0, len(c.Servers))
	for _, s := range c.Servers {
		if frame == "" || len(s.Frames) == 0 {
			out = append(out, s)
			continue
		}
		for _, f := range s.Frames {
			if f == frame {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// AddServer appends s to the config unless a server with the same Name is
// already present, in which case the existing entry is kept and the call is
// a no-op. The boolean reports whether s was added (true) or skipped as a
// duplicate (false). Dedup-by-name keeps re-importing idempotent and avoids
// the registry collision two same-named servers would cause (their tools
// share the "<name>__" prefix, so the second would clobber the first).
func (c *Config) AddServer(s ServerConfig) bool {
	for _, existing := range c.Servers {
		if existing.Name == s.Name {
			return false
		}
	}
	c.Servers = append(c.Servers, s)
	return true
}

// Find returns a pointer to the server config with the given name, or nil.
// The pointer indexes into c.Servers so mutations through it are captured by
// a subsequent config.Save.
func (c *Config) Find(name string) *ServerConfig {
	for i := range c.Servers {
		if c.Servers[i].Name == name {
			return &c.Servers[i]
		}
	}
	return nil
}

// splitToolName splits a combined registry name "<server>__<tool>" into its
// server and raw-tool parts. ok is false for built-in tools (no separator),
// which carry no server prefix.
func splitToolName(combined string) (server, raw string, ok bool) {
	i := strings.Index(combined, ToolNameSeparator)
	if i <= 0 {
		return "", "", false
	}
	return combined[:i], combined[i+len(ToolNameSeparator):], true
}

// ToolExposed reports whether a combined tool name ("<server>__<tool>") should
// be advertised to the model. Built-in tools (no separator) are always
// exposed. A tool whose server is absent from the config, or whose server has
// an empty allowlist, is exposed. Only an explicit non-empty Tools allowlist
// that omits the raw name hides it.
func (c Config) ToolExposed(combined string) bool {
	server, raw, ok := splitToolName(combined)
	if !ok {
		return true
	}
	sc := (&c).Find(server)
	if sc == nil || len(sc.Tools) == 0 {
		return true
	}
	for _, t := range sc.Tools {
		if t == raw {
			return true
		}
	}
	return false
}

// AutoApproves reports whether the server that owns a combined tool name is
// marked AutoApprove. Built-in tools and tools from unconfigured servers
// return false, leaving the decision to the other approver layers.
func (c Config) AutoApproves(combined string) bool {
	server, _, ok := splitToolName(combined)
	if !ok {
		return false
	}
	sc := (&c).Find(server)
	return sc != nil && sc.AutoApprove
}

// expandEnv returns a KEY=VAL slice suitable for exec.Cmd.Env: the
// current process environment, with the provided overrides applied (and
// `${VAR}` expanded against the same process environment). The "appended
// wins" semantics of exec.Cmd.Env mean overrides land on top of the
// inherited values.
//
// Used by Connect to compose the child process's environment. Pulled out
// of Connect so the env-expansion path is unit-testable on its own.
func expandEnv(env map[string]string) []string {
	base := os.Environ()
	if len(env) == 0 {
		return base
	}
	// Stable order in the override section keeps tests deterministic
	// (map iteration is randomized). We don't sort the inherited
	// os.Environ() because exec.Cmd doesn't promise an order anyway and
	// touching it would surprise users debugging $PATH.
	out := make([]string, 0, len(base)+len(env))
	out = append(out, base...)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		v := os.ExpandEnv(env[k])
		out = append(out, k+"="+v)
	}
	return out
}

// sortStrings is a tiny insertion sort to avoid a sort package import
// for this single use site. The override list is small (handful of
// entries per server), so an insertion sort is fine.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && strings.Compare(s[j-1], s[j]) > 0; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
