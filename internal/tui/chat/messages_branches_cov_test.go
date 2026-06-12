package chat

import (
	"errors"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// subscribeErrLog embeds an EventLog and overrides Subscribe to fail so
// the subscribeCmd error branch is exercised.
type subscribeErrLog struct{ agent.EventLog }

func (subscribeErrLog) Subscribe(string) (<-chan agent.Event, func(), error) {
	return nil, nil, errors.New("subscribe boom")
}

// TestSubscribeCmd_ErrorSurfacesAsErrMsg covers the Subscribe-failed
// branch: the command degrades to an errMsg instead of panicking.
func TestSubscribeCmd_ErrorSurfacesAsErrMsg(t *testing.T) {
	msg := subscribeCmd(subscribeErrLog{}, "agent-x")()
	em, ok := msg.(errMsg)
	if !ok {
		t.Fatalf("failed Subscribe should produce errMsg; got %T", msg)
	}
	if em.err == nil {
		t.Error("errMsg should carry the underlying error")
	}
}

// TestUpdate_SubscriptionReadyArmsPump walks the subscriptionReady
// branch: it stashes the channel + cancel and returns the pump cmd.
func TestUpdate_SubscriptionReadyArmsPump(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000SU02"
	seedAgent(t, log, agentID, "subready", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	ch := make(chan agent.Event)
	cancelled := false
	updated, cmd := m.Update(subscriptionReady{
		ch:     ch,
		cancel: func() { cancelled = true },
	})
	m = updated.(*Model)
	if m.subCh == nil {
		t.Error("subscriptionReady should stash the live channel")
	}
	if m.subCancel == nil {
		t.Error("subscriptionReady should stash the cancel func")
	}
	if cmd == nil {
		t.Error("subscriptionReady should arm the event pump")
	}
	m.subCancel()
	if !cancelled {
		t.Error("stashed cancel should be the one we passed in")
	}
}

// TestUpdate_ApprovalRequestParksAndPumps walks the approvalRequestMsg
// branch: the request is parked on pendingApproval and a pump cmd comes
// back.
func TestUpdate_ApprovalRequestParksAndPumps(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000SU03"
	seedAgent(t, log, agentID, "apprq", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	m.approver = NewTUIApprover()
	defer m.approver.Close()

	req := &ApprovalRequest{Tool: "bash"}
	updated, cmd := m.Update(approvalRequestMsg{req: req})
	m = updated.(*Model)
	if m.pendingApproval != req {
		t.Error("approvalRequestMsg should park the request on pendingApproval")
	}
	if cmd == nil {
		t.Error("approvalRequestMsg should re-arm the approval pump")
	}
}
