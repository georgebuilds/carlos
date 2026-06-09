// Phase F-5: full-screen takeover frame switcher.
//
// Bound to Ctrl+F (toggle). When open, the chat content dims and a 3x2
// grid of tiles floats above the transcript. Each tile is a rounded-
// border box with the frame's glyph centered large + name underneath;
// the active frame's tile wears a thick border in the frame's accent.
//
// A "+ new frame" placeholder sits in the next empty slot but is not
// actionable here - the wizard ships in F-10. This slice only owns the
// visual switcher + keyboard navigation + the existing SwitchActive
// hook.
//
// Responsive: 3 columns when innerW >= 100, 2 columns at 70-99, 1
// column (tall list) below 70. The 1-col fallback is the accepted
// degraded mode.
//
// Pagination: when more than `visible` frames exist for the chosen
// column count, Ctrl+← / Ctrl+→ flip pages; a footer counter renders
// "page n/N".
//
// Animation: omitted in v1. The brief flagged it as optional, and
// wiring harmonica into the chat tick loop would be more risk than the
// micro-polish is worth right now. The static render is the load-
// bearing piece.

package chat

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/frame"
)

const (
	switcherTileWidth  = 26
	switcherTileHeight = 8
	switcherGridRows   = 2
)

// switcherColumns returns the responsive column count for the given
// inner width per the brief's breakpoints.
func switcherColumns(innerW int) int {
	switch {
	case innerW >= 100:
		return 3
	case innerW >= 70:
		return 2
	default:
		return 1
	}
}

// switcherVisible returns how many tiles fit on one page including the
// "+ new frame" placeholder slot.
func switcherVisible(innerW int) int {
	return switcherColumns(innerW) * switcherGridRows
}

// switcherPageCount returns the number of pages needed to display N
// real frames + one "+ new frame" placeholder.
func switcherPageCount(nFrames, innerW int) int {
	visible := switcherVisible(innerW)
	if visible <= 0 {
		return 1
	}
	total := nFrames + 1
	pages := (total + visible - 1) / visible
	if pages < 1 {
		pages = 1
	}
	return pages
}

// switcherPageBounds returns the [start, end) frame indices visible on
// `page` for the given column count. The end is clamped at nFrames so
// callers know when the "+ new frame" tile fits in the trailing slot.
func switcherPageBounds(nFrames, innerW, page int) (start, end int) {
	visible := switcherVisible(innerW)
	if visible <= 0 {
		return 0, 0
	}
	start = page * visible
	end = start + visible
	if end > nFrames {
		end = nFrames
	}
	if start > nFrames {
		start = nFrames
	}
	return start, end
}

// renderFrameSwitcher composes the takeover overlay. Returns the
// complete block - caller (renderInner) stacks it where the other
// overlays sit. width/height are the chat box's inner dimensions.
//
// The grid is centered horizontally; vertically the tiles sit roughly
// in the middle of the chat area with a header above and a footer of
// keybinds below. When showHelp (the in-overlay ? toggle) is true a
// small help line replaces the default footer.
func renderFrameSwitcher(
	ui FrameUI,
	cursor, page int,
	innerW, innerH int,
	showHelp bool,
) string {
	cols := switcherColumns(innerW)
	visible := switcherVisible(innerW)
	pages := switcherPageCount(len(ui.Available), innerW)
	if page >= pages {
		page = pages - 1
	}
	if page < 0 {
		page = 0
	}
	start, end := switcherPageBounds(len(ui.Available), innerW, page)

	header := renderSwitcherHeader(ui.Active, page, pages, innerW)
	body := renderSwitcherGrid(ui, cursor, start, end, cols, visible, innerW)
	footer := renderSwitcherFooter(showHelp, pages > 1)

	parts := []string{header, "", body, "", footer}
	block := strings.Join(parts, "\n")

	// Pad vertically so the grid sits roughly centered in the chat area
	// - purely cosmetic; the caller's renderInner already reserves the
	// full inner height for the overlay.
	if innerH > lipgloss.Height(block)+2 {
		topPad := (innerH - lipgloss.Height(block)) / 3
		if topPad > 0 {
			block = strings.Repeat("\n", topPad) + block
		}
	}
	return block
}

func renderSwitcherHeader(active string, page, pages, width int) string {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)

	left := titleStyle.Render("frames")
	if active != "" {
		left += "  " + mutedStyle.Render("active: "+active)
	}
	var right string
	if pages > 1 {
		right = mutedStyle.Render(fmt.Sprintf("page %d/%d", page+1, pages))
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return lipgloss.PlaceHorizontal(width, lipgloss.Left,
		left+strings.Repeat(" ", gap)+right)
}

