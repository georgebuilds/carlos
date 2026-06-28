package main

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/georgebuilds/carlos/internal/mcp"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// toolExposer decides whether a combined tool name ("<server>__<tool>") is
// advertised to the model. mcp.Config satisfies it directly (read-only,
// single-goroutine paths like headless `please`); the interactive TUI uses
// *mcpAvailability, which guards the same decision behind a lock because the
// /mcp overlay mutates the allowlist on the UI goroutine while the agent loop
// reads it per turn on another.
type toolExposer interface {
	ToolExposed(combined string) bool
}

// newToolSelector returns the chatglue/headless ToolSelect closure. Per turn
// it (1) hides MCP tools the user has not allowlisted (per-server
// availability, via exposer) and (2) caps the remaining list to the model's
// tool limit, logging any cap drops to w. exposer and provider are captured
// for the session; model is supplied per turn so a /model swap re-evaluates
// the cap.
//
// Availability is the intended lever (keep the list short on purpose); the cap
// is the safety net so a too-long list can never hard-400 a provider.
func newToolSelector(exposer toolExposer, provider string, w io.Writer) func([]providers.ToolSpec, string) []providers.ToolSpec {
	return func(specs []providers.ToolSpec, model string) []providers.ToolSpec {
		if exposer != nil {
			kept := make([]providers.ToolSpec, 0, len(specs))
			for _, s := range specs {
				if exposer.ToolExposed(s.Name) {
					kept = append(kept, s)
				}
			}
			specs = kept
		}
		max := providers.MaxToolsFor(provider, model)
		kept, dropped := providers.CapTools(specs, max)
		if len(dropped) > 0 && w != nil {
			fmt.Fprintf(w, "carlos: tools: %s caps at %d; dropped %d tool(s) to fit (trim with /mcp)\n",
				model, max, len(dropped))
		}
		return kept
	}
}

// mcpAvailability is the lock-guarded availability snapshot the interactive
// TUI reads per turn (agent-loop goroutine) and the /mcp overlay updates
// (UI goroutine). Keyed by server name -> allowlist of raw tool names; an
// absent/empty entry exposes all of that server's tools.
type mcpAvailability struct {
	mu    sync.RWMutex
	allow map[string][]string
}

// newMCPAvailability seeds the snapshot from config's per-server allowlists.
func newMCPAvailability(c mcp.Config) *mcpAvailability {
	a := &mcpAvailability{allow: map[string][]string{}}
	for _, s := range c.Servers {
		if len(s.Tools) > 0 {
			a.allow[s.Name] = append([]string(nil), s.Tools...)
		}
	}
	return a
}

// set replaces a server's allowlist (nil/empty => expose all). Copies the
// slice so the caller's slice can't mutate the stored snapshot afterward.
func (a *mcpAvailability) set(server string, allow []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(allow) == 0 {
		delete(a.allow, server)
		return
	}
	a.allow[server] = append([]string(nil), allow...)
}

func (a *mcpAvailability) ToolExposed(combined string) bool {
	i := strings.Index(combined, mcp.ToolNameSeparator)
	if i <= 0 {
		return true // built-in tool, no server prefix
	}
	server, raw := combined[:i], combined[i+len(mcp.ToolNameSeparator):]
	a.mu.RLock()
	list, ok := a.allow[server]
	a.mu.RUnlock()
	if !ok || len(list) == 0 {
		return true
	}
	for _, t := range list {
		if t == raw {
			return true
		}
	}
	return false
}

// exposedToolCount counts how many of the registry's tools the MCP
// availability allowlist exposes (built-ins always count). Drives the boot
// cap notice.
func exposedToolCount(reg *tools.Registry, mcpCfg mcp.Config) int {
	if reg == nil {
		return 0
	}
	n := 0
	for _, t := range reg.All() {
		if mcpCfg.ToolExposed(t.Name()) {
			n++
		}
	}
	return n
}

// mcpAutoApproveSet snapshots the MCP servers marked AutoApprove into the
// set the LayeredApprover's per-server trust layer consults. nil when none.
func mcpAutoApproveSet(c mcp.Config) map[string]bool {
	if len(c.Servers) == 0 {
		return nil
	}
	m := make(map[string]bool, len(c.Servers))
	for _, s := range c.Servers {
		if s.AutoApprove {
			m[s.Name] = true
		}
	}
	return m
}

// capNotice returns a one-line boot notice when the exposed tool count exceeds
// the model's cap, or "" when there is no cap or no overflow.
func capNotice(provider, model string, exposed int) string {
	max := providers.MaxToolsFor(provider, model)
	if max == 0 || exposed <= max {
		return ""
	}
	return fmt.Sprintf("tools: %d exposed exceeds %s's %d-tool limit; %d auto-dropped per turn (trim with /mcp)",
		exposed, model, max, exposed-max)
}
