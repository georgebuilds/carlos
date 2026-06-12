package web

import (
	"testing"
	"time"
)

func TestWebTextSource_AppendAccumulatesAndFansOut(t *testing.T) {
	hub := newEphemeralHub()
	ch, unsub := hub.subscribe("t1")
	defer unsub()
	src := NewWebTextSource(hub)

	src.Append("t1", "Hel")
	src.Append("t1", "lo")

	if got := src.Get("t1"); got != "Hello" {
		t.Errorf("Get = %q, want Hello", got)
	}

	// Two delta events fanned out.
	for i, want := range []string{"Hel", "lo"} {
		select {
		case ev := <-ch:
			if ev.Kind != "delta" || ev.Data.(map[string]any)["text"] != want {
				t.Errorf("delta %d = %+v, want text %q", i, ev, want)
			}
			if ev.Seq != 0 {
				t.Error("delta is ephemeral and must not carry a seq")
			}
		case <-time.After(time.Second):
			t.Fatalf("missing delta %d", i)
		}
	}
}

func TestWebTextSource_ResetClearsAndSignals(t *testing.T) {
	hub := newEphemeralHub()
	ch, unsub := hub.subscribe("t1")
	defer unsub()
	src := NewWebTextSource(hub)

	src.Append("t1", "partial")
	<-ch // drain the delta
	src.Reset("t1")

	if got := src.Get("t1"); got != "" {
		t.Errorf("after reset Get = %q, want empty", got)
	}
	select {
	case ev := <-ch:
		if ev.Kind != "delta_reset" {
			t.Errorf("expected delta_reset, got %s", ev.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("reset did not fan out delta_reset")
	}
}

func TestWebTextSource_EmptyAppendIsNoop(t *testing.T) {
	hub := newEphemeralHub()
	ch, unsub := hub.subscribe("t1")
	defer unsub()
	src := NewWebTextSource(hub)

	src.Append("t1", "")
	select {
	case ev := <-ch:
		t.Errorf("empty append should not fan out, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// expected: nothing published
	}
}

// WebTextSource must satisfy the structural TextSource the chatglue.Loop
// expects (Append/Reset). This compile-time check documents that contract.
var _ interface {
	Append(string, string)
	Reset(string)
} = (*WebTextSource)(nil)
