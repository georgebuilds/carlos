package onboarding

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/config"
)

// skillsModel is the convention-pick screen. Carlos always LOADS from both
// .claude/skills/ and .agents/skills/ (see SPEC § Skill model § Convention
// paths); this screen captures which one to WRITE to when carlos creates
// a new skill (induced PROPOSALs, /skills new). Default favors the open
// standard.
type skillsModel struct {
	choice int // 0 = agents (default), 1 = claude
}

type skillsResult struct{ convention string }

func newSkillsModel() skillsModel { return skillsModel{choice: 0} }

func (m skillsModel) Init() tea.Cmd { return nil }

func (m skillsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "up", "k":
			if m.choice > 0 {
				m.choice--
			}
			return m, nil
		case "down", "j":
			if m.choice < 1 {
				m.choice++
			}
			return m, nil
		case "1":
			m.choice = 0
			return m, nil
		case "2":
			m.choice = 1
			return m, nil
		case "enter":
			conv := config.SkillsConventionAgents
			if m.choice == 1 {
				conv = config.SkillsConventionClaude
			}
			return m, nextScreen(skillsResult{convention: conv})
		}
	}
	return m, nil
}

// View body only — Flow.renderRightPane owns the title.
func (m skillsModel) View() string {
	var sb strings.Builder
	sb.WriteString(styleHint.Render(
		"Carlos loads skills from BOTH .claude/skills/ and .agents/skills/."))
	sb.WriteString("\n")
	sb.WriteString(styleHint.Render(
		"Pick the convention for skills carlos WRITES (induced + /skills new)."))
	sb.WriteString("\n\n")

	type option struct {
		label, sub string
	}
	opts := []option{
		{".agents/skills/", "open agentskills.io standard (default; portable across tools)"},
		{".claude/skills/", "Claude Code convention"},
	}
	for i, o := range opts {
		marker := lipgloss.NewStyle().Foreground(colorMuted).Render("(  )")
		label := o.label
		if i == m.choice {
			marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("(●)")
			label = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(o.label)
		}
		sb.WriteString(fmt.Sprintf("  %s  %s\n", marker, label))
		sb.WriteString(fmt.Sprintf("       %s\n\n", styleHint.Render(o.sub)))
	}
	sb.WriteString(styleHint.Render(
		"↑/↓ or 1/2 to pick · enter to continue"))
	return sb.String()
}
