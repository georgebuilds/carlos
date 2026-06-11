// agent_card.go renders sub-agent ("agent" tool) invocations as their
// own bordered two-line card in the chat transcript, peeled off from
// the single-line activity strip that compacts ordinary tool calls.
//
// Why a separate visual: spawning a sub-agent is a heavyweight action
// (another carlos with its own context, model, mode, and runtime). The
// strip's "▸ glob · bash · agent · web_search" treatment erases that
// distinction; the card gives the sub-task visible status and the
// vertical real estate that signals "peer task in flight".
//
// Visual envelope:
//
//	╭─────────────────────────────────────────────────────╮
//	│ 🧢 agent · running 12s                              │
//	│  ↳ scaffolding webgpu shader module for compute pass│
//	╰─────────────────────────────────────────────────────╯
//
// Exactly two content lines (header + body), four lines total with the
// rounded border. Width matches the strip's effective content width.
package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// agentToolName is the canonical name of the sub-agent delegation tool
// (see internal/agent/agent_tool.go's AgentTool.Name). Centralised here
// so the chat package doesn't sprinkle raw "agent" literals.
const agentToolName = "agent"

// agentCardEmoji is the carlos cap. The sub-agent is another carlos,
// so we lead the card with the same brand mark the assistant turn uses.
const agentCardEmoji = "🧢"

// agentCardSideMargin matches the activity strip's stripIndent so the
// card's outer edge aligns with the strip's left margin, keeping the
// transcript's vertical rhythm intact when a card sits next to strips.
const agentCardSideMargin = stripIndent

// minAgentCardWidth is the floor below which we stop scaling the inner
// box. Mirrors the strip's minStripContentW philosophy: accept a
// minimum readable width and let terminal wrap handle anything tighter.
const minAgentCardWidth = 32

// nowFn is the clock seam for the running-state elapsed display. Tests
// swap it to advance "now" relative to entry.toolCalledAt; production
// always uses time.Now. Kept package-private; no exported clock API.
var nowFn = time.Now

