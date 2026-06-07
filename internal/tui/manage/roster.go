package manage

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
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
// orchestrator: terminal sizing, the focused agent ID (rendered with
// a highlight), and the optional sparkline accessor for the focused
// agent.
type rosterRenderOptions struct {
	width    int
	height   int
	focusID  string
	scroll   int // top of the visible window
	spark    func(id string) string
	elapsed  func(createdAt time.Time) time.Duration
	maxDepth int // cap rendered indentation at this many levels
}

// defaultMaxDepth caps visible nesting at 3 levels (per the brief);
// deeper subtrees show "…N more" on the deepest visible parent.
const defaultMaxDepth = 3

// buildRosterRows flattens the projection into a depth-ordered list,
// applying lineage indentation. Sort order is preserved for siblings;
// the parent/child relation is computed from parent_id and the rows
// are rendered DFS so a parent always precedes its children.
func buildRosterRows(rows []agent.AgentRow, focusID string, maxDepth int) []rosterRow {
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}

	// Index by ID for cheap children() lookups.
	byID := make(map[string]agent.AgentRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	// Children groups keyed by parent_id. We iterate `rows` (the
	// caller's sorted slice) so sibling order matches the active sort
	// instead of falling back to ID order.
	childrenOf := make(map[string][]agent.AgentRow, len(rows))
	rootList := make([]agent.AgentRow, 0, len(rows))
	for _, r := range rows {
		if r.ParentID == "" {
			rootList = append(rootList, r)
			continue
		}
		// If the parent is missing from this slice (filter narrowed
		// it out), treat the orphaned child as a root so it stays
		// visible - losing rows silently would be a worse UX than
		// rendering a stray child at the root level.
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
			// We have reached the visible cap. Render the row itself
			// at this depth, then if it has descendants, attach a
			// "…N more" marker.
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

// countDescendants returns the total number of nodes under id in the
// adjacency map.
func countDescendants(id string, childrenOf map[string][]agent.AgentRow) int {
	kids := childrenOf[id]
	n := len(kids)
	for _, k := range kids {
		n += countDescendants(k.ID, childrenOf)
	}
	return n
}

// renderRoster composes the roster header + the visible window of
// rows. The output is the table as a single string suitable for
// JoinVertical with the focus pane.
//
// Column layout (fixed widths):
//
//	id  state            intent                model     tok        cost     spark        elapsed
//	8   18 (badge)       variable              16        11         7        12           7
//
// Total fixed columns ~= 8 + 1 + 18 + 1 + (variable) + 1 + 16 + 1 + 11 + 1 + 7 + 1 + 12 + 1 + 7 = 105 + intent
// We allocate intent the leftover width after the fixed slots, with a
// floor of 12 chars; below the floor we drop the model column to
// keep the row legible.
func renderRoster(rows []rosterRow, opts rosterRenderOptions) string {
	w := opts.width
	if w < 40 {
		w = 40
	}
	const (
		idW     = 8
		stateW  = 18
		modelW  = 16
		tokensW = 11
		costW   = 7
		sparkW  = 12
		timeW   = 7
		gap     = 1
	)
	fixed := idW + stateW + modelW + tokensW + costW + sparkW + timeW + gap*7
	intentW := w - fixed
	dropModel := false
	if intentW < 12 {
		intentW = w - (fixed - modelW - gap)
		dropModel = true
	}
	if intentW < 8 {
		intentW = 8
	}

	header := renderRosterHeader(intentW, dropModel)
	body := make([]string, 0, len(rows)+1)
	body = append(body, header)

	// Virtualization: only render the visible slice of rows.
	visible := opts.height - 1 // header row
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

	for i := start; i < end; i++ {
		body = append(body, renderRosterRow(rows[i], opts, intentW, dropModel))
	}

	// If the visible window is shorter than the height, pad with
	// blanks so the focus pane on the right doesn't shift up.
	for i := end - start; i < visible; i++ {
		body = append(body, "")
	}
	return strings.Join(body, "\n")
}

// renderRosterHeader is the bold column-name row. We don't render any
// border around the table - the outer manage view provides the frame.
func renderRosterHeader(intentW int, dropModel bool) string {
	style := lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
	parts := []string{
		padRight("id", 8),
		padRight("state", 18),
		padRight("intent", intentW),
	}
	if !dropModel {
		parts = append(parts, padRight("model", 16))
	}
	parts = append(parts,
		padRight("tokens", 11),
		padRight("cost", 7),
		padRight("spark", 12),
		padRight("time", 7),
	)
	return style.Render(strings.Join(parts, " "))
}

// renderRosterRow is the per-row column-assembled line. We compose
// raw strings + apply lipgloss styles, then pad each column to its
// fixed width AFTER coloring (because pad treats ANSI escapes as
// zero-width but lipgloss doesn't).
func renderRosterRow(rr rosterRow, opts rosterRenderOptions, intentW int, dropModel bool) string {
	r := rr.row

	// Lineage indent on the title cell.
	indent := strings.Repeat("  ", rr.indent)
	intent := indent + r.Title
	if rr.collapsed && rr.hiddenKids > 0 {
		intent += " "
		intent += lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true).
			Render(suffixMore(rr.hiddenKids))
	}
	intent = truncate(intent, intentW)

	idCell := shortID(r.ID)
	idStyle := lipgloss.NewStyle().Foreground(colorSubtle)
	if r.ID == opts.focusID {
		idStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Reverse(true)
	}

	badge := stateBadge(r.State)

	model := r.Model
	if model == "" {
		model = "-"
	}
	model = truncate(model, 16)

	tokens := formatTokensColumn(r.TokensIn, r.TokensOut)
	cost := formatCost(r.CostCents)

	spark := rr.spark
	if spark == "" {
		spark = strings.Repeat(string(sparkBlocks[0]), numSparkBuckets)
	}
	elapsed := formatElapsed(rr.elapsed)

	// padRight respects ANSI by computing visible width with
	// lipgloss.Width.
	parts := []string{
		padRightANSI(idStyle.Render(idCell), 8),
		padRightANSI(badge, 18),
		padRightANSI(intent, intentW),
	}
	if !dropModel {
		parts = append(parts,
			padRightANSI(lipgloss.NewStyle().Foreground(colorMuted).Render(model), 16),
		)
	}
	parts = append(parts,
		padRightANSI(lipgloss.NewStyle().Foreground(colorAgent).Render(tokens), 11),
		padRightANSI(lipgloss.NewStyle().Foreground(colorAccent).Render(cost), 7),
		padRightANSI(spark, 12),
		padRightANSI(lipgloss.NewStyle().Foreground(colorMuted).Render(elapsed), 7),
	)
	row := strings.Join(parts, " ")

	// Focused row gets a faint reverse-video so the user can locate
	// the cursor even when the focus pane is showing the same agent.
	if r.ID == opts.focusID {
		row = lipgloss.NewStyle().Foreground(colorAgent).Render("▸ ") + row
	} else {
		row = "  " + row
	}
	return row
}

// suffixMore returns the "…N more" marker for a collapsed subtree.
// N=1 reads as "…1 more"; pluralization not worth the cycles.
func suffixMore(n int) string {
	return "…" + itoa(n) + " more"
}

// itoa is a tiny utility (we only need it for small counts; avoid
// strconv import here to keep allocations minimal in the render path).
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

// truncate cuts s to at most n runes; appends "…" if truncated. We
// count runes (not bytes) because emoji/non-ASCII titles otherwise
// over-truncate.
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
// width of n. lipgloss.Width discounts the escape sequences. If s is
// wider than n we leave it alone - truncating an already-styled
// string would corrupt the escape pairs.
func padRightANSI(s string, n int) string {
	gap := n - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

// formatElapsed compacts a duration to fit a 7-char column.
// <1m → "Ns" / "NNs"; <1h → "NmMMs" or just "NNm"; ≥1h → "NhMMm".
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
