package onboarding

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/config"
)

// providerEntry describes one of the four built-in providers and the
// secret it needs. Keep order stable: this is also the iteration order in
// the onboarding UI.
type providerEntry struct {
	name        string // canonical key for the config map
	label       string // human-readable name
	secretLabel string // "API key" or "base URL"
	isURL       bool   // true → goes into BaseURL, false → APIKey
	urlDefault  string // pre-fill for base URL case
}

var providerEntries = []providerEntry{
	{name: "anthropic", label: "Anthropic", secretLabel: "API key"},
	{name: "openai", label: "OpenAI", secretLabel: "API key"},
	{name: "gemini", label: "Google Gemini", secretLabel: "API key"},
	{name: "openrouter", label: "OpenRouter", secretLabel: "API key"},
	{name: "ollama", label: "Ollama", secretLabel: "base URL", isURL: true, urlDefault: "http://localhost:11434"},
}

// providerStage tracks where we are in the per-provider sub-flow.
type providerStage int

const (
	stageAsking providerStage = iota // y/n prompt
	stageEntering
	stageReviewing // after all four, deciding to advance or loop
)

// providerModel is screen 3. It walks the provider list, asks y/l/n, on yes
// prompts for the secret, and loops until at least one is configured (or
// at least one row exists with a "set later" placeholder).
type providerModel struct {
	idx     int
	stage   providerStage
	enabled map[string]bool
	keys    map[string]string
	// setLater marks providers the user chose to configure later.
	// Distinct from skipped providers (which don't get a config row
	// at all): set-later providers land in cfg.Providers with no
	// secret, enabled=false, so `/provider <name>` can fill them in.
	setLater map[string]bool
	input    textinput.Model
	warn     string // non-fatal validation message shown above the prompt
}

// providerResult is the payload emitted on advance.
type providerResult struct {
	providers       map[string]config.ProviderConfig
	defaultProvider string
}

func newProviderModel() providerModel {
	ti := textinput.New()
	ti.CharLimit = 256
	ti.Width = 60
	ti.Prompt = "> "
	return providerModel{
		idx:      0,
		stage:    stageAsking,
		enabled:  map[string]bool{},
		keys:     map[string]string{},
		setLater: map[string]bool{},
		input:    ti,
	}
}

func (m providerModel) Init() tea.Cmd { return textinput.Blink }