func renderSwitcherGrid(
	ui FrameUI,
	cursor, start, end, cols, visible, width int,
) string {
	tiles := make([]string, 0, visible)
	for i := start; i < end; i++ {
		name := ui.Available[i]
		tileMode := ""
		if ui.Active == name {
			tileMode = ui.Mode
		}
		tile := renderSwitcherTile(
			name,
			ui.Glyph,        // active glyph; non-active tiles use DefaultGlyphFor inside
			ui.Active == name,
			i == cursor,
			ui.Active,
			tileMode,
		)
		tiles = append(tiles, tile)
	}
	// "+ new frame" placeholder occupies the next slot when it fits on
	// this page. The wizard is F-10; the placeholder lives at
	// position len(Available), and the cursor can land on it (the
	// switcherCursorOnNewTile predicate). When focused we paint it
	// in accent color so the user can tell it's selected — without
	// that, the grey-on-grey tile reads as "non-actionable" even
	// when it's the active selection.
	if len(tiles) < visible {
		newFrameFocused := cursor == len(ui.Available) && end == len(ui.Available)
		tiles = append(tiles, renderSwitcherNewFrameTile(newFrameFocused))
	}
	// Pad remaining slots with empty placeholders so the row math holds.
	for len(tiles) < visible {
		tiles = append(tiles, renderSwitcherEmptySlot())
	}

	rows := make([]string, 0, switcherGridRows)
	for r := 0; r < switcherGridRows; r++ {
		rowTiles := tiles[r*cols : (r+1)*cols]
		row := lipgloss.JoinHorizontal(lipgloss.Top, joinWithGap(rowTiles, "  ")...)
		rows = append(rows, lipgloss.PlaceHorizontal(width, lipgloss.Center, row))
	}
	return strings.Join(rows, "\n")
}

// joinWithGap interleaves a 2-cell gap between tiles for visual breathing
// room. lipgloss.JoinHorizontal won't add inter-tile spacing on its own.
func joinWithGap(tiles []string, gap string) []string {
	if len(tiles) <= 1 {
		return tiles
	}
	out := make([]string, 0, len(tiles)*2-1)
	for i, t := range tiles {
		if i > 0 {
			out = append(out, gap)
		}
		out = append(out, t)
	}
	return out
}

// renderSwitcherTile paints one frame's tile. Each tile uses its
// frame's accent color so the switcher reads as a palette of
// distinct identities, not a uniform grid in the global theme
// accent. Active tiles wear a thick frame-accent border; focused
// (cursor) tiles wear a thick frame-accent border too, so the
// cursor moving across the grid feels like the highlight changes
// hue tile-by-tile. Idle tiles get a rounded subtle border so the
// grid scaffolds visibly.
func renderSwitcherTile(name, activeGlyph string, isActive, isFocused bool, activeName, mode string) string {
	glyph := activeGlyph
	if !isActive || glyph == "" {
		glyph = frame.DefaultGlyphFor(name)
	}

	accentName := name
	col := frame.AccentColor(accentName)
	if col == "" {
		col = colorAccent
	}

	glyphStyle := lipgloss.NewStyle().Foreground(col).Bold(true)
	// Name uses the FRAME's own accent so the user sees the
	// identity color repeated on every label, not the global theme
	// accent that's identical across every tile.
	nameStyle := lipgloss.NewStyle().Foreground(col)
	if isFocused || isActive {
		nameStyle = nameStyle.Bold(true)
	} else {
		// Idle (not focused, not active): dim the name so the
		// focused / active tile pops by contrast.
		nameStyle = lipgloss.NewStyle().Foreground(colorMuted)
	}

	var summary string
	if isActive {
		label := "active"
		if mode != "" && mode != "solo" {
			label = "active · " + mode
		}
		summary = lipgloss.NewStyle().Foreground(colorSubtle).Italic(true).Render(label)
	} else if isFocused {
		summary = lipgloss.NewStyle().Foreground(colorSubtle).Italic(true).Render("press enter")
	} else {
		summary = " "
	}

	body := lipgloss.JoinVertical(lipgloss.Center,
		"",
		glyphStyle.Render(glyph),
		"",
		nameStyle.Render(name),
		summary,
	)

	border := lipgloss.RoundedBorder()
	borderColor := colorSubtle
	switch {
	case isActive:
		border = lipgloss.ThickBorder()
		borderColor = col
	case isFocused:
		border = lipgloss.ThickBorder()
		borderColor = col
	}

	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColor).
		Width(switcherTileWidth - 2).
		Height(switcherTileHeight - 2).
		Align(lipgloss.Center).
		Render(body)
}

