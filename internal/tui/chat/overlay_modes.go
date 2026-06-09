// Mode switcher: full-screen takeover card-picker for the three
// orchestrator modes (tight / solo / orchestrator).
//
// Bound to Ctrl+O (toggle). When open the chat content dims and a
// single row of three tall cards floats above the transcript. Each
// card is a magic-card-feel tile with terminal art that hints at the
// mode's posture: a clamped laser beam for tight, a lone pillar for
// solo, a branching delegation tree for orchestrator.
//
// Why Ctrl+O instead of Ctrl+M: in Bubbletea, KeyCtrlM and KeyEnter
// share the keyCR codepoint (literally the same 0x0D byte on macOS
// Terminal, which doesn't speak the Kitty keyboard protocol). Binding
// Ctrl+M would shadow Enter and break message submission. Ctrl+O is
// the closest free mnemonic - "mode of operation" - and pairs nicely
// with the existing Ctrl+F (frame switcher) sibling.
//
// Card art is plain Unicode box-drawing + block-elements - rendered
// 1:1 in macOS Terminal. No Kitty graphics, no sixel, no Nerd Fonts.

package chat

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/frame"
)

const (
	modeCardWidth  = 30
	modeCardHeight = 16
)

// modeOption is one card in the picker. Ordered left → right so the
// cursor index 0/1/2 maps to tight/solo/orchestrator and the user can
// reason about position without consulting a key.
type modeOption struct {
	name    string   // canonical mode id (frame.ModeTight / ModeSolo / ModeOrchestrator)
	title   string   // display title in caps
	tagline string   // one-line description under the title
	art     []string // pre-laid-out art lines, centered inside the card body
}

// modeOptions is the ordered list painted by the switcher. Position is
// load-bearing (left/right arrow nav, card colors). Keep tight first
// and orchestrator last so the row reads as a constraint → freedom
// gradient.
var modeOptions = []modeOption{
	{
		name:    frame.ModeTight,
		title:   "TIGHT",
		tagline: "strictly single-thread",
		art: []string{
			"┌──[ focus ]──┐",
			"│      ║      │",
			"│      ║      │",
			"│      ║      │",
			"│      ║      │",
			"│      ║      │",
			"│      ▼      │",
			"│      •      │",
			"└─────────────┘",
		},
	},
	{
		name:    frame.ModeSolo,
		title:   "SOLO",
		tagline: "the lone wanderer",
		art: []string{
			"   ┌─┐   ",
			"   │ │   ",
			"   │ │   ",
			"   │ │   ",
			"   │ │   ",
			"   │█│   ",
			"   │█│   ",
			"   │█│   ",
			"  ─┴─┴─  ",
		},
	},
	{
		name:    frame.ModeOrchestrator,
		title:   "ORCHESTRATOR",
		tagline: "subagent-forward",
		art: []string{
			"      ▣      ",
			"     ╱│╲     ",
			"    ╱ │ ╲    ",
			"   ▢  ▢  ▢   ",
			"  ╱│  │  │╲  ",
			" ▢ ▢  ▢  ▢ ▢ ",
			"             ",
			"   ↘  ↓  ↙   ",
			"   results   ",
		},
	},
}

// modeIndexFromName returns the cursor position for a canonical mode
// id, or 1 (solo) when the name isn't one of the three known modes.
// Solo is the documented default fallback per internal/frame.EffectiveMode
// so the cursor lands somewhere sensible even when the wired value is
// empty or stale.
func modeIndexFromName(name string) int {
	for i, opt := range modeOptions {
		if opt.name == name {
			return i
		}
	}
	return 1
}

// modeCardAccent returns the foreground color for a mode card. The
// gradient is intentional: tight uses the warn color (constrained,
// careful), solo uses the global accent (the user's brand identity),
// orchestrator uses the ok color (expansive, capability-on). Reads as
// a temperature scale across the three cards even in monochrome
// because the cards are still positional.
func modeCardAccent(name string) lipgloss.Color {
	switch name {
	case frame.ModeTight:
		return colorWarn
	case frame.ModeOrchestrator:
		return colorOK
	default:
		return colorAccent
	}
}

