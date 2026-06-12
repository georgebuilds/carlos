package manage

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// View composes the manage TUI inside a rounded accent-colored outer
// border, matching the chat surface. v0.7.4 ground-up redesign:
//
//   - Roster pane renders agents as bordered "button" cards stacked
//     vertically. Selection flips the focused card to a thick-bordered
//     reverse-video fill so the cursor position is unmistakable.
//   - Detail pane (right) is a structured info panel: title card,
//     stats grid, then a scrollable activity transcript below.
//   - The outer border math matches chat's recipe (Width(w-2) +
//     Height(h-2)) so the top edge renders reliably across Ghostty,
//     iTerm, and Kitty.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	w, h := m.width, m.height
	if w == 0 || h == 0 {
		w, h = 120, 40
	}
	if w < minTermWidth || h < minTermHeight {
		return lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("carlos manage needs at least %dx%d. Current: %dx%d.",
				minTermWidth, minTermHeight, w, h))
	}

	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Width(w-2).
		Height(h-2).
		Padding(0, 1)

	inner := m.renderInner(border.GetWidth()-2, border.GetHeight())
	return border.Render(inner)
}

func (m *Model) renderInner(innerW, innerH int) string {
	header := m.renderHeader(innerW)
	footer := m.renderFooter(innerW)
	topRule := renderHRule(innerW)
	bottomRule := renderHRule(innerW)

	headerH := lipgloss.Height(header)
	footerH := lipgloss.Height(footer)
	ruleH := lipgloss.Height(topRule)

	bodyH := innerH - headerH - footerH - 2*ruleH
	if bodyH < cardLines {
		bodyH = cardLines
	}

	// Slice 4h approval pane takes over the body when active.
	if m.view == viewApprovals {
		body := m.approvals.render(innerW, bodyH)
		overlay := m.renderOverlay(innerW)
		parts := []string{header, topRule, body, bottomRule}
		if overlay != "" {
			parts = append(parts, overlay)
		}
		parts = append(parts, footer)
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}

	rosterW := rosterPaneWidth(innerW)
	focusW := innerW - rosterW - 3
	if focusW < 30 {
		focusW = 30
	}

	// Track viewport for the activity transcript portion of the
	// focus pane. The detail header eats a chunk of the right column;
	// the viewport gets what's left.
	detailHeaderH := focusDetailHeaderH
	activityH := bodyH - detailHeaderH - 1 // -1 for the section rule
	if activityH < 3 {
		activityH = 3
	}
	m.focus.Resize(focusW-2, activityH) // -2 for the card border

	// Roster window math: visible card slots.
	m.win.Total = len(m.rosterRows)
	cardCap := bodyH / cardLines
	if cardCap < 1 {
		cardCap = 1
	}
	if m.win.Visible != cardCap {
		m.win.Visible = cardCap
		m.win = m.win.Clamp()
	}

	rosterRows := m.populateSparklines(m.rosterRows)

	rosterPane := renderRoster(rosterRows, rosterRenderOptions{
		width:     rosterW,
		height:    bodyH,
		focusID:   m.focus.AgentID(),
		cursorIdx: m.cursor,
		scroll:    m.win.Top,
		maxDepth:  defaultMaxDepth,
	})

	focusPane := m.renderFocusPane(focusW, bodyH)

	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Width(rosterW).Render(rosterPane),
		" ",
		lipgloss.NewStyle().Width(focusW).Render(focusPane),
	)

	overlay := m.renderOverlay(innerW)
	parts := []string{header, topRule, body, bottomRule}
	if overlay != "" {
		parts = append(parts, overlay)
	}
	parts = append(parts, footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// focusDetailHeaderH is the line budget for the detail-pane header
// card (title + stats grid + intent block). Kept as a constant so the
// activity viewport height calculation is trivial.
const focusDetailHeaderH = 10

// renderHRule paints a single-row horizontal rule.
func renderHRule(w int) string {
	if w < 1 {
		w = 1
	}
	return lipgloss.NewStyle().Foreground(colorSubtle).Render(strings.Repeat("─", w))
}

// renderHeader is the top bar.
func (m *Model) renderHeader(w int) string {
	brand := lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render("carlos")
	sep := lipgloss.NewStyle().Foreground(colorMuted).Render(" · ")
	mode := lipgloss.NewStyle().Foreground(colorAccent).Render("agents")

	count := lipgloss.NewStyle().Foreground(colorMuted).Render(
		fmt.Sprintf("%d agent%s", len(m.rosterRows), plural(len(m.rosterRows))),
	)
	sortStr := lipgloss.NewStyle().Foreground(colorSubtle).Render(
		"sort: " + m.sortKey.String() + sortDirGlyph(m.sortAsc),
	)

	left := brand + sep + mode + sep + count + sep + sortStr

	if reporter, ok := m.sup.(ModeReporter); ok {
		chip := lipgloss.NewStyle().Foreground(colorSubtle).Render(
			fmt.Sprintf("mode=%s (cap %d)", reporter.Mode(), reporter.SpawnCap()),
		)
		left += sep + chip
	}

	if m.filter.Active() {
		chip := lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true).
			Render("filter: " + m.filter.Query)
		left += sep + chip
	}

	right := ""
	if m.rosterRefreshErr != "" {
		right = lipgloss.NewStyle().Foreground(colorErr).Render(
			"snapshot err: " + truncate(m.rosterRefreshErr, 40))
	}

	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// renderFocusPane composes the right-hand detail panel: header card
// with title + stats grid, a section rule, then the activity viewport
// underneath. When no agent is selected (focus unbound) the pane shows
// a hint and pads to the same height so the rest of the layout
// doesn't shift.
func (m *Model) renderFocusPane(w, h int) string {
	id := m.focus.AgentID()
	if id == "" {
		hint := lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render(
			"select an agent (↑/↓ then enter) to see its detail")
		card := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSubtle).
			Padding(1, 2).
			Width(w-2).
			Height(h-2).
			Align(lipgloss.Center, lipgloss.Center).
			Render(hint)
		return card
	}

	row, ok := m.findRow(id)
	if !ok {
		return lipgloss.NewStyle().Foreground(colorMuted).Render(
			"focused agent " + shortID(id) + " no longer in projection")
	}

	header := m.renderFocusDetailHeader(row, w)
	rule := lipgloss.NewStyle().Foreground(colorSubtle).Render(
		strings.Repeat("─", w-2),
	)
	activityLabel := lipgloss.NewStyle().
		Foreground(colorMuted).
		Bold(true).
		Render(" activity ")
	activity := m.focus.View()

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		rule,
		activityLabel,
		activity,
	)
}

