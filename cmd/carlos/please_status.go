// Live status panel for `carlos please` (post-v0.7.0 UX polish).
//
// Same shape as research_status.go but tuned for the open-ended
// `please` shape: there's no fixed phase list, so we show what's
// happening RIGHT NOW (a tool running, the model streaming text,
// or just thinking) and a running counter for the rest.
//
// Borrowed wholesale from the same TUI research note that shaped
// research_status.go:
//
//   - "perceived responsiveness ... never block the UI; never block
//     on I/O" - the agent loop runs in a goroutine, the bubbletea
//     loop consumes tool + text deltas as tea.Msg.
//   - "spinner only for ≥200ms operations; output IS the activity
//     indicator when it's streaming" - the spinner here doubles as
//     the "I'm streaming words at you" indicator, with the latest
//     assistant text snippet preview-ing on line 2 between tool
//     calls. Tool runs (which are nearly always ≥200ms because of
//     network or shell I/O) get their own focused line.
//   - "bordered panel rather than full-screen takeover" - bubbletea
//     inline so the terminal scrollback survives.
//   - "no decorative motion; motion communicates state or progress"
//     - the spinner runs only while we're actively waiting; on done
//     it locks to ✓ (or ✗ on error) and the program quits, then the
//     final assistant text prints below the box.
//
// Visual shape (3 logical content rows inside a rounded accent
// border):
//
//	┌─────────────────────────────────────────────────────────────┐
//	│ ⠋ working on: add an empty html file to my desktop          │
//	│   ● bash · {"cmd":"touch …/sample.html"} · 0.8s             │
//	│   tools: 2 done · 4.2s elapsed · openrouter gemini-3.5-flash│
//	└─────────────────────────────────────────────────────────────┘

package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/theme"
)

// pleaseToolStartMsg lands when agent.LoopOptions.OnToolCall fires
// inside the loop. inputJSON is the raw tool input bytes the model
// emitted; the view truncates for the preview slot.
type pleaseToolStartMsg struct {
	name      string
	inputJSON string
	t         time.Time
}

// pleaseToolDoneMsg lands when OnToolResult fires. errMsg is non-
// empty when the result was tagged isError so the panel can paint
// the count in warn color (a follow-on polish; for now we just
// stash it).
type pleaseToolDoneMsg struct {
	name    string
	elapsed time.Duration
	errMsg  string
}

// pleaseTextDeltaMsg lands when the assistant streams a chunk of
// text. We only need the last non-empty line for the activity-row
// preview; the full buffer is captured by the textsink wrapper and
// printed after the program exits.
type pleaseTextDeltaMsg struct {
	text string
}

// pleaseDoneMsg signals agent.Run has returned. errMsg is non-empty
// on loop failure so the panel paints the failure state before
// quitting.
type pleaseDoneMsg struct {
	errMsg string
}

// pleaseTickMsg drives the spinner animation. Same cadence as the
// research panel.
type pleaseTickMsg time.Time

type pleaseStatusModel struct {
	prompt    string
	providerN string
	modelID   string
	pal       theme.Palette
	width     int

	started time.Time

	currentTool  string
	currentInput string
	toolStarted  time.Time
	toolsDone    int
	lastError    string

	// lastTextLine is the latest non-empty line of streaming
	// assistant text. We show it on line 2 between tool calls so
	// the user can see the model is actively producing prose, not
	// stalled. Trimmed + truncated at render time.
	lastTextLine string

	spinnerFrame int
	done         bool
}

func newPleaseStatusModel(prompt, providerName, modelID string, pal theme.Palette) pleaseStatusModel {
	return pleaseStatusModel{
		prompt:    prompt,
		providerN: providerName,
		modelID:   modelID,
		pal:       pal,
		started:   time.Now(),
	}
}

func (m pleaseStatusModel) Init() tea.Cmd { return pleaseTick() }

func pleaseTick() tea.Cmd {
	return tea.Tick(spinnerTickInterval, func(t time.Time) tea.Msg {
		return pleaseTickMsg(t)
	})
}

