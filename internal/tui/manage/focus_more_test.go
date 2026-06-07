package manage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestFocusPane_ApplyBackfill_FoldsAllEvents seeds a slice of events
// through ApplyBackfill and asserts each surfaces in the rendered
// transcript so the orchestrator's "focus change → backfill" path
// stays exercised.
func TestFocusPane_ApplyBackfill_FoldsAllEvents(t *testing.T) {
	f := NewFocusPane()
	f.Bind("01HVfocusabc1234")
	f.Resize(80, 24)

	now := time.Now().UTC()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "01HVfocusabc1234", RootID: "01HVfocusabc1234", Title: "backfill demo", Model: "fake",
	})
	transition, _ := agent.NewStateChangeTransition(agent.StateRunning)
	steerPayload, _ := json.Marshal(map[string]string{"text": "go faster"})
	usrPayload, _ := json.Marshal(map[string]string{"text": "hello"})
	toolPayload, _ := json.Marshal(agent.ToolCall{Name: "bash"})
	tokenPayload, _ := json.Marshal(agent.TokenUsage{DeltaOut: 42})

	events := []agent.Event{
		{Type: agent.EvtStateChange, TS: now, Payload: created},
		{Type: agent.EvtStateChange, TS: now.Add(time.Second), Payload: transition},
		{Type: agent.EvtProviderCall, TS: now.Add(2 * time.Second)},
		{Type: agent.EvtToolCall, TS: now.Add(3 * time.Second), Payload: toolPayload},
		{Type: agent.EvtToolResult, TS: now.Add(4 * time.Second)},
		{Type: agent.EvtUserMessage, TS: now.Add(5 * time.Second), Payload: usrPayload},
		{Type: agent.EvtSteering, TS: now.Add(6 * time.Second), Payload: steerPayload},
		{Type: agent.EvtTokenUsage, TS: now.Add(7 * time.Second), Payload: tokenPayload},
		{Type: agent.EvtHeartbeat, TS: now.Add(8 * time.Second)},
		{Type: agent.EvtArtifactRef, TS: now.Add(9 * time.Second)},
	}
	f.ApplyBackfill(events)

	view := f.View()
	for _, want := range []string{
		"backfill demo", "running", "provider call", "bash", "result",
		"hello", "go faster", "artifact",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("backfill view missing %q:\n%s", want, view)
		}
	}

	// Ring should have absorbed the token delta.
	if ring := f.Ring(); ring != nil {
		var sum int64
		for _, v := range ring.Snapshot() {
			sum += v
		}
		if sum != 42 {
			t.Errorf("ring sum = %d, want 42", sum)
		}
	}
}

// TestFocusPane_Heartbeat_Coalesces drives two heartbeats within 5s
// and asserts only the first records an entry - the suppression window
// is what keeps a sea of heartbeats from drowning the transcript.
func TestFocusPane_Heartbeat_Coalesces(t *testing.T) {
	f := NewFocusPane()
	f.Bind("01HVhbabc1234567")
	f.Resize(80, 24)

	base := time.Now().UTC()
	f.ApplyEvent(agent.Event{Type: agent.EvtHeartbeat, TS: base})
	// Second heartbeat 1s later - inside the 5s coalesce window.
	f.ApplyEvent(agent.Event{Type: agent.EvtHeartbeat, TS: base.Add(1 * time.Second)})

	if got := len(f.entries); got != 1 {
		t.Errorf("heartbeat entries = %d, want 1 (coalesced)", got)
	}

	// 6s later - outside the window, should land.
	f.ApplyEvent(agent.Event{Type: agent.EvtHeartbeat, TS: base.Add(6 * time.Second)})
	if got := len(f.entries); got != 2 {
		t.Errorf("heartbeat entries after window = %d, want 2", got)
	}
}

// TestFocusPane_Scroll_TogglesAutoScroll asserts the manual-scroll
// interactions flip autoScroll the way the orchestrator depends on:
// scrolling up suspends auto-pinning to the bottom; scrolling back to
// the bottom re-enables it.
func TestFocusPane_Scroll_TogglesAutoScroll(t *testing.T) {
	f := NewFocusPane()
	f.Bind("01HVscrollabcxyz")
	f.Resize(40, 4)

	// Push enough entries so the viewport actually has overflow.
	now := time.Now().UTC()
	for i := 0; i < 30; i++ {
		payload, _ := json.Marshal(map[string]string{"text": "line"})
		f.ApplyEvent(agent.Event{
			Type:    agent.EvtUserMessage,
			TS:      now.Add(time.Duration(i) * time.Second),
			Payload: payload,
		})
	}

	// After ApplyEvent at-bottom, the pane snaps to bottom + autoScroll true.
	if !f.autoScroll {
		t.Fatalf("initial autoScroll should be true after ApplyEvent at-bottom")
	}

	// ScrollUp suspends autoScroll.
	f.ScrollUp()
	if f.autoScroll {
		t.Error("ScrollUp() did not suspend autoScroll")
	}

	// PageUp suspends autoScroll.
	f.autoScroll = true
	f.PageUp()
	if f.autoScroll {
		t.Error("PageUp() did not suspend autoScroll")
	}

	// PageDown back to the bottom should re-enable autoScroll.
	f.PageDown()
	f.PageDown()
	f.PageDown()
	if !f.vp.AtBottom() {
		t.Fatalf("PageDown chain didn't reach bottom (vp height=%d)", f.vp.Height)
	}
	if !f.autoScroll {
		t.Error("PageDown() at bottom should re-enable autoScroll")
	}

	// ScrollDown at the bottom should keep autoScroll true.
	f.ScrollDown()
	if !f.autoScroll {
		t.Error("ScrollDown() at-bottom should keep autoScroll true")
	}
}

// TestFocusPane_Resize_NoOpOnSameDims is a small invariant: passing the
// same dimensions twice does not re-render unnecessarily. We can't
// directly observe re-render, but we can confirm the call returns
// without panic + the viewport dims are preserved.
func TestFocusPane_Resize_NoOpOnSameDims(t *testing.T) {
	f := NewFocusPane()
	f.Bind("01HVresizeabc123")
	f.Resize(80, 24)
	f.Resize(80, 24)
	f.Resize(-1, -1) // clamps to 1x1
	if f.vp.Width < 1 || f.vp.Height < 1 {
		t.Errorf("Resize(-1, -1) didn't clamp: %dx%d", f.vp.Width, f.vp.Height)
	}
}