func (m providerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, isKey := msg.(tea.KeyMsg)

	switch m.stage {
	case stageAsking:
		if isKey {
			switch strings.ToLower(k.String()) {
			case "y":
				// Enter input mode for this provider.
				entry := providerEntries[m.idx]
				m.input.Reset()
				if entry.isURL {
					m.input.Placeholder = entry.urlDefault
					m.input.SetValue(entry.urlDefault)
					// Masked input for API keys only; URLs
					// are non-secret, render as plain text.
					m.input.EchoMode = textinput.EchoNormal
				} else {
					m.input.Placeholder = "sk-..."
					m.input.EchoMode = textinput.EchoPassword
					m.input.EchoCharacter = '•'
				}
				m.input.Focus()
				m.stage = stageEntering
				m.warn = ""
				return m, textinput.Blink
			case "l":
				// Set later: writes a disabled placeholder row so
				// `/provider <name>` (or `carlos onboard --only
				// providers`) can fill in the secret without
				// re-running the full onboarding.
				name := providerEntries[m.idx].name
				m.enabled[name] = false
				m.setLater[name] = true
				return m.nextProvider(), nil
			case "n", "enter":
				// Skip this provider entirely (no config row).
				m.enabled[providerEntries[m.idx].name] = false
				m.setLater[providerEntries[m.idx].name] = false
				return m.nextProvider(), nil
			}
		}
		return m, nil

	case stageEntering:
		if isKey {
			switch k.String() {
			case "enter":
				value := strings.TrimSpace(m.input.Value())
				entry := providerEntries[m.idx]
				if value == "" {
					m.warn = fmt.Sprintf("%s cannot be empty. Press esc to back out and pick [l] to set later, or [n] to skip.", entry.secretLabel)
					return m, nil
				}
				m.enabled[entry.name] = true
				m.keys[entry.name] = value
				m.input.Reset()
				m.stage = stageAsking
				return m.nextProvider(), nil
			case "esc":
				// Bail on this provider's input, go back to
				// y/n. Lets the user fix a fat-fingered "y".
				m.input.Reset()
				m.stage = stageAsking
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case stageReviewing:
		if isKey {
			switch k.String() {
			case "enter":
				// At least one configured? Otherwise loop.
				if !m.anyConfigured() {
					m.warn = "Configure at least one provider to continue."
					m.idx = 0
					m.stage = stageAsking
					return m, nil
				}
				return m, nextScreen(m.toResult())
			case "r":
				// Re-run the loop.
				m.idx = 0
				m.stage = stageAsking
				m.warn = ""
				return m, nil
			}
		}
	}
	return m, nil
}

// nextProvider advances to the next provider, or transitions to
// stageReviewing when we've walked past the last one.
func (m providerModel) nextProvider() providerModel {
	m.idx++
	if m.idx >= len(providerEntries) {
		m.stage = stageReviewing
		// Validation: if nothing configured, immediately loop back
		// (caught on next enter in stageReviewing).
	}
	return m
}

func (m providerModel) anyConfigured() bool {
	for _, e := range providerEntries {
		if m.enabled[e.name] {
			return true
		}
	}
	return false
}

// toResult converts the per-key collected state into a providerResult
// suitable for merging into the config. Both configured and
// "set-later" providers land in the map; only configured ones can
// claim the default-provider slot since the default needs a working
// secret to dispatch through.
func (m providerModel) toResult() providerResult {
	r := providerResult{providers: map[string]config.ProviderConfig{}}
	for _, e := range providerEntries {
		switch {
		case m.enabled[e.name]:
			pc := config.ProviderConfig{}
			if e.isURL {
				pc.BaseURL = m.keys[e.name]
			} else {
				pc.APIKey = m.keys[e.name]
			}
			r.providers[e.name] = pc
			if r.defaultProvider == "" {
				// First-configured wins as default. The model picker
				// screen can present this to the user later if we add
				// a "change default" affordance.
				r.defaultProvider = e.name
			}
		case m.setLater[e.name]:
			// Placeholder row with no secret. config.IsComplete
			// rejects this until the user fills in a key, so the
			// next launch will re-trigger onboarding if no other
			// provider was configured.
			r.providers[e.name] = config.ProviderConfig{}
		}
	}
	return r
}

func (m providerModel) View() string {
	// Title owned by Flow.renderRightPane; we render the body only.
	var sb strings.Builder
	sb.WriteString(styleHint.Render("Carlos works with one. Enable as many as you like."))
	sb.WriteString("\n\n")

	// Compact summary of what's been chosen so far.
	for i, e := range providerEntries {
		var mark string
		switch {
		case i > m.idx:
			mark = lipgloss.NewStyle().Foreground(colorMuted).Render("[ ]")
		case i == m.idx && m.stage != stageReviewing:
			mark = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("[>]")
		case m.enabled[e.name]:
			mark = lipgloss.NewStyle().Foreground(colorSuccess).Render("[x]")
		case m.setLater[e.name]:
			mark = lipgloss.NewStyle().Foreground(colorWarn).Render("[~]")
		default:
			mark = lipgloss.NewStyle().Foreground(colorMuted).Render("[-]")
		}
		suffix := ""
		if m.setLater[e.name] {
			suffix = " " + styleHint.Render("(set later)")
		}
		sb.WriteString(fmt.Sprintf("  %s  %s%s\n", mark, e.label, suffix))
	}
	sb.WriteString("\n")

	if m.warn != "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorWarn).Render(m.warn))
		sb.WriteString("\n\n")
	}

	switch m.stage {
	case stageAsking:
		e := providerEntries[m.idx]
		sb.WriteString(fmt.Sprintf("Enable %s?\n",
			lipgloss.NewStyle().Bold(true).Render(e.label)))
		sb.WriteString("  ")
		sb.WriteString(styleKey.Render("[y]"))
		sb.WriteString(styleHint.Render(" configure now   "))
		sb.WriteString(styleKey.Render("[l]"))
		sb.WriteString(styleHint.Render(" set later   "))
		sb.WriteString(styleKey.Render("[n]"))
		sb.WriteString(styleHint.Render(" skip"))
	case stageEntering:
		e := providerEntries[m.idx]
		sb.WriteString(fmt.Sprintf("%s %s:\n", e.label, e.secretLabel))
		sb.WriteString(m.input.View())
		sb.WriteString("\n")
		sb.WriteString(styleHint.Render("  [enter] save   [esc] skip this provider"))
	case stageReviewing:
		if m.anyConfigured() {
			sb.WriteString(styleHint.Render("[enter] continue   [r] reconfigure"))
		} else {
			sb.WriteString(lipgloss.NewStyle().Foreground(colorWarn).Render(
				"No providers configured. Press [enter] to loop back."))
		}
	}
	return sb.String()
}
