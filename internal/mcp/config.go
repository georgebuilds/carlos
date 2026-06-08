// Package mcp wires Model Context Protocol servers into carlos's tool
// registry. v1 supports stdio-transport tool servers only: at boot, the
// configured servers are spawned, their tools are discovered, each one is
// wrapped in a tools.Tool adapter, and the adapter is registered under a
// "<server>__<tool>" name so the provider sees them alongside the
// built-in tools.
//
// Out of scope for v1 (tracked as TODOs):
//   - Resources, prompts, sampling. Tool calls only.
//   - Streamable HTTP transport. CommandTransport (stdio) only.
//   - Per-tool approval categories. MCP tools inherit the standard
//     LayeredApprover path: anything not in the built-in read-only
//     allowlist falls through to the user-prompt approver.
//   - Dynamic server reload. Config changes pick up on the next restart.
//   - Mid-session /mcp restart <name>.
package mcp

import (
	"os"
	"strings"
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

// ServerConfig captures a single MCP server's spawn parameters.
//
// Name is a local nickname used for tool prefixing (e.g. "github" yields
// "github__list_issues"). Command is the executable; if it's just a bare
// binary name (no slash) the OS PATH is searched at spawn time. Args are
// the literal argv after Command.
//
// Env is merged onto os.Environ() with `${VAR}` expansion via os.ExpandEnv,
// so a config can pin a secret via `${GITHUB_TOKEN}` without inlining the
// value into the YAML. Empty values still override (the same semantics as
// `KEY=` in a unix env list).
//
// Frames gates the server to a subset of frames. Empty (the common case)
// means "available in every frame" - the same convention skills use.
type ServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Frames  []string          `json:"frames,omitempty"`
	// (Future: Transport "stdio"|"http"; URL string; etc. Keep stdio
	// implicit for v1 so the surface stays tight.)
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