func (m pleaseStatusModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case pleaseTickMsg:
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		if m.done {
			return m, nil
		}
		return m, pleaseTick()
	case pleaseToolStartMsg:
		m.currentTool = msg.name
		m.currentInput = msg.inputJSON
		m.toolStarted = msg.t
		// Tool starts: the model's prior streaming text is now
		// stale ("I'll create the file" no longer reflects what's
		// running). Clear it so the tool gets the focus.
		m.lastTextLine = ""
		return m, nil
	case pleaseToolDoneMsg:
		m.toolsDone++
		if msg.errMsg != "" {
			m.lastError = msg.errMsg
		}
		if msg.name == m.currentTool {
			m.currentTool = ""
			m.currentInput = ""
		}
		return m, nil
	case pleaseTextDeltaMsg:
		// Walk the delta and keep the last non-empty line. Streaming
		// chunks rarely contain newlines but the assistant's final
		// turn does; we want the most recent line, not the first.
		for _, ln := range strings.Split(msg.text, "\n") {
			if tln := strings.TrimSpace(ln); tln != "" {
				m.lastTextLine = tln
			}
		}
		return m, nil
	case pleaseDoneMsg:
		m.done = true
		if msg.errMsg != "" {
			m.lastError = msg.errMsg
		}
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m pleaseStatusModel) View() string {
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
	warn := lipgloss.NewStyle().Foreground(m.pal.Warn).Bold(true)

	// Line 1 — spinner + headline + elapsed since start.
	spinner := spinnerFrames[m.spinnerFrame]
	if m.done {
		spinner = "✓"
		if m.lastError != "" {
			spinner = "✗"
		}
	}
	elapsed := time.Since(m.started)
	const headPrefix = "working on: "
	promptMax := boxW - len(headPrefix) - 22
	if promptMax < 10 {
		promptMax = 10
	}
	line1 := accent.Render(spinner) + " " +
		bold.Render(headPrefix) +
		muted.Render(truncateOneLineForResearch(m.prompt, promptMax)) +
		muted.Render(fmt.Sprintf("   · %s elapsed", formatResearchDuration(elapsed)))

	// Line 2 — current activity. Three states + the done branch:
	//   1. Done   → "done · printing reply…" or "✗ failed: <msg>"
	//   2. Tool   → "● <tool> · <input preview> · <elapsed>"
	//   3. Writing → "◐ writing · <last text line>"
	//   4. Idle   → "◐ thinking…"
	var line2 string
	switch {
	case m.done:
		if m.lastError != "" {
			line2 = warn.Render("✗ failed: ") + muted.Render(m.lastError)
		} else {
			line2 = subtle.Render("done · printing reply…")
		}
	case m.currentTool != "":
		toolElapsed := time.Since(m.toolStarted)
		previewMax := boxW - 8 - len(m.currentTool) - 14 // reserve for " · <elapsed>"
		if previewMax < 8 {
			previewMax = 8
		}
		preview := truncateOneLineForResearch(m.currentInput, previewMax)
		line2 = accent.Render("● ") + bold.Render(m.currentTool)
		if preview != "" {
			line2 += muted.Render(" · " + preview)
		}
		line2 += muted.Render(fmt.Sprintf(" · %s", formatResearchDuration(toolElapsed)))
	case m.lastTextLine != "":
		previewMax := boxW - 16
		if previewMax < 8 {
			previewMax = 8
		}
		line2 = accent.Render("◐ ") +
			bold.Render("writing") +
			muted.Render(" · "+truncateOneLineForResearch(m.lastTextLine, previewMax))
	default:
		line2 = subtle.Render("◐ thinking…")
	}

	// Line 3 — counter + provider/model. Stable layout (doesn't
	// jitter as tools come and go) so the eye locks onto line 2 for
	// motion.
	counter := fmt.Sprintf("tools: %d done", m.toolsDone)
	if m.toolsDone == 0 {
		counter = "tools: none yet"
	}
	providerLabel := m.providerN
	if m.modelID != "" {
		providerLabel += " " + m.modelID
	}
	line3 := muted.Render(counter) + muted.Render(" · ") + subtle.Render(providerLabel)

	body := lipgloss.JoinVertical(lipgloss.Left, line1, "  "+line2, "  "+line3)
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.pal.Accent).
		Padding(0, 1).
		Width(boxW).
		Render(body)
	return "\n" + border + "\n"
}

// pleaseTextSink is the io.Writer plugged into agent.LoopOptions.
// TextSink that captures the full assistant text for printing after
// the panel exits AND sends a coalesced delta msg to the bubbletea
// program so the View can show a live preview of the latest line.
//
// Coalescing: streaming providers emit dozens of small deltas per
// second; sending one tea.Msg per delta would flood the program
// queue. We send a msg on every Write but the model only keeps the
// last non-empty line, so the cost is bounded.
type pleaseTextSink struct {
	mu     sync.Mutex
	buf    strings.Builder
	prog   *tea.Program // nil = quiet mode (non-TTY)
}

func (s *pleaseTextSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buf.Write(p)
	s.mu.Unlock()
	if s.prog != nil {
		s.prog.Send(pleaseTextDeltaMsg{text: string(p)})
	}
	return len(p), nil
}

func (s *pleaseTextSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// runPleaseDriver runs work in a goroutine while the bubbletea
// program renders the inline panel. The work function receives a
// "send" closure it uses to ferry tool / done events into the
// program; the program quits when work returns and sends
// pleaseDoneMsg. Returns work's error.
//
// This shape mirrors runResearchWithStatus's "engine in a goroutine"
// pattern but accepts an arbitrary work function so the agent.Run
// call site stays in runtime_headless.go where its plumbing lives.
func runPleaseDriver(
	ctx context.Context,
	prompt, providerName, modelID string,
	work func(prog *tea.Program) error,
) error {
	pal := loadPickerPalette()
	model := newPleaseStatusModel(prompt, providerName, modelID, pal)
	prog := tea.NewProgram(model)

	var workErr error
	go func() {
		workErr = work(prog)
		errMsg := ""
		if workErr != nil && !isContextCanceled(workErr) {
			errMsg = workErr.Error()
		}
		prog.Send(pleaseDoneMsg{errMsg: errMsg})
	}()

	if _, err := prog.Run(); err != nil {
		return fmt.Errorf("please status program: %w", err)
	}
	return workErr
}

// isContextCanceled reports whether err is a context.Canceled (or
// wraps one). The bubbletea-level done message suppresses the
// failure mark when the user ctrl-c'd us — that's not a failure.
func isContextCanceled(err error) bool {
	if err == nil {
		return false
	}
	for e := err; e != nil; {
		if e == context.Canceled {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := e.(unwrapper); ok {
			e = u.Unwrap()
			continue
		}
		return false
	}
	return false
}
