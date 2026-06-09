package manage

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// rosterRow is one renderable line of the roster. We compute the
// "display" representation here (indented title, formatted columns)
// rather than during sort/filter so the rendering layer can stay
// straight string assembly.
type rosterRow struct {
	row        agent.AgentRow
	indent     int    // 0 = root, 1 = direct child, 2 = grandchild, etc.
	collapsed  bool   // true → the row hides a deeper subtree; shows "…N more"
	hiddenKids int    // number of descendants collapsed under this row
	tool       string // current tool name placeholder; "" → "-"
	spark      string // pre-rendered sparkline (focused agent only)
	elapsed    time.Duration
}

// rosterRenderOptions bundles inputs the renderer needs from the
// orchestrator.
type rosterRenderOptions struct {
	width     int
	height    int
	focusID   string
	cursorIdx int // index into rows the ↑/↓ cursor sits on; -1 = no cursor highlight
	scroll    int // top of the visible card window
	spark     func(id string) string
	elapsed   func(createdAt time.Time) time.Duration
	maxDepth  int // cap rendered indentation at this many levels
}

// defaultMaxDepth caps visible nesting at 3 levels; deeper subtrees
// show "…N more" on the deepest visible parent.
const defaultMaxDepth = 3

// cardLines is the per-agent card height in terminal rows. Layout:
//
//	row 0  top border (rounded / thick when selected)
//	row 1  ID + state badge + indent gutter
//	row 2  intent (truncated to card width)
//	row 3  meta strip: tokens · cost · elapsed · sparkline
//	row 4  bottom border
//
// Pinned as a constant so the virtualization window math stays trivial:
// visible_cards = bodyH / cardLines.
const cardLines = 5

// buildRosterRows flattens the projection into a depth-ordered list,
// applying lineage indentation. Sort order is preserved for siblings;
// the parent/child relation is computed from parent_id and the rows
// are rendered DFS so a parent always precedes its children.
func buildRosterRows(rows []agent.AgentRow, focusID string, maxDepth int) []rosterRow {
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}

	byID := make(map[string]agent.AgentRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	childrenOf := make(map[string][]agent.AgentRow, len(rows))
	rootList := make([]agent.AgentRow, 0, len(rows))
	for _, r := range rows {
		if r.ParentID == "" {
			rootList = append(rootList, r)
			continue
		}
		// If the parent is missing from this slice (filter narrowed
		// it out), treat the orphaned child as a root so it stays
		// visible.
		if _, ok := byID[r.ParentID]; !ok {
			rootList = append(rootList, r)
			continue
		}
		childrenOf[r.ParentID] = append(childrenOf[r.ParentID], r)
	}

	var out []rosterRow
	var walk func(r agent.AgentRow, depth int)
	walk = func(r agent.AgentRow, depth int) {
		if depth >= maxDepth {
			out = append(out, rosterRow{
				row:        r,
				indent:     depth,
				collapsed:  true,
				hiddenKids: countDescendants(r.ID, childrenOf),
			})
			return
		}
		out = append(out, rosterRow{row: r, indent: depth})
		for _, kid := range childrenOf[r.ID] {
			walk(kid, depth+1)
		}
	}
	for _, root := range rootList {
		walk(root, 0)
	}
	return out
}

// countDescendants returns the total number of nodes under id.
func countDescendants(id string, childrenOf map[string][]agent.AgentRow) int {
	kids := childrenOf[id]
	n := len(kids)
	for _, k := range kids {
		n += countDescendants(k.ID, childrenOf)
	}
	return n
}