// renderFocusDetailHeader is the rich info card at the top of the
// focus pane. Layout:
//
//	┌─ 01KTP1RQ  ● running ──────────────────╮
//	│ chat with you about whatever this is   │
//	│                                        │
//	│ tokens  123 in · 456 out               │
//	│ cost    $0.04                          │
//	│ elapsed 1m11s                          │
//	│ model   google/gemini-3.5-flash        │
//	│ parent  01KTKMC9                       │
//	╰────────────────────────────────────────╯
//
// Heights are fixed (cardW × focusDetailHeaderH) so the activity
// viewport math below it stays trivial.
func (m *Model) renderFocusDetailHeader(row agent.AgentRow, paneW int) string {
	innerW := paneW - 4
	if innerW < 16 {
		innerW = 16
	}

	idLabel := lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render(shortID(row.ID))
	stateText := "[" + theme.StateGlyph(row.State) + " " + row.State.String() + "]"
	titleLine := idLabel + "  " + lipgloss.NewStyle().Foreground(stateColor(row.State)).Render(stateText)

	intent := row.Title
	if intent == "" {
		intent = "(no intent recorded)"
	}
	intent = truncate(intent, innerW)
	intentLine := lipgloss.NewStyle().Foreground(colorAgent).Render(intent)

	tokens := fmt.Sprintf("%s in · %s out", formatTokens(row.TokensIn), formatTokens(row.TokensOut))
	cost := formatCost(row.CostCents)
	elapsed := formatElapsed(nowFunc().Sub(row.CreatedAt))
	model := row.Model
	if model == "" {
		model = "(unset)"
	}
	parent := row.ParentID
	if parent == "" {
		parent = "(root)"
	} else {
		parent = shortID(parent)
	}

	stats := renderStatsGrid(innerW, [][2]string{
		{"tokens", tokens},
		{"cost", cost},
		{"elapsed", elapsed},
		{"model", truncate(model, innerW-9)},
		{"parent", parent},
	})

	body := lipgloss.JoinVertical(lipgloss.Left,
		padCellsToWidth(titleLine, innerW),
		padCellsToWidth(intentLine, innerW),
		stats,
	)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAgent).
		Padding(0, 1).
		Width(paneW - 2).
		Render(body)
}

