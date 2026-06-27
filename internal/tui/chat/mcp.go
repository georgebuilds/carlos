package chat

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/mcp"
)

// MCPServerStatus is the live connection snapshot for one configured MCP
// server, surfaced in the `/mcp` listing. It is built by cmd/carlos from
// the sessions ConnectAll brought up at boot and injected via
// [WithMCPStatus]. MCP servers connect once at startup (mid-session reload
// is not yet wired), so a boot-time snapshot stays accurate for the life
// of the session.
type MCPServerStatus struct {
	// Name matches ServerConfig.Name so the listing can join the
	// on-disk config against live state.
	Name string
	// Connected is true when the server's session came up at boot.
	Connected bool
	// Tools is the number of tools the server contributed. Zero is a
	// valid value (a server that exposes none) and also the fallback
	// when the count is unknown.
	Tools int
}

// mcpSlash routes the `/mcp` verb. With no args (or `list`) it prints the
// configured servers and their live status; `add` / `remove` edit the
// on-disk config; `help` prints usage. Config edits go through the same
// atomic config.Save path /schedule uses and take effect on the next
// carlos start (servers connect at boot).
func (m *Model) mcpSlash(args string) tea.Cmd {
	verb, rest, _ := strings.Cut(strings.TrimSpace(args), " ")
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "", "list":
		return m.mcpList()
	case "add":
		return m.mcpAdd(strings.TrimSpace(rest))
	case "remove", "rm", "delete":
		return m.mcpRemove(strings.TrimSpace(rest))
	case "help":
		return m.mcpEcho(mcpHelpText(), "mcp: see usage above", statusInfo)
	default:
		return statusCmd("usage: /mcp [list|add|remove|help]", statusWarn)
	}
}

// mcpList renders the configured MCP servers, annotated with live
// connection status when [WithMCPStatus] is wired. The empty state is a
// deliberate teaching moment: it tells the user there are no servers and
// exactly how to add one.
func (m *Model) mcpList() tea.Cmd {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		// No config yet (e.g. CARLOS_CONFIG points at a path onboarding
		// hasn't written) is not an error worth surfacing - it's the
		// empty state. Onboarding writes a config before the chat runs,
		// so this is an edge path, but a raw "no such file" would read
		// as a bug rather than "you have no servers".
		if errors.Is(err, os.ErrNotExist) {
			return m.mcpEcho(mcpEmptyText(), "mcp: no servers configured", statusInfo)
		}
		return statusCmd("mcp: load config: "+err.Error(), statusWarn)
	}
	var live []MCPServerStatus
	if m.mcpStatus != nil {
		live = m.mcpStatus()
	}
	// Scope the listing to the active frame so the connected/total
	// counts line up with the live snapshot (also frame-scoped). An
	// empty active frame (legacy single-shelf mode) returns every
	// server, matching ForFrame's contract.
	servers := cfg.MCP.ForFrame(m.frame.Active)
	text, summary := composeMCPList(servers, live)
	return m.mcpEcho(text, summary, statusInfo)
}

