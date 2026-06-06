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

// suggestedDefaultModel returns the suggested default model for a
// provider. Pulled from the per-provider curated list in models.go —
// the entry at index 0 is the canonical "press enter on it" default.
//
// Kept as a distinct function so tests can pin defaults independently
// of any list reordering and so callers that only need the slug don't
// have to know about ModelSuggestion.
func suggestedDefaultModel(provider string) string {
	list := providerModels(provider)
	if len(list) == 0 {
		return ""
	}
	return list[0].Slug
}

// modelModel is screen 4: per-provider text input + filtered dropdown.
// We walk the providers in stable order; one prompt at a time keeps
// the interaction quiet.
//
// The dropdown is a discoverability aid, not a gate — users can ignore
// the cursor and type any slug they want. Pressing enter on a raw
// (non-highlighted) value accepts it verbatim. cursor == -1 means "no
// dropdown selection, use the raw input"; cursor >= 0 means "highlight
// suggestion N; enter or tab uses that slug instead".
type modelModel struct {
	providers []string // ordered list of enabled provider keys
	idx       int
	input     textinput.Model
	chosen    map[string]string

	// cursor is the highlighted index in the filtered suggestion list.
	// Reset to -1 (no selection) whenever the input text changes.
	cursor int
}

// maxSuggestions caps how many dropdown rows we render. The full
// curated lists are short (≤ 11) but in case the catalog grows we
// don't want to push the input off-screen.
const maxSuggestions = 8

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
	return modelModel{chosen: map[string]string{}, input: ti, cursor: -1}
}

// currentProvider returns the provider key the screen is asking about
// right now, or "" if the iteration has finished. Exposed so the View
// path can drop the dropdown into the right namespace.
func (m modelModel) currentProvider() string {
	if m.idx < 0 || m.idx >= len(m.providers) {
		return ""
	}
	return m.providers[m.idx]
}

// suggestions returns the suggestion list for the current provider.
// When the input text exactly matches one of the curated slugs (the
// initial prefill case AND the post-tab-complete case), we treat the
// list as un-filtered so the user can keep browsing alternatives;
// otherwise we substring-filter so typing narrows the menu. Capped at
// maxSuggestions so the dropdown always fits the pane.
func (m modelModel) suggestions() []ModelSuggestion {
	prov := m.currentProvider()
	if prov == "" {
		return nil
	}
	val := strings.TrimSpace(m.input.Value())
	all := providerModels(prov)
	for _, s := range all {
		if s.Slug == val {
			if len(all) > maxSuggestions {
				return all[:maxSuggestions]
			}
			return all
		}
	}
	out := filterModels(prov, val)
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return out
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
		// Dropdown navigation. Up/down move the cursor; tab + enter
		// commit. Plain typing falls through to the textinput and
		// resets the cursor — the dropdown filters off whatever the
		// user typed.
		switch k.String() {
		case "up":
			suggs := m.suggestions()
			if len(suggs) == 0 {
				return m, nil
			}
			if m.cursor < 0 {
				m.cursor = len(suggs) - 1
			} else {
				m.cursor--
				if m.cursor < -1 {
					m.cursor = len(suggs) - 1
				}
			}
			return m, nil
		case "down":
			suggs := m.suggestions()
			if len(suggs) == 0 {
				return m, nil
			}
			if m.cursor < 0 {
				m.cursor = 0
			} else {
				m.cursor++
				if m.cursor >= len(suggs) {
					m.cursor = -1
				}
			}
			return m, nil
		case "tab":
			// Tab completes the highlighted suggestion (if any) into
			// the input without committing. Useful for users who
			// want to confirm the slug they picked before pressing
			// enter.
			suggs := m.suggestions()
			if m.cursor >= 0 && m.cursor < len(suggs) {
				m.input.SetValue(suggs[m.cursor].Slug)
				m.input.CursorEnd()
				m.cursor = -1
			}
			return m, nil
		case "enter":
			value := strings.TrimSpace(m.input.Value())
			// A highlighted suggestion takes precedence over the
			// raw input — that's what "I picked one with arrow
			// keys" means to the user.
			if suggs := m.suggestions(); m.cursor >= 0 && m.cursor < len(suggs) {
				value = suggs[m.cursor].Slug
			}
			if value == "" {
				value = suggestedDefaultModel(m.providers[m.idx])
			}
			m.chosen[m.providers[m.idx]] = value
			m.idx++
			m.cursor = -1
			if m.idx >= len(m.providers) {
				return m, nextScreen(modelResult{models: m.chosen})
			}
			// Prime the next provider's input.
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
	// Anything else (typing, backspace, etc.) goes to the textinput
	// and clears the dropdown cursor — the filtered list re-derives
	// from the new value on the next View().
	prev := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != prev {
		m.cursor = -1
	}
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
		"Provider %d of %d  •  ↑/↓ to pick · tab completes · enter accepts",
		m.idx+1, len(m.providers))))
	sb.WriteString("\n\n")

	cur := m.providers[m.idx]
	label := lipgloss.NewStyle().Bold(true).Render(cur)
	sb.WriteString(fmt.Sprintf("%s default model:\n", label))
	sb.WriteString(m.input.View())
	sb.WriteString("\n")

	suggs := m.suggestions()
	if len(suggs) > 0 {
		sb.WriteString("\n")
		sb.WriteString(m.renderDropdown(suggs))
	} else if m.input.Value() != "" {
		// User typed something with no match in the curated list.
		// That's a valid path — they're entering a custom slug —
		// so we tell them what'll happen without flagging it as an
		// error.
		sb.WriteString("\n")
		sb.WriteString(styleHint.Render(
			"(no match in the curated list — enter accepts your input verbatim)"))
	}
	return sb.String()
}

// renderDropdown formats the filtered suggestions for display below
// the textinput. The cursor row uses the brand accent; the others sit
// in the muted palette so the picked row pops without being noisy.
func (m modelModel) renderDropdown(suggs []ModelSuggestion) string {
	// Widths so the columns align: slug | label | note.
	slugW := 0
	for _, s := range suggs {
		if w := lipgloss.Width(s.Slug); w > slugW {
			slugW = w
		}
	}
	if slugW > 36 {
		slugW = 36
	}
	var sb strings.Builder
	noteStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	for i, s := range suggs {
		slug := padRight(s.Slug, slugW)
		row := fmt.Sprintf("  %s  %s", slug, s.Label)
		if s.Note != "" {
			row += "  " + noteStyle.Render(s.Note)
		}
		if i == m.cursor {
			row = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ") +
				lipgloss.NewStyle().Foreground(colorAccent).Render(row[2:])
		} else {
			row = lipgloss.NewStyle().Foreground(colorMuted).Render(row)
		}
		sb.WriteString(row)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// padRight pads s with spaces to width w. Truncates with an ellipsis
// when s exceeds w so the dropdown's column alignment survives an
// over-long slug.
func padRight(s string, w int) string {
	width := lipgloss.Width(s)
	if width == w {
		return s
	}
	if width > w {
		if w <= 1 {
			return "…"
		}
		// Truncate runes from the right until we fit (w-1) cells,
		// leaving room for the ellipsis.
		runes := []rune(s)
		for len(runes) > 0 && lipgloss.Width(string(runes))+1 > w {
			runes = runes[:len(runes)-1]
		}
		return string(runes) + "…"
	}
	return s + strings.Repeat(" ", w-width)
}
