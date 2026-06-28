package chat

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderMCPOverlay paints the /mcp tool-management overlay at the two levels.
// Style matches the palette overlay: dashed rules, dim corner tags, ▸ cursor
// marker, [*]/[ ] checkboxes, accent/muted/subtle tiers. No solid box.
func renderMCPOverlay(m *Model, innerW, innerH int) string {
	if m.mcpLevel == 1 {
		return renderMCPTools(m, innerW, innerH)
	}
	return renderMCPServers(m, innerW, innerH)
}

func renderMCPServers(m *Model, innerW, innerH int) string {
	dim := lipgloss.NewStyle().Foreground(colorMuted)
	tag := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	ruleW := clampInt(innerW, 1, 80)

	var b strings.Builder
	b.WriteString(dim.Render(strings.Repeat("┄", ruleW)) + " " + tag.Render("mcp servers") + "\n")
	if banner := mcpCapBanner(m); banner != "" {
		b.WriteString(banner + "\n")
	}
	b.WriteString("\n")

	rows := make([]string, 0, len(m.mcpServers))
	for i, s := range m.mcpServers {
		rows = append(rows, renderMCPServerRow(s, i == m.mcpSrvCursor, innerW))
	}
	body, _ := scrollWindow(rows, m.mcpSrvCursor, innerH-6)
	b.WriteString(strings.Join(body, "\n"))
	b.WriteString("\n\n")
	b.WriteString(mcpFooter(
		fk("↑↓", "move"), fk("enter", "tools"), fk("a", "auto-approve"), fk("esc", "close"),
	))
	return b.String()
}

func renderMCPServerRow(s MCPManagedServer, focused bool, w int) string {
	marker := "  "
	if focused {
		marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
	}
	dot := lipgloss.NewStyle().Foreground(colorSubtle).Render("○")
	if s.Connected {
		dot = lipgloss.NewStyle().Foreground(colorOK).Render("●")
	}
	nameStyle := lipgloss.NewStyle().Foreground(colorAgent)
	if focused {
		nameStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	}
	enabled := enabledCount(s)
	total := len(s.Tools)
	count := lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%d/%d tools", enabled, total))
	badge := lipgloss.NewStyle().Foreground(colorSubtle).Render("ask")
	if s.AutoApprove {
		badge = lipgloss.NewStyle().Foreground(colorAccent).Render("auto")
	}
	return fmt.Sprintf("%s%s %s   %s   %s", marker, dot, nameStyle.Render(s.Name), count, badge)
}

func renderMCPTools(m *Model, innerW, innerH int) string {
	dim := lipgloss.NewStyle().Foreground(colorMuted)
	tag := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	accent := lipgloss.NewStyle().Foreground(colorAccent)
	ruleW := clampInt(innerW, 1, 80)

	srv := m.mcpServerByName(m.mcpWorkingSrv)
	enabled := 0
	total := 0
	if srv != nil {
		total = len(srv.Tools)
		for _, t := range srv.Tools {
			if m.mcpWorking[t.Raw] {
				enabled++
			}
		}
	}

	var b strings.Builder
	b.WriteString(dim.Render(strings.Repeat("┄", ruleW)) + " " +
		tag.Render(fmt.Sprintf("%s · %d/%d enabled", m.mcpWorkingSrv, enabled, total)) + "\n")

	// Query row (always shown so the affordance is discoverable).
	query := accent.Render("⌕ ") + m.mcpFilter
	if m.mcpFilterMode {
		query += accent.Render("▌")
	} else if m.mcpFilter == "" {
		query += tag.Render("/ to filter")
	}
	b.WriteString(query + "\n")
	b.WriteString(dim.Render(strings.Repeat("┄", clampInt(innerW, 1, 80))) + "\n")

	tools := m.filteredMCPTools()
	rows := make([]string, 0, len(tools))
	for i, t := range tools {
		rows = append(rows, renderMCPToolRow(t, m.mcpWorking[t.Raw], i == m.mcpToolCursor, innerW))
	}
	if len(rows) == 0 {
		rows = append(rows, dim.Render("  no tools match"))
	}
	body, more := scrollWindow(rows, m.mcpToolCursor, innerH-7)
	b.WriteString(strings.Join(body, "\n"))
	if more > 0 {
		b.WriteString("\n" + dim.Render(fmt.Sprintf("  %d more ↓", more)))
	}
	b.WriteString("\n\n")
	b.WriteString(mcpFooter(
		fk("space", "toggle"), fk("a/n", "all/none"), fk("/", "filter"), fk("esc", "back · saved"),
	))
	return b.String()
}

func renderMCPToolRow(t MCPManagedTool, enabled, focused bool, w int) string {
	marker := "  "
	if focused {
		marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
	}
	box := lipgloss.NewStyle().Foreground(colorMuted).Render("[ ]")
	if enabled {
		box = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("[*]")
	}
	nameStyle := lipgloss.NewStyle().Foreground(colorAgent)
	if focused {
		nameStyle = lipgloss.NewStyle().Foreground(colorAccent)
	}
	line := marker + box + " " + nameStyle.Render(t.Raw)
	if d := strings.TrimSpace(t.Description); d != "" {
		avail := w - lipgloss.Width(line) - 4
		if avail > 8 {
			line += "  " + lipgloss.NewStyle().Foreground(colorSubtle).Render(truncateRight(d, avail))
		}
	}
	return line
}

// mcpCapBanner returns the model-cap warning line when the exposed tool set
// overflows the active model's cap, or "" otherwise.
func mcpCapBanner(m *Model) string {
	if m.mcpMgr == nil {
		return ""
	}
	model, cap, exposed := m.mcpMgr.CapStatus()
	if cap == 0 || exposed <= cap {
		return ""
	}
	warn := lipgloss.NewStyle().Foreground(colorWarn)
	return warn.Render(fmt.Sprintf("  ⚠ %s caps at %d · %d exposed · trim %d below",
		model, cap, exposed, exposed-cap))
}

// mcpFooter joins keybind segments with the shared footer separator.
func mcpFooter(segs ...string) string {
	return strings.Join(segs, footerSep())
}

// fk renders one "<key> <label>" footer segment using the shared helpers.
func fk(key, label string) string {
	return footerKey(key) + footerLabel(" "+label)
}

// scrollWindow returns the slice of rows centered enough to keep cursor
// visible within capacity lines, plus the count hidden below the window.
func scrollWindow(rows []string, cursor, capacity int) (window []string, hiddenBelow int) {
	if capacity < 1 {
		capacity = 1
	}
	if len(rows) <= capacity {
		return rows, 0
	}
	start := 0
	if cursor >= capacity {
		start = cursor - capacity + 1
	}
	if start+capacity > len(rows) {
		start = len(rows) - capacity
	}
	if start < 0 {
		start = 0
	}
	end := start + capacity
	return rows[start:end], len(rows) - end
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
