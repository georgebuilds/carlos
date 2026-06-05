package onboarding

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/config"
)

// suggestedDefaultModel returns the SPEC's suggested default model for a
// provider as of 2026-06. These are *suggestions only*: the screen lets
// the user override. Keep them current with the README provider matrix.
func suggestedDefaultModel(provider string) string {
	switch provider {
	case "anthropic":
		return "claude-opus-4-7"
	case "openai":
		return "gpt-5"
	case "openrouter":
		// OpenRouter encourages explicit provider/model addressing;
		// we point at the same Claude flagship for parity with the
		// Anthropic default — easy mental model.
		return "anthropic/claude-opus-4-7"
	case "ollama":
		// Local default that ships in most ollama installs. The user
		// will almost always override; this is just "something works
		// on enter".
		return "llama3.1:8b"
	}
	return ""
}

// modelModel is screen 4: a per-provider text input pre-filled with the
// suggested default. We walk the providers in stable order; one prompt
// at a time keeps the interaction quiet.
type modelModel struct {
	providers []string // ordered list of enabled provider keys
	idx       int
	input     textinput.Model
	chosen    map[string]string
}

// modelResult is the payload returned on advance — a map keyed by
// provider name.
type modelResult struct {
	models map[string]string
}

func newModelModel() modelModel {
	ti := textinput.New()
	ti.CharLimit = 128
	ti.Width = 48
	ti.Prompt = "> "
	ti.Focus()
	return modelModel{chosen: map[string]string{}, input: ti}
}

// syncFromConfig refreshes the model list from the current config. Called
// by Flow each frame so back-navigation from provider → model picks up
// changed selections without losing what's already been entered.
func (m *modelModel) syncFromConfig(cfg *config.Config) {
	want := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		want = append(want, name)
	}
	sort.Strings(want)
	// Detect change.
	if equalStrSlices(m.providers, want) {
		return
	}
	m.providers = want
	// Clamp index in case providers shrank.
	if m.idx >= len(m.providers) {
		m.idx = 0
	}
	m.primeInput(cfg)
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// primeInput sets the text input's value to the current provider's prior
// pick (if revisiting) or the suggested default.
func (m *modelModel) primeInput(cfg *config.Config) {
	if len(m.providers) == 0 {
		return
	}
	p := m.providers[m.idx]
	v := m.chosen[p]
	if v == "" {
		if existing, ok := cfg.Providers[p]; ok && existing.DefaultModel != "" {
			v = existing.DefaultModel
		} else {
			v = suggestedDefaultModel(p)
		}
	}
	m.input.SetValue(v)
	m.input.Placeholder = suggestedDefaultModel(p)
}

func (m modelModel) Init() tea.Cmd { return textinput.Blink }

func (m modelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if len(m.providers) == 0 {
		// Defensive: provider screen should have enforced ≥1.
		return m, nextScreen(modelResult{models: map[string]string{}})
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "enter":
			value := strings.TrimSpace(m.input.Value())
			if value == "" {
				value = suggestedDefaultModel(m.providers[m.idx])
			}
			m.chosen[m.providers[m.idx]] = value
			m.idx++
			if m.idx >= len(m.providers) {
				return m, nextScreen(modelResult{models: m.chosen})
			}
			// Prime the next provider's input. We don't have a
			// *config.Config here, but we know what we've already
			// chosen — use that as fallback.
			next := m.providers[m.idx]
			v := m.chosen[next]
			if v == "" {
				v = suggestedDefaultModel(next)
			}
			m.input.SetValue(v)
			m.input.Placeholder = suggestedDefaultModel(next)
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m modelModel) View() string {
	if len(m.providers) == 0 {
		return styleHint.Render("(no providers configured — go back with shift-tab)")
	}
	// Defensive: after the user submits the LAST provider's model,
	// Update increments m.idx past len(providers) and returns a
	// nextScreen cmd. Bubbletea renders one more frame before that
	// cmd's msg propagates through the Flow, so View() can run with
	// idx == len. Prior to this guard that crashed with an out-of-
	// range index on the next-cur lookup.
	if m.idx >= len(m.providers) {
		return styleHint.Render("(advancing…)")
	}
	// Title owned by Flow.renderRightPane; we render the body only.
	var sb strings.Builder
	sb.WriteString(styleHint.Render(fmt.Sprintf(
		"Provider %d of %d  •  press enter to accept the suggestion",
		m.idx+1, len(m.providers))))
	sb.WriteString("\n\n")

	cur := m.providers[m.idx]
	label := lipgloss.NewStyle().Bold(true).Render(cur)
	sb.WriteString(fmt.Sprintf("%s default model:\n", label))
	sb.WriteString(m.input.View())
	sb.WriteString("\n")
	return sb.String()
}
