package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/tui/chatglue"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestWireState_UnderscoreForm(t *testing.T) {
	cases := map[agent.State]string{
		agent.StateRunning:       "running",
		agent.StateAwaitingInput: "awaiting_input",
		agent.StateBlocked:       "blocked",
		agent.StatePausedByUser:  "paused",
		agent.StateDone:          "done",
		agent.StateFailed:        "failed",
		agent.StateOrphaned:      "orphaned",
		agent.StateCompacting:    "compacting",
		agent.StateCancelling:    "cancelling",
		agent.StateQueued:        "queued",
		agent.StateSpawning:      "spawning",
	}
	for st, want := range cases {
		if got := wireState(st); got != want {
			t.Errorf("wireState(%v) = %q, want %q", st, got, want)
		}
		// Underscore discipline: the wire form never carries a dash, even
		// though State.String() does (D-C).
		if strings.Contains(want, "-") {
			t.Errorf("wire string %q must not contain a dash", want)
		}
	}
}

func TestEventToWire_UserMessage(t *testing.T) {
	ev := agent.Event{Seq: 7, AgentID: "t1", TS: time.Now().UTC(), Type: agent.EvtUserMessage,
		Payload: mustJSON(t, agent.MessagePayload{Text: "hello carlos"})}
	we, ok := eventToWire(ev)
	if !ok {
		t.Fatal("expected user_message to convert")
	}
	if we.Kind != "user_message" || we.Seq != 7 || we.Thread != "t1" {
		t.Errorf("unexpected wire event: %+v", we)
	}
	if got := we.Data.(map[string]any)["text"]; got != "hello carlos" {
		t.Errorf("text = %v", got)
	}
}

func TestEventToWire_AssistantErrorPrefixStripped(t *testing.T) {
	ev := agent.Event{Seq: 8, AgentID: "t1", TS: time.Now().UTC(), Type: agent.EvtAssistantMessage,
		Payload: mustJSON(t, agent.MessagePayload{Text: chatglue.ErrorEventPrefix + "boom"})}
	we, _ := eventToWire(ev)
	d := we.Data.(map[string]any)
	if d["error"] != true {
		t.Error("error flag should be true")
	}
	if d["text"] != "boom" {
		t.Errorf("prefix not stripped: text = %v", d["text"])
	}
}

func TestEventToWire_ToolResultTruncationDerived(t *testing.T) {
	big := strings.Repeat("x", chatglue.ToolResultPreviewCap)
	ev := agent.Event{Seq: 9, AgentID: "t1", TS: time.Now().UTC(), Type: agent.EvtToolResult,
		Payload: mustJSON(t, agent.ToolResult{Name: "Bash", Output: []byte(big), IsError: false})}
	we, _ := eventToWire(ev)
	d := we.Data.(map[string]any)
	if d["truncated"] != true {
		t.Error("output at the cap should be marked truncated (D-D)")
	}

	small := agent.Event{Seq: 10, AgentID: "t1", TS: time.Now().UTC(), Type: agent.EvtToolResult,
		Payload: mustJSON(t, agent.ToolResult{Name: "Read", Output: []byte("short")})}
	we2, _ := eventToWire(small)
	if we2.Data.(map[string]any)["truncated"] != false {
		t.Error("short output should not be truncated")
	}
}

func TestEventToWire_ToolCallInputIsRawJSON(t *testing.T) {
	input := []byte(`{"command":"ls -la"}`)
	ev := agent.Event{Seq: 11, AgentID: "t1", TS: time.Now().UTC(), Type: agent.EvtToolCall,
		Payload: mustJSON(t, agent.ToolCall{Name: "Bash", Input: input})}
	we, _ := eventToWire(ev)
	// The input should round-trip through the full WireEvent marshal as a
	// nested object, not a re-stringified blob.
	out := mustJSON(t, we)
	if !strings.Contains(string(out), `"input":{"command":"ls -la"}`) {
		t.Errorf("tool input not passed through as raw JSON: %s", out)
	}
}

func TestEventToWire_StateChangeMapsToWireState(t *testing.T) {
	to := agent.StateAwaitingInput
	payload := mustJSON(t, agent.StateChangePayload{Kind: agent.StateChangeTransition, To: &to})
	ev := agent.Event{Seq: 12, AgentID: "t1", TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: payload}
	we, ok := eventToWire(ev)
	if !ok {
		t.Fatal("state change should convert")
	}
	if we.Kind != "state" || we.Data.(map[string]any)["state"] != "awaiting_input" {
		t.Errorf("unexpected state wire: %+v", we)
	}
}

func TestEventToWire_UnforwardedKindsSkipped(t *testing.T) {
	for _, typ := range []agent.EventType{
		agent.EvtProviderCall, agent.EvtTokenUsage, agent.EvtHeartbeat,
		agent.EvtApprovalProposed, agent.EvtSteering,
	} {
		ev := agent.Event{Seq: 1, AgentID: "t1", TS: time.Now().UTC(), Type: typ, Payload: []byte(`{}`)}
		if _, ok := eventToWire(ev); ok {
			t.Errorf("event type %q should NOT be forwarded to the browser", typ)
		}
	}
}
