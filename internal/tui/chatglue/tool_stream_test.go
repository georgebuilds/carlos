package chatglue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
)

// TestPersistToolCall_AppendsEvent confirms the OnToolCall hook
// lands an EvtToolCall in the event log immediately. This is the
// load-bearing change behind the "tool cards appear as they happen"
// UX: without it the chat sees every tool call in one post-turn
// batch and the user sits staring at a frozen screen.
func TestPersistToolCall_AppendsEvent(t *testing.T) {
	log := openTestLog(t)
	const id = "01HZ0000000000000000000001"
	seedAgent(t, log, id)
	l := &Loop{log: log, agentID: id, ctx: context.Background()}

	use := providers.Block{
		Kind:      "tool_use",
		ToolName:  "bash",
		ToolInput: []byte(`{"cmd":"ls"}`),
	}
	l.persistToolCall(use)

	evs, err := log.Read(context.Background(), id, 0)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var found bool
	for _, ev := range evs {
		if ev.Type != agent.EvtToolCall {
			continue
		}
		var tc agent.ToolCall
		_ = json.Unmarshal(ev.Payload, &tc)
		if tc.Name == "bash" && strings.Contains(string(tc.Input), `"cmd":"ls"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected EvtToolCall for bash, got none")
	}
}

// TestPersistToolResult_AppendsEvent verifies the result side of the
// streaming pair. The chat side already knows how to fold this back
// into the call card on receipt.
func TestPersistToolResult_AppendsEvent(t *testing.T) {
	log := openTestLog(t)
	const id = "01HZ0000000000000000000002"
	seedAgent(t, log, id)
	l := &Loop{log: log, agentID: id, ctx: context.Background()}

	use := providers.Block{Kind: "tool_use", ToolName: "bash"}
	result := providers.Block{Kind: "tool_result", ToolResult: []byte("total 12\ndrwxr-xr-x  3 user")}
	l.persistToolResult(use, result)

	evs, err := log.Read(context.Background(), id, 0)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var found bool
	for _, ev := range evs {
		if ev.Type != agent.EvtToolResult {
			continue
		}
		var tr agent.ToolResult
		_ = json.Unmarshal(ev.Payload, &tr)
		if tr.Name == "bash" && strings.Contains(string(tr.Output), "drwxr-xr-x") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected EvtToolResult for bash, got none")
	}
}

// TestPersistToolResult_TruncatesPreviewCap proves giant tool outputs
// land in the log clipped to ToolResultPreviewCap so a `ls -R /` reply
// can't blow up the event log row size.
func TestPersistToolResult_TruncatesPreviewCap(t *testing.T) {
	log := openTestLog(t)
	const id = "01HZ0000000000000000000003"
	seedAgent(t, log, id)
	l := &Loop{log: log, agentID: id, ctx: context.Background()}

	big := make([]byte, ToolResultPreviewCap+500)
	for i := range big {
		big[i] = 'x'
	}
	use := providers.Block{ToolName: "huge"}
	l.persistToolResult(use, providers.Block{ToolResult: big})

	evs, _ := log.Read(context.Background(), id, 0)
	for _, ev := range evs {
		if ev.Type != agent.EvtToolResult {
			continue
		}
		var tr agent.ToolResult
		_ = json.Unmarshal(ev.Payload, &tr)
		if len(tr.Output) != ToolResultPreviewCap {
			t.Errorf("output length = %d, want %d (clipped)", len(tr.Output), ToolResultPreviewCap)
		}
	}
}

// TestPersistToolResult_FlagsErrorOnRejectedPrefix pins the IsError
// detection so the chat surface paints the error border on rejected
// or errored tools.
func TestPersistToolResult_FlagsErrorOnRejectedPrefix(t *testing.T) {
	log := openTestLog(t)
	const id = "01HZ0000000000000000000004"
	seedAgent(t, log, id)
	l := &Loop{log: log, agentID: id, ctx: context.Background()}

	use := providers.Block{ToolName: "bash"}
	l.persistToolResult(use, providers.Block{ToolResult: []byte("(rejected by user)")})

	evs, _ := log.Read(context.Background(), id, 0)
	for _, ev := range evs {
		if ev.Type != agent.EvtToolResult {
			continue
		}
		var tr agent.ToolResult
		_ = json.Unmarshal(ev.Payload, &tr)
		if !tr.IsError {
			t.Errorf("expected IsError=true for rejected tool")
		}
	}
}
