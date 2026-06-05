package onboarding

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// daemonModel is screen 5: a single y/N prompt asking whether to enable
// the background daemon. Default is NO — daemon enables future remote
// gateways (Telegram, Discord, scheduled runs) but the TUI works without
// it, so opt-in keeps the surface area minimal.
//
// Phase 0.5 ONLY records the user's choice into config. The actual
// launchd/systemd install lives in Phase 8 — see TODO note in Flow.View.
type daemonModel struct {
	choice  bool // current toggle; default false
	decided bool // true once user confirmed
}

// daemonResult is the payload returned on advance.
type daemonResult struct{ enabled bool }

func newDaemonModel() daemonModel { return daemonModel{choice: false} }

func (m daemonModel) Init() tea.Cmd { return nil }

func (m daemonModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch strings.ToLower(k.String()) {
		case "y":
			m.choice = true
			return m, nextScreen(daemonResult{enabled: true})
		case "n":
			m.choice = false
			return m, nextScreen(daemonResult{enabled: false})
		case "enter":
			// Enter accepts the default (no), matching the
			// "press enter through it" affordance.
			return m, nextScreen(daemonResult{enabled: m.choice})
		}
	}
	return m, nil
}

func (m daemonModel) View() string {
	// Title owned by Flow.renderRightPane; we render the body only.
	var sb strings.Builder
	sb.WriteString(styleHint.Render(
		"Required for scheduled runs and future remote gateways (Telegram, Discord, push)."))
	sb.WriteString("\n")
	sb.WriteString(styleHint.Render(
		"The TUI works without it. You can flip this later with `carlos daemon enable`."))
	sb.WriteString("\n\n")
	prompt := "Run carlos as a background daemon? "
	def := lipgloss.NewStyle().Foreground(colorMuted).Render("[y/N]")
	sb.WriteString(prompt + def)
	return sb.String()
}
