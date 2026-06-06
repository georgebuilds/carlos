// Inline TTY frame picker for `carlos research` and `carlos please`
// (Phase F-19). Runs as a bordered, non-AltScreen bubbletea component
// so it scrolls into the user's terminal history once a selection is
// made, matching the inline-status feel of research_status.go.
//
// Visual shape at 80x24 with 5 frames:
//
//	  carlos research · "what's on my calendar tomorrow?"
//	   ◉ 1 personal     ▣ 2 work     ⛰ 3 ludus     ◈ 4 research     ✦ 5 writing
//	  pick a frame · 1-5 or ↑/↓/enter · esc cancel
//
// Narrow-terminal fallback (< 60 cols) collapses the inline row to a
// vertical column, capped at 7 visible frames with a "…" sentinel.
//
// Selection input:
//   - 1-9: jump-pick the Nth frame.
//   - ↑/↓ or j/k: move the cursor.
//   - enter: confirm the cursor position.
//   - esc / ctrl+c: cancel (returns errFramePickerCancelled).
//
// TTY detection lives in the caller: callers gate the picker behind a
// terminal check (see cmd/carlos/main.go) so this file stays testable
// against arbitrary frame lists without needing a real TTY.

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xterm "github.com/charmbracelet/x/term"

	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/theme"
)

// errFramePickerCancelled is the sentinel returned when the user esc's or
// ctrl-c's out of the picker. Subcommands return this up to main so
// the process exits non-zero without printing a noisy error.
var errFramePickerCancelled = errors.New("frame picker cancelled")

// inlinePickerMinWidth is the cut-off below which the picker collapses
// to a vertical column. 60 was empirically picked: at 80 cols with five
// frames the inline row eats ~70 visible cells, so anything under 60
// can't fit even a small inline list without wrapping awkwardly.
const inlinePickerMinWidth = 60

// inlinePickerMaxVertical is the cap on rows shown in the vertical
// fallback. Anything past this gets a "… N more" sentinel so the
// picker stays under 10 lines total even with a 20-frame config.
const inlinePickerMaxVertical = 7

// inlinePickerActiveSortThreshold is the frame count at which the
// active frame sorts to the front of the inline row so it sits near
// the "1" shortcut. Below this every frame stays in its config order.
const inlinePickerActiveSortThreshold = 6

// stdinIsTTY reports whether stdin is attached to a terminal. Used by
// callers to gate the picker. Pulled out so the call site stays one
// line and so a future test can stub it if we ever need to exercise
// the TTY branch directly.
func stdinIsTTY() bool {
	return xterm.IsTerminal(os.Stdin.Fd())
}

// RunInlineFramePicker presents the inline frame picker and blocks
// until the user picks a frame or cancels. Returns the picked frame
// name on success, errFramePickerCancelled on cancel.
//
// Callers MUST handle the single-frame case themselves (skip the
// picker, print a one-line dim "running in <name>" hint). The picker
// still works correctly when given a single frame — it returns it
// immediately on enter — but the caller's UX is better when it skips
// the prompt entirely in that case.
func RunInlineFramePicker(cmdName, prompt string, frames *frame.Config) (string, error) {
	pal := loadPickerPalette()
	model := newInlinePickerModel(cmdName, prompt, frames, pal)
	prog := tea.NewProgram(model)
	final, err := prog.Run()
	if err != nil {
		return "", fmt.Errorf("inline frame picker: %w", err)
	}
	m, ok := final.(inlinePickerModel)
	if !ok {
		return "", fmt.Errorf("inline frame picker: unexpected final model %T", final)
	}
	if m.cancelled {
		return "", errFramePickerCancelled
	}
	if m.cursor < 0 || m.cursor >= len(m.frames) {
		return "", errFramePickerCancelled
	}
	return m.frames[m.cursor].Name, nil
}

// inlinePickerModel is the bubbletea Model for the picker. Tiny by
// design — the whole UX is a row of frames + a footer.
type inlinePickerModel struct {
	cmdName string
	prompt  string
	frames  []frame.Frame
	pal     theme.Palette

	cursor    int
	width     int
	cancelled bool
}

// newInlinePickerModel constructs the model. The frame ordering rule
// kicks in here: when len(frames) is large enough to hide the active
// frame from view, sort it to the front so it sits next to the "1"
// shortcut and stays accessible.
func newInlinePickerModel(cmdName, prompt string, cfg *frame.Config, pal theme.Palette) inlinePickerModel {
	ordered := orderedFramesFor(cfg)
	cursor := indexOf(ordered, activeNameFor(cfg))
	if cursor < 0 {
		cursor = 0
	}
	return inlinePickerModel{
		cmdName: cmdName,
		prompt:  prompt,
		frames:  ordered,
		pal:     pal,
		cursor:  cursor,
	}
}