// renderRoster composes the visible window of agent cards. Each card
// is a self-contained bordered button — rounded on idle/focus, thick
// + reverse-video filled on cursor selection. Cards stack vertically
// with no inter-card gap so the borders themselves provide segmentation.
//
// Replaces the v0.7.x column-table layout, which broke down once the
// roster pane fell below ~90 cells (rows wrapped, columns desynced).
// The card format is height-bounded instead of width-bounded so it
// degrades gracefully on narrow terminals.
func renderRoster(rows []rosterRow, opts rosterRenderOptions) string {
	w := opts.width
	if w < cardMinWidth {
		w = cardMinWidth
	}

	if len(rows) == 0 {
		return renderRosterEmptyState(w, opts.height)
	}

	// Window math: how many full cards fit in the visible height.
	visible := opts.height / cardLines
	if visible < 1 {
		visible = 1
	}
	start := opts.scroll
	if start < 0 {
		start = 0
	}
	if start > len(rows) {
		start = len(rows)
	}
	end := start + visible
	if end > len(rows) {
		end = len(rows)
	}

	cards := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		cards = append(cards, renderAgentCard(rows[i], w, i == opts.cursorIdx, rows[i].row.ID == opts.focusID))
	}

	// Footer chip — overflow indicator. Tells the user the visible
	// window is a slice of a larger roster so they don't think the
	// pane is showing everything.
	if len(rows) > visible {
		chip := lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render(
			fmt.Sprintf("  · showing %d–%d of %d ·", start+1, end, len(rows)),
		)
		cards = append(cards, lipgloss.PlaceHorizontal(w, lipgloss.Center, chip))
	}

	return strings.Join(cards, "\n")
}

// cardMinWidth is the floor below which a card refuses to render
// content (it still draws the border so the layout stays stable).
const cardMinWidth = 32

// renderAgentCard paints one agent as a bordered button. Selection
// state takes visual priority over focus state — when the cursor lands
// on a card, the whole card flips to a thick-bordered, reverse-video
// fill so selection is unmistakable. Focus (the agent whose transcript
// shows in the right pane) carries an accent-colored rounded border;
// idle cards use a subtle rounded border.
//
// The card body is always rendered in plain text (no inner ANSI
// styles) so the outer Reverse(true) wrapper cleanly inverts every
// cell when selected. Pre-styled inner runs would leak their original
// colors through the reverse flip and look glitchy.
func renderAgentCard(rr rosterRow, paneW int, isCursor, isFocus bool) string {
	r := rr.row

	// Inner width: pane width minus border (2) minus horizontal padding (2).
	innerW := paneW - 4
	if innerW < 8 {
		innerW = 8
	}

	// Line 1: ID + state badge. Indent gutter renders as "  " per level
	// so lineage is visible without consuming horizontal range.
	gutter := strings.Repeat("  ", rr.indent)
	id := shortID(r.ID)
	stateText := "[" + theme.StateGlyph(r.State) + " " + r.State.String() + "]"
	titleLeft := gutter + id + "  " + stateText

	// Line 2: intent.
	intent := r.Title
	if intent == "" {
		intent = "(no intent recorded)"
	}
	if rr.collapsed && rr.hiddenKids > 0 {
		intent += " " + suffixMore(rr.hiddenKids)
	}
	intent = truncate(intent, innerW)

	// Line 3: meta strip — tokens · cost · elapsed · sparkline.
	tokens := formatTokensColumn(r.TokensIn, r.TokensOut) + " tok"
	cost := formatCost(r.CostCents)
	elapsed := formatElapsed(rr.elapsed)
	meta := tokens + "  ·  " + cost + "  ·  " + elapsed
	if rr.spark != "" && lipgloss.Width(meta)+2+numSparkBuckets <= innerW {
		// Sparkline aligns to the right when there's room. We strip
		// ANSI from the spark when selected so the reverse-video flip
		// doesn't leave colored residue.
		spark := rr.spark
		if isCursor {
			spark = stripCSI(spark)
		}
		gap := innerW - lipgloss.Width(meta) - lipgloss.Width(spark)
		if gap < 2 {
			gap = 2
		}
		meta = meta + strings.Repeat(" ", gap) + spark
	}

	// Body before styling: three lines, padded to inner width so
	// background fills the whole card uniformly when reversed.
	body := lipgloss.JoinVertical(lipgloss.Left,
		padCellsToWidth(titleLeft, innerW),
		padCellsToWidth(intent, innerW),
		padCellsToWidth(meta, innerW),
	)

	switch {
	case isCursor:
		// Selected: thick border, accent foreground (border), full
		// reverse-video on the body so every cell inverts as a unit.
		// Bold to add weight to the inverted text.
		return lipgloss.NewStyle().
			Border(lipgloss.ThickBorder()).
			BorderForeground(colorAccent).
			Padding(0, 1).
			Width(paneW - 2).
			Reverse(true).
			Bold(true).
			Render(body)
	case isFocus:
		// Focused (in detail pane, but cursor is elsewhere): rounded
		// agent-accent border, body in agent color so the user can
		// trace which card the right pane is showing.
		title := lipgloss.NewStyle().Foreground(colorAgent).Bold(true).Render(
			padCellsToWidth(titleLeft, innerW),
		)
		styled := lipgloss.JoinVertical(lipgloss.Left,
			title,
			lipgloss.NewStyle().Foreground(colorMuted).Render(padCellsToWidth(intent, innerW)),
			lipgloss.NewStyle().Foreground(colorSubtle).Render(padCellsToWidth(meta, innerW)),
		)
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAgent).
			Padding(0, 1).
			Width(paneW - 2).
			Render(styled)
	default:
		// Idle: rounded subtle border + colored state badge inline.
		titleColored := lipgloss.NewStyle().Foreground(colorMuted).Render(gutter+id) +
			"  " + stateBadge(r.State)
		styled := lipgloss.JoinVertical(lipgloss.Left,
			padCellsToWidth(titleColored, innerW),
			lipgloss.NewStyle().Foreground(colorMuted).Render(padCellsToWidth(intent, innerW)),
			lipgloss.NewStyle().Foreground(colorSubtle).Render(padCellsToWidth(meta, innerW)),
		)
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSubtle).
			Padding(0, 1).
			Width(paneW - 2).
			Render(styled)
	}
}

