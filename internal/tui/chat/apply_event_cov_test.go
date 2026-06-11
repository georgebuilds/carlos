package chat

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/usershell"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestApplyEvent_UserShellStartAppendsRunningEntry(t *testing.T) {
	m := newTestModel(t)
	ev := agent.Event{
		Type:    agent.EvtUserShellStart,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, usershell.StartPayload{JobID: "job-1", Command: "ls -la", Background: false}),
	}
	m.applyEvent(ev)
	idx := m.findUserShellEntry("job-1")
	if idx == -1 {
		t.Fatal("start event should append a user-shell entry")
	}
	e := m.transcript[idx]
	if !e.shellRunning || e.shellCommand != "ls -la" {
		t.Errorf("start entry fields wrong: %+v", e)
	}
}

func TestApplyEvent_UserShellEndFoldsIntoRunningEntry(t *testing.T) {
	m := newTestModel(t)
	m.applyEvent(agent.Event{
		Type:    agent.EvtUserShellStart,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, usershell.StartPayload{JobID: "job-2", Command: "make"}),
	})
	m.applyEvent(agent.Event{
		Type: agent.EvtUserShellEnd,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, usershell.EndPayload{
			JobID:        "job-2",
			ExitCode:     0,
			Duration:     2 * time.Second,
			OutputInline: "done\n",
		}),
	})
	idx := m.findUserShellEntry("job-2")
	if idx == -1 {
		t.Fatal("entry should still exist after end")
	}
	e := m.transcript[idx]
	if e.shellRunning {
		t.Error("end event should clear shellRunning")
	}
	if e.shellOutput != "done\n" {
		t.Errorf("end output not folded in; got %q", e.shellOutput)
	}
}

func TestApplyEvent_UserShellEndOrphanSynthesizesRow(t *testing.T) {
	m := newTestModel(t)
	// End with no prior start → synthesized orphan row.
	m.applyEvent(agent.Event{
		Type: agent.EvtUserShellEnd,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, usershell.EndPayload{
			JobID:     "orphan-1",
			ExitCode:  3,
			Cancelled: true,
		}),
	})
	idx := m.findUserShellEntry("orphan-1")
	if idx == -1 {
		t.Fatal("orphan end should synthesize a row")
	}
	e := m.transcript[idx]
	if e.shellExitCode != 3 || !e.shellCancelled {
		t.Errorf("orphan row fields wrong: %+v", e)
	}
}

func TestApplyEvent_UserShellStartBadPayloadEmitsSystemNote(t *testing.T) {
	m := newTestModel(t)
	m.applyEvent(agent.Event{
		Type:    agent.EvtUserShellStart,
		TS:      time.Now().UTC(),
		Payload: []byte("{not json"),
	})
	last := m.transcript[len(m.transcript)-1]
	if last.kind != entrySystemNote {
		t.Errorf("bad start payload should emit a system note; got %v", last.kind)
	}
}

func TestApplyEvent_UserShellEndBadPayloadEmitsSystemNote(t *testing.T) {
	m := newTestModel(t)
	m.applyEvent(agent.Event{
		Type:    agent.EvtUserShellEnd,
		TS:      time.Now().UTC(),
		Payload: []byte("{not json"),
	})
	last := m.transcript[len(m.transcript)-1]
	if last.kind != entrySystemNote {
		t.Errorf("bad end payload should emit a system note; got %v", last.kind)
	}
}

func TestApplyEvent_AssistantErrorPrefixRendersErrorCard(t *testing.T) {
	m := newTestModel(t)
	// chatglue tags loop errors so the chat surfaces them as an error
	// card rather than a normal avatar reply.
	raw := mustJSON(t, agent.MessagePayload{Text: chatglueErrorPrefix + "provider timed out"})
	m.applyEvent(agent.Event{
		Type:    agent.EvtAssistantMessage,
		TS:      time.Now().UTC(),
		Payload: raw,
	})
	last := m.transcript[len(m.transcript)-1]
	if last.kind != entryError {
		t.Errorf("error-prefixed assistant message should be an error entry; got %v", last.kind)
	}
}

func TestApplyEvent_ToolCallThenResultFoldIntoOneCard(t *testing.T) {
	m := newTestModel(t)
	m.applyEvent(agent.Event{
		Type:    agent.EvtToolCall,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, agent.ToolCall{Name: "write", Input: []byte(`{"path":"x"}`)}),
	})
	n := len(m.transcript)
	m.applyEvent(agent.Event{
		Type:    agent.EvtToolResult,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, agent.ToolResult{Name: "write", Output: []byte("ok")}),
	})
	// Result folds into the existing tool-call entry; no new row.
	if len(m.transcript) != n {
		t.Errorf("tool result should fold into the call (no new row); was %d now %d", n, len(m.transcript))
	}
	last := m.transcript[len(m.transcript)-1]
	if !last.hasResult || last.toolResult != "ok" {
		t.Errorf("result not folded in: %+v", last)
	}
}
