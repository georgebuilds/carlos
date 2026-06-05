package chatglue

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
)

// memSource is a hand-rolled TextSource implementation used by these
// tests — same shape as chat.MemTextSource but local so we don't
// import the chat package and risk a cycle in the future.
type memSource struct {
	mu   sync.Mutex
	bufs map[string]string
}

func newMemSource() *memSource { return &memSource{bufs: map[string]string{}} }

func (m *memSource) Append(id, delta string) {
	m.mu.Lock()
	m.bufs[id] += delta
	m.mu.Unlock()
}
func (m *memSource) Reset(id string) {
	m.mu.Lock()
	delete(m.bufs, id)
	m.mu.Unlock()
}
func (m *memSource) Get(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bufs[id]
}

func openTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(tmp, "artifacts"))
	log, err := agent.OpenSQLiteEventLog(filepath.Join(tmp, "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func seedAgent(t *testing.T, log *agent.SQLiteEventLog, id string) {
	t.Helper()
	ctx := context.Background()
	payload, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: id, RootID: id, Title: id, Model: "fake",
	})
	if _, err := log.Append(ctx, agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: payload,
	}); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	now := time.Now().UTC()
	if err := log.InsertAgent(ctx, agent.AgentRow{
		ID: id, RootID: id, State: agent.StateRunning, Attempt: 1,
		Title: id, Model: "fake", CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
}

func appendUserMessage(t *testing.T, log *agent.SQLiteEventLog, id, text string) {
	t.Helper()
	payload, _ := json.Marshal(agent.MessagePayload{Text: text})
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtUserMessage, Payload: payload,
	}); err != nil {
		t.Fatalf("append user_message: %v", err)
	}
}

// waitForAssistant polls the event log until an EvtAssistantMessage
// for agentID with text containing want lands, or the deadline trips.
// Returns the sealed text on success.
func waitForAssistant(t *testing.T, log *agent.SQLiteEventLog, id, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		evs, _ := log.Read(context.Background(), id, 0)
		for _, ev := range evs {
			if ev.Type != agent.EvtAssistantMessage {
				continue
			}
			var p agent.MessagePayload
			_ = json.Unmarshal(ev.Payload, &p)
			if strings.Contains(p.Text, want) {
				return p.Text
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no EvtAssistantMessage containing %q within deadline", want)
	return ""
}

func scriptedProvider(deltas ...string) *fake.Provider {
	script := make(fake.Script, 0, len(deltas)+1)
	for _, d := range deltas {
		script = append(script, providers.Event{Kind: providers.EventTextDelta, Text: d})
	}
	script = append(script, providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"})
	return fake.New("fake", script)
}

func TestLoop_StreamsDeltasIntoSourceAndSealsAssistantEvent(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-1"
	seedAgent(t, log, id)
	src := newMemSource()

	l := NewLoop(Config{Provider: scriptedProvider("Hello, ", "Boss.")}, log, src, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	// Let Subscribe register before the user message lands so the
	// pump goroutine sees it via the subscription, not just on next
	// read (the real chat flow goes through both paths).
	time.Sleep(50 * time.Millisecond)
	appendUserMessage(t, log, id, "hey carlos")

	full := waitForAssistant(t, log, id, "Hello, Boss.")
	if full != "Hello, Boss." {
		t.Errorf("sealed text = %q, want exact match", full)
	}
	// TextSource should have been reset on turn end.
	if got := src.Get(id); got != "" {
		t.Errorf("TextSource not reset; still holds %q", got)
	}
}

// TestLoop_SessionResetDropsPriorContext is the regression check for
// `/clear`: without the reset, the model kept replying as if mid-
// conversation (the "after /clear and 'hi', carlos kept trying to run
// the previous bash" report from field testing). With the reset
// marker, buildHistory drops everything before the latest reset.
func TestLoop_SessionResetDropsPriorContext(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-reset"
	seedAgent(t, log, id)

	// Prior turn.
	appendUserMessage(t, log, id, "delete the readme")
	priorAssistant, _ := json.Marshal(agent.MessagePayload{Text: "deleted."})
	_, _ = log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtAssistantMessage, Payload: priorAssistant,
	})
	// /clear lands.
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtSessionReset, Payload: []byte("{}"),
	}); err != nil {
		t.Fatalf("append reset: %v", err)
	}
	// New user turn after the reset.
	appendUserMessage(t, log, id, "hi")

	l := NewLoop(Config{Provider: scriptedProvider("hey")}, log, newMemSource(), id)
	hist, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Only the post-reset user message should remain.
	if len(hist) != 1 {
		t.Fatalf("history len = %d, want 1 (only the post-reset 'hi')", len(hist))
	}
	if hist[0].Role != "user" || hist[0].Content[0].Text != "hi" {
		t.Errorf("history[0] = %+v, want user/'hi'", hist[0])
	}
}

