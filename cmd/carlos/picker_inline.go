// Inline TTY frame picker for `carlos research` and `carlos please`
// (Phase F-19). Runs as a bordered, non-AltScreen bubbletea component
// so it scrolls into the user's terminal history once a selection is
// made, matching the inline-status feel of research_status.go.
//
// Visual shape at 80x24 with 3 frames:
//
//	carlos research · "what's on my calendar tomorrow?"
//
//	╭───────────╮  ╭───────────╮  ╭───────────╮
//	│     ◉     │  │     ▣     │  │     ⛰     │
//	│  personal │  │   work    │  │   ludus   │
//	│    [1]    │  │    [2]    │  │    [3]    │
//	╰───────────╯  ╰───────────╯  ╰───────────╯
//
//	←/→ navigate · 1-3 pick · enter confirm · esc cancel
//
// The selected card carries an accent-colored border and bold name;
// non-selected cards use a muted border with dim names. Frames wrap
// to additional rows when they exceed the per-row capacity at the
// current terminal width.
//
// Narrow-terminal fallback (< 60 cols) collapses to a vertical
// column, capped at 7 visible frames with a "…" sentinel — the
// horizontal-card layout is sized for room and the vertical column
// stays readable when cards would crush.
//
// Selection input:
//   - 1-9: jump-pick the Nth frame.
//   - ←/→ or h/l: move the cursor between cards. Wraps across rows
//     so reading left-to-right always advances by one frame.
//   - ↑/↓ or j/k: jump by one row in the multi-row case; degenerate
//     to ←/→ when there's only one row (so users who reach for the
//     old binding still land on a useful action).
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
	"github.com/georgebuilds/carlos/internal/tui/termscrub"
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
// still works correctly when given a single frame - it returns it
// immediately on enter - but the caller's UX is better when it skips
// the prompt entirely in that case.
func RunInlineFramePicker(cmdName, prompt string, frames *frame.Config) (string, error) {
	pal := loadPickerPalette()
	model := newInlinePickerModel(cmdName, prompt, frames, pal)
	prog := tea.NewProgram(model, tea.WithFilter(termscrub.FilterTerminalLeaks))
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
// design - the whole UX is a row of frames + a footer.
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
	case "left", "h":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "right", "l":
		if m.cursor < len(m.frames)-1 {
			m.cursor++
		}
		return m, nil
	case "up", "k":
		// In a multi-row layout ↑ jumps one row at a time so a
		// user with 8 frames laid out 4-and-4 can land on the
		// "other row" in one keystroke. When only one row exists
		// (the common case for 3-5 frames at 80 cols), fall back
		// to ←-equivalent so the muscle-memory keystroke still
		// does something useful instead of being a no-op.
		perRow := m.perRow()
		if perRow > 0 && m.cursor >= perRow {
			m.cursor -= perRow
		} else if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		perRow := m.perRow()
		if perRow > 0 && m.cursor+perRow < len(m.frames) {
			m.cursor += perRow
		} else if m.cursor < len(m.frames)-1 {
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

// perRow reports the number of cards the current width fits per row.
// Called by the ↑/↓ key handler to translate a row-jump into a
// cursor delta. The renderer uses cardsPerRow with the same
// arithmetic so the visible layout and the keymap stay in lockstep.
func (m inlinePickerModel) perRow() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	return cardsPerRow(w, len(m.frames))
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

// Card-layout constants. cardInnerW is the content width inside the
// border; cardW is the outer width. cardGap is the horizontal spacing
// between adjacent cards.
const (
	cardInnerW = 11
	cardW      = cardInnerW + 2 // +2 for the left/right border columns
	cardGap    = 2
)

// cardsPerRow reports how many cards fit at the supplied terminal
// width, capped at the actual frame count so a single-frame picker
// doesn't compute "fits 5 per row, render 1, cursor is off by 4."
// Always returns at least 1 so the layout never goes degenerate.
func cardsPerRow(w, total int) int {
	if total <= 0 {
		return 0
	}
	avail := w - 2 // leave a 1-col gutter on each side
	if avail < cardW {
		return 1
	}
	// First card costs cardW; each additional costs cardW + cardGap.
	n := 1 + (avail-cardW)/(cardW+cardGap)
	if n < 1 {
		n = 1
	}
	if n > total {
		n = total
	}
	return n
}

// renderInline is the card-grid layout: header, one or more rows of
// horizontal cards, footer. The selected card carries an
// accent-colored rounded border; non-selected cards carry a muted
// border. Wraps to additional rows when the frame count exceeds the
// per-row capacity at the current width.
func (m inlinePickerModel) renderInline(w int) string {
	bold := lipgloss.NewStyle().Bold(true).Foreground(m.pal.Accent)
	italic := lipgloss.NewStyle().Italic(true).Foreground(m.pal.Muted)
	dim := lipgloss.NewStyle().Foreground(m.pal.Subtle)

	header := bold.Render(m.cmdName) + " " + dim.Render("·") + " " +
		italic.Render(quoteForHeader(m.prompt, w-len(m.cmdName)-6))

	grid := m.renderCardGrid(w)
	footer := dim.Render(fmt.Sprintf("←/→ navigate · 1-%d pick · enter confirm · esc cancel", len(m.frames)))

	return "\n" + header + "\n\n" + grid + "\n\n" + footer + "\n"
}

// renderCardGrid composes the cards into rows sized by cardsPerRow.
// Within a row, cards join horizontally with cardGap spaces between;
// rows themselves stack vertically with a single blank-line gap so
// multi-row layouts breathe.
func (m inlinePickerModel) renderCardGrid(w int) string {
	perRow := cardsPerRow(w, len(m.frames))
	if perRow == 0 {
		return ""
	}
	gap := strings.Repeat(" ", cardGap)
	rows := make([]string, 0, (len(m.frames)+perRow-1)/perRow)
	for start := 0; start < len(m.frames); start += perRow {
		end := start + perRow
		if end > len(m.frames) {
			end = len(m.frames)
		}
		cards := make([]string, 0, (end-start)*2-1)
		for i := start; i < end; i++ {
			if i > start {
				cards = append(cards, gap)
			}
			cards = append(cards, m.renderCard(i))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
	}
	return strings.Join(rows, "\n\n")
}

// renderCard paints one frame as a bordered 3-line tile: glyph, name,
// numeric badge. Selected: accent-colored rounded border, bold name,
// bracketed badge. Non-selected: muted border, dim name, plain
// numeric badge. Glyph always carries its frame-accent colour so the
// colour-coded glance still works at rest.
func (m inlinePickerModel) renderCard(i int) string {
	f := m.frames[i]
	isCursor := i == m.cursor

	glyph := f.Glyph
	if glyph == "" {
		glyph = frame.DefaultGlyphFor(f.Name)
	}
	glyphStyle := lipgloss.NewStyle()
	if col := frame.AccentColor(f.Accent); col != "" {
		glyphStyle = glyphStyle.Foreground(col)
	}
	glyphStyle = glyphStyle.Bold(true)

	nameStyle := lipgloss.NewStyle().Foreground(m.pal.Subtle)
	if isCursor {
		nameStyle = lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true)
	}
	name := f.Name
	if lipgloss.Width(name) > cardInnerW-2 {
		name = name[:cardInnerW-3] + "…"
	}

	num := fmt.Sprintf("%d", i+1)
	if i >= 9 {
		num = "·"
	}
	if isCursor {
		num = "[" + num + "]"
	}
	numStyle := lipgloss.NewStyle().Foreground(m.pal.Muted)
	if isCursor {
		numStyle = lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true)
	}

	body := lipgloss.JoinVertical(
		lipgloss.Center,
		glyphStyle.Render(glyph),
		nameStyle.Render(name),
		numStyle.Render(num),
	)

	borderColor := m.pal.Subtle
	if isCursor {
		borderColor = m.pal.Accent
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(cardInnerW).
		Align(lipgloss.Center).
		Render(body)
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