// renderSwitcherNewFrameTile paints the trailing "+ new frame" tile.
// focused=true means the cursor is on it: the border and label
// promote from muted/subtle to accent + bold so the selection state
// is unmistakable. The "(F-10)" hint stays muted in both modes so it
// reads as ancillary affordance, not part of the selection signal.
func renderSwitcherNewFrameTile(focused bool) string {
	glyphStyle := lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(colorMuted)
	borderColor := colorSubtle
	border := lipgloss.RoundedBorder()
	if focused {
		glyphStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
		labelStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
		borderColor = colorAccent
		border = lipgloss.ThickBorder()
	}
	plus := glyphStyle.Render("+")
	label := labelStyle.Render("new frame")
	hint := lipgloss.NewStyle().Foreground(colorSubtle).Italic(true).Render("(F-10)")
	body := lipgloss.JoinVertical(lipgloss.Center,
		"",
		plus,
		"",
		label,
		hint,
	)
	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColor).
		Width(switcherTileWidth - 2).
		Height(switcherTileHeight - 2).
		Align(lipgloss.Center).
		Render(body)
}

// renderSwitcherEmptySlot is an invisible filler used when the last
// page has fewer than `visible` tiles, so the grid columns line up.
func renderSwitcherEmptySlot() string {
	return lipgloss.NewStyle().
		Width(switcherTileWidth).
		Height(switcherTileHeight).
		Render("")
}

func renderSwitcherFooter(showHelp, paginated bool) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(colorMuted)
	subtleStyle := lipgloss.NewStyle().Foreground(colorSubtle)

	if showHelp {
		help := []string{
			keyStyle.Render("↑↓←→ / hjkl") + bodyStyle.Render(" move"),
			keyStyle.Render("1-6") + bodyStyle.Render(" jump"),
			keyStyle.Render("enter") + bodyStyle.Render(" switch"),
			keyStyle.Render("esc / ctrl+f") + bodyStyle.Render(" close"),
		}
		if paginated {
			help = append(help, keyStyle.Render("ctrl+←/→")+bodyStyle.Render(" page"))
		}
		help = append(help, keyStyle.Render("?")+bodyStyle.Render(" hide help"))
		return subtleStyle.Render("  ") + strings.Join(help, bodyStyle.Render("  ·  "))
	}

	parts := []string{
		keyStyle.Render("enter") + bodyStyle.Render(" switch"),
		keyStyle.Render("esc") + bodyStyle.Render(" close"),
		keyStyle.Render("?") + bodyStyle.Render(" help"),
	}
	return subtleStyle.Render("  ") + strings.Join(parts, bodyStyle.Render("  ·  "))
}

// handleFrameSwitcherKey routes one key while the takeover is open.
// Returns (newModel, cmd, handled). When handled=false the caller
// falls through to the normal Update routing (only ctrl+c today).
func (m *Model) handleFrameSwitcherKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		return m, nil, false
	case "esc", "ctrl+f":
		m.closeFrameSwitcher()
		return m, nil, true
	case "enter":
		// Enter on the "+ new frame" tile opens the F-10 wizard;
		// enter on a regular tile commits a switch as before.
		if m.switcherCursorOnNewTile() {
			m.openNewFrameWizard("")
			return m, nil, true
		}
		return m, m.frameSwitcherCommit(), true
	case "n", "N":
		// Phase F-10: shortcut to the wizard from anywhere in the
		// switcher; mirrors gmail/vim's "n" for new.
		m.openNewFrameWizard("")
		return m, nil, true
	case "up", "k":
		m.switcherMoveVertical(-1)
		return m, nil, true
	case "down", "j":
		m.switcherMoveVertical(1)
		return m, nil, true
	case "left", "h":
		m.switcherMoveHorizontal(-1)
		return m, nil, true
	case "right", "l":
		m.switcherMoveHorizontal(1)
		return m, nil, true
	case "ctrl+left":
		m.switcherPagePrev()
		return m, nil, true
	case "ctrl+right":
		m.switcherPageNext()
		return m, nil, true
	case "?":
		m.switcherHelp = !m.switcherHelp
		m.rerenderViewport()
		return m, nil, true
	case "1", "2", "3", "4", "5", "6":
		idx := int(msg.String()[0]-'0') - 1
		m.switcherJumpTo(idx)
		return m, nil, true
	}
	return m, nil, true
}

// switcherCursorOnNewTile reports whether the focused index is the
// trailing "+ new frame" placeholder. The placeholder lives at
// len(Available) - one slot past the last real frame.
func (m *Model) switcherCursorOnNewTile() bool {
	return m.switcherCursor == len(m.frame.Available)
}

// closeFrameSwitcher resets the overlay state. Idempotent.
func (m *Model) closeFrameSwitcher() {
	m.showFrameSwitcher = false
	m.switcherHelp = false
	m.rerenderViewport()
}

