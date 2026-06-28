package providers

import "strings"

// Model-aware tool caps. Some providers reject a request outright when the
// tool list is too long: xAI/Grok returns HTTP 400 "Maximum tools limit
// reached. N tools have been provided but the maximum is 200." A coding
// agent wired to a couple of large MCP servers (DigitalOcean's MCP alone
// exposes hundreds of tools) blows past that easily.
//
// MaxToolsFor reports the per-request tool ceiling for a model (0 = no known
// cap), keyed off the model id the same way the oacompat schema sanitizers
// match providers - so it is correct whether the model is reached directly
// or routed through OpenRouter. CapTools then trims the list to fit, keeping
// the agent's built-in tools (its core loop) and dropping MCP overflow, and
// returns the dropped names so the caller can surface what was cut. This is
// a safety net: the real lever is per-server tool availability (see the mcp
// package), which keeps the list short before it ever reaches here.

// mcpToolSep marks an MCP tool name ("<server>__<tool>"). It mirrors
// mcp.ToolNameSeparator without importing that package (providers must stay
// dependency-light). Built-in tool names use single underscores only, so a
// "__" substring reliably identifies an MCP-contributed tool.
const mcpToolSep = "__"

// MaxToolsFor returns the maximum number of tools a model accepts in one
// request, or 0 when no cap is known (the common case - most providers
// accept far more tools than carlos ships). provider is accepted for future
// use and API symmetry; routing is determined by the model id today because
// OpenRouter slugs (e.g. "x-ai/grok-4") carry the upstream identity.
//
// Only caps confirmed empirically are encoded. xAI/Grok = 200 (observed
// 400). Others are added as their limits are confirmed rather than guessed,
// so we never over-trim a provider that would have accepted the full set.
func MaxToolsFor(provider, model string) int {
	m := strings.ToLower(model)
	if strings.Contains(m, "grok") || strings.Contains(m, "x-ai/") {
		return 200
	}
	return 0
}

// CapTools trims specs to at most max entries, preferring to keep the
// agent's built-in tools over MCP-contributed ones. It returns the kept
// slice (original order preserved) and the names of any dropped tools.
//
// max <= 0 means "no cap" - specs is returned unchanged. When len(specs) is
// already within the cap, specs is returned unchanged with no allocation.
//
// Priority: every built-in tool is kept (they are the agent's core loop and
// number ~40, well under any real cap); MCP tools are kept from the front of
// the list until the budget fills, and the remainder are dropped. In the
// degenerate case where built-ins alone exceed max, the first max built-ins
// are kept and the rest (built-in and MCP alike) are dropped.
func CapTools(specs []ToolSpec, max int) (kept []ToolSpec, dropped []string) {
	if max <= 0 || len(specs) <= max {
		return specs, nil
	}
	builtinCount := 0
	for _, s := range specs {
		if !isMCPTool(s.Name) {
			builtinCount++
		}
	}
	mcpBudget := max - builtinCount
	if mcpBudget < 0 {
		mcpBudget = 0
	}
	kept = make([]ToolSpec, 0, max)
	mcpKept, builtinKept := 0, 0
	for _, s := range specs {
		if isMCPTool(s.Name) {
			if mcpKept < mcpBudget {
				kept = append(kept, s)
				mcpKept++
			} else {
				dropped = append(dropped, s.Name)
			}
			continue
		}
		if builtinKept < max {
			kept = append(kept, s)
			builtinKept++
		} else {
			dropped = append(dropped, s.Name)
		}
	}
	return kept, dropped
}

func isMCPTool(name string) bool { return strings.Contains(name, mcpToolSep) }
