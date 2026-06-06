// Live status panel for `carlos research` (post-v0.3.0 UX polish).
//
// Pulled from personal/projects/carlos/research/2026-06-05 How to Make
// a TUI Feel Awesome in 2026.md:
//
//   - "perceived responsiveness ... never block the UI; never block on
//     I/O" — the engine runs in a goroutine, the bubbletea loop
//     consumes phase events as tea.Msg
//   - "spinner only for ≥200ms operations; output IS the activity
//     indicator when it's streaming" — the spinner is the activity
//     indicator here because research phases don't stream
//   - "always-visible contextual help" — keybind hint at the bottom
//   - "bordered panel rather than full-screen takeover for compact
//     surfaces" — bubbletea inline (no AltScreen) so the terminal
//     resumes cleanly on exit
//   - "no decorative motion; motion communicates state or progress" —
//     spinner runs only while a phase is in flight
//
// Visual shape (3 logical content rows inside a rounded accent
// border):
//
//	┌─────────────────────────────────────────────────────────────┐
//	│ ⠋ researching: steak restaurants in tulsa                    │
//	│   ● search · 12.3s    (web 4 results, fetch queued)          │
//	│   ✓ decompose  ◐ search  ○ fetch  ○ read  ○ synth  ○ verify  │
//	└─────────────────────────────────────────────────────────────┘
//
// On completion the panel clears itself and the rendered report
// prints inline as before.

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/theme"
)

// researchPhases is the fixed phase order — must match the engine's
// runtime order (internal/research/engine.go). Used for the
// per-phase progress glyph row.
var researchPhases = []string{
	"decompose",
	"search",
	"fetch",
	"read",
	"synthesize",
	"verify",
}

// spinnerFrames are the Unicode Braille frames the activity spinner
// cycles through. Eight steps × 100ms = ~12fps, slow enough to read
// individual frames but fast enough to feel responsive.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerTickInterval is the cadence at which the spinner advances.
const spinnerTickInterval = 100 * time.Millisecond

// runResearchWithStatus drives engine.Run while a bubbletea inline
// panel renders live progress. Returns the final report and any
// engine error.
//
// The bubbletea Program runs the panel; engine.Run executes in a
// background goroutine. Phase callbacks (OnPhaseStart / OnPhaseDone)
// fire researchPhaseMsg into the program; engine completion fires
// researchDoneMsg. On done, the program quits and we return.
func runResearchWithStatus(ctx context.Context, engine *research.Engine, question string) (*research.Report, error) {
	pal := loadPickerPalette()
	model := newResearchStatusModel(question, pal)

	// Run engine in a goroutine + ferry phase + completion events
	// to the bubbletea program via a Send hook.
	prog := tea.NewProgram(model)

	engine.OnPhaseStart = func(phase string) {
		prog.Send(researchPhaseStartMsg{phase: phase, t: time.Now()})
	}
	engine.OnPhaseDone = func(phase string, elapsed time.Duration, err error) {
		prog.Send(researchPhaseDoneMsg{phase: phase, elapsed: elapsed, errMsg: errString(err)})
	}

	var report *research.Report
	var runErr error
	go func() {
		report, runErr = engine.Run(ctx, question)
		prog.Send(researchDoneMsg{})
	}()

	if _, err := prog.Run(); err != nil {
		return report, fmt.Errorf("research status program: %w", err)
	}
	return report, runErr
}

// errString returns err's message or "" when nil. Used because tea
// messages are best kept as plain data (no func types or errors with
// hidden state).
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// --- tea messages ---------------------------------------------------

// researchPhaseStartMsg lands when the engine enters a new phase.
type researchPhaseStartMsg struct {
	phase string
	t     time.Time
}

// researchPhaseDoneMsg lands when a phase finishes (success or
// failure). errMsg is non-empty on failure.
type researchPhaseDoneMsg struct {
	phase   string
	elapsed time.Duration
	errMsg  string
}

// researchDoneMsg signals engine.Run has returned. The panel
// completes its final render and quits.
type researchDoneMsg struct{}

// researchTickMsg drives the spinner animation.
type researchTickMsg time.Time

// --- model ----------------------------------------------------------

type phaseState int

const (
	phaseStatePending phaseState = iota
	phaseStateRunning
	phaseStateDone
	phaseStateFailed
)

type researchStatusModel struct {
	question string
	started  time.Time
	pal      theme.Palette
	width    int

	currentPhase string
	phaseStarted time.Time
	phaseStates  map[string]phaseState
	phaseErrs    map[string]string

	spinnerFrame int

	done bool
}

