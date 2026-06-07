package manage

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// focusEntry is one renderable line in the focus pane's transcript.
// Mirrors chat's transcriptEntry but kept local since the chat
// package is don't-edit.
type focusEntry struct {
	kind focusEntryKind
	ts   time.Time
	text string
	tool string
	to   agent.State
}

type focusEntryKind int

const (
	focusStateChange focusEntryKind = iota
	focusProviderCall
	focusToolCall
	focusToolResult
	focusUserMessage
	focusSteering
	focusHeartbeat
	focusSystemNote
)

// FocusPane is the right-hand scrollable transcript for the selected
// agent. Holds its own viewport + token ring; the orchestrator drives
// agent changes via Bind(agentID).
//
// The pane is intentionally stateful - it owns the viewport's scroll
// offset, the ring buffer, and the most-recent focus binding - so the
// top-level Model doesn't have to thread those fields through every
// message handler.
type FocusPane struct {
	agentID    string
	entries    []focusEntry
	vp         viewport.Model
	ring       *TokenRing
	autoScroll bool

	// hbSuppressUntil is set after rendering a heartbeat marker so a
	// stream of heartbeats coalesces into a single dot rather than
	// flooding the pane.
	hbSuppressUntil time.Time

	// lastEventTS is the timestamp of the most-recently applied event
	// (used to attribute token deltas to the right ring slot via the
	// orchestrator's spark-advance tick).
	lastEventTS time.Time
}

// NewFocusPane constructs a fresh, unbound focus pane. Use Bind() to
// point it at an agent.
func NewFocusPane() *FocusPane {
	vp := viewport.New(0, 0)
	vp.MouseWheelEnabled = true
	return &FocusPane{
		vp:         vp,
		autoScroll: true,
		ring:       &TokenRing{},
	}
}

// Bind switches the pane to a fresh agent. Clears the transcript +
// ring so the next backfill paints from a known starting point. Does
// NOT touch the viewport's scroll position - the orchestrator manages
// that via Resize.
func (f *FocusPane) Bind(agentID string) {
	f.agentID = agentID
	f.entries = nil
	f.ring = &TokenRing{}
	f.autoScroll = true
	f.vp.SetContent("")
	f.vp.GotoTop()
}

// AgentID returns the currently-bound agent ID, or "" if unbound.
func (f *FocusPane) AgentID() string { return f.agentID }

// Ring exposes the token ring for the orchestrator's per-row
// sparkline rendering. Nil if the pane is unbound.
func (f *FocusPane) Ring() *TokenRing { return f.ring }

// Resize sets the viewport dimensions; the orchestrator calls this
// every layout pass so a window resize doesn't desync the pane's
// scroll math.
func (f *FocusPane) Resize(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if f.vp.Width != w || f.vp.Height != h {
		f.vp.Width = w
		f.vp.Height = h
		f.rerender()
	}
}

// ApplyBackfill folds an entire event-history slice into the pane.
// Used on focus change so the transcript is populated in one shot.
func (f *FocusPane) ApplyBackfill(events []agent.Event) {
	for _, ev := range events {
		f.applyEvent(ev)
	}
	f.rerender()
	f.vp.GotoBottom()
}

// ApplyEvent folds a single live event into the pane. Tracks whether
// the user has scrolled up - if they have, we don't auto-scroll.
func (f *FocusPane) ApplyEvent(ev agent.Event) {
	wasAtBottom := f.vp.AtBottom() || f.autoScroll
	f.applyEvent(ev)
	f.rerender()
	if wasAtBottom {
		f.vp.GotoBottom()
		f.autoScroll = true
	}
}

// ScrollUp / ScrollDown forward scrolling keys to the viewport.
// Marking autoScroll=false on PgUp/Up so a manual scroll-up disables
// the auto-pin until the user scrolls back to bottom.
func (f *FocusPane) ScrollUp() {
	f.vp.ScrollUp(1)
	f.autoScroll = false
}
func (f *FocusPane) ScrollDown() {
	f.vp.ScrollDown(1)
	if f.vp.AtBottom() {
		f.autoScroll = true
	}
}
func (f *FocusPane) PageUp() {
	f.vp.ScrollUp(f.vp.Height)
	f.autoScroll = false
}
func (f *FocusPane) PageDown() {
	f.vp.ScrollDown(f.vp.Height)
	if f.vp.AtBottom() {
		f.autoScroll = true
	}
}

// View renders the current pane content with a left-edge brand
// separator so it visually distinguishes from the roster on its
// left.
func (f *FocusPane) View() string { return f.vp.View() }

