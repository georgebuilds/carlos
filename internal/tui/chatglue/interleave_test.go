package chatglue

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// callScripted returns a different event slice per Stream call, so a test can
// drive a genuine multi-iteration turn (text+tool, then text+end_turn) that
// the replay-the-same-script fake.Provider can't express.
type callScripted struct {
	mu      sync.Mutex
	calls   int
	perCall [][]providers.Event
}

func (p *callScripted) Name() string                         { return "callscripted" }
func (p *callScripted) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (p *callScripted) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	p.mu.Lock()
	i := p.calls
	p.calls++
	p.mu.Unlock()
	evs := []providers.Event{{Kind: providers.EventStopReason, Stop: "end_turn"}}
	if i < len(p.perCall) {
		evs = p.perCall[i]
	}
	ch := make(chan providers.Event, len(evs))
	go func() {
		defer close(ch)
		for _, e := range evs {
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

type okProbe struct{}

func (okProbe) Name() string                                    { return "probe" }
func (okProbe) Description() string                             { return "probe" }
func (okProbe) Schema() []byte                                  { return []byte(`{"type":"object"}`) }
func (okProbe) Execute(context.Context, []byte) ([]byte, error) { return []byte("ok"), nil }

func appendAssistant(t *testing.T, log *agent.SQLiteEventLog, id, text string) {
	t.Helper()
	payload, _ := json.Marshal(agent.MessagePayload{Text: text})
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtAssistantMessage, Payload: payload,
	}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
}

func appendToolCall(t *testing.T, log *agent.SQLiteEventLog, id, name string) {
	t.Helper()
	payload, _ := json.Marshal(agent.ToolCall{Name: name, Input: []byte(`{}`)})
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtToolCall, Payload: payload,
	}); err != nil {
		t.Fatalf("append tool_call: %v", err)
	}
}

// TestSealPendingText: sealing persists the buffered segment as its own
// assistant event, resets the live source, and records that text was written;
// an empty buffer is a no-op.
func TestSealPendingText(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-seal"
	seedAgent(t, log, id)
	src := newMemSource()
	src.Append(id, "  preamble text  ")
	l := &Loop{log: log, agentID: id, ctx: context.Background(), source: src}

	l.sealPendingText()
	if src.Get(id) != "" {
		t.Errorf("source not reset after seal: %q", src.Get(id))
	}
	if !l.wroteText {
		t.Error("wroteText should be set after sealing a segment")
	}
	got := ""
	evs, _ := log.Read(context.Background(), id, 0)
	for _, ev := range evs {
		if ev.Type == agent.EvtAssistantMessage {
			var p agent.MessagePayload
			_ = json.Unmarshal(ev.Payload, &p)
			got = p.Text
		}
	}
	if got != "preamble text" {
		t.Errorf("sealed segment = %q, want trimmed %q", got, "preamble text")
	}

	// Empty source: no-op, no new event, flag unchanged.
	l.wroteText = false
	l.sealPendingText()
	if l.wroteText {
		t.Error("sealing an empty buffer must not set wroteText")
	}
}

// TestBuildHistory_MergesConsecutiveAssistant: a turn now logs one assistant
// event per iteration (interleaved with tool events in the transcript), but
// buildHistory must hand the model ONE assistant message per turn, since
// providers like Anthropic reject consecutive same-role messages.
func TestBuildHistory_MergesConsecutiveAssistant(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-merge"
	seedAgent(t, log, id)
	appendUserMessage(t, log, id, "hi")
	appendAssistant(t, log, id, "let me check")
	appendToolCall(t, log, id, "notes_search") // dropped from history, but separates the segments
	appendAssistant(t, log, id, "here it is")

	l := &Loop{log: log, agentID: id, source: newMemSource()}
	hist, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatalf("buildHistory: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("history len = %d, want 2 (user + one merged assistant): %+v", len(hist), hist)
	}
	if hist[0].Role != "user" || hist[1].Role != "assistant" {
		t.Fatalf("roles = %q,%q want user,assistant", hist[0].Role, hist[1].Role)
	}
	if got, want := hist[1].Content[0].Text, "let me check\n\nhere it is"; got != want {
		t.Errorf("merged assistant text = %q, want %q", got, want)
	}
}

// TestLoop_InterleavesTextAndTools drives a real two-iteration turn (preamble
// + tool, then conclusion) and asserts the event log interleaves: the preamble
// is sealed as its own assistant event ABOVE the tool, not collected into one
// block after it.
func TestLoop_InterleavesTextAndTools(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-interleave"
	seedAgent(t, log, id)
	reg := tools.NewRegistry()
	reg.Register(okProbe{})
	prov := &callScripted{perCall: [][]providers.Event{
		{
			{Kind: providers.EventTextDelta, Text: "preamble "},
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "t1", Name: "probe", Input: []byte(`{}`)}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "t1", Name: "probe", Input: []byte(`{}`)}},
			{Kind: providers.EventStopReason, Stop: "tool_use"},
		},
		{
			{Kind: providers.EventTextDelta, Text: "conclusion"},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	l := NewLoop(Config{Provider: prov, Tools: reg, Approver: agent.AutoApprover{}, MaxIterations: 5}, log, newMemSource(), id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()
	time.Sleep(50 * time.Millisecond)
	appendUserMessage(t, log, id, "go")
	_ = waitForAssistant(t, log, id, "conclusion")

	var seq []string
	evs, _ := log.Read(context.Background(), id, 0)
	for _, ev := range evs {
		switch ev.Type {
		case agent.EvtUserMessage:
			seq = append(seq, "user")
		case agent.EvtAssistantMessage:
			var p agent.MessagePayload
			_ = json.Unmarshal(ev.Payload, &p)
			seq = append(seq, "asst:"+strings.TrimSpace(p.Text))
		case agent.EvtToolCall:
			seq = append(seq, "tool_call")
		case agent.EvtToolResult:
			seq = append(seq, "tool_result")
		}
	}
	want := []string{"user", "asst:preamble", "tool_call", "tool_result", "asst:conclusion"}
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Errorf("event order = %v\nwant interleaved %v", seq, want)
	}
}
