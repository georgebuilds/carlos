package chat

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/tui/slash"
)

// slashSuggest is the autocomplete state for the chat composer's
// "slash mode". Activates whenever the textarea's value starts with
// "/" and no modal overlay is up. The state powers three surfaces:
//
//   - Inline ghost text on the input row itself (fish-style): the
//     currently-selected suggestion paints in dim color after the
//     cursor so the user sees what tab will complete to.
//   - A thin hint band above the input separator: a one-row palette
//     of alternative matches plus a one-row description of the
//     selected spec.
//   - A keybind reminder row pinned just below the hint band.
//
// We deliberately avoid a bordered popup. The slash composer is a
// passive aid; a heavy floating panel competes with the transcript
// and pushes input down the screen. Light, inline, ghost-style.
type slashSuggest struct {
	// open is true iff the current textarea value starts with "/"
	// AND the user hasn't explicitly dismissed the suggest with Esc.
	open bool

	// dismissed is set by Esc and cleared whenever the input leaves
	// slash mode (value no longer starts with "/"). Lets the user
	// silence the panel without erasing their typed input.
	dismissed bool

	// matches is the filtered Spec list under the user's verb. Stays
	// ordered by Builtins so the popup matches the help-panel reading
	// order. Empty when the verb is unknown.
	matches []slash.Spec

	// cursor is the index into matches under the user's selection.
	// Wraps with ↑↓; Tab uses it to pick the completion.
	cursor int

	// verb is the lowercased fragment before the first space (e.g.
	// "fr" for "/fr", "frame" for "/frame switch ..."). Empty when
	// the user has only typed "/".
	verb string

	// inArgs is true once the user has typed past the verb (a space
	// or further). Triggers the args-hint render mode.
	inArgs bool
}

// refreshSlashSuggest updates the suggest state from the current
// textarea value. Idempotent; safe to call after every keystroke.
// Tries to keep the cursor stable across narrowing: when the
// previous selection still appears in the new matches list, we
// preserve its position by index-into-matches.
func (s *slashSuggest) refresh(value string) {
	if !looksLikeSlash(value) {
		s.reset()
		return
	}
	if s.dismissed {
		// User pressed Esc but kept typing. Leave the band hidden
		// until they exit slash mode (clearing the prefix re-arms).
		s.open = false
		s.matches = nil
		s.cursor = 0
		s.verb = ""
		s.inArgs = false
		return
	}
	prev, hadPrev := s.selected()
	matches, verb, inArgs := slash.Filter(value)
	s.open = true
	s.matches = matches
	s.verb = verb
	s.inArgs = inArgs
	// Recenter the cursor on the previous selection when still present;
	// otherwise pin to 0 so the first match is the obvious default.
	s.cursor = 0
	if hadPrev {
		for i, m := range matches {
			if m.Name == prev.Name {
				s.cursor = i
				break
			}
		}
	}
}

// dismiss is the Esc handler: hide the band without erasing the
// user's typed input. Cleared automatically the next time refresh
// sees a value that doesn't start with "/".
func (s *slashSuggest) dismiss() {
	s.open = false
	s.dismissed = true
}

func (s *slashSuggest) reset() {
	*s = slashSuggest{}
}

// selected returns the spec under the cursor, or (Spec{}, false).
func (s *slashSuggest) selected() (slash.Spec, bool) {
	if !s.open || len(s.matches) == 0 {
		return slash.Spec{}, false
	}
	if s.cursor < 0 || s.cursor >= len(s.matches) {
		return slash.Spec{}, false
	}
	return s.matches[s.cursor], true
}

func (s *slashSuggest) cursorUp() {
	if !s.open || len(s.matches) == 0 {
		return
	}
	s.cursor--
	if s.cursor < 0 {
		s.cursor = len(s.matches) - 1
	}
}

func (s *slashSuggest) cursorDown() {
	if !s.open || len(s.matches) == 0 {
		return
	}
	s.cursor++
	if s.cursor >= len(s.matches) {
		s.cursor = 0
	}
}

// handleSlashSuggestKey processes a keystroke when slash mode is
// active. Returns handled=true when the key was consumed by the
// suggest layer; the caller must NOT then route the key to the
// textarea. Tab completes to the selected verb (with a trailing
// space when the spec takes args). ↑↓ navigate the matches list
// (and only intercept when more than one match exists, so the user
// can keep using ↑↓ as ordinary textarea motion when there's
// nothing to navigate). Esc dismisses the band without erasing the
// input.
//
// Returns the optional tea.Cmd as a courtesy for symmetry with the
// rest of the overlay handlers; today no command is ever issued.
func (m *Model) handleSlashSuggestKey(key string) (tea.Cmd, bool) {
	if !m.slashSuggest.open || m.readOnly {
		return nil, false
	}
	switch key {
	case "tab":
		completion := m.slashSuggest.completion()
		if completion == "" {
			return nil, true
		}
		m.ta.SetValue(completion)
		m.ta.CursorEnd()
		m.slashSuggest.refresh(completion)
		return nil, true
	case "up":
		if len(m.slashSuggest.matches) > 1 {
			m.slashSuggest.cursorUp()
			return nil, true
		}
	case "down":
		if len(m.slashSuggest.matches) > 1 {
			m.slashSuggest.cursorDown()
			return nil, true
		}
	case "esc":
		m.slashSuggest.dismiss()
		return nil, true
	}
	return nil, false
}