// renderStatsGrid lays out a two-column label/value table. Labels are
// left-justified in a 9-char gutter; values fill the remaining width.
// Each row is padded to innerW so the surrounding card border draws
// cleanly when lipgloss right-pads the card.
func renderStatsGrid(innerW int, rows [][2]string) string {
	labelW := 9
	valueW := innerW - labelW
	if valueW < 1 {
		valueW = 1
	}
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		label := lipgloss.NewStyle().Foreground(colorMuted).Render(padRight(r[0], labelW))
		value := lipgloss.NewStyle().Foreground(colorSubtle).Render(truncate(r[1], valueW))
		line := label + value
		lines = append(lines, padCellsToWidth(line, innerW))
	}
	return strings.Join(lines, "\n")
}

// renderFooter is the keybind row.
func (m *Model) renderFooter(w int) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(colorMuted)

	var keybinds string

	if m.view == viewApprovals {
		left := keyStyle.Render("y") + hintStyle.Render(" accept  ") +
			keyStyle.Render("r") + hintStyle.Render(" reject  ") +
			keyStyle.Render("R") + hintStyle.Render(" refresh")
		right := keyStyle.Render("↑/↓") + hintStyle.Render(" select  ") +
			keyStyle.Render("A/esc") + hintStyle.Render(" back  ") +
			keyStyle.Render("q") + hintStyle.Render(" quit")
		gap := w - lipgloss.Width(left) - lipgloss.Width(right)
		if gap < 1 {
			gap = 1
		}
		keybinds = left + strings.Repeat(" ", gap) + right
	} else {
		verbs := keyStyle.Render("s") + hintStyle.Render(" steer  ") +
			keyStyle.Render("i") + hintStyle.Render(" interrupt  ") +
			keyStyle.Render("x") + hintStyle.Render(" stop")

		nav := keyStyle.Render("↑/↓") + hintStyle.Render(" select  ") +
			keyStyle.Render("enter") + hintStyle.Render(" focus  ") +
			keyStyle.Render("/") + hintStyle.Render(" filter  ") +
			keyStyle.Render("1-5") + hintStyle.Render(" sort  ") +
			keyStyle.Render("A") + hintStyle.Render(" approvals  ") +
			keyStyle.Render("q") + hintStyle.Render(" quit")

		left := verbs
		right := nav
		gap := w - lipgloss.Width(left) - lipgloss.Width(right)
		if gap < 1 {
			gap = 1
		}
		keybinds = left + strings.Repeat(" ", gap) + right
	}

	if m.status == "" {
		return keybinds
	}
	statusStyle := lipgloss.NewStyle().Foreground(colorAccent).Italic(true)
	if strings.Contains(strings.ToLower(m.status), "not implemented") ||
		strings.Contains(strings.ToLower(m.status), "no supervisor") {
		statusStyle = lipgloss.NewStyle().Foreground(colorWarn)
	}
	return statusStyle.Render(m.status) + "\n" + keybinds
}

// renderOverlay returns the active overlay's prompt + textinput.
func (m *Model) renderOverlay(w int) string {
	if m.overlay == overlayNone {
		return ""
	}
	id := m.selectedID()
	var intent string
	if row, ok := m.findRow(id); ok {
		intent = row.Title
	}
	prompt := overlayPromptLabel(m.overlay, intent)
	style := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	switch m.overlay {
	case overlayInterruptConfirm, overlayStopConfirm:
		return style.Render(prompt) +
			lipgloss.NewStyle().Foreground(colorMuted).Render("(esc to cancel)")
	default:
		m.input.Width = w - lipgloss.Width(prompt) - 4
		if m.input.Width < 10 {
			m.input.Width = 10
		}
		return style.Render(prompt) + m.input.View()
	}
}

// populateSparklines decorates the rendered rows with sparkline +
// elapsed time, computing the focused agent's spark from the live
// token ring.
func (m *Model) populateSparklines(rows []rosterRow) []rosterRow {
	now := nowFunc()
	out := make([]rosterRow, len(rows))
	for i, rr := range rows {
		rr.elapsed = now.Sub(rr.row.CreatedAt)
		if rr.row.ID == m.focus.AgentID() {
			rr.spark = RenderSparkline(m.focus.Ring(), rr.row.State)
		}
		out[i] = rr
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func sortDirGlyph(asc bool) string {
	if asc {
		return " ↑"
	}
	return " ↓"
}

// findRow returns the agent.AgentRow for id in the most-recent
// snapshot, or false. Linear scan.
func (m *Model) findRow(id string) (agent.AgentRow, bool) {
	for _, r := range m.rawRows {
		if r.ID == id {
			return r, true
		}
	}
	return agent.AgentRow{}, false
}
