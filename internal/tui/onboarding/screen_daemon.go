package onboarding

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
	sb.WriteString(styleHint.Render("the daemon runs in the background so carlos can:"))
	sb.WriteString("\n\n")
	sb.WriteString(styleHint.Render("   - fire scheduled runs (\"every weekday at 9am, summarize my inbox\")"))
	sb.WriteString("\n")
	sb.WriteString(styleHint.Render("   - receive messages from telegram / ntfy / signal"))
	sb.WriteString("\n")
	sb.WriteString(styleHint.Render("   - post a daily digest"))
	sb.WriteString("\n\n")
	sb.WriteString(styleHint.Render("without the daemon, carlos only runs when you launch him."))
	sb.WriteString("\n\n")
	sb.WriteString("enable now?")
	sb.WriteString("\n\n")
	sb.WriteString("   ")
	sb.WriteString(styleKey.Render("[enter]"))
	sb.WriteString(styleHint.Render(" no, just the tui for now"))
	sb.WriteString("\n")
	sb.WriteString("   ")
	sb.WriteString(styleKey.Render("[y]"))
	sb.WriteString(styleHint.Render("     yes, install the background service"))
	return sb.String()
}