// composeMCPList is the pure, testable formatter behind mcpList. It joins
// the configured servers against the live status snapshot and returns the
// multi-line transcript body plus a one-line footer summary. Pulled out
// so tests can pin the exact text without standing up a chat Model.
func composeMCPList(servers []mcp.ServerConfig, live []MCPServerStatus) (body, summary string) {
	if len(servers) == 0 {
		return mcpEmptyText(), "mcp: no servers configured"
	}
	statusByName := make(map[string]MCPServerStatus, len(live))
	for _, s := range live {
		statusByName[s.Name] = s
	}
	// Stable, name-sorted order so the listing reads the same every
	// invocation (config order is insertion order, which surprises).
	sorted := make([]mcp.ServerConfig, len(servers))
	copy(sorted, servers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	b.WriteString(fmt.Sprintf("MCP servers (%d configured)\n", len(servers)))
	connected := 0
	for _, s := range sorted {
		line := "  " + mcpStatusGlyph(s.Name, live, statusByName) + " " + s.Name
		line += "  " + mcpTargetSummary(s)
		if st, ok := statusByName[s.Name]; ok && st.Connected {
			connected++
			line += fmt.Sprintf("  (%s)", pluralTools(st.Tools))
		}
		if len(s.Frames) > 0 {
			line += "  frames: " + strings.Join(s.Frames, ",")
		}
		b.WriteString(line + "\n")
	}
	if live != nil {
		b.WriteString(fmt.Sprintf("\n%d/%d connected this session.", connected, len(servers)))
		b.WriteString(" Changes apply on next carlos start.")
		summary = fmt.Sprintf("mcp: %d configured, %d connected", len(servers), connected)
	} else {
		b.WriteString("\nConnection status unavailable in this session.")
		summary = fmt.Sprintf("mcp: %d configured", len(servers))
	}
	b.WriteString("\nAdd one with /mcp add, remove with /mcp remove <name>.")
	return b.String(), summary
}

// mcpStatusGlyph picks the leading glyph for a server row. When no live
// snapshot is available at all (live == nil) we render a neutral bullet
// rather than implying the server is down. With a snapshot, a connected
// server is a filled dot and everything else an open dot.
func mcpStatusGlyph(name string, live []MCPServerStatus, byName map[string]MCPServerStatus) string {
	if live == nil {
		return "•"
	}
	if st, ok := byName[name]; ok && st.Connected {
		return "●"
	}
	return "○"
}

// mcpTargetSummary renders the transport plus its connection target so a
// `/mcp list` row is self-describing: stdio shows the command, http/sse
// the URL.
func mcpTargetSummary(s mcp.ServerConfig) string {
	switch s.TransportKind() {
	case mcp.TransportHTTP, mcp.TransportSSE:
		return s.TransportKind() + " " + s.URL
	default:
		cmd := s.Command
		if len(s.Args) > 0 {
			cmd += " " + strings.Join(s.Args, " ")
		}
		return "stdio " + cmd
	}
}

// pluralTools renders "N tool" / "N tools" so the summary reads naturally.
func pluralTools(n int) string {
	if n == 1 {
		return "1 tool"
	}
	return fmt.Sprintf("%d tools", n)
}

// mcpEmptyText is the no-servers teaching state. It names the two add
// forms (stdio and http) so the user can copy-paste a working command.
func mcpEmptyText() string {
	return strings.Join([]string{
		"No MCP servers configured.",
		"",
		"MCP servers extend carlos with extra tools (GitHub, Postgres, etc.).",
		"Add one without leaving the chat:",
		"",
		"  /mcp add github npx -y @modelcontextprotocol/server-github",
		"  /mcp add -t http docs https://example.com/mcp",
		"",
		"Or import your Claude Code servers by re-running `carlos onboard --only mcp`.",
		"Servers connect on the next carlos start.",
	}, "\n")
}

// mcpHelpText is the /mcp help body.
func mcpHelpText() string {
	return strings.Join([]string{
		"/mcp - manage Model Context Protocol servers",
		"",
		"  /mcp                 list configured servers and their status",
		"  /mcp list            same as /mcp",
		"  /mcp add <name> <command> [args...]   add a stdio server",
		"  /mcp add -t http <name> <url>         add a Streamable-HTTP server (-t sse for SSE)",
		"  /mcp add ... -e KEY=VAL               env override; -H \"H: v\" for http/sse headers",
		"  /mcp remove <name>   remove a server",
		"  /mcp help            this message",
		"",
		"Servers connect at startup; add/remove take effect on the next run.",
	}, "\n")
}

// mcpAdd parses an add command and appends the server to the config. The
// grammar is shared with the `carlos mcp add` CLI via mcp.ParseAddSpec, so
// the TUI accepts stdio (`<name> <command> [args...]` or `<name> -- <command>
// [args...]`), remote (`<name> --http|--sse <url>`), and `-e KEY=VAL` env /
// `-H "K: V"` header overrides. The server is validated before it touches
// disk so a malformed entry fails here rather than as a confusing boot-time
// connect error. Input is whitespace-split, so values that contain spaces
// must be added via the `carlos mcp add` CLI instead.
func (m *Model) mcpAdd(rest string) tea.Cmd {
	sc, err := mcp.ParseAddSpec(strings.Fields(rest))
	if err != nil {
		return statusCmd(err.Error(), statusWarn)
	}

	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return statusCmd("mcp add: load config: "+err.Error(), statusWarn)
	}
	if !cfg.MCP.AddServer(sc) {
		return statusCmd(fmt.Sprintf("mcp: server %q already exists", sc.Name), statusWarn)
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		return statusCmd("mcp add: save config: "+err.Error(), statusWarn)
	}
	return statusCmd(fmt.Sprintf("added MCP server %q (%s); restart carlos to connect", sc.Name, sc.TransportKind()), statusInfo)
}

// mcpRemove deletes a server by name and persists the change.
func (m *Model) mcpRemove(name string) tea.Cmd {
	name = strings.TrimSpace(name)
	if name == "" {
		return statusCmd("usage: /mcp remove <name>", statusWarn)
	}
	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return statusCmd("mcp remove: load config: "+err.Error(), statusWarn)
	}
	out := cfg.MCP.Servers[:0]
	removed := false
	for _, s := range cfg.MCP.Servers {
		if s.Name == name {
			removed = true
			continue
		}
		out = append(out, s)
	}
	if !removed {
		return statusCmd(fmt.Sprintf("mcp: no server named %q", name), statusWarn)
	}
	cfg.MCP.Servers = out
	if err := config.Save(cfgPath, cfg); err != nil {
		return statusCmd("mcp remove: save config: "+err.Error(), statusWarn)
	}
	return statusCmd(fmt.Sprintf("removed MCP server %q; restart carlos to apply", name), statusInfo)
}

// mcpEcho appends the long body to the transcript (so the user can scroll
// back to it) and mirrors a short summary into the footer status. Mirrors
// whoamiSlash's two-surface echo so multi-line content never has to fit in
// the single-line footer.
func (m *Model) mcpEcho(body, summary string, kind statusKind) tea.Cmd {
	m.transcript = append(m.transcript, transcriptEntry{
		kind: entrySlashEcho,
		ts:   time.Now().UTC(),
		text: body,
	})
	m.rerenderViewport()
	return statusCmd(summary, kind)
}
