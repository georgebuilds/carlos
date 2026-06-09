package manage

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// View composes the manage TUI:
//
//	┌─ carlos · manage · N agents ──────────────────────────────────┐
//	│ roster (40% width)                │ focus pane                │
//	│ ...                                                            │
//	│                                                                │
//	├────────────────────────────────────────────────────────────────┤
//	│ s steer  i interrupt  x stop   /  filter  enter focus  q quit │
//	└────────────────────────────────────────────────────────────────┘
//
// Below the minimum terminal size we refuse to render - same posture
// as onboarding + chat.
// View renders the manage TUI. v0.7.2 swap: the previous outer
// rounded border was the source of the persistent "top border
// clipped under Ghostty tabs" reports. Two compounding causes
// drove that bug: (a) alt-screen content slightly taller than the
// reported viewport scrolls, hiding row 0, and (b) tabbed
// terminals overlay their own chrome on row 0 of the alt-screen.
// Both interact badly with a top-edge `─` glyph.
//
// Modern TUI projects in this space (lazygit, k9s, gh dashboard,
// btop) drop the outer frame entirely; the terminal IS the frame.
// We mirror that: status bar at the top, body in the middle, hint
// row at the bottom, with thin horizontal rules between sections
// instead of a heavy outer box. There's no top `─` anymore, so the
// Ghostty clip is a non-event by construction.
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
	return m.renderInner(w, h)
}

func (m *Model) renderInner(innerW, innerH int) string {
	header := m.renderHeader(innerW)
	footer := m.renderFooter(innerW)
	topRule := renderHRule(innerW)
	bottomRule := renderHRule(innerW)

	headerH := lipgloss.Height(header)
	footerH := lipgloss.Height(footer)
	ruleH := lipgloss.Height(topRule) // both rules are 1 row by design

	// Body gets everything left over after the four chrome rows
	// (header + top rule + bottom rule + footer). Floor at 4 so
	// extremely short windows still render something usable.
	bodyH := innerH - headerH - footerH - 2*ruleH
	if bodyH < 4 {
		bodyH = 4
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
	if focusW < 20 {
		focusW = 20
	}

	// Mutate the focus pane's viewport to track the body box. The
	// orchestrator's relayout already does this on resize; we redo
	// here so a transient render before WindowSizeMsg still works.
	m.focus.Resize(focusW, bodyH-1)

	// Roster window: total + visible may have drifted since the last
	// snapshot; clamp before render.
	m.win.Total = len(m.rosterRows)
	if m.win.Visible != bodyH-1 {
		m.win.Visible = bodyH - 1
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

	divider := lipgloss.NewStyle().Foreground(colorSubtle).Render(
		strings.Repeat("│\n", bodyH),
	)

	focusHeader := m.renderFocusHeader(focusW)
	focusBody := m.focus.View()
	focusPane := lipgloss.JoinVertical(lipgloss.Left, focusHeader, focusBody)

	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Width(rosterW).Render(rosterPane),
		divider,
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

// renderHRule paints a single-row horizontal rule using the box-
// drawing light horizontal glyph in the subtle palette slot. Used
// as the section divider above the body and above the footer.
// Modern TUI surfaces (lazygit, k9s, btop) lean on thin rules
// instead of bordered boxes to chunk vertical space; we follow
// suit so the chrome stays out of the way of the data.
func renderHRule(w int) string {
	if w < 1 {
		w = 1
	}
	return lipgloss.NewStyle().Foreground(colorSubtle).Render(strings.Repeat("─", w))
}

// renderHeader is the top bar: brand, agent count, filter chip,
// sort indicator. When the wired VerbDispatcher implements ModeReporter
// we append a "mode=X (cap N)" chip so the operator can see at a glance
// which orchestrator mode is gating new Spawn calls.
func (m *Model) renderHeader(w int) string {
	brand := lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render("carlos")
	sep := lipgloss.NewStyle().Foreground(colorMuted).Render(" · ")
	mode := lipgloss.NewStyle().Foreground(colorAccent).Render("manage")

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

// renderFocusHeader labels the focus pane with the bound agent's
// shortened ID + state badge. Empty when nothing is bound.
func (m *Model) renderFocusHeader(w int) string {
	id := m.focus.AgentID()
	if id == "" {
		return lipgloss.NewStyle().
			Foreground(colorMuted).
			Render("focus: (none - enter on a row)")
	}
	row, ok := m.findRow(id)
	if !ok {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("focus: " + shortID(id))
	}
	idStyle := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	return idStyle.Render(shortID(id)) + " " + stateBadge(row.State) + " " +
		lipgloss.NewStyle().Foreground(colorMuted).Render("· "+row.Title)
}

// renderFooter is the keybind row, always shown in brand accent so
// the three verbs are discoverable. Status echo (verb result, errors)
// sits above the keybind row when present.
func (m *Model) renderFooter(w int) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(colorMuted)

	var keybinds string

	if m.view == viewApprovals {
		// Approval pane vocabulary is narrower + different.
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
		// Roster (default) vocabulary.
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

// renderOverlay returns the active overlay's prompt + textinput, or
// "" when no overlay is active.
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
		// Confirm prompts don't need the textinput - just the
		// y/N prompt.
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
// token ring. Non-focused rows show an empty placeholder so the
// column stays consistent.
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

// plural returns "s" when n != 1, "" otherwise. Used by the header
// count cell ("3 agents" vs "1 agent").
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// sortDirGlyph returns a tiny ASCII arrow indicating sort direction.
func sortDirGlyph(asc bool) string {
	if asc {
		return " ↑"
	}
	return " ↓"
}

// findRow returns the agent.AgentRow for id in the most-recent
// snapshot, or false. Linear scan - the projection is ~tens of rows.
func (m *Model) findRow(id string) (agent.AgentRow, bool) {
	for _, r := range m.rawRows {
		if r.ID == id {
			return r, true
		}
	}
	return agent.AgentRow{}, false
}