func TestLoop_HistoryProjectionIncludesPriorTurns(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-2"
	seedAgent(t, log, id)
	src := newMemSource()

	// Seed a prior turn directly so the new run sees it in history.
	appendUserMessage(t, log, id, "what's the weather")
	priorAssistant, _ := json.Marshal(agent.MessagePayload{Text: "sunny."})
	_, _ = log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtAssistantMessage, Payload: priorAssistant,
	})

	l := NewLoop(Config{Provider: scriptedProvider("ack")}, log, src, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = l.Start(ctx)
	defer l.Stop()
	time.Sleep(50 * time.Millisecond)

	// Drive buildHistory directly to confirm both prior turns surface.
	hist, err := l.buildHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("history len = %d, want 2 (prior user + prior assistant)", len(hist))
	}
	if hist[0].Role != "user" || hist[1].Role != "assistant" {
		t.Errorf("history roles = %s/%s, want user/assistant", hist[0].Role, hist[1].Role)
	}
}

func TestLoop_StopIsIdempotent(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-3"
	seedAgent(t, log, id)
	src := newMemSource()
	l := NewLoop(Config{Provider: scriptedProvider("x")}, log, src, id)
	_ = l.Start(context.Background())
	l.Stop()
	l.Stop() // second call must not panic
}

// finalAssistantText must concat text across ALL assistant messages
// in the returned slice, not just the last — a tool-use turn yields
// (assistant: "let me check…", tool_use), then (assistant: "here it is").
// Persisting only the second one drops the preamble the user just
// watched stream in.
func TestFinalAssistantText_MultiIterationConcat(t *testing.T) {
	msgs := []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}},
		{Role: "assistant", Content: []providers.Block{
			{Kind: "text", Text: "let me check"},
			{Kind: "tool_use", Text: ""},
		}},
		{Role: "tool", Content: []providers.Block{{Kind: "tool_result", Text: ""}}},
		{Role: "assistant", Content: []providers.Block{{Kind: "text", Text: "here it is"}}},
	}
	got := finalAssistantText(msgs)
	want := "let me check\n\nhere it is"
	if got != want {
		t.Errorf("finalAssistantText = %q, want %q", got, want)
	}
}

func TestLoop_NonUserEventsIgnored(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-4"
	seedAgent(t, log, id)
	src := newMemSource()
	l := NewLoop(Config{Provider: scriptedProvider("nope")}, log, src, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = l.Start(ctx)
	defer l.Stop()
	time.Sleep(50 * time.Millisecond)

	// Append a heartbeat. chatglue should NOT spin a run for this.
	_, _ = log.Append(ctx, agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtHeartbeat, Payload: []byte("{}"),
	})

	// Give the loop a chance to do nothing.
	time.Sleep(100 * time.Millisecond)
	evs, _ := log.Read(ctx, id, 0)
	for _, ev := range evs {
		if ev.Type == agent.EvtAssistantMessage {
			t.Fatalf("heartbeat triggered an assistant turn (event seq=%d)", ev.Seq)
		}
	}
}
