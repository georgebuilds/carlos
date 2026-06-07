// Session picker (`carlos --resume`).
//
// Design pulled from personal/projects/carlos/research/2026-06-05 How
// to Make a TUI Feel Awesome in 2026.md:
//
//   - Single-column list with one focused row (lazygit-style),
//     accent-bordered, fixed skeleton
//   - Always-visible footer hints (Zellij + Helix discoverability)
//   - "/" opens an inline filter (universal idiom)
//   - Empty state with a call-to-action ("no past sessions - run
//     `carlos` to start one")
//   - No decorative motion; Lipgloss adaptive color via theme.Load
//   - Sub-100ms response (the list is in memory; nothing to wait on)
package main

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/oklog/ulid/v2"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/theme"
)

// errPickerCancelled is the sentinel callers (runDefault) treat as
// "user backed out - exit cleanly without launching chat".
var errPickerCancelled = errors.New("session picker: cancelled by user")

// runSessionPicker opens an interactive bubbletea list of past
// user-facing sessions and returns the chosen ID. Returns
// errPickerCancelled when the user hits esc / ctrl-c without
// selecting; returns ErrNoSessions when the agents table is empty
// (callers degrade to fresh-session).
func runSessionPicker(ctx context.Context) (string, error) {
	log, err := openStateDBForPicker()
	if err != nil {
		return "", err
	}
	defer log.Close()

	sessions, err := agent.ListUserSessions(ctx, log, "")
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", agent.ErrNoSessions
	}

	pal := loadPickerPalette()
	m := newSessionPickerModel(sessions, pal)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return "", err
	}
	fm := final.(sessionPickerModel)
	if fm.cancelled {
		return "", errPickerCancelled
	}
	return fm.chosen, nil
}

// openStateDBForPicker opens ~/.carlos/state.db in a self-contained
// way so the picker can pre-flight WITHOUT going through the rest of
// runDefault's setup. We reuse the same path the chat surface uses.
func openStateDBForPicker() (*agent.SQLiteEventLog, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("session picker: home dir: %w", err)
	}
	dbPath := filepath.Join(home, ".carlos", "state.db")
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No state.db yet - no sessions. Caller falls back
			// to fresh-session.
			return nil, agent.ErrNoSessions
		}
		return nil, fmt.Errorf("session picker: stat state.db: %w", err)
	}
	return agent.OpenStateDB(dbPath)
}

// loadPickerPalette mirrors what the chat surface does at startup so
// the picker's colors match the rest of carlos. nil config (no
// onboarding yet) is handled by theme.Load defaults.
func loadPickerPalette() theme.Palette {
	var opts theme.Options
	if cfg, err := config.Load(config.DefaultPath()); err == nil && cfg != nil {
		opts.ForcedVariant = cfg.Theme.Variant
		opts.AccentOverride = cfg.Theme.Accent
	}
	return theme.Load(opts)
}

// sessionPickerModel is the bubbletea Model for the picker. Tiny by
// design - the whole UX is a list + filter + footer.
type sessionPickerModel struct {
	all      []agent.Session
	filtered []int // indices into all that match the current filter
	cursor   int   // index into filtered

	filter      string
	filterMode  bool // true while the user is typing into the filter
	cancelled   bool
	chosen      string

	width  int
	height int
	pal    theme.Palette
	now    func() time.Time
}

func newSessionPickerModel(sessions []agent.Session, pal theme.Palette) sessionPickerModel {
	m := sessionPickerModel{
		all: sessions,
		pal: pal,
		now: time.Now,
	}
	m.refilter()
	return m
}

func (m sessionPickerModel) Init() tea.Cmd { return nil }

func (m sessionPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		// Filter-mode key handling is its own tiny REPL - typing
		// adds to the filter, esc exits filter without clearing
		// the picker, enter commits the highlighted match.
		if m.filterMode {
			switch msg.Type {
			case tea.KeyEsc:
				m.filterMode = false
				return m, nil
			case tea.KeyEnter:
				m.filterMode = false
				return m.commitSelection()
			case tea.KeyBackspace:
				if len(m.filter) > 0 {
					m.filter = m.filter[:len(m.filter)-1]
					m.refilter()
				}
				return m, nil
			case tea.KeyRunes:
				m.filter += string(msg.Runes)
				m.refilter()
				return m, nil
			case tea.KeySpace:
				m.filter += " "
				m.refilter()
				return m, nil
			}
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.cancelled = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "j":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
			return m, nil
		case "g", "home":
			m.cursor = 0
			return m, nil
		case "G", "end":
			if len(m.filtered) > 0 {
				m.cursor = len(m.filtered) - 1
			}
			return m, nil
		case "/":
			m.filterMode = true
			return m, nil
		case "enter":
			return m.commitSelection()
		}
	}
	return m, nil
}

// commitSelection finalizes the picker, setting m.chosen to the
// highlighted session's ID. No-op when the filtered list is empty.
func (m sessionPickerModel) commitSelection() (tea.Model, tea.Cmd) {
	if len(m.filtered) == 0 {
		return m, nil
	}
	m.chosen = m.all[m.filtered[m.cursor]].ID
	return m, tea.Quit
}

