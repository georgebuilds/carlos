package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// sequenceProvider is a test-local provider that emits a different
// scripted stream on each Stream() call. Lets us simulate the multi-turn
// loop: turn 1 emits a tool_use; turn 2 emits text + end_turn.
type sequenceProvider struct {
	mu     sync.Mutex
	scripts [][]providers.Event
	calls  int
	lastReq providers.Request
}

func (p *sequenceProvider) Name() string                       { return "seq" }
func (p *sequenceProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }

func (p *sequenceProvider) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	p.mu.Lock()
	if p.calls >= len(p.scripts) {
		p.mu.Unlock()
		return nil, errors.New("seq: no more scripts")
	}
	script := p.scripts[p.calls]
	p.calls++
	p.lastReq = req
	p.mu.Unlock()

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

// echoTool is a test tool that returns its input as the result, so tests
// can assert the loop wired the input through correctly.
type echoTool struct{}

func (echoTool) Name() string         { return "echo" }
func (echoTool) Description() string  { return "echoes input" }
func (echoTool) Schema() []byte       { return []byte(`{"type":"object"}`) }
func (echoTool) Execute(_ context.Context, in []byte) ([]byte, error) {
	return []byte("echoed: " + string(in)), nil
}

func TestLoop_TextOnlyOneTurn(t *testing.T) {
	p := &sequenceProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventTextDelta, Text: "Hello, "},
			{Kind: providers.EventTextDelta, Text: "Boss."},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	var sink bytes.Buffer
	out, err := agent.Run(context.Background(), p, tools.NewRegistry(),
		agent.LoopOptions{Model: "x", TextSink: &sink},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}}},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sink.String() != "Hello, Boss." {
		t.Errorf("sink = %q", sink.String())
	}
	if len(out) != 2 {
		t.Fatalf("messages: want 2 got %d", len(out))
	}
	if out[1].Role != "assistant" || out[1].Content[0].Text != "Hello, Boss." {
		t.Errorf("assistant turn malformed: %+v", out[1])
	}
}

// TestLoop_TextSinkScrubsControlChars pins the v0.7.6 fix: the live
// text sink must drop terminal control bytes (ESC, BEL, OSC lead, C1
// range, DEL) so a streamed chunk containing raw ANSI cannot reach the
// terminal and provoke an OSC 11 / OSC 4 response that bubbletea then
// reads back into the chat composer as garbage. The persisted
// assistant message keeps the raw text — only the sink is scrubbed.
func TestLoop_TextSinkScrubsControlChars(t *testing.T) {
	// Hostile chunk: OSC 11 query (ESC ] 11 ; ? BEL), CSI fg red,
	// then plain text and a final CSI reset. The model never emits
	// these legitimately; in the wild they come from a corrupt SSE
	// envelope leaking bytes through the EventTextDelta channel.
	hostile := "\x1b]11;?\x07\x1b[31mhello\x1b[0m"
	p := &sequenceProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventTextDelta, Text: hostile},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	var sink bytes.Buffer
	out, err := agent.Run(context.Background(), p, tools.NewRegistry(),
		agent.LoopOptions{Model: "x", TextSink: &sink},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}}},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := sink.String()
	// Sink received printable text only.
	if got != "]11;?[31mhello[0m" {
		t.Errorf("sink = %q, want %q (control bytes stripped, payload preserved)", got, "]11;?[31mhello[0m")
	}
	// Specific guards: no ESC, no BEL, no DEL byte reached the sink.
	for _, c := range []byte{0x1b, 0x07, 0x7f} {
		if strings.ContainsRune(got, rune(c)) {
			t.Errorf("sink leaked control byte 0x%02x: %q", c, got)
		}
	}
	// Persisted message retains the raw text so the model's own
	// context window stays faithful across re-renders.
	if len(out) != 2 {
		t.Fatalf("messages: want 2 got %d", len(out))
	}
	if out[1].Content[0].Text != hostile {
		t.Errorf("persisted text was modified: %q, want raw %q", out[1].Content[0].Text, hostile)
	}
}

