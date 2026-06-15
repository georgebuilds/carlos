package web

import (
	"context"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// The roster overlay (Attached/Frame) and backend tag come from the
// injected oracle + reader name, not from a hardcoded "carlos" or a
// sibling method. This is the seam that lets the read-only server and the
// interactive backend share one reader (the embedding virtual-dispatch
// trap is dissolved by injecting attachment as a func).
func TestCarlosReader_ListThreadsHonorsOracle(t *testing.T) {
	log, _ := newTestLog(t)
	seedThread(t, log, "t1", "one", "hi")
	seedThread(t, log, "t2", "two", "yo")

	r := NewCarlosReader(log, "carlos", readOnlyCaps, func(id string) (bool, string) {
		if id == "t1" {
			return true, "work"
		}
		return false, ""
	})

	out, err := r.ListThreads(context.Background())
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	by := map[string]ThreadSummary{}
	for _, s := range out {
		by[s.ID] = s
	}
	if !by["t1"].Attached || by["t1"].Frame != "work" {
		t.Errorf("t1 overlay = {attached:%v frame:%q}, want {true work}", by["t1"].Attached, by["t1"].Frame)
	}
	if by["t2"].Attached || by["t2"].Frame != "" {
		t.Errorf("t2 overlay = {attached:%v frame:%q}, want {false \"\"}", by["t2"].Attached, by["t2"].Frame)
	}
	if by["t1"].Backend != "carlos" {
		t.Errorf("backend tag = %q, want carlos", by["t1"].Backend)
	}
}

// ReadEvents honors both the exclusive lower bound (from) and the
// exclusive upper bound (upTo, the SSE gap-repair window), and drops
// non-forwarded event types.
func TestCarlosReader_ReadEventsHonorsBounds(t *testing.T) {
	log, _ := newTestLog(t)
	seedThread(t, log, "t1", "one", "hi")                                                  // seq 1: user_message
	appendEvent(t, log, "t1", agent.EvtProviderCall, map[string]any{"provider": "x"})      // seq 2: NOT forwarded
	appendEvent(t, log, "t1", agent.EvtAssistantMessage, agent.MessagePayload{Text: "a"})  // seq 3
	s4 := appendEvent(t, log, "t1", agent.EvtUserMessage, agent.MessagePayload{Text: "b"}) // seq 4
	r := NewCarlosReader(log, "carlos", readOnlyCaps, nil)
	ctx := context.Background()

	// Unbounded from 0: the provider_call (seq 2) is dropped, leaving 3.
	all, err := r.ReadEvents(ctx, "t1", 0, 0)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unbounded read = %d events, want 3 (provider_call dropped)", len(all))
	}

	// from=1 excludes seq<=1.
	tail, _ := r.ReadEvents(ctx, "t1", 1, 0)
	for _, we := range tail {
		if we.Seq <= 1 {
			t.Errorf("from=1 returned seq %d (should exclude <=1)", we.Seq)
		}
	}

	// upTo=s4 excludes seq>=s4 (the gap-repair window is exclusive).
	win, _ := r.ReadEvents(ctx, "t1", 0, s4)
	for _, we := range win {
		if we.Seq >= s4 {
			t.Errorf("upTo=%d returned seq %d (should exclude >=upTo)", s4, we.Seq)
		}
	}
}

// Subscribe forwards forwardable events as WireEvents (dropping the rest)
// and its translation goroutine exits cleanly on unsub: the outbound
// channel closes and no goroutine leaks. Run under -race to catch a
// double-close or a send-after-unsub.
func TestCarlosReader_SubscribeForwardsAndClosesOnUnsub(t *testing.T) {
	log, _ := newTestLog(t)
	seedThread(t, log, "t1", "one", "hi")
	r := NewCarlosReader(log, "carlos", readOnlyCaps, nil)

	ch, unsub, err := r.Subscribe("t1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// A forwarded event arrives as a wire event; a non-forwarded one does
	// not (the goroutine drops it, so the next forwarded event is next).
	appendEvent(t, log, "t1", agent.EvtProviderCall, map[string]any{"p": "x"})
	want := appendEvent(t, log, "t1", agent.EvtAssistantMessage, agent.MessagePayload{Text: "reply"})

	select {
	case we, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before delivering the assistant message")
		}
		if we.Kind != "assistant_message" || we.Seq != want {
			t.Fatalf("got {kind:%q seq:%d}, want {assistant_message %d}", we.Kind, we.Seq, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the forwarded event")
	}

	// Unsub stops the goroutine and closes the outbound channel.
	unsub()
	unsub() // idempotent: a second call must not panic (double-close guard)
	select {
	case _, ok := <-ch:
		if ok {
			// Drain any in-flight value, then it must close.
			select {
			case _, ok2 := <-ch:
				if ok2 {
					t.Fatal("outbound channel still delivering after unsub")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("outbound channel did not close after unsub")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("outbound channel did not close after unsub")
	}
}
