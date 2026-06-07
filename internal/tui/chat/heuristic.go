// Phase O five-checkbox heuristic. When the active frame is in
// orchestrator mode and the user submits a non-trivial prompt, carlos
// pauses before sending and shows a short overlay with five yes/no
// questions. The user toggles checkboxes, then picks delegate or solo.
//
// Delegate prepends a one-line addendum to the user message that nudges
// the model toward sub-agent spawning; the base system prompt stays
// unchanged. Solo sends the prompt as-is. Esc cancels and returns the
// prompt to the composer.
//
// Threshold values:
//   - heuristicCharThreshold = 80   (prompt must exceed this to trigger)
//   - heuristicYesThreshold  = 3    (>= yeses default to delegate)
//
// Out of scope here: auto-spawn, persistent "don't ask again", inline
// split layout. The overlay only nudges via a per-turn sysprompt addendum.

package chat

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	heuristicCharThreshold = 80
	heuristicYesThreshold  = 3
	heuristicQuestionCount = 5
)

// heuristicAddendum is prepended to the user's message when they pick
// "delegate". The base sysprompt already has mode-aware text; this is
// the per-turn nudge.
const heuristicAddendum = "This task is suitable for orchestration. Consider spawning sub-agents for independent parts."

// heuristicQuestions are the five yes/no checks the overlay renders.
// Order is load-bearing: tests pick by index.
var heuristicQuestions = [heuristicQuestionCount]string{
	"Independent sub-tasks present?",
	"Long context required (>= ~50k tokens)?",
	"Multiple files to touch?",
	"Clearly-bounded inputs (no clarification round needed)?",
	"Estimated work exceeds 5 minutes?",
}

// shouldShowHeuristic decides whether the overlay fires for this
// submission. All conditions must hold; chained tool-result turns or
// short prompts skip straight to the model.
func shouldShowHeuristic(mode, prompt string, disabled bool) bool {
	if disabled {
		return false
	}
	if mode != "orchestrator" {
		return false
	}
	trimmed := strings.TrimSpace(prompt)
	if len(trimmed) <= heuristicCharThreshold {
		return false
	}
	return true
}

// heuristicYesCount returns the number of checked boxes.
func heuristicYesCount(checks [heuristicQuestionCount]bool) int {
	n := 0
	for _, c := range checks {
		if c {
			n++
		}
	}
	return n
}

// heuristicDefaultDelegate reports whether the default action with the
// current checks is "delegate" (true) or "solo" (false).
func heuristicDefaultDelegate(checks [heuristicQuestionCount]bool) bool {
	return heuristicYesCount(checks) >= heuristicYesThreshold
}

// renderHeuristicOverlay composes the bordered panel shown above the
// composer. width is the inner chat width minus the border allowance;
// the caller (renderInner) reserves height via the same dance every
// other overlay does.
func renderHeuristicOverlay(
	pendingPrompt string,
	checks [heuristicQuestionCount]bool,
	showHelp bool,
	width int,
) string {
	boxW := width - 2
	if boxW < 40 {
		boxW = 40
	}
	contentW := boxW - 2
	if contentW < 30 {
		contentW = 30
	}

	header := renderHeuristicHeader(contentW)
	preview := renderHeuristicPreview(pendingPrompt, contentW)
	body := renderHeuristicChecklist(checks, contentW)
	summary := renderHeuristicSummary(checks, contentW)
	footer := renderHeuristicFooter(showHelp, heuristicDefaultDelegate(checks))

	parts := []string{header, "", preview, "", body, "", summary, "", footer}
	block := strings.Join(parts, "\n")

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(boxW).
		Render(block)
}

func renderHeuristicHeader(width int) string {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	left := titleStyle.Render("delegation check")
	right := mutedStyle.Render("orchestrator mode")
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func renderHeuristicPreview(prompt string, width int) string {
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	preview := oneLine(prompt, width)
	if preview == "" {
		preview = "(empty prompt)"
	}
	return mutedStyle.Render("prompt: " + preview)
}

func renderHeuristicChecklist(checks [heuristicQuestionCount]bool, width int) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(colorAgent)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	rows := make([]string, 0, heuristicQuestionCount)
	for i, q := range heuristicQuestions {
		box := "[ ]"
		if checks[i] {
			box = "[x]"
		}
		num := keyStyle.Render(intRune(i+1) + ".")
		text := bodyStyle.Render(q)
		mark := mutedStyle.Render(box)
		if checks[i] {
			mark = keyStyle.Render(box)
		}
		row := num + " " + mark + " " + text
		rows = append(rows, row)
	}
	_ = width
	return strings.Join(rows, "\n")
}

