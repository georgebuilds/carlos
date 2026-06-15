package chat

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// The textTick fires every 33ms for the whole session. It must only do
// the expensive transcript recompose (glamour over every sealed entry +
// SetContent + GotoBottom) while something is actually animating - a
// settled conversation that repaints 30x/sec flickers the input and
// starves keystrokes ("input flickers/lags when scrolled to the
// bottom"). thinkingTick is bumped only inside that animating branch, so
// it's the observable proxy for "this tick did the heavy path".

func applyAssistantMessage(t *testing.T, m *Model, agentID, text string) *Model {
	t.Helper()
	payload, _ := json.Marshal(agent.MessagePayload{Text: text})
	updated, _ := m.Update(eventMsg{ev: agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtAssistantMessage,
		Payload: payload,
	}})
	return updated.(*Model)
}

func transitionRunning(t *testing.T, m *Model, agentID string) *Model {
	t.Helper()
	payload, err := agent.NewStateChangeTransition(agent.StateRunning)
	if err != nil {
		t.Fatalf("transition payload: %v", err)
	}
	updated, _ := m.Update(eventMsg{ev: agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtStateChange,
		Payload: payload,
	}})
	return updated.(*Model)
}

// TestTextTick_IdleSkipsRepaint is the regression guard: on a settled
// conversation (assistant turn landed, no live buffer), a textTick must
// NOT run the animating path.
func TestTextTick_IdleSkipsRepaint(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000IDLE01"
	seedAgent(t, log, agentID, "idle tick", "fake")
	seedUserMessage(t, log, agentID, "hello")

	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	m = applyAssistantMessage(t, m, agentID, "all done")

	if m.isThinking() {
		t.Fatal("precondition: a settled conversation should not be thinking")
	}
	before := m.thinkingTick
	updated, _ := m.Update(textTickMsg{})
	m = updated.(*Model)
	if m.thinkingTick != before {
		t.Errorf("idle textTick advanced the animating path (thinkingTick %d -> %d); it must skip the repaint", before, m.thinkingTick)
	}
}

// TestTextTick_ThinkingRepaints proves the gate still animates the
// thinking pulse between submit and the first token.
func TestTextTick_ThinkingRepaints(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000THINK1"
	seedAgent(t, log, agentID, "thinking tick", "fake")
	seedUserMessage(t, log, agentID, "hello")

	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	m = transitionRunning(t, m, agentID)

	if !m.isThinking() {
		t.Fatal("precondition: in-flight with a trailing user message should be thinking")
	}
	before := m.thinkingTick
	updated, _ := m.Update(textTickMsg{})
	m = updated.(*Model)
	if m.thinkingTick != before+1 {
		t.Errorf("thinking textTick should advance the pulse (thinkingTick %d -> %d)", before, m.thinkingTick)
	}
}

// TestTextTick_StreamingRepaints proves the gate animates while a live
// assistant buffer is present, and advances the typewriter reveal.
func TestTextTick_StreamingRepaints(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV000000000000000STREAM1"
	seedAgent(t, log, agentID, "streaming tick", "fake")
	seedUserMessage(t, log, agentID, "hello")

	src := NewMemTextSource()
	m := New(log, agentID, src)
	m = drive(t, m, 120, 30)
	src.Append(agentID, "a streaming assistant reply in flight")

	if m.isThinking() {
		t.Fatal("precondition: live text should suppress the thinking pulse")
	}
	beforeTick := m.thinkingTick
	beforeReveal := m.typeRevealed
	updated, _ := m.Update(textTickMsg{})
	m = updated.(*Model)
	if m.thinkingTick != beforeTick+1 {
		t.Errorf("streaming textTick should run the animating path (thinkingTick %d -> %d)", beforeTick, m.thinkingTick)
	}
	if !reducedMotion && m.typeRevealed <= beforeReveal {
		t.Errorf("streaming textTick should advance the typewriter reveal (%d -> %d)", beforeReveal, m.typeRevealed)
	}
}
