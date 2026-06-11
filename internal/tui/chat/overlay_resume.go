// overlay_resume.go - the /resume session picker.
//
// /resume opens a takeover overlay that lists every top-level chat
// session in the SQLite event log as a bordered card, styled after
// the manage-view roster cards: rounded border + subtle/agent/accent
// progressions for idle/focused/cursor states. ↑↓ navigates, Enter
// returns the picked session id back to the runtime via the new
// ResumeRequested() getter, Esc dismisses.
//
// Why a picker and not a slash echo: the user manually copy-pasting a
// 26-char ULID from a status line is hostile UX. Cards expose enough
// context (last user message preview, model, age) for the user to
// pick the right thread without scrolling through history. Reuses the
// shortID + stateBadge helpers already in this package; no new
// styling tokens added.

package chat

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// resumeSession is the rendered shape for one row of the picker. We
// keep the field set tiny (no Title or extra metadata) so the card
// layout stays consistent with what the user actually scans for:
// "what was I last talking about, and how old is the thread".
type resumeSession struct {
	ID        string
	Model     string
	State     agent.State
	UpdatedAt time.Time
	Preview   string
	UserMsgs  int
}

// openResumePicker loads the user-session list off the chat's event
// log + flips the overlay flag. Excludes the current session (the
// one already being viewed) since "resume the session I'm in" is a
// no-op. Errors loading the list surface as a status echo so the user
// sees what went wrong without crashing into the picker shell.
func (m *Model) openResumePicker() tea.Cmd {
	log, ok := m.log.(*agent.SQLiteEventLog)
	if !ok {
		return statusCmd("/resume: only available with SQLite event log", statusWarn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Best-effort prune before listing: a user opening /resume is
	// exactly the moment they care about a clean picker. The user
	// can resume from a session that pre-dates the grace window
	// even if the same row would have been pruned, because the
	// only candidates are zero-message + zero-tool orphans — there
	// is nothing in those rows to resume into. Errors go to the
	// best-effort diag writer (io.Discard by default — never stderr,
	// which would corrupt the live frame) and never block the picker.
	if _, err := log.DeleteEmptyOrphanedAgents(ctx, agent.DefaultOrphanPruneAge); err != nil {
		w := m.diag
		if w == nil {
			w = io.Discard
		}
		fmt.Fprintf(w, "carlos: prune empty orphans: %v\n", err)
	}
	sessions, err := agent.ListUserSessions(ctx, log, m.agentID)
	if err != nil {
		return statusCmd("/resume: list failed: "+err.Error(), statusWarn)
	}
	if len(sessions) == 0 {
		return statusCmd("/resume: no other sessions to resume", statusInfo)
	}
	m.resumeSessions = make([]resumeSession, len(sessions))
	for i, s := range sessions {
		m.resumeSessions[i] = resumeSession{
			ID:        s.ID,
			Model:     s.Model,
			State:     s.State,
			UpdatedAt: s.UpdatedAt,
			Preview:   s.Preview,
			UserMsgs:  s.UserMsgs,
		}
	}
	m.showResume = true
	m.resumeCursor = 0
	m.rerenderViewport()
	return nil
}

// closeResumePicker returns to the underlying chat without picking.
// Esc-bound; mirrors closeFrameSwitcher's shape so the overlay state
// machine reads as uniform.
func (m *Model) closeResumePicker() {
	m.showResume = false
	m.resumeSessions = nil
	m.resumeCursor = 0
	m.rerenderViewport()
}

// handleResumeKey is the picker's key router. Enter commits the
// picked session id by stashing it on m.resumeSelected + quitting
// the chat program so the outer runtime loop (cmd/carlos/runtime_tui)
// can swap in a new chat.New(...) against the chosen agent id.
func (m *Model) handleResumeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		return m, nil, false
	case "esc":
		m.closeResumePicker()
		return m, nil, true
	case "up", "k":
		if len(m.resumeSessions) == 0 {
			return m, nil, true
		}
		m.resumeCursor--
		if m.resumeCursor < 0 {
			m.resumeCursor = len(m.resumeSessions) - 1
		}
		m.rerenderViewport()
		return m, nil, true
	case "down", "j":
		if len(m.resumeSessions) == 0 {
			return m, nil, true
		}
		m.resumeCursor++
		if m.resumeCursor >= len(m.resumeSessions) {
			m.resumeCursor = 0
		}
		m.rerenderViewport()
		return m, nil, true
	case "enter":
		if len(m.resumeSessions) == 0 || m.resumeCursor < 0 ||
			m.resumeCursor >= len(m.resumeSessions) {
			m.closeResumePicker()
			return m, nil, true
		}
		picked := m.resumeSessions[m.resumeCursor].ID
		m.resumeSelected = picked
		m.quitting = true
		if m.subCancel != nil {
			m.subCancel()
		}
		return m, tea.Quit, true
	}
	return m, nil, true
}

