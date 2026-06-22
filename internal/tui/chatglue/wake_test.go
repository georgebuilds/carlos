package chatglue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// appendBackgroundComplete appends an EvtBackgroundComplete wake event for
// jobID to the agent's stream, mirroring what the runtime's job-completion
// watcher does.
func appendBackgroundComplete(t *testing.T, log *agent.SQLiteEventLog, id, jobID string) {
	t.Helper()
	payload, _ := json.Marshal(agent.BackgroundCompletePayload{JobID: jobID})
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtBackgroundComplete, Payload: payload,
	}); err != nil {
		t.Fatalf("append background-complete: %v", err)
	}
}

// TestWake_BackgroundCompletionRunsTurn drives the wake path end-to-end: an
// EvtBackgroundComplete event on the agent stream must make the loop run a
// turn (with no user message) and persist the assistant's reply, so the
// model reacts to a finished background job on its own.
func TestWake_BackgroundCompletionRunsTurn(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-wake"
	seedAgent(t, log, id)
	src := newMemSource()

	l := NewLoop(Config{Provider: scriptedProvider("the build finished cleanly")}, log, src, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	time.Sleep(50 * time.Millisecond)
	// No user message at all - the wake event alone must trigger a turn.
	appendBackgroundComplete(t, log, id, "job-xyz")

	full := waitForAssistant(t, log, id, "build finished cleanly")
	if !strings.Contains(full, "build finished cleanly") {
		t.Errorf("wake turn did not produce the assistant reply: %q", full)
	}
}

// TestWake_NudgeNamesTheJob confirms handleBackgroundCompletion seeds the
// turn with a nudge that names the finished job, so the model knows which
// job it is reacting to. We assert on the history the loop assembles by
// capturing it through the provider's recorded request.
func TestWake_NudgeNamesTheJob(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-wake-nudge"
	seedAgent(t, log, id)
	src := newMemSource()

	prov := scriptedProvider("ok")
	l := NewLoop(Config{Provider: prov}, log, src, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	time.Sleep(50 * time.Millisecond)
	appendBackgroundComplete(t, log, id, "job-42")
	waitForAssistant(t, log, id, "ok")

	// The fake provider records the last request it saw; the nudge is the
	// final user message and must reference the job id.
	last := prov.LastRequest()
	if len(last.Messages) == 0 {
		t.Fatal("provider saw no request")
	}
	var found bool
	for _, m := range last.Messages {
		for _, b := range m.Content {
			if m.Role == "user" && strings.Contains(b.Text, "job-42") && strings.Contains(b.Text, "[background]") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("wake nudge naming job-42 not found in request messages: %+v", last.Messages)
	}
}
