// overlay_mcp.go - the /mcp tool-management overlay.
//
// Bare `/mcp` opens a two-level takeover overlay for managing MCP servers:
//
//	Level 0 (servers): one row per configured server - connection dot, name,
//	  "enabled/total" tool count, and an auto-approve badge. Enter drills into
//	  the server's tool picker; `a` toggles the server's auto-approve.
//	Level 1 (tools): a searchable [*]/[ ] checklist of that server's tools.
//	  space toggles one, `a`/`n` enable/disable all, `/` filters. Leaving the
//	  level (esc/left) persists the allowlist.
//
// A header banner ties this to the model-aware tool cap: when the exposed set
// overflows the active model's limit it shows "<model> caps at N - trim M".
//
// Both availability and auto-approve apply immediately (next turn): the tool
// list is rebuilt every turn from the registry filtered by the live config,
// and the approver re-reads its auto-approve snapshot on each toggle. Adding a
// brand-new server still needs a restart to connect it, but that is /mcp add's
// job, not this overlay (which only configures already-connected servers).
//
// Style follows the palette/resume overlays: dashed rules with dim corner
// tags, the ▸ cursor marker, [*]/[ ] checkboxes, accent/muted/subtle tiers.
// No left-stripe, no solid box.

package chat

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// hideAllToolsSentinel is the single allowlist entry that means "expose none"
// (distinct from an empty/absent allowlist, which means "expose all"). It is
// the empty string because a real MCP tool name is never empty, so it matches
// nothing and hides every tool. ToolExposed needs no special case for it.
const hideAllToolsSentinel = ""

// MCPManagedTool is one tool a server advertises, with its current exposed
// state (per the availability allowlist).
type MCPManagedTool struct {
	Raw         string
	Description string
	Enabled     bool
}

// MCPManagedServer is the overlay's view of one configured MCP server.
type MCPManagedServer struct {
	Name        string
	Connected   bool
	AutoApprove bool
	Tools       []MCPManagedTool
}

// MCPManager is the seam the runtime implements (cmd/carlos) so the overlay
// can read the live server/tool catalog and persist edits without the chat
// package importing the mcp + config packages directly.
type MCPManager interface {
	// Servers returns the current catalog: every configured server with its
	// full tool list and per-tool exposed state.
	Servers() []MCPManagedServer
	// SetAllow persists the availability allowlist for a server. allow is the
	// raw tool names to expose; nil/empty means "expose all".
	SetAllow(server string, allow []string) error
	// SetAutoApprove persists the per-server auto-approve flag.
	SetAutoApprove(server string, on bool) error
	// CapStatus reports the active model's tool cap and how many tools are
	// currently exposed, for the header banner. cap == 0 means uncapped.
	CapStatus() (model string, cap, exposed int)
}

// WithMCPManager injects the runtime's MCP manager so `/mcp` can open the
// interactive overlay. Without it, `/mcp` falls back to the text list.
func WithMCPManager(mgr MCPManager) Option {
	return func(m *Model) { m.mcpMgr = mgr }
}

// WithInterrupter injects the callback that aborts the in-flight assistant
// turn (esc-to-interrupt). The runtime points it at the live chatglue.Loop's
// Interrupt. Without it, esc never interrupts (the read-only daemon/web paths
// have no turn to abort).
func WithInterrupter(fn func()) Option {
	return func(m *Model) { m.interrupt = fn }
}

// openMCPOverlay snapshots the catalog and enters the servers level. Returns
// a status echo (not the overlay) when no manager is wired or no servers are
// configured, so the user gets a concrete next step.
func (m *Model) openMCPOverlay() tea.Cmd {
	if m.mcpMgr == nil {
		return m.mcpList() // graceful fallback to the text listing
	}
	servers := m.mcpMgr.Servers()
	if len(servers) == 0 {
		return statusCmd("/mcp: no servers configured - add one with /mcp add", statusInfo)
	}
	m.mcpServers = servers
	m.showMCP = true
	m.mcpLevel = 0
	m.mcpSrvCursor = 0
	m.mcpToolCursor = 0
	m.mcpFilter = ""
	m.mcpFilterMode = false
	m.rerenderViewport()
	return nil
}

