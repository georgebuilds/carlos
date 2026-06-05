package chat

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// approveInGoroutine runs ApproveToolCall asynchronously so the test
// can drive the chat Model's reply channel without deadlocking on the
// synchronous Approver call.
func approveInGoroutine(t *testing.T, a *TUIApprover, name string, input []byte) (got chan bool) {
	t.Helper()
	got = make(chan bool, 1)
	go func() { got <- a.ApproveToolCall(name, input) }()
	return got
}

// receiveRequest blocks until the approver enqueues one request, with
// a tight deadline so a regression hangs the test rather than the
// whole suite.
func receiveRequest(t *testing.T, a *TUIApprover) *ApprovalRequest {
	t.Helper()
	select {
	case req := <-a.Requests():
		return req
	case <-time.After(time.Second):
		t.Fatal("no approval request received within 1s")
		return nil
	}
}

func TestTUIApprover_AllowReturnsTrue(t *testing.T) {
	a := NewTUIApprover()
	got := approveInGoroutine(t, a, "bash", []byte(`{"cmd":"ls"}`))
	req := receiveRequest(t, a)
	a.Reply(req, ApprovalAllow)
	if !<-got {
		t.Error("ApproveToolCall returned false after Allow")
	}
}

func TestTUIApprover_DenyReturnsFalse(t *testing.T) {
	a := NewTUIApprover()
	got := approveInGoroutine(t, a, "bash", []byte(`{"cmd":"ls"}`))
	req := receiveRequest(t, a)
	a.Reply(req, ApprovalDeny)
	if <-got {
		t.Error("ApproveToolCall returned true after Deny")
	}
}

func TestTUIApprover_AlwaysCachesPerTool(t *testing.T) {
	a := NewTUIApprover()
	// First call: dispatch via the queue + Reply Always.
	got1 := approveInGoroutine(t, a, "read", []byte(`{"path":"a"}`))
	req := receiveRequest(t, a)
	a.Reply(req, ApprovalAllowAlways)
	if !<-got1 {
		t.Fatal("first call denied")
	}

	// Second call to the SAME tool: should bypass the queue entirely.
	gotCh := make(chan bool, 1)
	go func() { gotCh <- a.ApproveToolCall("read", []byte(`{"path":"b"}`)) }()
	select {
	case <-a.Requests():
		t.Fatal("second `read` call was queued despite prior Always")
	case <-time.After(50 * time.Millisecond):
		// expected — no request enqueued
	}
	if !<-gotCh {
		t.Error("second call denied despite prior Always")
	}

	// Third call to a DIFFERENT tool: still queued.
	gotCh2 := make(chan bool, 1)
	go func() { gotCh2 <- a.ApproveToolCall("bash", []byte(`{"cmd":"x"}`)) }()
	req2 := receiveRequest(t, a) // would time out if Always leaked
	a.Reply(req2, ApprovalDeny)
	if <-gotCh2 {
		t.Error("`bash` approved despite only `read` having Always")
	}
}

