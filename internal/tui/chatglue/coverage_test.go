package chatglue

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/tools"
)

// errorScriptProvider returns a fake provider whose stream surfaces an
// EventError, which agent.Run propagates as a run error. Used to drive
// the surfaceError / handleUserMessage error branch.
func errorScriptProvider(err error) *fake.Provider {
	return fake.New("fake-err", fake.Script{
		{Kind: providers.EventTextDelta, Text: "partial"},
		{Kind: providers.EventError, Err: err},
	})
}

// TestLoop_SurfaceError_PersistsTaggedErrorCard drives the agent.Run
// error path end-to-end: a provider stream that errors mid-turn must
// land a single EvtAssistantMessage tagged with ErrorEventPrefix so the
// chat surface renders an error card, and the live TextSource must be
// reset.
func TestLoop_SurfaceError_PersistsTaggedErrorCard(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-err"
	seedAgent(t, log, id)
	src := newMemSource()

	l := NewLoop(Config{Provider: errorScriptProvider(errors.New("boom"))}, log, src, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	time.Sleep(50 * time.Millisecond)
	appendUserMessage(t, log, id, "hi")

	full := waitForAssistant(t, log, id, ErrorEventPrefix)
	if !strings.HasPrefix(full, ErrorEventPrefix) {
		t.Errorf("error card not tagged: %q", full)
	}
	if !strings.Contains(full, "boom") {
		t.Errorf("error card lost the underlying error text: %q", full)
	}
	// TextSource must be reset after surfaceError.
	if got := src.Get(id); got != "" {
		t.Errorf("TextSource not reset after error; still holds %q", got)
	}
}

// TestLoop_SurfaceError_Unit calls surfaceError directly and confirms
// it persists exactly one tagged assistant event and resets the source.
func TestLoop_SurfaceError_Unit(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-err-unit"
	seedAgent(t, log, id)
	src := newMemSource()
	l := &Loop{log: log, agentID: id, source: src}

	l.surfaceError(context.Background(), errors.New("load history: disk gone"))

	evs, _ := log.Read(context.Background(), id, 0)
	var n int
	var text string
	for _, ev := range evs {
		if ev.Type == agent.EvtAssistantMessage {
			n++
			var p agent.MessagePayload
			_ = json.Unmarshal(ev.Payload, &p)
			text = p.Text
		}
	}
	if n != 1 {
		t.Fatalf("surfaceError persisted %d assistant events, want 1", n)
	}
	if !strings.HasPrefix(text, ErrorEventPrefix) || !strings.Contains(text, "disk gone") {
		t.Errorf("surfaceError text = %q, want prefixed + underlying error", text)
	}
}

// noTextToolUseMsgs models the slice agent.Run returns when the model
// ran a tool but emitted no wrap-up text. handleUserMessage uses
// hadToolUse to detect this and substitute the placeholder.
func TestHadToolUse(t *testing.T) {
	withTool := []providers.Message{
		{Role: "assistant", Content: []providers.Block{
			{Kind: "tool_use", ToolName: "bash"},
		}},
		{Role: "tool", Content: []providers.Block{{Kind: "tool_result"}}},
	}
	if !hadToolUse(withTool) {
		t.Error("hadToolUse should report true when a tool_use block is present")
	}
	textOnly := []providers.Message{
		{Role: "assistant", Content: []providers.Block{{Kind: "text", Text: "hello"}}},
	}
	if hadToolUse(textOnly) {
		t.Error("hadToolUse should report false for a text-only turn")
	}
	if hadToolUse(nil) {
		t.Error("hadToolUse(nil) should be false")
	}
}

// echoTool is a minimal tools.Tool: it succeeds with no output, which is
// enough to let agent.Run finish a tool_use iteration cleanly.
type echoTool struct{ name string }

func (e echoTool) Name() string        { return e.name }
func (e echoTool) Description() string { return "echo for tests" }
func (e echoTool) Schema() []byte      { return []byte(`{"type":"object"}`) }
func (e echoTool) Execute(context.Context, []byte) ([]byte, error) {
	return []byte("ok"), nil
}

// TestBuildToolSpecs covers both the nil-registry (text-only) branch and
// the populated-registry lift into []providers.ToolSpec.
func TestBuildToolSpecs(t *testing.T) {
	if specs := buildToolSpecs(nil); specs != nil {
		t.Errorf("buildToolSpecs(nil) = %v, want nil (text-only run)", specs)
	}

	reg := tools.NewRegistry()
	reg.Register(echoTool{name: "alpha"})
	reg.Register(echoTool{name: "beta"})
	specs := buildToolSpecs(reg)
	if len(specs) != 2 {
		t.Fatalf("buildToolSpecs returned %d specs, want 2", len(specs))
	}
	// All() sorts by name; spec order must follow.
	if specs[0].Name != "alpha" || specs[1].Name != "beta" {
		t.Errorf("spec names/order = %q,%q want alpha,beta", specs[0].Name, specs[1].Name)
	}
	if specs[0].Description != "echo for tests" || string(specs[0].Schema) != `{"type":"object"}` {
		t.Errorf("spec[0] lost description/schema: %+v", specs[0])
	}
}

// TestLoop_NoFollowUpText_AfterTools drives the full handleUserMessage
// path where the model runs a tool and then stops WITHOUT a wrap-up
// message. handleUserMessage must seal the "(no follow-up text after
// tools)" placeholder rather than dropping the turn silently.
//
// Script: a tool_use round-trip ending in stop_reason "tool_use", then
// on the second iteration the SAME script replays — but with
// MaxIterations clamped we instead arrange a provider that ends the
// second pass with end_turn and no text. We use a two-phase fake by
// scripting tool_use, then end_turn, relying on the loop replaying the
// script each Stream call: the first call sees tool_use (executes the
// tool), the second call sees the same events again. To make the second
// pass terminate without text we register a tool and cap iterations so
// the loop exits via ErrMaxIterations is avoided — instead we use a
// dedicated provider that flips behavior across calls.
func TestLoop_NoFollowUpText_AfterTools(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-notext"
	seedAgent(t, log, id)
	src := newMemSource()

	reg := tools.NewRegistry()
	reg.Register(echoTool{name: "noop"})

	// Phase-flipping provider: first Stream → tool_use; second Stream →
	// end_turn with no text. This produces the (tool ran, no wrap-up)
	// shape agent.Run hands back, exercising the hadToolUse placeholder.
	prov := &phaseProvider{
		phases: []fake.Script{
			{
				{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "t1", Name: "noop", Input: []byte(`{}`)}},
				{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "t1", Name: "noop"}},
				{Kind: providers.EventStopReason, Stop: "tool_use"},
			},
			{
				{Kind: providers.EventStopReason, Stop: "end_turn"},
			},
		},
	}

	l := NewLoop(Config{Provider: prov, Tools: reg}, log, src, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	time.Sleep(50 * time.Millisecond)
	appendUserMessage(t, log, id, "run the tool")

	full := waitForAssistant(t, log, id, "no follow-up text")
	if full != "(no follow-up text after tools)" {
		t.Errorf("placeholder text = %q, want exact no-follow-up message", full)
	}
}