// refilter recomputes m.filtered from m.filter (case-insensitive
// substring match across title + preview + model + id). Cursor
// clamps to the new bounds. Empty filter shows everything.
func (m *sessionPickerModel) refilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter))
	m.filtered = m.filtered[:0]
	for i, s := range m.all {
		if q == "" {
			m.filtered = append(m.filtered, i)
			continue
		}
		hay := strings.ToLower(s.Title + " " + s.Preview + " " + s.Model + " " + s.ID)
		if strings.Contains(hay, q) {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m sessionPickerModel) View() string {
	w, h := m.width, m.height
	if w == 0 || h == 0 {
		w, h = 100, 30
	}

	header := lipgloss.NewStyle().
		Foreground(m.pal.Accent).
		Bold(true).
		Render("Resume a session")

	subtitle := lipgloss.NewStyle().
		Foreground(m.pal.Muted).
		Render(fmt.Sprintf("%d session%s · pick one to continue", len(m.filtered), pluralS(len(m.filtered))))

	// Filter row - visible only while in filter mode OR a filter is set.
	var filterRow string
	if m.filterMode || m.filter != "" {
		caret := ""
		if m.filterMode {
			caret = lipgloss.NewStyle().Foreground(m.pal.Accent).Render("▎")
		}
		filterRow = lipgloss.NewStyle().Foreground(m.pal.Muted).Render("filter: ") +
			lipgloss.NewStyle().Foreground(m.pal.Accent).Render(m.filter) +
			caret
	}

	// List rows.
	var rows []string
	if len(m.filtered) == 0 {
		rows = append(rows, lipgloss.NewStyle().Foreground(m.pal.Muted).Render(
			"(no matches - backspace to clear filter, esc to cancel)"))
	}
	for i, idx := range m.filtered {
		rows = append(rows, m.renderRow(m.all[idx], i == m.cursor, w))
	}

	body := strings.Join(rows, "\n")

	footer := m.renderFooter()

	pane := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		subtitle,
		"",
		filterRow,
		body,
	)
	border := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(m.pal.Accent).
		Padding(1, 2).
		Width(w - 2).
		Height(h - 4)
	return "\n\n" + border.Render(pane) + "\n" + footer
}

// renderRow formats a single session in the list. Focused rows get
// the accent foreground + a leading ▸; unfocused rows are muted so
// the focused one pops without competing decoration.
func (m sessionPickerModel) renderRow(s agent.Session, focused bool, w int) string {
	titleStyle := lipgloss.NewStyle().Foreground(m.pal.Muted)
	previewStyle := lipgloss.NewStyle().Foreground(m.pal.Subtle).Italic(true)
	metaStyle := lipgloss.NewStyle().Foreground(m.pal.Subtle)
	marker := "  "
	if focused {
		titleStyle = lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true)
		previewStyle = lipgloss.NewStyle().Foreground(m.pal.Accent)
		metaStyle = lipgloss.NewStyle().Foreground(m.pal.Accent)
		marker = lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true).Render("▸ ")
	}
	title := s.Title
	if title == "" {
		title = "(untitled)"
	}
	rel := relativeTime(m.now(), s.UpdatedAt)
	meta := fmt.Sprintf("%s · %s · %d msg%s", rel, s.Model, s.UserMsgs, pluralS(s.UserMsgs))
	first := marker + titleStyle.Render(title) + "  " + metaStyle.Render(meta)
	if s.Preview == "" {
		return first
	}
	// Two-line entry: title row + preview row indented under the
	// marker. Indent matches the leading "  "/"▸ " width (2 cells).
	indent := strings.Repeat(" ", 4)
	return first + "\n" + indent + previewStyle.Render(truncatePickerLine(s.Preview, w-8))
}

func (m sessionPickerModel) renderFooter() string {
	key := lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true)
	body := lipgloss.NewStyle().Foreground(m.pal.Muted)
	parts := []string{
		key.Render("↑/↓") + body.Render(" navigate"),
		key.Render("enter") + body.Render(" resume"),
		key.Render("/") + body.Render(" filter"),
		key.Render("esc") + body.Render(" cancel"),
	}
	return body.Render(" ") + strings.Join(parts, body.Render("  ·  "))
}

// relativeTime is a 2-token "5m ago" / "3h ago" / "2d ago" formatter.
// Newer than 1 minute reads as "just now"; older than 30 days
// switches to a calendar date so the picker doesn't claim something
// happened "180d ago".
func relativeTime(now, past time.Time) string {
	d := now.Sub(past)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
	return past.Local().Format("2006-01-02")
}

// truncatePickerLine clips s to the terminal width's preview budget.
// Different from agent.truncatePreview because this one knows about
// the picker's specific layout (indent + style overhead).
func truncatePickerLine(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return s[:max-1] + "…"
}

// pluralS is the bare "s" suffix used by the picker headers + meta
// rows. Lives here (not in a shared util) because cmd/carlos doesn't
// have a pluralization helper today and this is the only caller.
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// sessionULIDEntropy is the monotonic random reader for fresh
// session IDs. ULID gives sortable IDs the rest of carlos already
// uses for agents + jobs + envelopes; session IDs use the same so
// they sit naturally next to sub-agent IDs in the agents table.
// Guarded by sessionULIDMu because oklog/ulid's MonotonicEntropy is
// not safe for concurrent reads (same recipe as
// internal/gateway/envelope.go).
var (
	sessionULIDMu      sync.Mutex
	sessionULIDEntropy = ulid.Monotonic(cryptorand.Reader, 0)
)

// mintSessionID returns a fresh ULID-string scoped to the given
// timestamp. Called once per `carlos` invocation (and once per
// `/resume` swap when that lands in R2).
func mintSessionID(now time.Time) (string, error) {
	sessionULIDMu.Lock()
	defer sessionULIDMu.Unlock()
	u, err := ulid.New(uint64(now.UnixMilli()), sessionULIDEntropy)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
