package onboarding

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DefaultVaultPath returns the suggested vault location. We keep notes
// inside ~/.carlos/ so the rest of carlos's state (config, state.db,
// artifacts) and the user's notes share one backup surface and one
// mental model - "carlos's stuff lives here".
//
// Users who already have an Obsidian vault override this in one
// keystroke from the onboarding screen. Empty home dir falls back to
// a relative path the way config.DefaultPath does.
func DefaultVaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".carlos", "notes")
	}
	return filepath.Join(home, ".carlos", "notes")
}

// vaultModel is the vault-path picker. Single text input prefilled with
// the default; empty input falls back to the default; on submit we
// MkdirAll the chosen path at 0o700 so the notes_* tools have somewhere
// to land on the very next launch.
type vaultModel struct {
	input textinput.Model
	// mkdirErr captures a failure to create the chosen directory so
	// we can surface it without crashing the onboarding flow.
	mkdirErr error
}

// vaultResult is what Flow consumes on advance.
type vaultResult struct{ path string }

func newVaultModel() vaultModel {
	def := DefaultVaultPath()
	ti := textinput.New()
	ti.Placeholder = def
	ti.SetValue(def)
	ti.CharLimit = 256
	ti.Width = 48
	ti.Prompt = "> "
	ti.Focus()
	return vaultModel{input: ti}
}

func (m vaultModel) Init() tea.Cmd { return textinput.Blink }

func (m vaultModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		if k.String() == "enter" {
			raw := strings.TrimSpace(m.input.Value())
			if raw == "" {
				raw = DefaultVaultPath()
			}
			path := expandTilde(raw)
			// MkdirAll is idempotent - re-running onboarding against
			// an existing vault is a no-op. 0o700 matches ~/.carlos.
			if err := os.MkdirAll(path, 0o700); err != nil {
				m.mkdirErr = err
				return m, nil
			}
			return m, nextScreen(vaultResult{path: path})
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View renders the body only - Flow.renderRightPane owns the title.
func (m vaultModel) View() string {
	var sb strings.Builder
	sb.WriteString(styleHint.Render(
		"Where should carlos store notes the notes_* tools read and write?"))
	sb.WriteString("\n")
	sb.WriteString(styleHint.Render(
		"Press enter for the default, or paste an existing Obsidian vault path."))
	sb.WriteString("\n\n")
	sb.WriteString(m.input.View())
	sb.WriteString("\n")
	if m.mkdirErr != nil {
		// Stay on the screen so the user can correct the path. Errors
		// here are usually "you typed /Volumes/missing" or "perm
		// denied" - both recoverable by editing the field.
		errStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
		sb.WriteString("\n")
		sb.WriteString(errStyle.Render("couldn't create that path: "))
		sb.WriteString(styleHint.Render(m.mkdirErr.Error()))
	}
	return sb.String()
}

// expandTilde resolves a leading "~" or "~/" against $HOME. Anything
// else is returned verbatim. Kept package-local because none of the
// other screens need it; the model screen accepts opaque IDs and the
// daemon screen takes a bool.
func expandTilde(p string) string {
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
