package chat

import (
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestMaybeInterrupt covers the esc-to-interrupt gate: esc fires the
// interrupter only when a turn is in flight, one is wired, AND the
// interrupter reports it actually cancelled a turn; every other case falls
// through so esc keeps its existing meanings.
func TestMaybeInterrupt(t *testing.T) {
	src := NewMemTextSource()
	const id = "a1"
	called := 0
	armed := true // the wired interrupter reports a turn was cancelled
	m := &Model{agentID: id, source: src, proj: agent.NewProjection(),
		interrupt: func() bool { called++; return armed }}

	// Idle (empty source, empty transcript => not busy): esc is a no-op and
	// never even calls the interrupter.
	if m.maybeInterrupt("esc") {
		t.Error("esc must not interrupt when idle")
	}
	if called != 0 {
		t.Errorf("interrupter consulted while idle: %d", called)
	}

	// Busy and the turn is armed: esc interrupts, shows status, sets the flag.
	src.Append(id, "partial reply")
	if !m.maybeInterrupt("esc") {
		t.Error("esc must interrupt when busy and armed")
	}
	if called != 1 {
		t.Errorf("interrupter called %d times, want 1", called)
	}
	if m.status == "" || !m.interrupting {
		t.Errorf("interrupt should set status + interrupting flag: status=%q interrupting=%v", m.status, m.interrupting)
	}

	// Busy but NOT armed (interrupter returns false, e.g. loop still building
	// history): esc must fall through without claiming the key or lying.
	m.status, m.interrupting, armed = "", false, false
	if m.maybeInterrupt("esc") {
		t.Error("esc must fall through when the turn is not interruptible yet")
	}
	if m.status != "" || m.interrupting {
		t.Errorf("must not set a false interrupting status: status=%q interrupting=%v", m.status, m.interrupting)
	}

	// Non-esc keys never interrupt, even when busy.
	armed = true
	called = 0
	if m.maybeInterrupt("ctrl+x") {
		t.Error("only esc interrupts")
	}
	if called != 0 {
		t.Errorf("a non-esc key consulted the interrupter: %d", called)
	}

	// No interrupter wired (read-only daemon/web paths): esc is a no-op.
	m2 := &Model{agentID: id, source: src, proj: agent.NewProjection()}
	if m2.maybeInterrupt("esc") {
		t.Error("a nil interrupter must not consume esc")
	}
}

// TestClearInterruptStatusIfDone: the transient status clears once the
// interrupted turn seals (assistant idle), but not while still busy, and it
// never stomps a status another path owns.
func TestClearInterruptStatusIfDone(t *testing.T) {
	src := NewMemTextSource()
	const id = "a1"
	m := &Model{agentID: id, source: src, proj: agent.NewProjection()}

	// Still busy (streaming): must not clear.
	src.Append(id, "partial")
	m.interrupting, m.status = true, "interrupting current turn"
	m.clearInterruptStatusIfDone()
	if !m.interrupting || m.status == "" {
		t.Error("must not clear while the assistant is still busy")
	}

	// Turn sealed (idle): clears both.
	src.Reset(id)
	m.clearInterruptStatusIfDone()
	if m.interrupting || m.status != "" {
		t.Errorf("idle should clear interrupt status: status=%q interrupting=%v", m.status, m.interrupting)
	}

	// Idle but a different status is showing: clear the flag, keep the status.
	m.interrupting, m.status = true, "saved config"
	m.clearInterruptStatusIfDone()
	if m.interrupting || m.status != "saved config" {
		t.Errorf("must not stomp another path's status: status=%q interrupting=%v", m.status, m.interrupting)
	}
}

func TestWithInterrupter(t *testing.T) {
	called := false
	m := &Model{}
	WithInterrupter(func() bool { called = true; return true })(m)
	if m.interrupt == nil {
		t.Fatal("WithInterrupter did not set the interrupter")
	}
	if !m.interrupt() || !called {
		t.Error("the wired interrupter was not invoked / did not return its value")
	}
}
