package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/usershell"
)

// Jobs overlay (Phase U S6).
//
// Composite palette panel modeled on Crush's command palette (per the
// TUI research note §3 "Command palette"): grouped sections, focused
// row in accent, always-visible footer keybinds, "/" filter.
//
// Sections, in priority order:
//
//   - Running  - fg slot, accent ▶
//   - Queued   - pending fg, dim ▸
//   - Background - running bg, ⬡
//   - Recent   - terminal states (done/failed/cancelled), badge per state
//
// Keys (handled in chat.go Update):
//
//   - ↑/↓ (or j/k)  navigate
//   - enter         foreground the highlighted job (no-op for already-fg)
//   - d             cancel the highlighted job
//   - c             clear all terminal-state entries from the manager
//                   (not yet implemented - needs Manager.ClearTerminal())
//   - /             filter
//   - esc           dismiss

// jobsRow is the flattened entry the overlay's cursor walks. Built
// fresh per render from manager.Jobs() so a state change is reflected
// on the very next frame without per-event bookkeeping in the model.
type jobsRow struct {
	section jobsSection
	snap    usershell.Snapshot
}

type jobsSection int

const (
	jobsSectionRunning jobsSection = iota
	jobsSectionQueued
	jobsSectionBackground
	jobsSectionRecent
)

func (s jobsSection) String() string {
	switch s {
	case jobsSectionRunning:
		return "Running"
	case jobsSectionQueued:
		return "Queued"
	case jobsSectionBackground:
		return "Background"
	case jobsSectionRecent:
		return "Recent"
	}
	return ""
}