// frameSwitcherCommit fires SwitchActive for the focused frame, closes
// the overlay, and returns a status echo. Defends against a stale
// cursor pointing at an empty Available slice.
func (m *Model) frameSwitcherCommit() tea.Cmd {
	defer m.closeFrameSwitcher()
	if len(m.frame.Available) == 0 || m.switcherCursor >= len(m.frame.Available) {
		return nil
	}
	target := m.frame.Available[m.switcherCursor]
	if target == m.frame.Active {
		return statusCmd("already in frame "+target, statusInfo)
	}
	if m.frame.SwitchActive == nil {
		return statusCmd("frame switching not wired in this session", statusWarn)
	}
	if err := m.frame.SwitchActive(target); err != nil {
		return statusCmd("switch failed: "+err.Error(), statusWarn)
	}
	m.frame.Active = target
	return statusCmd(
		"switched to "+target+
			" (provider/model take effect at next session start)",
		statusInfo,
	)
}

// switcherMoveVertical moves the cursor by one row in the responsive
// grid. Clamps at the bounds; arrow keys do not wrap. The "+ new
// frame" placeholder counts as one extra slot at index len(Available)
// so cursor nav can reach it.
func (m *Model) switcherMoveVertical(delta int) {
	cols := switcherColumns(m.switcherInnerW())
	target := m.switcherCursor + delta*cols
	if target < 0 || target > len(m.frame.Available) {
		return
	}
	m.switcherCursor = target
	m.alignPageToCursor()
	m.rerenderViewport()
}

// switcherMoveHorizontal moves the cursor by one column. Clamps at the
// row boundary so arrow keys do not jump across rows. The "+ new
// frame" tile counts as one extra slot at index len(Available).
func (m *Model) switcherMoveHorizontal(delta int) {
	cols := switcherColumns(m.switcherInnerW())
	row := m.switcherCursor / cols
	col := m.switcherCursor%cols + delta
	if col < 0 || col >= cols {
		return
	}
	target := row*cols + col
	if target > len(m.frame.Available) {
		return
	}
	m.switcherCursor = target
	m.alignPageToCursor()
	m.rerenderViewport()
}

// switcherJumpTo selects the Nth tile on the current page (1-indexed
// in the keymap, 0-indexed here). No-op when the index points beyond
// the available frames on this page.
func (m *Model) switcherJumpTo(idx int) {
	visible := switcherVisible(m.switcherInnerW())
	if idx < 0 || idx >= visible {
		return
	}
	target := m.switcherPage*visible + idx
	if target >= len(m.frame.Available) {
		return
	}
	m.switcherCursor = target
	m.rerenderViewport()
}

func (m *Model) switcherPagePrev() {
	pages := switcherPageCount(len(m.frame.Available), m.switcherInnerW())
	if pages <= 1 || m.switcherPage == 0 {
		return
	}
	m.switcherPage--
	visible := switcherVisible(m.switcherInnerW())
	m.switcherCursor = m.switcherPage * visible
	m.rerenderViewport()
}

func (m *Model) switcherPageNext() {
	pages := switcherPageCount(len(m.frame.Available), m.switcherInnerW())
	if pages <= 1 || m.switcherPage >= pages-1 {
		return
	}
	m.switcherPage++
	visible := switcherVisible(m.switcherInnerW())
	m.switcherCursor = m.switcherPage * visible
	if m.switcherCursor >= len(m.frame.Available) {
		m.switcherCursor = len(m.frame.Available) - 1
	}
	m.rerenderViewport()
}

// alignPageToCursor pulls switcherPage in line when the cursor moves
// off the current visible window. Vertical/horizontal nav keeps the
// user on the same page in normal use, but a /frame switch from a
// slash + reopen can land us out-of-window.
func (m *Model) alignPageToCursor() {
	visible := switcherVisible(m.switcherInnerW())
	if visible <= 0 {
		return
	}
	m.switcherPage = m.switcherCursor / visible
}

// switcherInnerW reports the inner width used for column math. Falls
// back to a sensible default when the WindowSizeMsg hasn't landed yet
// (test paths that call key handlers before View runs).
func (m *Model) switcherInnerW() int {
	if m.width <= 0 {
		return 100
	}
	// Mirror View()'s math: border eats 2 cols, padding eats 2 more.
	w := m.width - 4
	if w < 30 {
		w = 30
	}
	return w
}

// openFrameSwitcher is the toggle entrypoint called from chat.Update
// when Ctrl+F lands and frames are wired. Snaps the cursor to the
// active frame so the user starts where they expect.
func (m *Model) openFrameSwitcher() {
	m.showFrameSwitcher = true
	m.switcherHelp = false
	m.switcherCursor = 0
	for i, n := range m.frame.Available {
		if n == m.frame.Active {
			m.switcherCursor = i
			break
		}
	}
	m.alignPageToCursor()
	m.rerenderViewport()
}