// completion returns the textarea replacement value when Tab is
// pressed: the full slash command up to (and including) the verb,
// with a trailing space when the spec takes args so the user
// immediately enters arg-entry mode. Returns "" when there's nothing
// to complete (no matches, or user typed "/" with no selection
// preference yet — Tab is a no-op).
func (s *slashSuggest) completion() string {
	spec, ok := s.selected()
	if !ok {
		return ""
	}
	if spec.ArgsHint != "" {
		return "/" + spec.Name + " "
	}
	return "/" + spec.Name
}

// looksLikeSlash mirrors the predicate slash.Filter uses internally,
// hoisted so the chat package can decide whether to keep ghost text
// engaged without re-filtering.
func looksLikeSlash(value string) bool {
	return strings.HasPrefix(strings.TrimLeft(value, " \t"), "/")
}

// renderSlashHint paints the two-row hint band that sits above the
// input separator while slash mode is active.
//
// Row 1 — alternates palette: dim chips of every matching command,
// the selected one bumped into the accent color + bold. When only
// one match exists (args mode, or a unique prefix), this row
// collapses into the description so the band stays compact.
//
// Row 2 — description / args hint: the selected spec's description,
// preceded by a turnstile glyph so the eye locks onto it.
//
// Row 3 — keybinds: ↑↓ select · tab complete · esc cancel.
//
// Returns "" when slash mode is off (lets the caller skip rendering
// without an extra branch).
func renderSlashHint(s slashSuggest, w int) string {
	if !s.open {
		return ""
	}
	// Indent so the band sits under the textarea's prompt gutter,
	// reading as a continuation of the composer rather than a popup.
	const indent = "  "
	contentW := w - len(indent)
	if contentW < 20 {
		contentW = 20
	}

	rows := make([]string, 0, 3)

	if row := renderSlashChips(s, contentW); row != "" {
		rows = append(rows, indent+row)
	}
	if row := renderSlashDescription(s, contentW); row != "" {
		rows = append(rows, indent+row)
	}
	rows = append(rows, indent+renderSlashKeyHints())

	return strings.Join(rows, "\n")
}

// renderSlashChips lays out matching command names as a single-line
// palette: selected in accent+bold, others in muted. The visible
// window slides so the cursor stays in view, with "+N" markers on
// either side to advertise the off-screen matches.
func renderSlashChips(s slashSuggest, w int) string {
	// In args mode with a single match, the chips row is redundant
	// with the description row — fold it.
	if s.inArgs {
		return ""
	}
	if len(s.matches) == 0 {
		warnStyle := lipgloss.NewStyle().Foreground(colorWarn)
		return warnStyle.Render("no matches for /" + s.verb)
	}
	if len(s.matches) == 1 {
		// Single match reads as a "definite hit"; the description
		// row alone carries it.
		return ""
	}
	selStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorMuted)
	moreStyle := lipgloss.NewStyle().Foreground(colorSubtle)

	// Slide the visible window so the cursor stays in view, with two
	// chips of context to its left when possible. Without this, the
	// user pressing ↓ past the visible window saw the description
	// row update but the chips row stayed frozen, confusing the
	// "what's highlighted right now?" read.
	const cushion = 2
	start := 0
	if s.cursor > cushion {
		start = s.cursor - cushion
	}

	const sep = "  "
	used := 0
	parts := make([]string, 0, len(s.matches)+2)

	// Left overflow marker advertises the chips we've scrolled past.
	if start > 0 {
		left := moreStyle.Render("+" + itoa(start) + " · ")
		parts = append(parts, left)
		used += lipgloss.Width(left)
	}

	for i := start; i < len(s.matches); i++ {
		chip := "/" + s.matches[i].Name
		render := dimStyle.Render(chip)
		if i == s.cursor {
			render = selStyle.Render(chip)
		}
		width := lipgloss.Width(chip)
		if i > start {
			width += len(sep)
		}
		// Reserve 6 cells for the right overflow tail.
		if used+width > w-6 {
			remaining := len(s.matches) - i
			parts = append(parts, moreStyle.Render(sep+"+"+itoa(remaining)))
			return strings.Join(parts, "")
		}
		if i > start {
			parts = append(parts, dimStyle.Render(sep))
		}
		parts = append(parts, render)
		used += width
	}
	return strings.Join(parts, "")
}