// TestLoop_TextSinkPreservesWhitespace guards against over-scrubbing:
// the scrub must keep \n, \r, \t since markdown rendering relies on
// them. Streamed code blocks, prose paragraphs, and table layouts
// would all break if printable whitespace got dropped.
func TestLoop_TextSinkPreservesWhitespace(t *testing.T) {
	p := &sequenceProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventTextDelta, Text: "line1\nline2\r\n\tindented"},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	var sink bytes.Buffer
	if _, err := agent.Run(context.Background(), p, tools.NewRegistry(),
		agent.LoopOptions{Model: "x", TextSink: &sink},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi"}}}},
	); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := sink.String(), "line1\nline2\r\n\tindented"; got != want {
		t.Errorf("sink = %q, want %q (whitespace preserved)", got, want)
	}
}

func TestLoop_ToolUseRoundTrip(t *testing.T) {
	p := &sequenceProvider{scripts: [][]providers.Event{
		// Turn 1: text + tool_use, stop=tool_use
		{
			{Kind: providers.EventTextDelta, Text: "Let me check. "},
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu-1", Name: "echo"}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu-1", Name: "echo", Input: []byte(`{"x":1}`)}},
			{Kind: providers.EventStopReason, Stop: "tool_use"},
		},
		// Turn 2: response after tool result
		{
			{Kind: providers.EventTextDelta, Text: "Done."},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	out, err := agent.Run(context.Background(), p, reg,
		agent.LoopOptions{Model: "x", Approver: agent.AutoApprover{}},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Expected: user (initial), assistant (text + tool_use), user (tool_result), assistant (text)
	if len(out) != 4 {
		for i, m := range out {
			t.Logf("msg %d: role=%s blocks=%d", i, m.Role, len(m.Content))
			for j, b := range m.Content {
				t.Logf("  block %d: kind=%s text=%q toolUseID=%s toolName=%s", j, b.Kind, b.Text, b.ToolUseID, b.ToolName)
			}
		}
		t.Fatalf("messages: want 4 got %d", len(out))
	}
	// Validate the tool_result was injected.
	if out[2].Role != "user" || len(out[2].Content) != 1 || out[2].Content[0].Kind != "tool_result" {
		t.Errorf("tool_result message malformed: %+v", out[2])
	}
	if !bytes.Contains(out[2].Content[0].ToolResult, []byte(`echoed: {"x":1}`)) {
		t.Errorf("tool_result body unexpected: %q", out[2].Content[0].ToolResult)
	}
}

func TestLoop_ApproverDenialSurfacesAsToolResult(t *testing.T) {
	p := &sequenceProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu-1", Name: "echo"}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu-1", Name: "echo", Input: []byte(`{}`)}},
			{Kind: providers.EventStopReason, Stop: "tool_use"},
		},
		{
			{Kind: providers.EventTextDelta, Text: "Okay, I won't."},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	out, err := agent.Run(context.Background(), p, reg,
		agent.LoopOptions{Model: "x", Approver: denyAll{}},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out[2].Content[0].ToolResult) != "(rejected by user)" {
		t.Errorf("expected denial body, got %q", out[2].Content[0].ToolResult)
	}
}

type denyAll struct{}

func (denyAll) ApproveToolCall(string, []byte) bool { return false }

func TestLoop_UnknownToolReportsErrorBackToModel(t *testing.T) {
	p := &sequenceProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu-1", Name: "ghost"}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu-1", Name: "ghost", Input: []byte(`{}`)}},
			{Kind: providers.EventStopReason, Stop: "tool_use"},
		},
		{
			{Kind: providers.EventTextDelta, Text: "Got it."},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	out, err := agent.Run(context.Background(), p, tools.NewRegistry(),
		agent.LoopOptions{Model: "x"},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(out[2].Content[0].ToolResult), "tool error") {
		t.Errorf("expected tool error in result, got %q", out[2].Content[0].ToolResult)
	}
}

func TestLoop_MaxIterationsGuard(t *testing.T) {
	// Provider always returns tool_use; loop must stop at MaxIterations.
	scripts := make([][]providers.Event, 100)
	for i := range scripts {
		scripts[i] = []providers.Event{
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu", Name: "echo"}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu", Name: "echo", Input: []byte(`{}`)}},
			{Kind: providers.EventStopReason, Stop: "tool_use"},
		}
	}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	p := &sequenceProvider{scripts: scripts}
	_, err := agent.Run(context.Background(), p, reg,
		agent.LoopOptions{Model: "x", MaxIterations: 3, Approver: agent.AutoApprover{}},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}},
	)
	if !errors.Is(err, agent.ErrMaxIterations) {
		t.Errorf("want ErrMaxIterations, got %v", err)
	}
	if p.calls != 3 {
		t.Errorf("calls: want 3 got %d", p.calls)
	}
}