// ResumeRequested returns the session id the user picked, or "" when
// the chat exited for any other reason. The runtime entry-point
// (cmd/carlos/runtime_tui.go) checks this after chat.Run() returns
// and, when non-empty, reseeds the loop with the new agent id.
func (m *Model) ResumeRequested() string { return m.resumeSelected }

// renderResumeOverlay paints the picker as a stack of bordered cards
// inside the outer chat box. innerW + innerH come from the same
// renderInner math the frame switcher uses, so the overlay sits
// inside the same padded inner area.
func renderResumeOverlay(m *Model, innerW, innerH int) string {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)

	header := titleStyle.Render("resume a session") + "  " +
		mutedStyle.Render(fmt.Sprintf("%d available", len(m.resumeSessions)))

	cardW := innerW - 2
	if cardW < 30 {
		cardW = 30
	}

	// Render at most a window of cards that fit vertically. Card
	// height is 3 (border = 2 + body = 3 - we use Padding(0,1) so the
	// body is 3 lines: id+state, model+age, preview). Plus header (2)
	// + footer (2) + spacing. Keep it simple: clamp to first N cards.
	const cardLines = 5 // border(2) + content(3)
	available := innerH - 6
	if available < cardLines {
		available = cardLines
	}
	maxVisible := available / cardLines
	if maxVisible < 1 {
		maxVisible = 1
	}
	start := 0
	if m.resumeCursor >= maxVisible {
		start = m.resumeCursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(m.resumeSessions) {
		end = len(m.resumeSessions)
	}

	rows := make([]string, 0, maxVisible+3)
	rows = append(rows, header, "")
	for i := start; i < end; i++ {
		rows = append(rows, renderResumeCard(m.resumeSessions[i], cardW, i == m.resumeCursor))
	}
	rows = append(rows, "", renderResumeFooter())
	return strings.Join(rows, "\n")
}

func renderResumeFooter() string {
	return footerKey("↑↓") + footerLabel(" pick") +
		footerSep() + footerKey("enter") + footerLabel(" resume") +
		footerSep() + footerKey("esc") + footerLabel(" cancel")
}

// renderResumeCard paints one session as a bordered card. Selection
// shows as a thick accent border only — no reverse-video fill. The
// fill made every selected card read as a single inverted slab that
// drowned the meta strip and clipped the corners; the border alone
// is enough signal and lets the body text stay readable.
func renderResumeCard(s resumeSession, w int, selected bool) string {
	innerW := w - 4
	if innerW < 8 {
		innerW = 8
	}
	id := shortID(s.ID)
	badge := stateBadge(s.State)
	titleLeft := id + "  " + badge

	model := displayModelName(s.Model)
	if model == "" {
		model = "(no model recorded)"
	}
	age := humanizeSessionAge(s.UpdatedAt)
	meta := mutedRender(model) + "  ·  " + subtleRender(age) + "  ·  " +
		subtleRender(fmt.Sprintf("%d msg", s.UserMsgs))

	preview := s.Preview
	if preview == "" {
		preview = "(no messages yet)"
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		padRight(titleLeft, innerW),
		padRight(truncateRight(preview, innerW), innerW),
		padRight(meta, innerW),
	)

	if selected {
		return lipgloss.NewStyle().
			Border(lipgloss.ThickBorder()).
			BorderForeground(colorAccent).
			Padding(0, 1).
			Width(w - 2).
			Render(body)
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorSubtle).
		Padding(0, 1).
		Width(w - 2).
		Render(body)
}

func mutedRender(s string) string {
	return lipgloss.NewStyle().Foreground(colorMuted).Render(s)
}

func subtleRender(s string) string {
	return lipgloss.NewStyle().Foreground(colorSubtle).Render(s)
}

// humanizeSessionAge returns a coarse relative-time label suitable
// for a card meta strip. Avoids importing a humantime dep: a session
// picker doesn't need second-precision; "5h ago" reads just fine.
func humanizeSessionAge(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}
