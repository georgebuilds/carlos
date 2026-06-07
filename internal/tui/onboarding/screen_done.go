package onboarding

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/config"
)

// doneModel is screen 6: a one-line confirmation. Single keypress to exit.
//
// Note: View() does not match tea.Model's signature (it takes the user
// name); Flow calls View directly with the name from its config so we
// don't have to duplicate config state into the model. Update/Init still
// satisfy tea.Model so we can route input through the same Flow.Update
// pipeline.
type doneModel struct{}

func newDoneModel() doneModel { return doneModel{} }

func (m doneModel) Init() tea.Cmd { return nil }

func (m doneModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		return m, quit()
	}
	return m, nil
}

// View is a no-op placeholder so doneModel satisfies tea.Model. Flow.View
// invokes renderName(name) directly; this method is never read.
func (m doneModel) View() string { return "" }

// renderName is the user-facing render. Flow.View calls this with the
// chosen name from cfg.UserName. The configPath argument is the
// actual resolved path Save() will write to (typically
// config.DefaultPath() which honours CARLOS_CONFIG), so the line the
// user sees matches the file the user can edit. Empty falls back to
// config.DefaultPath() so older callers (tests) still work.
func (m doneModel) renderName(name string, configPath string) string {
	if name == "" {
		name = "Boss"
	}
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	headline := lipgloss.NewStyle().
		Bold(true).
		Foreground(colorAccent).
		Render(fmt.Sprintf("Ready, %s.", name))
	sub := styleHint.Render(fmt.Sprintf("Config written to %s. Your personal frame is now live.", configPath))
	nextHeader := styleHint.Render("next moves")
	keyA := styleKey.Render("Ctrl+F")
	keyB := styleKey.Render("/frame new")
	keyC := styleKey.Render("carlos --help")
	hintA := styleHint.Render("        open the frame switcher")
	hintB := styleHint.Render("    add a frame, optionally cloned from personal")
	hintC := styleHint.Render(" list every cli verb (please, research, memory, schedule, gateway, ...)")
	return lipgloss.JoinVertical(lipgloss.Left,
		headline,
		"",
		sub,
		"",
		nextHeader,
		keyA+hintA,
		keyB+hintB,
		keyC+hintC,
	)
}