// orderedFramesFor returns the frame list in the order the picker
// renders them. When the config has six or more frames, the active
// (or default) frame floats to position 0 so it sits adjacent to the
// "1" jump-pick shortcut.
func orderedFramesFor(cfg *frame.Config) []frame.Frame {
	if cfg == nil || len(cfg.List) == 0 {
		return nil
	}
	out := make([]frame.Frame, len(cfg.List))
	copy(out, cfg.List)
	if len(out) < inlinePickerActiveSortThreshold {
		return out
	}
	target := activeNameFor(cfg)
	idx := -1
	for i, f := range out {
		if f.Name == target {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return out
	}
	moved := out[idx]
	out = append(out[:idx], out[idx+1:]...)
	out = append([]frame.Frame{moved}, out...)
	return out
}

// activeNameFor returns the name the picker treats as "current":
// Config.Active when set, else Config.Default, else the first frame's
// name. Mirrors the precedence in frame.ResolveActive's fallback chain.
func activeNameFor(cfg *frame.Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.Active != "" {
		return cfg.Active
	}
	if cfg.Default != "" {
		return cfg.Default
	}
	if len(cfg.List) > 0 {
		return cfg.List[0].Name
	}
	return ""
}

func indexOf(frames []frame.Frame, name string) int {
	for i, f := range frames {
		if f.Name == name {
			return i
		}
	}
	return -1
}

func (m inlinePickerModel) Init() tea.Cmd { return nil }

func (m inlinePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m inlinePickerModel) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c", "esc":
		m.cancelled = true
		return m, tea.Quit
	case "enter":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.frames)-1 {
			m.cursor++
		}
		return m, nil
	}
	// Digit 1-9 jump-picks the Nth frame.
	s := k.String()
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		idx := int(s[0] - '1')
		if idx < len(m.frames) {
			m.cursor = idx
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m inlinePickerModel) View() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	if w < inlinePickerMinWidth {
		return m.renderVertical(w)
	}
	return m.renderInline(w)
}

// renderInline is the standard 3-line layout. Bold command name, italic
// quoted prompt, frame row with accented glyphs, dim footer.
func (m inlinePickerModel) renderInline(w int) string {
	bold := lipgloss.NewStyle().Bold(true).Foreground(m.pal.Accent)
	italic := lipgloss.NewStyle().Italic(true).Foreground(m.pal.Muted)
	dim := lipgloss.NewStyle().Foreground(m.pal.Subtle)

	header := bold.Render(m.cmdName) + " " + dim.Render("·") + " " +
		italic.Render(quoteForHeader(m.prompt, w-len(m.cmdName)-6))

	row := m.renderFrameRow()
	footer := dim.Render(fmt.Sprintf("pick a frame · 1-%d or ↑/↓/enter · esc cancel", len(m.frames)))

	return "\n" + header + "\n" + row + "\n" + footer + "\n"
}

// renderFrameRow paints the inline frames as
// "<glyph> <num> <name>" separated by three spaces. The cursor frame
// gets a bold name; non-cursor frames render dim. The glyph keeps its
// frame-accent color in both states so the colour-coded glance still
// works even when the cursor moves off.
func (m inlinePickerModel) renderFrameRow() string {
	dim := lipgloss.NewStyle().Foreground(m.pal.Subtle)
	cells := make([]string, 0, len(m.frames))
	for i, f := range m.frames {
		glyph := f.Glyph
		if glyph == "" {
			glyph = frame.DefaultGlyphFor(f.Name)
		}
		col := frame.AccentColor(f.Accent)
		glyphStyle := lipgloss.NewStyle()
		if col != "" {
			glyphStyle = glyphStyle.Foreground(col)
		}
		nameStyle := dim
		if i == m.cursor {
			nameStyle = lipgloss.NewStyle().Bold(true).Foreground(m.pal.Accent)
		}
		num := fmt.Sprintf("%d", i+1)
		if i >= 9 {
			num = "·"
		}
		cells = append(cells, glyphStyle.Render(glyph)+" "+dim.Render(num)+" "+nameStyle.Render(f.Name))
	}
	sep := strings.Repeat(" ", 3)
	return " " + strings.Join(cells, sep)
}

// renderVertical is the < 60-col fallback. One frame per line, cursor
// marker on the left, dim footer with the same key affordances.
func (m inlinePickerModel) renderVertical(w int) string {
	bold := lipgloss.NewStyle().Bold(true).Foreground(m.pal.Accent)
	italic := lipgloss.NewStyle().Italic(true).Foreground(m.pal.Muted)
	dim := lipgloss.NewStyle().Foreground(m.pal.Subtle)

	header := bold.Render(m.cmdName) + " " + dim.Render("·") + " " +
		italic.Render(quoteForHeader(m.prompt, w-len(m.cmdName)-6))

	visible := m.frames
	overflow := 0
	if len(visible) > inlinePickerMaxVertical {
		overflow = len(visible) - inlinePickerMaxVertical
		visible = visible[:inlinePickerMaxVertical]
	}

	lines := make([]string, 0, len(visible)+1)
	lines = append(lines, header)
	for i, f := range visible {
		glyph := f.Glyph
		if glyph == "" {
			glyph = frame.DefaultGlyphFor(f.Name)
		}
		col := frame.AccentColor(f.Accent)
		glyphStyle := lipgloss.NewStyle()
		if col != "" {
			glyphStyle = glyphStyle.Foreground(col)
		}
		marker := "  "
		nameStyle := dim
		if i == m.cursor {
			marker = "› "
			nameStyle = lipgloss.NewStyle().Bold(true).Foreground(m.pal.Accent)
		}
		num := fmt.Sprintf("%d", i+1)
		if i >= 9 {
			num = "·"
		}
		lines = append(lines, marker+glyphStyle.Render(glyph)+" "+dim.Render(num)+" "+nameStyle.Render(f.Name))
	}
	if overflow > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf("  … %d more", overflow)))
	}
	lines = append(lines, dim.Render(fmt.Sprintf("pick a frame · 1-%d or ↑/↓/enter · esc cancel", len(m.frames))))
	return "\n" + strings.Join(lines, "\n") + "\n"
}

// quoteForHeader wraps the prompt in straight quotes and truncates to
// fit the header width. Negative or tiny maxes collapse to a single
// ellipsis so the layout still parses.
func quoteForHeader(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if max <= 2 {
		return "\"…\""
	}
	body := s
	if len(body) > max-2 {
		if max-3 <= 0 {
			body = "…"
		} else {
			body = body[:max-3] + "…"
		}
	}
	return "\"" + body + "\""
}