func newResearchStatusModel(question string, pal theme.Palette) researchStatusModel {
	return researchStatusModel{
		question:    question,
		pal:         pal,
		started:     time.Now(),
		phaseStates: map[string]phaseState{},
		phaseErrs:   map[string]string{},
	}
}

func (m researchStatusModel) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(spinnerTickInterval, func(t time.Time) tea.Msg {
		return researchTickMsg(t)
	})
}

func (m researchStatusModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case researchTickMsg:
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		if m.done {
			return m, nil
		}
		return m, tick()
	case researchPhaseStartMsg:
		m.currentPhase = msg.phase
		m.phaseStarted = msg.t
		m.phaseStates[msg.phase] = phaseStateRunning
		return m, nil
	case researchPhaseDoneMsg:
		if msg.errMsg != "" {
			m.phaseStates[msg.phase] = phaseStateFailed
			m.phaseErrs[msg.phase] = msg.errMsg
		} else {
			m.phaseStates[msg.phase] = phaseStateDone
		}
		if msg.phase == m.currentPhase {
			m.currentPhase = ""
		}
		return m, nil
	case researchDoneMsg:
		m.done = true
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m researchStatusModel) View() string {
	w := m.width
	if w <= 0 {
		w = 90
	}
	if w > 100 {
		w = 100
	}
	boxW := w - 2
	if boxW < 50 {
		boxW = 50
	}

	accent := lipgloss.NewStyle().Foreground(m.pal.Accent)
	bold := lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(m.pal.Muted)
	subtle := lipgloss.NewStyle().Foreground(m.pal.Subtle).Italic(true)

	// Line 1 — spinner + headline + elapsed since start.
	spinner := spinnerFrames[m.spinnerFrame]
	if m.done {
		spinner = "✓"
	}
	elapsed := time.Since(m.started)
	line1 := accent.Render(spinner) + " " +
		bold.Render("researching: ") +
		muted.Render(truncateOneLineForResearch(m.question, boxW-len(spinner)-20)) +
		muted.Render(fmt.Sprintf("   · %s elapsed", formatResearchDuration(elapsed)))

	// Line 2 — current phase or completion summary.
	var line2 string
	if m.done {
		line2 = subtle.Render("done · rendering report…")
	} else if m.currentPhase != "" {
		phaseElapsed := time.Since(m.phaseStarted)
		line2 = accent.Render("● ") +
			bold.Render(m.currentPhase) +
			muted.Render(fmt.Sprintf(" · %s", formatResearchDuration(phaseElapsed)))
	} else {
		line2 = subtle.Render("starting…")
	}

	// Line 3 — per-phase progress glyphs in fixed order.
	line3 := m.renderPhaseGlyphs()

	body := lipgloss.JoinVertical(lipgloss.Left, line1, "  "+line2, "  "+line3)
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.pal.Accent).
		Padding(0, 1).
		Width(boxW).
		Render(body)
	return "\n" + border + "\n"
}

func (m researchStatusModel) renderPhaseGlyphs() string {
	accent := lipgloss.NewStyle().Foreground(m.pal.Accent)
	bold := lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(m.pal.Muted)
	warn := lipgloss.NewStyle().Foreground(m.pal.Warn).Bold(true)
	subtle := lipgloss.NewStyle().Foreground(m.pal.Subtle)

	parts := make([]string, 0, len(researchPhases))
	for _, p := range researchPhases {
		state := m.phaseStates[p]
		var glyph, label string
		switch state {
		case phaseStateDone:
			glyph = accent.Render("✓")
			label = muted.Render(shortPhaseLabel(p))
		case phaseStateRunning:
			glyph = bold.Render("◐")
			label = bold.Render(shortPhaseLabel(p))
		case phaseStateFailed:
			glyph = warn.Render("✗")
			label = warn.Render(shortPhaseLabel(p))
		default:
			glyph = subtle.Render("○")
			label = subtle.Render(shortPhaseLabel(p))
		}
		parts = append(parts, glyph+" "+label)
	}
	return strings.Join(parts, muted.Render("  "))
}

// shortPhaseLabel returns the abbreviation used in the per-phase
// glyph row. Full names ("synthesize") are too long to fit six side-
// by-side at typical widths; six-char-max keeps the row compact.
func shortPhaseLabel(phase string) string {
	switch phase {
	case "decompose":
		return "decomp"
	case "synthesize":
		return "synth"
	default:
		return phase
	}
}

// truncateOneLineForResearch trims s for the headline. Single-line
// only; newlines collapse to spaces.
func truncateOneLineForResearch(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return s[:max-1] + "…"
}

// formatResearchDuration emits a compact "<n.n>s" / "<n>m<n>s" form
// for the status panel.
func formatResearchDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%ds", mins, secs)
}