// padCellsToWidth right-pads s with spaces to exactly w terminal
// cells, counting via lipgloss.Width so embedded ANSI escapes don't
// distort the gap calculation. If s is already wider than w, it's
// returned unchanged (the card border will clip it).
func padCellsToWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	gap := w - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

// stripCSI removes ANSI CSI escape sequences from s. Used on the
// sparkline before rendering a selected card, since lipgloss
// Reverse(true) on the outer style doesn't propagate into already-
// styled inner runs.
func stripCSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				j++
				if (c >= 0x40 && c <= 0x7e) {
					break
				}
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// renderRosterEmptyState is shown when the projection has zero rows
// (or all rows were filtered out). Centered, italicized, dim.
func renderRosterEmptyState(w, h int) string {
	dim := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	msg := dim.Render("no agents yet")
	hint := dim.Render("/research or /please will spawn one")
	body := lipgloss.JoinVertical(lipgloss.Center, msg, "", hint)
	if h < 1 {
		h = 3
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, body)
}

// suffixMore returns the "…N more" marker for a collapsed subtree.
func suffixMore(n int) string {
	return "…" + itoa(n) + " more"
}

// itoa is a tiny utility for small counts.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// truncate cuts s to at most n runes; appends "…" if truncated.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(runes[:n-1]) + "…"
}

// padRight pads a plain string to width n with spaces. Used by the
// header (no ANSI) only.
func padRight(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

// padRightANSI pads s (which may contain ANSI escapes) to a visible
// width of n. lipgloss.Width discounts the escape sequences.
func padRightANSI(s string, n int) string {
	gap := n - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

// formatElapsed compacts a duration to fit a 7-char column.
func formatElapsed(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d < time.Minute {
		return itoa(int(d.Seconds())) + "s"
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if m < 10 {
			return itoa(m) + "m" + itoa(s) + "s"
		}
		return itoa(m) + "m"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return itoa(h) + "h" + itoa(m) + "m"
}