// renderModeSwitcher composes the takeover overlay. Returns the
// complete block - caller (renderInner) stacks it where the other
// overlays sit. width/height are the chat box's inner dimensions.
func renderModeSwitcher(
	ui FrameUI,
	cursor int,
	innerW, innerH int,
	showHelp bool,
) string {
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(modeOptions) {
		cursor = len(modeOptions) - 1
	}

	header := renderModeSwitcherHeader(ui.Mode, innerW)
	body := renderModeSwitcherRow(ui, cursor, innerW)
	footer := renderModeSwitcherFooter(showHelp)

	parts := []string{header, "", body, "", footer}
	block := strings.Join(parts, "\n")

	if innerH > lipgloss.Height(block)+2 {
		topPad := (innerH - lipgloss.Height(block)) / 3
		if topPad > 0 {
			block = strings.Repeat("\n", topPad) + block
		}
	}
	return block
}

func renderModeSwitcherHeader(active string, width int) string {
	titleStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	left := titleStyle.Render("modes")
	if active == "" {
		active = frame.ModeSolo
	}
	left += "  " + mutedStyle.Render("current: "+active)
	return lipgloss.PlaceHorizontal(width, lipgloss.Left, left)
}

func renderModeSwitcherRow(ui FrameUI, cursor, width int) string {
	active := ui.Mode
	if active == "" {
		active = frame.ModeSolo
	}
	cards := make([]string, 0, len(modeOptions))
	for i, opt := range modeOptions {
		cards = append(cards, renderModeCard(opt, i == cursor, opt.name == active))
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, joinWithGap(cards, "  ")...)
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, row)
}

// renderModeCard paints one mode tile: title + tagline + centered art
// + status. Focused cards wear a thick accent border in the mode's own
// color; idle cards a rounded subtle border. Active mode reuses the
// thick border so "active" and "focused-active" look identical when
// the user opens the picker on the active mode.
func renderModeCard(opt modeOption, isFocused, isActive bool) string {
	col := modeCardAccent(opt.name)

	titleStyle := lipgloss.NewStyle().Foreground(col).Bold(true)
	taglineStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	artStyle := lipgloss.NewStyle().Foreground(col)
	statusStyle := lipgloss.NewStyle().Foreground(colorSubtle).Italic(true)

	if !isFocused && !isActive {
		titleStyle = lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
		artStyle = lipgloss.NewStyle().Foreground(colorSubtle)
	}

	art := make([]string, len(opt.art))
	for i, line := range opt.art {
		art[i] = artStyle.Render(line)
	}
	artBlock := strings.Join(art, "\n")

	var status string
	switch {
	case isActive && isFocused:
		status = statusStyle.Render("active · press enter to keep")
	case isActive:
		status = statusStyle.Render("active")
	case isFocused:
		status = statusStyle.Render("press enter")
	default:
		status = " "
	}

	body := lipgloss.JoinVertical(lipgloss.Center,
		titleStyle.Render(opt.title),
		taglineStyle.Render(opt.tagline),
		"",
		artBlock,
		"",
		status,
	)

	border := lipgloss.RoundedBorder()
	borderColor := colorSubtle
	if isFocused || isActive {
		border = lipgloss.ThickBorder()
		borderColor = col
	}

	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColor).
		Width(modeCardWidth - 2).
		Height(modeCardHeight - 2).
		Align(lipgloss.Center).
		Render(body)
}