// closeMCPOverlay persists any pending edit and tears the overlay down. It
// returns the flush's status cmd (nil on success) so the caller can surface a
// save failure.
func (m *Model) closeMCPOverlay() tea.Cmd {
	cmd := m.flushMCPWorking()
	m.showMCP = false
	m.mcpServers = nil
	m.mcpWorking = nil
	m.mcpWorkingSrv = ""
	m.mcpDirty = false
	m.mcpFilter = ""
	m.mcpFilterMode = false
	m.rerenderViewport()
	return cmd
}

// enterMCPTools loads the working enabled-set for the focused server and
// switches to the tools level.
func (m *Model) enterMCPTools() {
	if m.mcpSrvCursor < 0 || m.mcpSrvCursor >= len(m.mcpServers) {
		return
	}
	srv := m.mcpServers[m.mcpSrvCursor]
	m.mcpWorking = make(map[string]bool, len(srv.Tools))
	for _, t := range srv.Tools {
		m.mcpWorking[t.Raw] = t.Enabled
	}
	m.mcpWorkingSrv = srv.Name
	m.mcpDirty = false
	m.mcpLevel = 1
	m.mcpToolCursor = 0
	m.mcpFilter = ""
	m.mcpFilterMode = false
}

// flushMCPWorking persists the working enabled-set as an allowlist if it was
// edited. An all-enabled set persists as nil (== "expose all"), so a server
// that later adds tools still exposes them. Returns a status cmd describing a
// save failure, or nil on success / no-op.
func (m *Model) flushMCPWorking() tea.Cmd {
	if !m.mcpDirty || m.mcpWorkingSrv == "" || m.mcpMgr == nil {
		m.mcpDirty = false
		return nil
	}
	srv := m.mcpServerByName(m.mcpWorkingSrv)
	allEnabled := true
	allow := make([]string, 0, len(m.mcpWorking))
	if srv != nil {
		for _, t := range srv.Tools {
			if m.mcpWorking[t.Raw] {
				allow = append(allow, t.Raw)
			} else {
				allEnabled = false
			}
		}
	}
	sort.Strings(allow)
	switch {
	case allEnabled:
		allow = nil // nil/absent allowlist == expose all (and future-proofs new tools)
	case len(allow) == 0:
		// None enabled. An empty allowlist already means "expose all", so to
		// persist "expose none" distinctly we store a single empty-string
		// entry: a real MCP tool name is never empty, so nothing matches it
		// and every tool stays hidden. Without this, disabling all tools
		// would silently read back as exposing all of them.
		allow = []string{hideAllToolsSentinel}
	}
	m.mcpDirty = false
	if err := m.mcpMgr.SetAllow(m.mcpWorkingSrv, allow); err != nil {
		return statusCmd("/mcp: saving tool availability failed: "+err.Error(), statusError)
	}
	// Refresh the snapshot so the servers level shows the new counts.
	m.mcpServers = m.mcpMgr.Servers()
	return nil
}

func (m *Model) mcpServerByName(name string) *MCPManagedServer {
	for i := range m.mcpServers {
		if m.mcpServers[i].Name == name {
			return &m.mcpServers[i]
		}
	}
	return nil
}

// filteredMCPTools returns the focused server's tools narrowed by the current
// filter (case-insensitive substring over raw name + description).
func (m *Model) filteredMCPTools() []MCPManagedTool {
	srv := m.mcpServerByName(m.mcpWorkingSrv)
	if srv == nil {
		return nil
	}
	q := strings.TrimSpace(strings.ToLower(m.mcpFilter))
	if q == "" {
		return srv.Tools
	}
	out := make([]MCPManagedTool, 0, len(srv.Tools))
	for _, t := range srv.Tools {
		if strings.Contains(strings.ToLower(t.Raw), q) || strings.Contains(strings.ToLower(t.Description), q) {
			out = append(out, t)
		}
	}
	return out
}

