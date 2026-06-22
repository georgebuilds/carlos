package onboarding

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/mcp"
	"github.com/georgebuilds/carlos/internal/mcp/ccimport"
)

// mcpImportModel is the multi-select picker for Claude Code MCP servers.
// carlos and Claude Code keep SEPARATE MCP configs by design; this screen
// is the one bridge - it lets the user copy individual server definitions
// across rather than wiring each one twice. Discovery runs once at
// construction (a couple of small file reads); the user toggles which
// servers to bring over and they're merged into the config one at a time on
// enter, deduped by name.
type mcpImportModel struct {
	items    []ccimport.Discovered
	selected map[int]bool
	cursor   int
}

// mcpImportResult carries the servers the user chose to import. Flow merges
// them via mcp.Config.AddServer (dedup-by-name) one at a time, so a name
// that already exists in the config is left untouched.
type mcpImportResult struct{ servers []mcp.ServerConfig }

// newMCPImportModel discovers Claude Code's MCP servers from the usual
// locations (~/.claude.json and the working directory's .mcp.json) and
// returns a picker over them. Discovery is best-effort: a machine without
// Claude Code installed yields an empty list and the screen degrades to a
// single "nothing to import" step the user pages past with enter.
func newMCPImportModel() mcpImportModel {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	return newMCPImportModelFromItems(ccimport.Discover(home, cwd))
}

// newMCPImportModelFromItems builds the picker from an already-discovered
// list. Split out from newMCPImportModel so tests can inject servers
// without staging fake Claude Code config files on disk.
func newMCPImportModelFromItems(items []ccimport.Discovered) mcpImportModel {
	return mcpImportModel{items: items, selected: make(map[int]bool)}
}

func (m mcpImportModel) Init() tea.Cmd { return nil }

func (m mcpImportModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "enter":
		// Advance with the selected servers (possibly none - the screen is
		// entirely skippable, like the gateway). Flow does the importing.
		return m, nextScreen(mcpImportResult{servers: m.chosen()})
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case " ", "x":
		if len(m.items) > 0 {
			m.selected[m.cursor] = !m.selected[m.cursor]
		}
	case "a":
		// Toggle-all: clear when everything is already selected, otherwise
		// select the whole set. Saves space-mashing when the user wants all.
		if m.allSelected() {
			m.selected = make(map[int]bool)
		} else {
			for i := range m.items {
				m.selected[i] = true
			}
		}
	}
	return m, nil
}

// chosen collects the selected servers in list order.
func (m mcpImportModel) chosen() []mcp.ServerConfig {
	var out []mcp.ServerConfig
	for i, it := range m.items {
		if m.selected[i] {
			out = append(out, it.Server)
		}
	}
	return out
}

// allSelected reports whether every discovered server is currently checked.
// An empty list is reported as not-all-selected so toggle-all still selects.
func (m mcpImportModel) allSelected() bool {
	if len(m.items) == 0 {
		return false
	}
	for i := range m.items {
		if !m.selected[i] {
			return false
		}
	}
	return true
}

// View renders the body only - Flow.renderRightPane owns the title.
func (m mcpImportModel) View() string {
	var sb strings.Builder
	if len(m.items) == 0 {
		sb.WriteString(styleHint.Render(
			"No Claude Code MCP servers found to import."))
		sb.WriteString("\n")
		sb.WriteString(styleHint.Render(
			"carlos keeps its own MCP config; you can add servers later under the mcp: block in ~/.carlos/config.yaml."))
		sb.WriteString("\n\n")
		sb.WriteString(styleHint.Render("enter to continue"))
		return sb.String()
	}

	sb.WriteString(styleHint.Render(
		"Found these MCP servers in Claude Code. Pick which to copy into carlos."))
	sb.WriteString("\n")
	sb.WriteString(styleHint.Render(
		"The configs stay separate; this copies the selected definitions across."))
	sb.WriteString("\n\n")

	for i, it := range m.items {
		box := lipgloss.NewStyle().Foreground(colorMuted).Render("[ ]")
		if m.selected[i] {
			box = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("[x]")
		}
		cursor := "  "
		name := it.Server.Name
		if i == m.cursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("> ")
			name = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(name)
		}
		// Show transport + its target (command for stdio, URL for http/sse)
		// so the user can tell two same-purpose servers apart at a glance.
		transport := it.Server.TransportKind()
		target := it.Server.Command
		if transport != mcp.TransportStdio {
			target = it.Server.URL
		}
		sb.WriteString(fmt.Sprintf("%s%s  %s  %s\n", cursor, box, name,
			styleHint.Render(fmt.Sprintf("(%s) %s", transport, target))))
		sb.WriteString(fmt.Sprintf("        %s\n", styleHint.Render("from "+it.Source)))
	}
	sb.WriteString("\n")
	sb.WriteString(styleHint.Render(
		"↑/↓ move · space toggle · a all · enter import selected"))
	return sb.String()
}