func TestLoop_ParallelToolUseAllExecuted(t *testing.T) {
	p := &sequenceProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu-1", Name: "echo"}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu-1", Name: "echo", Input: []byte(`{"i":1}`)}},
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu-2", Name: "echo"}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu-2", Name: "echo", Input: []byte(`{"i":2}`)}},
			{Kind: providers.EventStopReason, Stop: "tool_use"},
		},
		{
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	out, err := agent.Run(context.Background(), p, reg,
		agent.LoopOptions{Model: "x"},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Tool-results message should have BOTH results, in order.
	tr := out[2].Content
	if len(tr) != 2 {
		t.Fatalf("tool_results count: want 2 got %d", len(tr))
	}
	if tr[0].ToolUseID != "tu-1" || tr[1].ToolUseID != "tu-2" {
		t.Errorf("tool_result order/IDs: %s, %s", tr[0].ToolUseID, tr[1].ToolUseID)
	}
}

func TestLoop_RequestCarriesToolsAndModel(t *testing.T) {
	p := &sequenceProvider{scripts: [][]providers.Event{
		{{Kind: providers.EventStopReason, Stop: "end_turn"}},
	}}
	bash := []providers.ToolSpec{{Name: "bash", Description: "run bash", Schema: []byte(`{}`)}}
	_, err := agent.Run(context.Background(), p, tools.NewRegistry(),
		agent.LoopOptions{Model: "claude-x", Tools: bash, System: "you are carlos"},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if p.lastReq.Model != "claude-x" {
		t.Errorf("model: %q", p.lastReq.Model)
	}
	if p.lastReq.System != "you are carlos" {
		t.Errorf("system: %q", p.lastReq.System)
	}
	if len(p.lastReq.Tools) != 1 || p.lastReq.Tools[0].Name != "bash" {
		t.Errorf("tools: %+v", p.lastReq.Tools)
	}
}

// Smoke: collectAssistant via Run preserves block ordering when text
// interleaves with tool_use.
func TestLoop_TextBeforeAndAfterToolUse(t *testing.T) {
	p := &sequenceProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventTextDelta, Text: "first "},
			{Kind: providers.EventTextDelta, Text: "block."},
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu", Name: "echo"}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu", Name: "echo", Input: []byte(`{}`)}},
			{Kind: providers.EventStopReason, Stop: "tool_use"},
		},
		{
			{Kind: providers.EventTextDelta, Text: "second block."},
			{Kind: providers.EventStopReason, Stop: "end_turn"},
		},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	out, err := agent.Run(context.Background(), p, reg,
		agent.LoopOptions{Model: "x"},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Assistant turn 1: text + tool_use (in that order).
	a1 := out[1].Content
	if len(a1) != 2 || a1[0].Kind != "text" || a1[1].Kind != "tool_use" {
		t.Errorf("turn1 ordering wrong: %+v", a1)
	}
	if a1[0].Text != "first block." {
		t.Errorf("turn1 text accumulation: %q", a1[0].Text)
	}
	// Assistant turn 2: text only.
	a2 := out[3].Content
	if len(a2) != 1 || a2[0].Kind != "text" || a2[0].Text != "second block." {
		t.Errorf("turn2: %+v", a2)
	}
}

// Validate that the JSON input passed to the tool is the exact bytes
// surfaced by the provider's tool_use_end event.
func TestLoop_PassesExactToolInputBytes(t *testing.T) {
	input := []byte(`{"deeply":{"nested":[1,2,3]},"key with spaces":"yes"}`)
	p := &sequenceProvider{scripts: [][]providers.Event{
		{
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu", Name: "echo"}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu", Name: "echo", Input: input}},
			{Kind: providers.EventStopReason, Stop: "tool_use"},
		},
		{{Kind: providers.EventStopReason, Stop: "end_turn"}},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	out, err := agent.Run(context.Background(), p, reg,
		agent.LoopOptions{Model: "x"},
		[]providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "go"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	body := out[2].Content[0].ToolResult
	// echo prepends "echoed: " — strip + compare.
	want := append([]byte("echoed: "), input...)
	if !bytes.Equal(body, want) {
		t.Errorf("input bytes mangled.\n want %s\n  got %s", want, body)
	}
	// Sanity: input round-trips through json.Unmarshal too.
	var any map[string]any
	if err := json.Unmarshal(input, &any); err != nil {
		t.Errorf("input not valid JSON to begin with: %v", err)
	}
}
