package onboarding

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/config"
)

// nameModel is screen 2: a single text input pre-filled with "Boss".
// Pressing enter on the default accepts it (the SPEC's "enter through it"
// affordance).
type nameModel struct {
	input textinput.Model
}

// nameResult is the payload emitted to Flow on advance.
type nameResult struct{ name string }

func newNameModel(initial string) nameModel {
	ti := textinput.New()
	ti.Placeholder = config.DefaultUserName
	ti.SetValue(initial)
	ti.CharLimit = 64
	ti.Width = 32
	ti.Prompt = "> "
	ti.Focus()
	return nameModel{input: ti}
}

func (m nameModel) Init() tea.Cmd { return textinput.Blink }

func (m nameModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "enter":
			name := strings.TrimSpace(m.input.Value())
			if name == "" {
				name = config.DefaultUserName
			}
			return m, nextScreen(nameResult{name: name})
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m nameModel) View() string {
	// Title is owned by Flow.renderRightPane; here we render only the
	// body (hint + input). Double-printing the title was the original
	// "appears twice" bug.
	intro := styleHint.Render("setting up your personal frame, the one carlos opens by default.")
	more := styleHint.Render("add work, side, or research frames later with /frame new or Ctrl+F.")
	hint := styleHint.Render("press enter to continue")
	return lipgloss.JoinVertical(lipgloss.Left,
		intro,
		more,
		"",
		m.input.View(),
		"",
		hint,
	)
}