func renderModeSwitcherFooter(showHelp bool) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(colorMuted)
	subtleStyle := lipgloss.NewStyle().Foreground(colorSubtle)

	if showHelp {
		help := []string{
			keyStyle.Render("←→ / hl") + bodyStyle.Render(" move"),
			keyStyle.Render("1-3") + bodyStyle.Render(" jump"),
			keyStyle.Render("enter") + bodyStyle.Render(" switch"),
			keyStyle.Render("esc / ctrl+o") + bodyStyle.Render(" close"),
			keyStyle.Render("?") + bodyStyle.Render(" hide help"),
		}
		return subtleStyle.Render("  ") + strings.Join(help, bodyStyle.Render("  ·  "))
	}
	parts := []string{
		keyStyle.Render("enter") + bodyStyle.Render(" switch"),
		keyStyle.Render("esc") + bodyStyle.Render(" close"),
		keyStyle.Render("?") + bodyStyle.Render(" help"),
	}
	return subtleStyle.Render("  ") + strings.Join(parts, bodyStyle.Render("  ·  "))
}

// handleModeSwitcherKey routes one key while the takeover is open.
// Same shape as handleFrameSwitcherKey so the Update routing pattern
// stays uniform across overlays.
func (m *Model) handleModeSwitcherKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		return m, nil, false
	case "esc", "ctrl+o":
		m.closeModeSwitcher()
		return m, nil, true
	case "enter":
		return m, m.modeSwitcherCommit(), true
	case "left", "h":
		m.modeSwitcherMove(-1)
		return m, nil, true
	case "right", "l":
		m.modeSwitcherMove(1)
		return m, nil, true
	case "?":
		m.modeSwitcherHelp = !m.modeSwitcherHelp
		m.rerenderViewport()
		return m, nil, true
	case "1", "2", "3":
		idx := int(msg.String()[0]-'0') - 1
		if idx >= 0 && idx < len(modeOptions) {
			m.modeSwitcherCursor = idx
			m.rerenderViewport()
		}
		return m, nil, true
	}
	return m, nil, true
}

// openModeSwitcher is the toggle entrypoint called from chat.Update
// when Ctrl+O lands and frames are wired. Snaps the cursor to the
// active mode so the user starts where they expect.
func (m *Model) openModeSwitcher() {
	m.showModeSwitcher = true
	m.modeSwitcherHelp = false
	m.modeSwitcherCursor = modeIndexFromName(m.frame.Mode)
	m.rerenderViewport()
}

// closeModeSwitcher resets the overlay state. Idempotent.
func (m *Model) closeModeSwitcher() {
	m.showModeSwitcher = false
	m.modeSwitcherHelp = false
	m.rerenderViewport()
}

// modeSwitcherMove shifts the cursor by one card, clamped at the row
// boundaries (no wrap - the picker is 3 wide and wraparound would be
// disorienting).
func (m *Model) modeSwitcherMove(delta int) {
	target := m.modeSwitcherCursor + delta
	if target < 0 || target >= len(modeOptions) {
		return
	}
	m.modeSwitcherCursor = target
	m.rerenderViewport()
}

// modeSwitcherCommit fires SwitchMode for the focused card, closes the
// overlay, and returns a status echo. Echoes "already <mode>" without
// touching SwitchMode when the cursor is already on the active mode.
// Mirrors the modeSlash semantics so the surface stays uniform.
func (m *Model) modeSwitcherCommit() tea.Cmd {
	defer m.closeModeSwitcher()
	if m.modeSwitcherCursor < 0 || m.modeSwitcherCursor >= len(modeOptions) {
		return nil
	}
	target := modeOptions[m.modeSwitcherCursor].name
	current := m.frame.Mode
	if current == "" {
		current = frame.ModeSolo
	}
	if target == current {
		return statusCmd("mode already "+target, statusInfo)
	}
	if m.frame.SwitchMode == nil {
		return statusCmd("mode switching not wired in this session", statusWarn)
	}
	if err := m.frame.SwitchMode(target); err != nil {
		return statusCmd("mode switch failed: "+err.Error(), statusWarn)
	}
	m.frame.Mode = target
	return statusCmd("mode is now "+target+" in "+m.frame.Active, statusInfo)
}
