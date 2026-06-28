package chat

import (
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestMaybeInterrupt covers the esc-to-interrupt gate: esc fires the
// interrupter only when a turn is in flight and one is wired; every other
// case falls through so esc keeps its existing meanings.
func TestMaybeInterrupt(t *testing.T) {
	src := NewMemTextSource()
	const id = "a1"
	called := 0
	m := &Model{agentID: id, source: src, proj: agent.NewProjection(), interrupt: func() { called++ }}

	// Idle (empty source, empty transcript => not busy): esc is a no-op.
	if m.maybeInterrupt("esc") {
		t.Error("esc must not interrupt when idle")
	}
	if called != 0 {
		t.Errorf("interrupt fired while idle: %d", called)
	}

	// Busy: streamed text in the source makes assistantBusy true.
	src.Append(id, "partial reply")
	if !m.maybeInterrupt("esc") {
		t.Error("esc must interrupt when busy")
	}
	if called != 1 {
		t.Errorf("interrupt called %d times, want 1", called)
	}
	if m.status == "" {
		t.Error("status should reflect that an interrupt was sent")
	}

	// Non-esc keys never interrupt, even when busy.
	if m.maybeInterrupt("ctrl+x") {
		t.Error("only esc interrupts")
	}
	if called != 1 {
		t.Errorf("a non-esc key fired the interrupter: %d", called)
	}

	// No interrupter wired (read-only daemon/web paths): esc is a no-op even
	// when busy, so it falls through to its normal handling.
	m2 := &Model{agentID: id, source: src, proj: agent.NewProjection()}
	if m2.maybeInterrupt("esc") {
		t.Error("a nil interrupter must not consume esc")
	}
}

func TestWithInterrupter(t *testing.T) {
	called := false
	m := &Model{}
	WithInterrupter(func() { called = true })(m)
	if m.interrupt == nil {
		t.Fatal("WithInterrupter did not set the interrupter")
	}
	m.interrupt()
	if !called {
		t.Error("the wired interrupter was not invoked")
	}
}
