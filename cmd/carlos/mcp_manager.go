package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/mcp"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
	"github.com/georgebuilds/carlos/internal/tui/chat"
)

// mcpManager implements chat.MCPManager: it reads the live MCP catalog from
// the registry + config and persists availability/auto-approve edits back to
// config. The registry holds every discovered tool (availability filtering
// happens at spec-assembly, not registration), so the picker can list the
// full set even for tools currently hidden.
type mcpManager struct {
	cfg       *config.Config
	reg       *tools.Registry
	connected map[string]bool
	// avail is the lock-guarded availability snapshot the per-turn tool
	// selector reads; SetAllow updates it so an allowlist edit applies this
	// session without racing the agent-loop goroutine. nil in non-TUI paths.
	avail *mcpAvailability
	// dispatch returns the live (provider, model) so CapStatus reflects a
	// /model swap. save persists config edits.
	dispatch func() (provider, model string)
	save     func() error
	// reapprove, when set, re-installs the approver's auto-approve snapshot
	// from config so a /mcp auto-approve toggle takes effect this session
	// (availability still needs a restart, but permission is live, matching
	// workspace trust). Optional.
	reapprove func()
}

// connectedSet returns the set of MCP server names that connected at boot.
func connectedSet(servers []*mcp.Server) map[string]bool {
	m := make(map[string]bool, len(servers))
	for _, s := range servers {
		m[s.Name] = true
	}
	return m
}

// Servers reports the active frame's configured servers with their full tool
// list and current exposed state.
func (mm *mcpManager) Servers() []chat.MCPManagedServer {
	byServer := map[string][]chat.MCPManagedTool{}
	for _, t := range mm.reg.All() {
		name := t.Name()
		i := strings.Index(name, mcp.ToolNameSeparator)
		if i <= 0 {
			continue
		}
		srv := name[:i]
		raw := name[i+len(mcp.ToolNameSeparator):]
		byServer[srv] = append(byServer[srv], chat.MCPManagedTool{
			Raw:         raw,
			Description: t.Description(),
			Enabled:     mm.cfg.MCP.ToolExposed(name),
		})
	}
	configured := mm.cfg.MCP.ForFrame(mm.cfg.Frames.Active)
	out := make([]chat.MCPManagedServer, 0, len(configured))
	for _, sc := range configured {
		toolList := byServer[sc.Name]
		sort.Slice(toolList, func(a, b int) bool { return toolList[a].Raw < toolList[b].Raw })
		out = append(out, chat.MCPManagedServer{
			Name:        sc.Name,
			Connected:   mm.connected[sc.Name],
			AutoApprove: sc.AutoApprove,
			Tools:       toolList,
		})
	}
	return out
}

func (mm *mcpManager) SetAllow(server string, allow []string) error {
	sc := mm.cfg.MCP.Find(server)
	if sc == nil {
		return fmt.Errorf("mcp: unknown server %q", server)
	}
	sc.Tools = allow
	if mm.avail != nil {
		mm.avail.set(server, allow) // apply to the live selector, race-free
	}
	return mm.save()
}

func (mm *mcpManager) SetAutoApprove(server string, on bool) error {
	sc := mm.cfg.MCP.Find(server)
	if sc == nil {
		return fmt.Errorf("mcp: unknown server %q", server)
	}
	sc.AutoApprove = on
	if err := mm.save(); err != nil {
		return err
	}
	if mm.reapprove != nil {
		mm.reapprove() // apply this session, not just next start
	}
	return nil
}

func (mm *mcpManager) CapStatus() (string, int, int) {
	provider, model := mm.dispatch()
	return model, providers.MaxToolsFor(provider, model), exposedToolCount(mm.reg, mm.cfg.MCP)
}