// renderSlashDescription is the second hint row: a turnstile glyph
// plus the selected spec's description, with the args hint dimly
// trailing so the user sees what fills the verb out.
func renderSlashDescription(s slashSuggest, w int) string {
	spec, ok := s.selected()
	if !ok {
		return ""
	}
	glyphStyle := lipgloss.NewStyle().Foreground(colorAccent)
	nameStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	argsStyle := lipgloss.NewStyle().Foreground(colorSubtle).Italic(true)
	descStyle := lipgloss.NewStyle().Foreground(colorMuted)

	parts := []string{
		glyphStyle.Render("↳ "),
		nameStyle.Render("/" + spec.Name),
	}
	if spec.ArgsHint != "" {
		parts = append(parts, " "+argsStyle.Render(spec.ArgsHint))
	}
	if spec.Description != "" {
		parts = append(parts, "  "+descStyle.Render(truncateRight(spec.Description, w-lipgloss.Width(strings.Join(parts, ""))-2)))
	}
	return strings.Join(parts, "")
}

func renderSlashKeyHints() string {
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dim := lipgloss.NewStyle().Foreground(colorSubtle)
	sep := dim.Render("  ·  ")
	return strings.Join([]string{
		keyStyle.Render("↑↓") + dim.Render(" select"),
		keyStyle.Render("tab") + dim.Render(" complete"),
		keyStyle.Render("enter") + dim.Render(" send"),
		keyStyle.Render("esc") + dim.Render(" cancel"),
	}, sep)
}

// renderSlashInputRow replaces ta.View() while slash mode is engaged
// so we can inline the ghost-text completion on the same visual row
// as the user's text. The textarea remains the source of truth for
// the value (key events still route through ta.Update); we only
// override the *render*.
//
// Layout for the first (visible) row:
//
//	│ /fr|ame                       ← /fr in accent, ame as dim ghost
//	│ /frame |[list|switch ...]     ← args ghost after verb-complete
//
// The cursor is drawn using the textarea's own cursor.Model so the
// blink stays in sync with the existing textarea.Blink command. We
// pad to taHeight rows so the band height matches what ta.View()
// would have produced — otherwise the layout under us jitters as the
// user types.
func renderSlashInputRow(m *Model, w int) string {
	prompt := m.ta.Prompt
	value := m.ta.Value()

	spec, hasSel := m.slashSuggest.selected()
	var ghost string
	if hasSel {
		ghost = slash.Ghost(value, spec)
	}

	// Style the user-typed slash verb in accent so it pops against
	// the dim ghost. The args portion (after the first space) stays
	// in the default color since the user has already chosen.
	valueRender := styleSlashValue(value)
	cursorView := m.ta.Cursor.View()
	ghostRender := styleSlashGhost(ghost)

	firstRow := prompt + valueRender + cursorView + ghostRender
	// Pad to width with a no-op so background highlights from the
	// underlying renderer don't extend past the row.
	firstRow = padRight(firstRow, w)

	// Subsequent rows: an empty prompt-only row, mimicking what the
	// textarea would render when value is single-line.
	emptyRow := padRight(prompt, w)

	rows := []string{firstRow}
	for i := 1; i < taHeight(m); i++ {
		rows = append(rows, emptyRow)
	}
	return strings.Join(rows, "\n")
}

// styleSlashValue colors a slash command line: the leading "/" + verb
// fragment in accent, anything after the first space in the default
// text color so the user's args stand on their own. Non-slash input
// (we get called defensively from places that don't pre-check)
// renders as-is.
func styleSlashValue(value string) string {
	leading := value[:len(value)-len(strings.TrimLeft(value, " \t"))]
	trimmed := strings.TrimLeft(value, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return value
	}
	verb, rest, hasSpace := strings.Cut(trimmed, " ")
	verbStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	out := leading + verbStyle.Render(verb)
	if hasSpace {
		out += " " + rest
	}
	return out
}

// styleSlashGhost dims the ghost suggestion and italicizes it so it
// reads as "not yet typed" against the user's solid text.
func styleSlashGhost(ghost string) string {
	if ghost == "" {
		return ""
	}
	return lipgloss.NewStyle().Foreground(colorSubtle).Italic(true).Render(ghost)
}

// taHeight returns the textarea's rendered height so our custom
// slash-mode renderer pads to match. The bubbles textarea exposes
// SetHeight but not GetHeight in this version, so we mirror the
// constant set in chat.New (3) and let chat update it through a
// helper if that ever changes.
func taHeight(m *Model) int {
	// Mirrors the SetHeight(3) call in chat.New. Hard-coded rather
	// than reflected so a test setup that builds a bare Model still
	// renders a stable height.
	_ = m
	return 3
}

// padRight extends s with spaces (no styling) so its visual width is
// at least w. Wider strings pass through untouched — clipping is the
// caller's problem.
func padRight(s string, w int) string {
	used := lipgloss.Width(s)
	if used >= w {
		return s
	}
	return s + strings.Repeat(" ", w-used)
}

// truncateRight clips s to at most w visual cells, adding an ellipsis
// when truncated. Used by the description row so a long carlos-
// specific description never pushes the keybind row off-screen.
func truncateRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if w < 2 {
		return "…"
	}
	cut := w - 1
	if cut > len(s) {
		cut = len(s)
	}
	return s[:cut] + "…"
}

// itoa is a tiny strconv.Itoa shim so suggest.go doesn't pull strconv
// just for one call site (the chip overflow tail). Negative inputs
// can't happen here (counts only) so we skip the sign branch.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