func TestTUIApprover_CloseDeniesPendingRequests(t *testing.T) {
	a := NewTUIApprover()
	got := approveInGoroutine(t, a, "bash", []byte(`{"cmd":"rm -rf /"}`))
	// Don't drain Requests — the request is in the channel but no one
	// is listening. Close should drain + deny.
	a.Close()
	select {
	case approved := <-got:
		if approved {
			t.Error("ApproveToolCall returned true after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("ApproveToolCall did not unblock after Close")
	}
}

func TestTUIApprover_DoubleReplyIsSafe(t *testing.T) {
	a := NewTUIApprover()
	got := approveInGoroutine(t, a, "bash", []byte(`{}`))
	req := receiveRequest(t, a)
	a.Reply(req, ApprovalAllow)
	a.Reply(req, ApprovalDeny) // second send must not panic
	if !<-got {
		t.Error("first Reply lost to second")
	}
}

func TestTUIApprover_NilReplyNoPanic(t *testing.T) {
	a := NewTUIApprover()
	a.Reply(nil, ApprovalAllow)
	a.Reply(&ApprovalRequest{}, ApprovalAllow) // no reply chan
}

// === Chat model integration =================================================

func TestChatModel_OverlayShowsAndClearsOnYes(t *testing.T) {
	log := openTempLog(t)
	const id = "agent-overlay-1"
	seedAgent(t, log, id, "approval flow", "fake")
	a := NewTUIApprover()
	m := New(log, id, NewMemTextSource(), WithTUIApprover(a))
	m = drive(t, m, 120, 30)

	// Inject an approval msg directly — same flow approvalPumpCmd takes.
	req := &ApprovalRequest{
		Tool:  "bash",
		Input: []byte(`{"cmd":"ls"}`),
		reply: make(chan ApprovalDecision, 1),
	}
	next, _ := m.Update(approvalRequestMsg{req: req})
	mm := next.(*Model)
	if mm.pendingApproval == nil {
		t.Fatal("pendingApproval not set after approvalRequestMsg")
	}
	if !strings.Contains(mm.View(), "wants to run") {
		t.Error("overlay text missing from View")
	}

	// User presses y.
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	mm = next.(*Model)
	if mm.pendingApproval != nil {
		t.Error("pendingApproval not cleared after y")
	}
	select {
	case d := <-req.reply:
		if d != ApprovalAllow {
			t.Errorf("reply = %d, want ApprovalAllow", d)
		}
	default:
		t.Fatal("no reply delivered for y")
	}
}

func TestChatModel_OverlayDeniesOnEsc(t *testing.T) {
	log := openTempLog(t)
	const id = "agent-overlay-2"
	seedAgent(t, log, id, "approval esc", "fake")
	a := NewTUIApprover()
	m := New(log, id, NewMemTextSource(), WithTUIApprover(a))
	m = drive(t, m, 120, 30)
	req := &ApprovalRequest{Tool: "bash", Input: []byte(`{}`), reply: make(chan ApprovalDecision, 1)}
	next, _ := m.Update(approvalRequestMsg{req: req})
	mm := next.(*Model)
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = next.(*Model)
	if mm.pendingApproval != nil {
		t.Error("pendingApproval not cleared after esc")
	}
	select {
	case d := <-req.reply:
		if d != ApprovalDeny {
			t.Errorf("reply = %d, want ApprovalDeny", d)
		}
	default:
		t.Fatal("no reply on esc")
	}
}

func TestChatModel_OverlaySwallowsArbitraryKeys(t *testing.T) {
	log := openTempLog(t)
	const id = "agent-overlay-3"
	seedAgent(t, log, id, "swallow", "fake")
	a := NewTUIApprover()
	m := New(log, id, NewMemTextSource(), WithTUIApprover(a))
	m = drive(t, m, 120, 30)
	req := &ApprovalRequest{Tool: "bash", Input: []byte(`{}`), reply: make(chan ApprovalDecision, 1)}
	next, _ := m.Update(approvalRequestMsg{req: req})
	mm := next.(*Model)

	// A random letter should NOT type into the textarea while the
	// overlay is active.
	before := mm.ta.Value()
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	mm = next.(*Model)
	if mm.ta.Value() != before {
		t.Errorf("textarea swallowed %q while overlay active: %q", "q", mm.ta.Value())
	}
	if mm.pendingApproval == nil {
		t.Error("overlay cleared by non-y/n/A key")
	}
}

// Race detector smoke: multiple goroutines hitting Always-cached calls.
func TestTUIApprover_ConcurrentCachedCalls(t *testing.T) {
	a := NewTUIApprover()
	got := approveInGoroutine(t, a, "read", []byte(`{}`))
	req := receiveRequest(t, a)
	a.Reply(req, ApprovalAllowAlways)
	<-got

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !a.ApproveToolCall("read", []byte(`{}`)) {
				t.Error("cached Always call denied under concurrency")
			}
		}()
	}
	wg.Wait()
}
