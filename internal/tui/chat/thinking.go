package chat

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Activity indicator ("carlos is thinking") for the gap between a
// user submission and the first model output.
//
// Why this exists: provider RTT plus first-token latency is typically
// 300ms – a few seconds. With nothing on-screen during that window the
// chat reads as frozen, and when several tool calls land in close
// succession they appear "all at once" instead of feeling streamed.
// A subtle live row anchors the wait, signals the model is working,
// and (once elapsed > 3s) reports how long the wait has been.
//
// Visual choice: a 3-dot bouncing-ball pattern is the most legible
// "typing indicator" idiom and avoids the cute-but-distracting word
// games some other CLIs use. Animation runs off the existing 33 ms
// textTick so we don't add another timer.

// thinkingFrames spells out the 3-dot pattern. Each frame is a
// bitmask: true = highlighted dot, false = dim dot. Four frames
// cycle bright dot left → middle → right → middle, which reads as
// a smooth wave.
var thinkingFrames = [][3]bool{
	{true, false, false},
	{false, true, false},
	{false, false, true},
	{false, true, false},
}

// thinkingFrameTicks is the number of textTicks (33 ms each) between
// animation frames. 5 ticks ≈ 165 ms per frame, ~660 ms per full
// cycle — slow enough not to feel jittery, fast enough to feel alive.
const thinkingFrameTicks = 5

// thinkingElapsedThreshold gates the "(Ns)" trailing timer so a quick
// reply doesn't get a stopwatch slapped on it. Once a wait crosses
// this threshold the timer kicks in and updates every render.
const thinkingElapsedThreshold = 3 * time.Second

// isThinking decides whether the chat should paint the activity row
// at the bottom of the transcript. Conditions:
//
//   - The projection reports an "in-flight" state — Spawning (early,
//     before the first state_change lands), Running, or Compacting.
//   - No live assistant text is buffered. Streaming text is its own
//     "alive" signal and the spinner would compete with it.
//   - There IS something in the transcript already (no spinner over a
//     blank welcome screen) AND the last entry is something the
//     model is responding to — a user message, tool call/result, or
//     user-shell command. We don't paint a spinner under a finished
//     assistant turn; that reads as if carlos is stalling.
//   - The chat isn't in research-progress mode — that row is its own
//     activity indicator.
func (m *Model) isThinking() bool {
	state, _ := m.headerState()
	switch state {
	case agent.StateSpawning, agent.StateRunning, agent.StateCompacting:
		// in-flight
	default:
		return false
	}
	if m.source.Get(m.agentID) != "" {
		return false
	}
	if len(m.transcript) == 0 {
		return false
	}
	last := m.transcript[len(m.transcript)-1]
	switch last.kind {
	case entryUserMessage, entryToolCall, entryToolResult, entryUserShell:
		return true
	}
	return false
}

// assistantBusy returns true when the assistant is mid-turn — either
// streaming text into the live buffer, or in one of the in-flight
// projection states (Spawning, Running, Compacting) waiting on a tool
// or model response. Used by submit() to decide whether a fresh
// user-typed line should dispatch immediately or be parked in the
// queue for flushQueuedUserMessage to release once the turn ends.
//
// isThinking() returns false during live text streaming (it defers to
// the streamed text as the alive signal), so OR-ing the two captures
// "the model is currently doing something on our behalf" without
// double-counting either condition.
func (m *Model) assistantBusy() bool {
	if m.source != nil && m.source.Get(m.agentID) != "" {
		return true
	}
	return m.isThinking()
}

// thinkingElapsed returns the wall-clock time since the most recent
// transcript entry. Zero when the transcript is empty. Used by the
// indicator to surface a "(Ns)" trailer once the wait crosses
// [thinkingElapsedThreshold] so the user can gauge whether carlos is
// stalled vs. just slow.
func (m *Model) thinkingElapsed() time.Duration {
	if len(m.transcript) == 0 {
		return 0
	}
	last := m.transcript[len(m.transcript)-1]
	return time.Since(last.ts)
}

// renderThinkingRow paints the activity indicator. Layout:
//
//	🧢   ● ∙ ∙    thinking    (3s)
//
// The bouncing dot is the focal element (accent color, bold); the
// "thinking" label is dim italic so the eye lands on the dots first
// and reads the verb as context. The elapsed timer is hidden under
// 3 s — quick replies stay quiet.
func renderThinkingRow(tick int, elapsed time.Duration, width int) string {
	capStyle := lipgloss.NewStyle().Foreground(colorAgent)
	dimStyle := lipgloss.NewStyle().Foreground(colorSubtle).Italic(true)

	dots := renderThinkingDots(tick)
	label := dimStyle.Render("thinking")

	line := capStyle.Render("🧢") + "   " + dots + "   " + label

	if elapsed >= thinkingElapsedThreshold {
		secs := int(elapsed.Seconds())
		timer := lipgloss.NewStyle().
			Foreground(colorSubtle).
			Render(fmt.Sprintf("   · %ds", secs))
		line += timer
	}

	// Width clamp via lipgloss so a future change to the avatar column
	// doesn't accidentally push the row past the viewport.
	if width > 0 {
		return lipgloss.NewStyle().MaxWidth(width).Render(line)
	}
	return line
}

// renderThinkingDots paints the 3-dot bouncing pattern in one row.
// One dot wears the accent (bright), the other two are subtle so the
// "wave" reads at a glance without forcing the eye to track motion.
func renderThinkingDots(tick int) string {
	frame := (tick / thinkingFrameTicks) % len(thinkingFrames)
	pattern := thinkingFrames[frame]
	bright := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dim := lipgloss.NewStyle().Foreground(colorMuted)
	parts := make([]string, 3)
	for i, on := range pattern {
		if on {
			parts[i] = bright.Render("●")
		} else {
			parts[i] = dim.Render("∙")
		}
	}
	return strings.Join(parts, " ")
}