// buildJobsRows flattens a Manager.Jobs() snapshot into the ordered
// row list the overlay's cursor walks. Section ordering matches the
// header order so the cursor's index aligns with what the user sees.
//
// filter narrows the rows via case-insensitive substring match across
// jobID + command. Empty filter shows everything.
func buildJobsRows(jobs []usershell.Snapshot, filter string) []jobsRow {
	var running, queued, bg, recent []jobsRow
	for _, s := range jobs {
		row := jobsRow{snap: s}
		switch {
		case s.State == usershell.StateRunning && s.Backgrounded:
			row.section = jobsSectionBackground
			bg = append(bg, row)
		case s.State == usershell.StateRunning:
			row.section = jobsSectionRunning
			running = append(running, row)
		case s.State == usershell.StatePending:
			row.section = jobsSectionQueued
			queued = append(queued, row)
		default: // terminal
			row.section = jobsSectionRecent
			recent = append(recent, row)
		}
	}
	out := make([]jobsRow, 0, len(jobs))
	out = append(out, running...)
	out = append(out, queued...)
	out = append(out, bg...)
	out = append(out, recent...)
	if filter == "" {
		return out
	}
	q := strings.ToLower(strings.TrimSpace(filter))
	filtered := out[:0]
	for _, r := range out {
		hay := strings.ToLower(r.snap.ID + " " + r.snap.Command)
		if strings.Contains(hay, q) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// renderJobsOverlay produces the bordered palette panel. Mirrors the
// help overlay's shape (rounded border + accent foreground + padding)
// for visual consistency.
func renderJobsOverlay(jobs []usershell.Snapshot, filter string, filterMode bool, cursor, innerW int) string {
	boxW := innerW - 2
	if boxW < 40 {
		boxW = 40
	}

	headerStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	sectionStyle := lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(colorMuted)
	subtleStyle := lipgloss.NewStyle().Foreground(colorSubtle)

	rows := buildJobsRows(jobs, filter)
	var sb strings.Builder
	sb.WriteString(headerStyle.Render("Shell jobs"))
	sb.WriteString("  ")
	sb.WriteString(subtleStyle.Render(fmt.Sprintf("%d total", len(rows))))
	sb.WriteString("\n")

	if filterMode || filter != "" {
		caret := ""
		if filterMode {
			caret = lipgloss.NewStyle().Foreground(colorAccent).Render("▎")
		}
		sb.WriteString(bodyStyle.Render("filter: "))
		sb.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Render(filter))
		sb.WriteString(caret)
		sb.WriteString("\n")
	}

	if len(rows) == 0 {
		sb.WriteString("\n")
		if filter != "" {
			sb.WriteString(subtleStyle.Render(
				"(no matches - backspace clears the filter)"))
		} else {
			sb.WriteString(subtleStyle.Render(
				"no jobs - type `!<cmd>` in the composer to start one"))
		}
		sb.WriteString("\n")
		footer := renderJobsFooter(false)
		sb.WriteString(footer)
		return wrapJobsBox(sb.String(), boxW)
	}

	// Walk rows, inserting a section header when section changes.
	lastSection := jobsSection(-1)
	for i, r := range rows {
		if r.section != lastSection {
			if lastSection != jobsSection(-1) {
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
			sb.WriteString(sectionStyle.Render(r.section.String()))
			sb.WriteString("\n")
			lastSection = r.section
		}
		sb.WriteString(renderJobsRow(r, i == cursor, boxW-4))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(renderJobsFooter(true))

	return wrapJobsBox(sb.String(), boxW)
}

// wrapJobsBox applies the rounded accent border around the body.
func wrapJobsBox(body string, boxW int) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(boxW).
		Render(body)
}

// renderJobsRow formats one job line. Focused rows get the accent
// foreground + ▸ marker; unfocused rows in muted gray.
func renderJobsRow(r jobsRow, focused bool, contentW int) string {
	bodyStyle := lipgloss.NewStyle().Foreground(colorMuted)
	jobStyle := lipgloss.NewStyle().Foreground(colorAccent)
	if focused {
		bodyStyle = lipgloss.NewStyle().Foreground(colorAccent)
		jobStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	}

	marker := "  "
	if focused {
		marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
	}

	glyph := jobsRowGlyph(r)
	id := jobStyle.Render("j" + shortID(r.snap.ID))
	cmd := truncateOneLine(r.snap.Command, contentW/2)
	dur := formatRowDuration(r.snap)
	tail := bodyStyle.Render(fmt.Sprintf(" · %s", dur))

	return marker + glyph + " " + id + "  " + bodyStyle.Render(cmd) + tail
}

// jobsRowGlyph returns the status emoji-ish marker per row. Kept to
// pure-ASCII / basic Unicode so the overlay reads in any terminal
// (per the TUI research's "no Nerd Font as load-bearing UI" rule).
func jobsRowGlyph(r jobsRow) string {
	switch r.section {
	case jobsSectionRunning:
		return lipgloss.NewStyle().Foreground(colorAccent).Render("▶")
	case jobsSectionQueued:
		return lipgloss.NewStyle().Foreground(colorMuted).Render("▸")
	case jobsSectionBackground:
		return lipgloss.NewStyle().Foreground(colorMuted).Render("⬡")
	case jobsSectionRecent:
		switch {
		case r.snap.State == usershell.StateCancelled:
			return lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render("✗")
		case r.snap.ExitCode != 0:
			return lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render("✗")
		default:
			return lipgloss.NewStyle().Foreground(colorOK).Bold(true).Render("✓")
		}
	}
	return " "
}

// formatRowDuration emits a duration suitable for the row's trailing
// metadata. Running jobs show "running 4.2s"; terminal jobs show the
// final duration. Sub-second durations on terminal jobs read as
// "<1s" so we don't clutter every row with "0.3s".
func formatRowDuration(s usershell.Snapshot) string {
	d := s.Duration()
	switch s.State {
	case usershell.StateRunning:
		return "running " + formatDuration(d)
	case usershell.StatePending:
		return "queued"
	default:
		if d < time.Second {
			return "<1s"
		}
		return formatDuration(d)
	}
}

// renderJobsFooter is the always-visible keybind row at the bottom of
// the overlay. Matches the TUI research's discoverability principle.
func renderJobsFooter(haveRows bool) string {
	key := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	body := lipgloss.NewStyle().Foreground(colorMuted)
	parts := []string{
		key.Render("↑/↓") + body.Render(" nav"),
		key.Render("/") + body.Render(" filter"),
		key.Render("esc") + body.Render(" close"),
	}
	if haveRows {
		parts = append([]string{
			key.Render("enter") + body.Render(" fg"),
			key.Render("d") + body.Render(" cancel"),
		}, parts...)
	}
	return body.Render("  ") + strings.Join(parts, body.Render("  ·  "))
}

// handleJobsOverlayKey processes one key while the jobs overlay is
// open. Returns (newModel, cmd, handled). When handled=false the
// caller falls through to the normal Update routing.
//
// Filter-mode is a tiny sub-REPL: typing accumulates into
// m.jobsFilter, esc exits filter without closing the overlay, enter
// commits the highlighted match (same as enter outside filter mode),
// backspace edits the filter buffer.
func (m *Model) handleJobsOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.jobsFilterMode {
		switch msg.Type {
		case tea.KeyEsc:
			m.jobsFilterMode = false
			m.rerenderViewport()
			return m, nil, true
		case tea.KeyEnter:
			m.jobsFilterMode = false
			return m.commitJobsOverlay()
		case tea.KeyBackspace:
			if len(m.jobsFilter) > 0 {
				m.jobsFilter = m.jobsFilter[:len(m.jobsFilter)-1]
				m.jobsCursor = 0
				m.rerenderViewport()
			}
			return m, nil, true
		case tea.KeyRunes:
			m.jobsFilter += string(msg.Runes)
			m.jobsCursor = 0
			m.rerenderViewport()
			return m, nil, true
		case tea.KeySpace:
			m.jobsFilter += " "
			m.jobsCursor = 0
			m.rerenderViewport()
			return m, nil, true
		}
		return m, nil, true
	}

	switch msg.String() {
	case "ctrl+c":
		// Don't swallow ctrl+c - the user can still quit even with
		// the overlay open. Fall through.
		return m, nil, false
	case "esc", "ctrl+j":
		m.showJobs = false
		m.jobsFilter = ""
		m.jobsFilterMode = false
		m.rerenderViewport()
		return m, nil, true
	case "up", "k":
		if m.jobsCursor > 0 {
			m.jobsCursor--
			m.rerenderViewport()
		}
		return m, nil, true
	case "down", "j":
		rows := buildJobsRows(m.usershell.Jobs(), m.jobsFilter)
		if m.jobsCursor < len(rows)-1 {
			m.jobsCursor++
			m.rerenderViewport()
		}
		return m, nil, true
	case "g":
		m.jobsCursor = 0
		m.rerenderViewport()
		return m, nil, true
	case "G":
		rows := buildJobsRows(m.usershell.Jobs(), m.jobsFilter)
		if len(rows) > 0 {
			m.jobsCursor = len(rows) - 1
			m.rerenderViewport()
		}
		return m, nil, true
	case "/":
		m.jobsFilterMode = true
		return m, nil, true
	case "enter":
		return m.commitJobsOverlay()
	case "d":
		return m.cancelHighlightedJob()
	}
	return m, nil, true // overlay swallows unknown keys to avoid stray textarea input
}

// commitJobsOverlay foregrounds the highlighted job. If the
// highlighted job is already in the fg slot or is in a terminal
// state, this is a no-op + status echo.
func (m *Model) commitJobsOverlay() (tea.Model, tea.Cmd, bool) {
	rows := buildJobsRows(m.usershell.Jobs(), m.jobsFilter)
	if len(rows) == 0 || m.jobsCursor >= len(rows) {
		return m, nil, true
	}
	row := rows[m.jobsCursor]
	mgr := m.usershell
	id := row.snap.ID
	switch row.section {
	case jobsSectionBackground:
		return m, func() tea.Msg {
			if err := mgr.Foreground(id); err != nil {
				return statusMsg{
					text: fmt.Sprintf("shell: fg %s: %v", shortID(id), err),
					kind: statusWarn,
				}
			}
			return statusMsg{
				text: fmt.Sprintf("shell: j%s moved to foreground", shortID(id)),
				kind: statusInfo,
			}
		}, true
	case jobsSectionQueued:
		return m, func() tea.Msg {
			return statusMsg{
				text: fmt.Sprintf("shell: j%s is queued - wait for fg slot to free", shortID(id)),
				kind: statusInfo,
			}
		}, true
	case jobsSectionRunning:
		return m, func() tea.Msg {
			return statusMsg{
				text: fmt.Sprintf("shell: j%s is already foreground", shortID(id)),
				kind: statusInfo,
			}
		}, true
	default:
		// Recent/terminal - show output? For v1, just echo.
		return m, func() tea.Msg {
			return statusMsg{
				text: fmt.Sprintf("shell: j%s already completed (exit %d)", shortID(id), row.snap.ExitCode),
				kind: statusInfo,
			}
		}, true
	}
}

// cancelHighlightedJob asks the Manager to cancel the highlighted
// row's job. No-op for terminal-state rows.
func (m *Model) cancelHighlightedJob() (tea.Model, tea.Cmd, bool) {
	rows := buildJobsRows(m.usershell.Jobs(), m.jobsFilter)
	if len(rows) == 0 || m.jobsCursor >= len(rows) {
		return m, nil, true
	}
	row := rows[m.jobsCursor]
	if row.snap.State.IsTerminal() {
		return m, func() tea.Msg {
			return statusMsg{
				text: "shell: that job is already complete",
				kind: statusInfo,
			}
		}, true
	}
	mgr := m.usershell
	id := row.snap.ID
	return m, func() tea.Msg {
		// Cancel doesn't need a ctx today (Manager owns one per
		// job); pass a generous deadline for any future expansion.
		_ = context.Background()
		if err := mgr.Cancel(id); err != nil {
			return statusMsg{
				text: fmt.Sprintf("shell: cancel %s: %v", shortID(id), err),
				kind: statusWarn,
			}
		}
		return statusMsg{
			text: fmt.Sprintf("shell: cancelled j%s", shortID(id)),
			kind: statusInfo,
		}
	}, true
}
