package chat

import (
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestBackfillCmd_ReadsSeededEvents drives the backfill command end to
// end: a seeded created + user_message must come back inside a
// backfillMsg with the right event count.
func TestBackfillCmd_ReadsSeededEvents(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000BF01"
	seedAgent(t, log, agentID, "bf", "claude-4.7-sonnet")
	seedUserMessage(t, log, agentID, "hi there")

	msg := backfillCmd(log, agentID)()
	bf, ok := msg.(backfillMsg)
	if !ok {
		t.Fatalf("backfillCmd should produce backfillMsg; got %T", msg)
	}
	if len(bf.events) != 2 {
		t.Errorf("want 2 backfilled events; got %d", len(bf.events))
	}
}

// TestBackfillCmd_ErrorSurfacesAsErrMsg closes the log so Read fails;
// the command must degrade to an errMsg rather than panicking.
func TestBackfillCmd_ErrorSurfacesAsErrMsg(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000BF02"
	seedAgent(t, log, agentID, "bferr", "claude-4.7-sonnet")
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	msg := backfillCmd(log, agentID)()
	if _, ok := msg.(errMsg); !ok {
		t.Fatalf("closed-log backfill should produce errMsg; got %T", msg)
	}
}

// TestSubscribeCmd_OpensChannel verifies the subscribe command returns
// a subscriptionReady carrying a live channel + cancel func.
func TestSubscribeCmd_OpensChannel(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000SU01"
	seedAgent(t, log, agentID, "sub", "claude-4.7-sonnet")

	msg := subscribeCmd(log, agentID)()
	ready, ok := msg.(subscriptionReady)
	if !ok {
		t.Fatalf("subscribeCmd should produce subscriptionReady; got %T", msg)
	}
	if ready.ch == nil || ready.cancel == nil {
		t.Fatal("subscriptionReady missing channel or cancel")
	}
	ready.cancel()
}

// TestPumpEventCmd_DeliversThenStops feeds one event through the pump:
// the first read produces an eventMsg, and after close the pump emits
// nil so the loop stops.
func TestPumpEventCmd_DeliversThenStops(t *testing.T) {
	ch := make(chan agent.Event, 1)
	ch <- agent.Event{AgentID: "a", Type: agent.EvtUserMessage, TS: time.Now()}
	msg := pumpEventCmd(ch)()
	em, ok := msg.(eventMsg)
	if !ok {
		t.Fatalf("pump should deliver eventMsg; got %T", msg)
	}
	if em.ev.AgentID != "a" {
		t.Errorf("wrong event delivered: %+v", em.ev)
	}
	close(ch)
	if got := pumpEventCmd(ch)(); got != nil {
		t.Errorf("closed channel should stop the pump (nil); got %T", got)
	}
}

// TestApprovalPumpCmd_DeliversThenStops mirrors the event pump for the
// approval request channel.
func TestApprovalPumpCmd_DeliversThenStops(t *testing.T) {
	ch := make(chan *ApprovalRequest, 1)
	req := &ApprovalRequest{Tool: "write"}
	ch <- req
	msg := approvalPumpCmd(ch)()
	got, ok := msg.(approvalRequestMsg)
	if !ok {
		t.Fatalf("approval pump should deliver approvalRequestMsg; got %T", msg)
	}
	if got.req == nil || got.req.Tool != "write" {
		t.Errorf("wrong approval request: %+v", got.req)
	}
	close(ch)
	if got := approvalPumpCmd(ch)(); got != nil {
		t.Errorf("closed channel should stop the approval pump; got %T", got)
	}
}

// TestScheduleTicks_ReturnNonNil pins the tick constructors return a
// runnable command (timer-based; we don't wait on the fire).
func TestScheduleTicks_ReturnNonNil(t *testing.T) {
	if scheduleTextTick() == nil {
		t.Error("scheduleTextTick should return a command")
	}
	if scheduleChildrenTick() == nil {
		t.Error("scheduleChildrenTick should return a command")
	}
}