func renderHeuristicSummary(checks [heuristicQuestionCount]bool, width int) string {
	n := heuristicYesCount(checks)
	defaultDelegate := heuristicDefaultDelegate(checks)
	countStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(colorMuted)
	defaultStyle := lipgloss.NewStyle().Foreground(colorOK).Bold(true)
	if !defaultDelegate {
		defaultStyle = lipgloss.NewStyle().Foreground(colorWarn).Bold(true)
	}
	defaultName := "solo"
	if defaultDelegate {
		defaultName = "delegate"
	}
	left := countStyle.Render(intRune(n)) + bodyStyle.Render(" of "+intRune(heuristicQuestionCount)+" favor delegation")
	right := bodyStyle.Render("default: ") + defaultStyle.Render(defaultName)
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func renderHeuristicFooter(showHelp, defaultDelegate bool) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(colorMuted)
	subtleStyle := lipgloss.NewStyle().Foreground(colorSubtle)

	if showHelp {
		help := []string{
			keyStyle.Render("1-5") + bodyStyle.Render(" toggle question"),
			keyStyle.Render("d") + bodyStyle.Render(" delegate"),
			keyStyle.Render("s") + bodyStyle.Render(" solo"),
			keyStyle.Render("enter") + bodyStyle.Render(" pick default"),
			keyStyle.Render("esc") + bodyStyle.Render(" cancel"),
			keyStyle.Render("?") + bodyStyle.Render(" hide help"),
		}
		return subtleStyle.Render("  ") + strings.Join(help, bodyStyle.Render("  ·  "))
	}

	defaultHint := "delegate"
	if !defaultDelegate {
		defaultHint = "solo"
	}
	parts := []string{
		keyStyle.Render("1-5") + bodyStyle.Render(" toggle"),
		keyStyle.Render("d") + bodyStyle.Render(" delegate"),
		keyStyle.Render("s") + bodyStyle.Render(" solo"),
		keyStyle.Render("enter") + bodyStyle.Render(" "+defaultHint),
		keyStyle.Render("esc") + bodyStyle.Render(" cancel"),
		keyStyle.Render("?") + bodyStyle.Render(" help"),
	}
	return subtleStyle.Render("  ") + strings.Join(parts, bodyStyle.Render("  ·  "))
}

// intRune turns 0..9 into a single-rune string. Helper so the inline
// number rendering doesn't need fmt.
func intRune(n int) string {
	if n < 0 || n > 9 {
		return "?"
	}
	return string(rune('0' + n))
}

// handleHeuristicKey routes one key while the heuristic overlay is
// open. Returns (newModel, cmd, handled). When handled=false the
// caller falls through to the normal Update routing.
func (m *Model) handleHeuristicKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		return m, nil, false
	case "esc":
		m.cancelHeuristic()
		return m, nil, true
	case "enter":
		if heuristicDefaultDelegate(m.heuristicChecks) {
			return m, m.heuristicCommit(true), true
		}
		return m, m.heuristicCommit(false), true
	case "d", "D":
		return m, m.heuristicCommit(true), true
	case "s", "S":
		return m, m.heuristicCommit(false), true
	case "?":
		m.heuristicHelp = !m.heuristicHelp
		m.rerenderViewport()
		return m, nil, true
	case "1", "2", "3", "4", "5":
		idx := int(msg.String()[0]-'0') - 1
		if idx >= 0 && idx < heuristicQuestionCount {
			m.heuristicChecks[idx] = !m.heuristicChecks[idx]
			m.rerenderViewport()
		}
		return m, nil, true
	}
	return m, nil, true
}

// openHeuristic stashes the pending prompt and snaps the overlay open
// with all checkboxes cleared. Submit-path callers use this when
// shouldShowHeuristic returns true.
func (m *Model) openHeuristic(prompt string) {
	m.showHeuristic = true
	m.heuristicPending = prompt
	m.heuristicChecks = [heuristicQuestionCount]bool{}
	m.heuristicHelp = false
	m.rerenderViewport()
}

// cancelHeuristic returns the pending prompt to the composer so the
// user can edit and resubmit. Idempotent.
func (m *Model) cancelHeuristic() {
	prompt := m.heuristicPending
	m.closeHeuristic()
	if prompt != "" {
		m.ta.SetValue(prompt)
		m.ta.CursorEnd()
	}
}

// closeHeuristic resets the overlay state without re-populating the
// composer. Used by delegate/solo commit and the session-disable path.
func (m *Model) closeHeuristic() {
	m.showHeuristic = false
	m.heuristicPending = ""
	m.heuristicChecks = [heuristicQuestionCount]bool{}
	m.heuristicHelp = false
	m.rerenderViewport()
}

// heuristicCommit closes the overlay and dispatches the pending prompt.
// When delegate is true the addendum is prepended; otherwise the prompt
// is sent unchanged.
func (m *Model) heuristicCommit(delegate bool) tea.Cmd {
	prompt := m.heuristicPending
	m.closeHeuristic()
	if prompt == "" {
		return nil
	}
	if delegate {
		return m.appendUserMessage(heuristicAddendum + "\n\n" + prompt)
	}
	return m.appendUserMessage(prompt)
}