// parseAgentObjective extracts the "objective" field from the agent
// tool's raw JSON input. Returns empty on parse failure or when the
// field is missing; the caller (applyEvent) still tags isAgent=true
// so the row peels out of the strip and renders as a card with an
// empty body line rather than silently rejoining the strip.
func parseAgentObjective(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var doc struct {
		Objective string `json:"objective"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(doc.Objective)
}

// formatAgentDuration renders an elapsed duration with the agent
// card's compact vocabulary:
//
//	d < 1s            → "<1s"
//	d < 60s           → "Ns"      (e.g. "12s")
//	60s <= d < 600s   → "Nm Ms"   (e.g. "1m 35s")
//	d >= 600s         → "Nm+"     (no seconds when very long)
//
// Negative durations clamp to "<1s" so a clock skew never surfaces as
// a negative number.
func formatAgentDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	secs := int(d / time.Second)
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	if secs >= 600 {
		return fmt.Sprintf("%dm+", mins)
	}
	return fmt.Sprintf("%dm %ds", mins, secs%60)
}

// agentCardState picks the verb + duration string for the header line
// based on the entry's hasResult / isError state, plus the matching
// color. Running cards take their color from the brand-tool palette
// (same hue the strip's neutral glyph uses), done cards take the muted
// success-neutral muted color, failed cards take the warn palette.
func agentCardState(e transcriptEntry) (state, dur string, color lipgloss.Color) {
	if !e.hasResult {
		// Still in flight. Compute elapsed against the call timestamp
		// so the card's "running 12s" updates as the periodic text
		// ticker rerenders the viewport.
		var elapsed time.Duration
		if !e.toolCalledAt.IsZero() {
			elapsed = nowFn().Sub(e.toolCalledAt)
		}
		return "running", formatAgentDuration(elapsed), colorTool
	}
	// Result landed; freeze duration at the result timestamp delta so
	// the card stops counting when the sub-agent finishes.
	var elapsed time.Duration
	if !e.toolCalledAt.IsZero() && !e.toolResultAt.IsZero() {
		elapsed = e.toolResultAt.Sub(e.toolCalledAt)
	}
	dur = formatAgentDuration(elapsed)
	if e.isError {
		return "failed in", dur, colorWarn
	}
	return "done in", dur, colorMuted
}

// agentCardInsight picks the body-line text. For failed cards we prefer
// the captured tool result (which is typically the child's error
// payload, e.g. "context canceled") because it's more informative than
// the parent's stale objective; falling back to the objective keeps
// the row populated when the tool returned with no body. For
// still-in-flight cards we prefer the matching child snapshot's most
// recent tool call so the body reads as a live action signal; if no
// match exists (sub-agent not yet acting, or ambiguous title) we fall
// back to the static objective.
func agentCardInsight(e transcriptEntry, snaps []ChildSnapshot) string {
	if e.hasResult && e.isError {
		if s := strings.TrimSpace(extractAgentErrorText(e.toolResult)); s != "" {
			return s
		}
	}
	if !e.hasResult {
		if child, ok := matchAgentChild(e, snaps); ok {
			if t := strings.TrimSpace(child.LastTool); t != "" {
				return "running " + t
			}
		}
	}
	return strings.TrimSpace(e.agentObjective)
}

// matchAgentChild finds the in-flight ChildSnapshot whose Title equals
// the entry's objective. Returns ok=false when no unambiguous match
// exists, duplicate-title concurrent spawns (rare; only orchestrator
// fan-out with identical objectives) fall back to the static body.
func matchAgentChild(e transcriptEntry, snaps []ChildSnapshot) (ChildSnapshot, bool) {
	objective := strings.TrimSpace(e.agentObjective)
	if objective == "" {
		return ChildSnapshot{}, false
	}
	var (
		hit   ChildSnapshot
		count int
	)
	for _, s := range snaps {
		if strings.TrimSpace(s.LastEvent) == objective {
			hit = s
			count++
		}
	}
	if count == 1 {
		return hit, true
	}
	return ChildSnapshot{}, false
}

// extractAgentErrorText reduces a sub-agent's result payload to a
// single line suitable for the card's body. The agent tool returns
// JSON on success and either JSON or a plain string on error; we walk
// the JSON shape best-effort and fall back to the raw text. Multi-line
// payloads collapse to their first non-empty line so the card stays
// at two content rows.
func extractAgentErrorText(raw string) string {
	if raw == "" {
		return ""
	}
	// Try the structured shape first: AgentTool.Run returns
	// {"error": "...", ...} on failure.
	var doc struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err == nil && strings.TrimSpace(doc.Error) != "" {
		return firstNonEmptyLine(doc.Error)
	}
	return firstNonEmptyLine(raw)
}

// firstNonEmptyLine returns the first non-blank line of s, trimmed.
// Helps collapse multi-line error bodies into the card's single body
// row without losing the lead message.
func firstNonEmptyLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			return ln
		}
	}
	return ""
}

// renderAgentCard paints the bordered two-line card. width is the
// viewport's content width (same value composeTranscript hands to
// renderToolStrip); we carve out the side margin to align with the
// strip's left edge. snaps is the live sub-agent roster the body line
// consults to surface "running {tool}" while the sub-agent is in
// flight; nil or empty is fine and falls back to the static objective.
func renderAgentCard(e transcriptEntry, width int, snaps []ChildSnapshot) string {
	totalW := width - agentCardSideMargin*2
	if totalW < minAgentCardWidth {
		totalW = minAgentCardWidth
	}
	boxW := totalW - 2 // border eats two cells
	contentW := boxW - 2
	if contentW < 16 {
		contentW = 16
	}

	state, dur, stateColor := agentCardState(e)
	stateStyle := lipgloss.NewStyle().Foreground(stateColor).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	sepStyle := lipgloss.NewStyle().Foreground(colorMuted)

	// Header: "🧢 agent · {state} {duration}"
	stateText := state
	if dur != "" {
		stateText = state + " " + dur
	}
	header := agentCardEmoji + " " +
		lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("agent") +
		sepStyle.Render(" · ") +
		stateStyle.Render(stateText)
	header = clampAgentLine(header, contentW)

	// Body: "↳ {insight}" with insight truncated to fit.
	insight := agentCardInsight(e, snaps)
	arrow := mutedStyle.Render(" ↳ ")
	arrowW := lipgloss.Width(arrow)
	bodyBudget := contentW - arrowW
	if bodyBudget < 4 {
		bodyBudget = 4
	}
	bodyText := oneLine(insight, bodyBudget)
	body := arrow + mutedStyle.Render(bodyText)

	rendered := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(stateColor).
		Padding(0, 1).
		Width(boxW).
		Render(lipgloss.JoinVertical(lipgloss.Left, header, body))

	pad := strings.Repeat(" ", agentCardSideMargin)
	rows := strings.Split(rendered, "\n")
	for i := range rows {
		rows[i] = pad + rows[i]
	}
	return strings.Join(rows, "\n")
}

// clampAgentLine soft-truncates a styled line so the visual width fits
// within contentW. The header line composes pre-styled segments so we
// can't trim raw text without ANSI math; instead we measure the
// rendered width and accept terminal wrap on the rare overflow case
// (matches the strip's "best-effort, don't crash on tiny widths"
// policy). The function exists as a single seam so future widening of
// the header content can be width-managed in one place.
func clampAgentLine(line string, contentW int) string {
	if lipgloss.Width(line) <= contentW {
		return line
	}
	return line
}