// handleMCPOverlayKey routes keys while the overlay is open. Returns handled
// = true to swallow the key (the overlay owns the keyboard); ctrl+c falls
// through so the user can always quit.
func (m *Model) handleMCPOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.String() == "ctrl+c" {
		return m, nil, false
	}
	if m.mcpLevel == 1 {
		return m.handleMCPToolsKey(msg)
	}
	return m.handleMCPServersKey(msg)
}

func (m *Model) handleMCPServersKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		cmd := m.closeMCPOverlay()
		return m, cmd, true
	case "up", "k":
		m.mcpSrvCursor = wrapCursor(m.mcpSrvCursor-1, len(m.mcpServers))
		m.rerenderViewport()
	case "down", "j":
		m.mcpSrvCursor = wrapCursor(m.mcpSrvCursor+1, len(m.mcpServers))
		m.rerenderViewport()
	case "enter", "right", "l":
		m.enterMCPTools()
		m.rerenderViewport()
	case "a":
		if srv := m.focusedServer(); srv != nil && m.mcpMgr != nil {
			if err := m.mcpMgr.SetAutoApprove(srv.Name, !srv.AutoApprove); err != nil {
				// Leave the displayed snapshot untouched: SetAutoApprove rolls
				// back its in-memory change on failure, so not refreshing keeps
				// the row showing the real (unsaved) state.
				return m, statusCmd("/mcp: saving auto-approve failed: "+err.Error(), statusError), true
			}
			m.mcpServers = m.mcpMgr.Servers()
			m.rerenderViewport()
		}
	}
	return m, nil, true
}

func (m *Model) handleMCPToolsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	// Filter mode captures runes for the query.
	if m.mcpFilterMode {
		switch msg.String() {
		case "esc":
			m.mcpFilterMode = false
		case "enter":
			m.mcpFilterMode = false
		case "backspace":
			if m.mcpFilter != "" {
				m.mcpFilter = m.mcpFilter[:len(m.mcpFilter)-1]
				m.mcpToolCursor = 0
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.mcpFilter += string(msg.Runes)
				m.mcpToolCursor = 0
			}
		}
		m.rerenderViewport()
		return m, nil, true
	}
	tools := m.filteredMCPTools()
	switch msg.String() {
	case "esc", "left", "h", "backspace":
		cmd := m.flushMCPWorking()
		m.mcpLevel = 0
		m.rerenderViewport()
		return m, cmd, true
	case "up", "k":
		m.mcpToolCursor = wrapCursor(m.mcpToolCursor-1, len(tools))
		m.rerenderViewport()
	case "down", "j":
		m.mcpToolCursor = wrapCursor(m.mcpToolCursor+1, len(tools))
		m.rerenderViewport()
	case " ", "x", "enter":
		if m.mcpToolCursor >= 0 && m.mcpToolCursor < len(tools) {
			raw := tools[m.mcpToolCursor].Raw
			m.mcpWorking[raw] = !m.mcpWorking[raw]
			m.mcpDirty = true
			m.rerenderViewport()
		}
	case "a": // enable all (the full set, not just the filtered view)
		m.setAllMCPWorking(true)
		m.rerenderViewport()
	case "n": // disable all
		m.setAllMCPWorking(false)
		m.rerenderViewport()
	case "/":
		m.mcpFilterMode = true
		m.rerenderViewport()
	}
	return m, nil, true
}

func (m *Model) setAllMCPWorking(on bool) {
	srv := m.mcpServerByName(m.mcpWorkingSrv)
	if srv == nil {
		return
	}
	for _, t := range srv.Tools {
		m.mcpWorking[t.Raw] = on
	}
	m.mcpDirty = true
}

func (m *Model) focusedServer() *MCPManagedServer {
	if m.mcpSrvCursor < 0 || m.mcpSrvCursor >= len(m.mcpServers) {
		return nil
	}
	return &m.mcpServers[m.mcpSrvCursor]
}

func wrapCursor(i, n int) int {
	if n == 0 {
		return 0
	}
	if i < 0 {
		return n - 1
	}
	if i >= n {
		return 0
	}
	return i
}

// enabledCount reports how many of a server's tools are currently exposed.
func enabledCount(s MCPManagedServer) int {
	n := 0
	for _, t := range s.Tools {
		if t.Enabled {
			n++
		}
	}
	return n
}
