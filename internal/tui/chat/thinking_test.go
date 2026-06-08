package chat

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestThinking_ShowsAfterUserMessageWhileRunning is the headline
// behavior: the user submits a prompt, the projection moves to
// running, and the activity indicator paints at the bottom of the
// transcript so the user sees signs of life before the first model
// output lands.
func TestThinking_ShowsAfterUserMessageWhileRunning(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000F00"
	seedAgent(t, log, agentID, "thinking", "fake")
	seedUserMessage(t, log, agentID, "hello")

	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	// Transition into running so the indicator's state predicate
	// trips.
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
	m = updated.(*Model)

	if !m.isThinking() {
		t.Fatal("expected isThinking after user msg + running transition")
	}
	if !strings.Contains(m.View(), "thinking") {
		t.Errorf("View did not include the 'thinking' label:\n%s", m.View())
	}
}

// TestThinking_HiddenWhenLiveTextStreaming guards against the
// indicator competing with streaming assistant text. Once tokens
// flow through the TextSource, the spinner should retire.
func TestThinking_HiddenWhenLiveTextStreaming(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000F01"
	seedAgent(t, log, agentID, "live text wins", "fake")
	seedUserMessage(t, log, agentID, "hello")

	src := NewMemTextSource()
	m := New(log, agentID, src)
	m = drive(t, m, 120, 30)

	// In running state but with live text — spinner should hide.
	payload, _ := agent.NewStateChangeTransition(agent.StateRunning)
	updated, _ := m.Update(eventMsg{ev: agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtStateChange,
		Payload: payload,
	}})
	m = updated.(*Model)
	src.Append(agentID, "first token...")

	if m.isThinking() {
		t.Error("expected isThinking=false while live text is streaming")
	}
}

// TestThinking_HiddenAfterAssistantMessage proves we don't paint a
// spinner under a completed assistant turn. Once an assistant
// message lands, the model is either waiting for input (no spinner
// needed) or running a follow-up (covered by the post-tool-call
// path); either way the spinner under a finished bubble would read
// as "still thinking" and confuse the user.
func TestThinking_HiddenAfterAssistantMessage(t *testing.T) {
	m := newThinkingTestModel(t, "01HV0000000000000000000F02")
	m.transcript = append(m.transcript, transcriptEntry{
		kind: entryAssistantMessage,
		ts:   time.Now(),
		text: "done",
	})
	// Force running state via the projection.
	forceRunning(t, m)

	if m.isThinking() {
		t.Error("expected isThinking=false when last entry is an assistant message")
	}
}

// TestThinking_ShowsBetweenToolCalls covers the multi-tool-call use
// case the user flagged: after a tool result lands the model is
// still computing, and the indicator should reappear so the gap
// before the next tool call doesn't feel dead.
func TestThinking_ShowsBetweenToolCalls(t *testing.T) {
	m := newThinkingTestModel(t, "01HV0000000000000000000F03")
	m.transcript = append(m.transcript, transcriptEntry{
		kind:      entryToolCall,
		ts:        time.Now(),
		tool:      "bash",
		hasResult: true,
	})
	forceRunning(t, m)

	if !m.isThinking() {
		t.Error("expected isThinking=true between tool calls")
	}
}

// TestThinking_TickAdvancesAnimation drives the textTickMsg path and
// verifies the frame counter walks forward — the visual difference
// is what gives the user the "alive, not stuck" read.
func TestThinking_TickAdvancesAnimation(t *testing.T) {
	m := newThinkingTestModel(t, "01HV0000000000000000000F04")
	start := m.thinkingTick
	for i := 0; i < thinkingFrameTicks*2; i++ {
		next, _ := m.Update(textTickMsg{})
		m = next.(*Model)
	}
	if m.thinkingTick <= start {
		t.Errorf("thinkingTick did not advance across ticks: start=%d end=%d", start, m.thinkingTick)
	}

	frameA := frameIndex(start)
	frameB := frameIndex(m.thinkingTick)
	if frameA == frameB {
		t.Errorf("animation frame did not advance: %d -> %d", frameA, frameB)
	}
}

// TestThinking_ElapsedTimerAppearsAfterThreshold proves the "(Ns)"
// trailer kicks in once a wait drags on, and stays hidden for the
// common quick reply.
func TestThinking_ElapsedTimerAppearsAfterThreshold(t *testing.T) {
	short := renderThinkingRow(0, 1*time.Second, 80)
	if strings.Contains(short, "s") && strings.Contains(short, "·") {
		t.Errorf("elapsed timer should be hidden under threshold:\n%s", short)
	}
	long := renderThinkingRow(0, 5*time.Second, 80)
	if !strings.Contains(long, "5s") {
		t.Errorf("elapsed timer should show after threshold:\n%s", long)
	}
}

// TestThinking_ElapsedZeroOnEmptyTranscript covers the early-return
// branch in thinkingElapsed when there's nothing to anchor on.
func TestThinking_ElapsedZeroOnEmptyTranscript(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000F05"
	seedAgent(t, log, agentID, "empty elapsed", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	m.transcript = nil
	if got := m.thinkingElapsed(); got != 0 {
		t.Errorf("empty transcript should return 0 elapsed; got %v", got)
	}
}

// TestThinking_HiddenWhenTranscriptEmpty pins the no-anchor branch
// of isThinking: even with the agent running we don't paint a
// spinner over a fresh, blank session.
func TestThinking_HiddenWhenTranscriptEmpty(t *testing.T) {
	m := newThinkingTestModel(t, "01HV0000000000000000000F06")
	m.transcript = nil
	forceRunning(t, m)
	if m.isThinking() {
		t.Error("expected isThinking=false when transcript is empty")
	}
}

// frameIndex mirrors renderThinkingDots' frame math so the test can
// reason about animation without rerendering.
func frameIndex(tick int) int {
	return (tick / thinkingFrameTicks) % len(thinkingFrames)
}

// newThinkingTestModel builds a Model with a seeded transcript so
// isThinking has something to anchor on, and a stub user message
// already in place. Used by the assertions that don't care about
// the SQLite log path.
func newThinkingTestModel(t *testing.T, agentID string) *Model {
	t.Helper()
	log := openTempLog(t)
	seedAgent(t, log, agentID, "thinking model", "fake")
	seedUserMessage(t, log, agentID, "hi")
	m := New(log, agentID, NewMemTextSource())
	return drive(t, m, 120, 30)
}

// forceRunning bumps the projection into StateRunning by injecting a
// state-change event through Update. Mirrors what a real provider
// does immediately after submit.
func forceRunning(t *testing.T, m *Model) {
	t.Helper()
	payload, err := agent.NewStateChangeTransition(agent.StateRunning)
	if err != nil {
		t.Fatalf("transition payload: %v", err)
	}
	updated, _ := m.Update(eventMsg{ev: agent.Event{
		AgentID: m.agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtStateChange,
		Payload: payload,
	}})
	_ = updated
}

// ensure tea import stays used even if Update drops it later.
var _ = tea.KeyMsg{}
