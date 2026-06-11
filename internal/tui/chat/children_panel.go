package chat

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// ChildrenView is the small interface the chat polls each render frame
// to learn whether the parent has live sub-agents. cmd/carlos backs this
// with a supervisor-scoped reader; tests pass a stub fixture.
//
// Snapshot must be cheap (the chat re-reads on a ~250ms tick while the
// panel is up). Implementations return an empty slice when no children
// are running - the chat treats that as "fall back to the single-stack
// layout".
type ChildrenView interface {
	Snapshot() []ChildSnapshot
}

// ChildrenViewFunc adapts a plain function into a ChildrenView. cmd/
// carlos uses this to wrap the supervisor's snapshot accessor without
// declaring a named type.
type ChildrenViewFunc func() []ChildSnapshot

func (f ChildrenViewFunc) Snapshot() []ChildSnapshot { return f() }

// ChildSnapshot is the per-child datum the right-side panel renders.
// One row per child; the supervisor stays the source of truth for
// state + spend, the chat just paints what it sees.
type ChildSnapshot struct {
	AgentID   string
	State     agent.State
	LastEvent string
	// LastTool carries the supervisor's ChildSnapshot.LastTool through
	// the adapter so the bordered agent card in the transcript can
	// surface "running {tool}" while the sub-agent is in flight. Not
	// consumed by the side panel (which has its own LastEvent column).
	LastTool  string
	Spend     ChildSpend
	StartedAt time.Time
}

// ChildSpend bundles the cost columns the panel surfaces inline. Tokens
// roll up to "4.1k tok" and Cents to "$0.014" - both are best-effort
// summaries; the manage TUI owns the full ledger.
type ChildSpend struct {
	Tokens int
	Cents  int
}

// Layout floors. The split appears when the chat has at least one live
// child AND the inner width clears splitMinWidth; below that the chat
// renders a single dim footer line instead so the transcript keeps the
// full width.
const (
	splitMinWidth      = 120
	panelMinWidth      = 40
	panelMaxWidth      = 60
	panelFractionNum   = 35
	panelFractionDenom = 100
	panelTickInterval  = 250 * time.Millisecond
)

// panelWidth computes the right-panel width from the chat's inner
// width. 35% of inner, floored at 40 cols and capped at 60. The chat
// renders the panel only when the caller has already confirmed
// innerW >= splitMinWidth; outside that range the math is meaningless
// but stays well-defined for fuzz/test calls.
func panelWidth(innerW int) int {
	w := innerW * panelFractionNum / panelFractionDenom
	if w < panelMinWidth {
		w = panelMinWidth
	}
	if w > panelMaxWidth {
		w = panelMaxWidth
	}
	if w > innerW-20 {
		w = innerW - 20
	}
	return w
}

// renderChildrenPanel paints the right-side sub-agent roster. Returns
// "" when snaps is empty so the caller can drop the panel without a
// layout-specific code path. width is the panel's allocated width
// (already computed by panelWidth at the renderInner seam).
func renderChildrenPanel(snaps []ChildSnapshot, width int, now time.Time) string {
	if len(snaps) == 0 {
		return ""
	}
	if width < panelMinWidth {
		width = panelMinWidth
	}

	header := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).
		Render(fmt.Sprintf("sub-agents (%d)", len(snaps)))

	rows := make([]string, 0, len(snaps)+3)
	rows = append(rows, header, "")
	for _, s := range snaps {
		rows = append(rows, renderChildRow(s, width, now))
	}

	totalTokens, totalCents := 0, 0
	for _, s := range snaps {
		totalTokens += s.Spend.Tokens
		totalCents += s.Spend.Cents
	}
	totalLine := lipgloss.NewStyle().Foreground(colorSubtle).Render(
		fmt.Sprintf("total: %s · %s", formatTokens(totalTokens), formatCents(totalCents)))
	hint := lipgloss.NewStyle().Foreground(colorMuted).Italic(true).
		Render("/agents for full view")

	rows = append(rows, "", totalLine, hint)
	return strings.Join(rows, "\n")
}

// renderChildRow formats a single line:
//
//	◆ a7f2  research  phase: synthesize       12s · 4.1k tok
//
// State glyph paints in the state's priority color; the rest stays in
// the muted/subtle band so the eye lands on the glyph + agent type
// first. Width drives LastEvent truncation so the time + token
// columns never spill off the panel.
func renderChildRow(s ChildSnapshot, width int, now time.Time) string {
	glyph := childStateGlyph(s.State)
	glyphStyle := lipgloss.NewStyle().Foreground(childStateColor(s.State)).Bold(true)
	idStyle := lipgloss.NewStyle().Foreground(colorMuted)
	typeStyle := lipgloss.NewStyle().Foreground(colorAgent).Bold(true)
	eventStyle := lipgloss.NewStyle().Foreground(colorSubtle)
	metaStyle := lipgloss.NewStyle().Foreground(colorMuted)

	id := shortChildID(s.AgentID)
	agentType := shortAgentType(s.LastEvent, s.AgentID)
	elapsed := formatElapsed(now.Sub(s.StartedAt))
	meta := fmt.Sprintf("%s · %s", elapsed, formatTokens(s.Spend.Tokens))

	// Layout budget for the row body. The two-cell glyph + space, the
	// short id (4 chars) + 2 spaces, the agent type + 2 spaces, and
	// the trailing meta + 1 space all subtract from width. Anything
	// left over goes to LastEvent.
	prefix := glyphStyle.Render(glyph) + " " + idStyle.Render(id) + "  " + typeStyle.Render(agentType)
	prefixW := lipgloss.Width(prefix)
	metaW := lipgloss.Width(meta)

	eventW := width - prefixW - metaW - 4
	if eventW < 6 {
		eventW = 6
	}
	event := truncateCells(s.LastEvent, eventW)
	eventRender := eventStyle.Render(event)

	gap := width - prefixW - lipgloss.Width(eventRender) - metaW - 2
	if gap < 1 {
		gap = 1
	}
	return prefix + "  " + eventRender + strings.Repeat(" ", gap) + metaStyle.Render(meta)
}