// TestLoop_BuildHistory_SkipsBadPayloadAndEmptyText covers buildHistory's
// two continue branches: an EvtUserMessage with a corrupt JSON payload
// (unmarshal fails → skipped) and an EvtAssistantMessage whose Text is
// empty (skipped). Neither should reach the projected history.
func TestLoop_BuildHistory_SkipsBadPayloadAndEmptyText(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-hist"
	seedAgent(t, log, id)

	ctx := context.Background()
	// Corrupt user payload — not valid JSON for MessagePayload.
	if _, err := log.Append(ctx, agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtUserMessage, Payload: []byte("{not json"),
	}); err != nil {
		t.Fatalf("append corrupt: %v", err)
	}
	// Empty-text assistant payload — valid JSON, empty Text → skipped.
	emptyAsst, _ := json.Marshal(agent.MessagePayload{Text: ""})
	if _, err := log.Append(ctx, agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtAssistantMessage, Payload: emptyAsst,
	}); err != nil {
		t.Fatalf("append empty asst: %v", err)
	}
	// A good user message that should survive.
	appendUserMessage(t, log, id, "keep me")

	l := NewLoop(Config{}, log, newMemSource(), id)
	hist, err := l.buildHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("history len = %d, want 1 (only the well-formed user message)", len(hist))
	}
	if hist[0].Role != "user" || hist[0].Content[0].Text != "keep me" {
		t.Errorf("surviving history entry = %+v, want user/'keep me'", hist[0])
	}
}

// TestLoop_BuildHistory_ReadError covers buildHistory's Read-error
// return: a closed event log fails the underlying query, and the error
// must propagate (not be swallowed into an empty history).
func TestLoop_BuildHistory_ReadError(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-readerr"
	seedAgent(t, log, id)
	_ = log.Close() // subsequent Read fails

	l := NewLoop(Config{}, log, newMemSource(), id)
	if _, err := l.buildHistory(context.Background()); err == nil {
		t.Error("buildHistory on a closed log should return the Read error")
	}
}

// TestLoop_HandleUserMessage_HistoryErrorSurfaces drives the
// handleUserMessage "load history" error branch: the log is closed
// after Subscribe registers, so buildHistory fails inside the handler
// and surfaceError must persist a tagged error card.
func TestLoop_HandleUserMessage_HistoryErrorSurfaces(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-histerr"
	seedAgent(t, log, id)
	src := newMemSource()

	l := NewLoop(Config{Provider: scriptedProvider("unused")}, log, src, id)
	// Call handleUserMessage directly with the log closed so buildHistory
	// fails. surfaceError persists into the (closed) log's in-memory
	// subscriber path — assert via the source reset, which surfaceError
	// always performs even when the append no-ops.
	_ = log.Close()
	l.handleUserMessage(context.Background(), agent.Event{Type: agent.EvtUserMessage})

	// surfaceError always resets the source; getting here without a
	// panic + with the buildHistory branch taken is the coverage signal.
	if got := src.Get(id); got != "" {
		t.Errorf("source should be reset after history error, holds %q", got)
	}
}

// TestStart_NilLoop covers the defensive nil-receiver guard in Start.
func TestStart_NilLoop(t *testing.T) {
	var l *Loop
	if err := l.Start(context.Background()); err == nil {
		t.Error("Start on nil loop should return an error")
	}
}

// phaseProvider is a providers.Provider that returns a different script
// on each successive Stream call, letting tests model a multi-iteration
// agent.Run turn (tool_use on call 1, end_turn on call 2). After the
// last phase it repeats the final phase so an over-eager loop still
// terminates instead of blocking.
type phaseProvider struct {
	phases []fake.Script
	call   int
}

func (p *phaseProvider) Name() string                         { return "phase" }
func (p *phaseProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }

func (p *phaseProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	idx := p.call
	if idx >= len(p.phases) {
		idx = len(p.phases) - 1
	}
	script := p.phases[idx]
	p.call++
	ch := make(chan providers.Event, len(script))
	go func() {
		defer close(ch)
		for _, ev := range script {
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
	}()
	return ch, nil
}

var _ providers.Provider = (*phaseProvider)(nil)