// applyEvent updates entries + ring (for token_usage). Errors from
// json.Unmarshal degrade to a system-note entry - the pane keeps
// running so a single malformed payload doesn't kill the transcript.
func (f *FocusPane) applyEvent(ev agent.Event) {
	f.lastEventTS = ev.TS
	switch ev.Type {
	case agent.EvtStateChange:
		var sp agent.StateChangePayload
		_ = json.Unmarshal(ev.Payload, &sp)
		if sp.Kind == agent.StateChangeCreated && sp.Created != nil {
			f.entries = append(f.entries, focusEntry{
				kind: focusStateChange,
				ts:   ev.TS,
				text: fmt.Sprintf("spawned: %s", sp.Created.Title),
			})
			return
		}
		if sp.Kind == agent.StateChangeTransition && sp.To != nil {
			f.entries = append(f.entries, focusEntry{
				kind: focusStateChange,
				ts:   ev.TS,
				to:   *sp.To,
				text: "→ " + sp.To.String(),
			})
		}
	case agent.EvtProviderCall:
		f.entries = append(f.entries, focusEntry{
			kind: focusProviderCall,
			ts:   ev.TS,
			text: "provider call",
		})
	case agent.EvtToolCall:
		var tc agent.ToolCall
		_ = json.Unmarshal(ev.Payload, &tc)
		f.entries = append(f.entries, focusEntry{
			kind: focusToolCall,
			ts:   ev.TS,
			tool: tc.Name,
		})
	case agent.EvtToolResult:
		// We don't have a result size in the payload yet; render a
		// muted placeholder that matches the brief.
		f.entries = append(f.entries, focusEntry{
			kind: focusToolResult,
			ts:   ev.TS,
			text: "← result",
		})
	case agent.EvtUserMessage:
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		f.entries = append(f.entries, focusEntry{
			kind: focusUserMessage,
			ts:   ev.TS,
			text: p.Text,
		})
	case agent.EvtSteering:
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		f.entries = append(f.entries, focusEntry{
			kind: focusSteering,
			ts:   ev.TS,
			text: p.Text,
		})
	case agent.EvtTokenUsage:
		var t agent.TokenUsage
		_ = json.Unmarshal(ev.Payload, &t)
		if f.ring != nil {
			f.ring.Add(t.DeltaOut)
		}
		// No transcript entry for token deltas - they're noise. The
		// burn-rate sparkline carries the visualization.
	case agent.EvtHeartbeat:
		// Coalesce: at most one heartbeat marker per 5 seconds.
		if ev.TS.Before(f.hbSuppressUntil) {
			return
		}
		f.hbSuppressUntil = ev.TS.Add(5 * time.Second)
		f.entries = append(f.entries, focusEntry{
			kind: focusHeartbeat,
			ts:   ev.TS,
			text: "·",
		})
	case agent.EvtArtifactRef:
		f.entries = append(f.entries, focusEntry{
			kind: focusSystemNote,
			ts:   ev.TS,
			text: "artifact written",
		})
	}
}

// rerender flattens entries into the viewport's content area. Width
// comes from the viewport itself; we wrap entries using lipgloss
// styles so multi-line bodies break cleanly.
func (f *FocusPane) rerender() {
	if f.vp.Width == 0 {
		return
	}
	var b strings.Builder
	for i, e := range f.entries {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(renderFocusEntry(e, f.vp.Width))
	}
	if f.agentID == "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render(
			"select an agent (j/k or ↑/↓ then enter) to view its transcript",
		))
	}
	f.vp.SetContent(b.String())
}

// renderFocusEntry styles one entry. Mirrors chat's renderEntry but
// adds the events the chat view doesn't surface (provider_call,
// heartbeat) since the focus pane is the developer-facing inspector.
func renderFocusEntry(e focusEntry, width int) string {
	body := lipgloss.NewStyle().Width(width).MaxWidth(width)
	stamp := lipgloss.NewStyle().Foreground(colorSubtle).Render(
		e.ts.Local().Format("15:04:05"),
	)
	switch e.kind {
	case focusStateChange:
		text := lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render(e.text)
		return body.Render(stamp + " " + text)
	case focusProviderCall:
		text := lipgloss.NewStyle().Foreground(colorSubtle).Render("→ provider call")
		return body.Render(stamp + " " + text)
	case focusToolCall:
		text := lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render("→ tool: " + e.tool)
		return body.Render(stamp + " " + text)
	case focusToolResult:
		text := lipgloss.NewStyle().Foreground(colorMuted).Render("← " + e.text)
		return body.Render(stamp + " " + text)
	case focusUserMessage:
		text := lipgloss.NewStyle().Foreground(colorAgent).Render(e.text)
		return body.Render(stamp + " 👤 " + text)
	case focusSteering:
		text := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("↻ steer: " + e.text)
		return body.Render(stamp + " " + text)
	case focusHeartbeat:
		text := lipgloss.NewStyle().Foreground(colorSubtle).Render("·")
		return body.Render(stamp + " " + text)
	case focusSystemNote:
		text := lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render("! " + e.text)
		return body.Render(stamp + " " + text)
	}
	return e.text
}