// renderChildrenFallbackLine is the sub-splitMinWidth substitute: a
// single dim footer line saying "N sub-agents running, /agents to view".
// Returns "" when nothing is live so the caller can drop the line
// without conditional rendering on every render path.
func renderChildrenFallbackLine(snaps []ChildSnapshot, width int) string {
	n := len(snaps)
	if n == 0 {
		return ""
	}
	noun := "sub-agents"
	verb := "are"
	if n == 1 {
		noun = "sub-agent"
		verb = "is"
	}
	text := fmt.Sprintf("%d %s %s running, /agents to view", n, noun, verb)
	if width > 0 && lipgloss.Width(text) > width {
		text = truncateCells(text, width)
	}
	return lipgloss.NewStyle().Foreground(colorSubtle).Italic(true).Render(text)
}

// shortChildID truncates a ULID for the panel's id column. Four chars
// is enough to disambiguate a handful of concurrent children while
// keeping the row compact.
func shortChildID(id string) string {
	if len(id) <= 4 {
		return id
	}
	// Use the tail of the ULID: the random suffix has more entropy
	// than the timestamp prefix, so collisions across the small set of
	// active children are unlikely.
	return strings.ToLower(id[len(id)-4:])
}

// shortAgentType extracts a one-word label for the row. The spec calls
// for "agent type", but SubAgent's Title is a free-form objective -
// so we take the first short token. Falls back to "agent" when nothing
// usable is available.
func shortAgentType(lastEvent, id string) string {
	// Strip any "phase:" / "diff:" / similar prefix from LastEvent -
	// those go in the event column. The agent type lives in the head
	// of LastEvent before any colon when LastEvent itself starts with
	// a category word.
	parts := strings.SplitN(strings.TrimSpace(lastEvent), " ", 2)
	if len(parts) > 0 && parts[0] != "" {
		head := strings.TrimRight(parts[0], ":")
		if isAgentTypeWord(head) {
			return head
		}
	}
	return "agent"
}

// isAgentTypeWord whitelists the strings we treat as an agent-type
// label. Any lowercase ASCII token under 16 chars qualifies - strict
// enough to reject things like JSON fragments, loose enough to allow
// future categories without code churn.
func isAgentTypeWord(s string) bool {
	if s == "" || len(s) > 15 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// truncateCells clamps s to maxW visual cells with a single-glyph
// ellipsis when cut. Distinct from research.go's rune-counting truncate
// because the panel's columns are visual-cell wide (lipgloss.Width)
// not rune-count wide.
func truncateCells(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	if maxW == 1 {
		return "…"
	}
	cut := maxW - 1
	if cut > len(s) {
		cut = len(s)
	}
	return s[:cut] + "…"
}

// formatElapsed renders a duration as the panel's compact time column.
// Under a minute reads as "Ns"; under an hour reads as "Nm"; above
// that reads as "Nh".
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// formatTokens prints a token count as "Nk tok" once the value clears
// 10,000. Smaller counts stay in their raw form so the user can tell
// 920 from 9.2k at a glance.
func formatTokens(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 10_000 {
		return fmt.Sprintf("%d tok", n)
	}
	whole := n / 1000
	tenth := (n % 1000) / 100
	if tenth == 0 {
		return fmt.Sprintf("%dk tok", whole)
	}
	return fmt.Sprintf("%d.%dk tok", whole, tenth)
}

// formatCents prints "$X.YYY" rounded to the third decimal so micro-
// spend ($0.014) reads naturally next to bigger numbers ($1.250). Zero
// reads as "$0.000" so the row stays a fixed-width column.
func formatCents(cents int) string {
	if cents < 0 {
		cents = 0
	}
	whole := cents / 100
	frac := cents % 100
	return fmt.Sprintf("$%d.%02d0", whole, frac)
}

// childStateGlyph picks a shape for the row's state column. Diamond
// for live work, dot for waiting / blocked, check for done. The shape
// alone distinguishes states under NO_COLOR.
func childStateGlyph(s agent.State) string {
	switch s {
	case agent.StateRunning, agent.StateCompacting:
		return "◆"
	case agent.StateAwaitingInput, agent.StateBlocked, agent.StatePausedByUser:
		return "◇"
	case agent.StateDone:
		return "✓"
	case agent.StateFailed, agent.StateOrphaned:
		return "✗"
	default:
		return "·"
	}
}

func childStateColor(s agent.State) lipgloss.Color {
	switch s {
	case agent.StateAwaitingInput, agent.StateBlocked, agent.StateOrphaned, agent.StateFailed:
		return colorWarn
	case agent.StateRunning, agent.StateCompacting:
		return colorAgent
	case agent.StateDone:
		return colorOK
	default:
		return colorMuted
	}
}
