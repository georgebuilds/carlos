package chatglue

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
)

// firstBlocksThenCompletes streams `partial` on the first turn then blocks
// until the context is cancelled (a model still "thinking"), and on the
// second turn streams `second` and ends cleanly. Lets a test interrupt the
// first turn and confirm the loop still serves the next message.
type firstBlocksThenCompletes struct {
	mu        sync.Mutex
	calls     int
	partial   string
	second    string
	streaming chan struct{} // closed once the first turn has streamed `partial`
}

func (p *firstBlocksThenCompletes) Name() string                       { return "blocking" }
func (p *firstBlocksThenCompletes) Capabilities() providers.Capabilities { return providers.Capabilities{} }

func (p *firstBlocksThenCompletes) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()

	ch := make(chan providers.Event)
	go func() {
		defer close(ch)
		if call == 1 {
			if p.partial != "" {
				select {
				case ch <- providers.Event{Kind: providers.EventTextDelta, Text: p.partial}:
				case <-ctx.Done():
					return
				}
			}
			close(p.streaming)
			<-ctx.Done() // block until interrupted; never send a stop reason
			return
		}
		select {
		case ch <- providers.Event{Kind: providers.EventTextDelta, Text: p.second}:
		case <-ctx.Done():
			return
		}
		select {
		case ch <- providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"}:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// TestLoop_InterruptSealsPartialAndKeepsServing is the esc-to-interrupt
// regression: an interrupted turn seals the text the user already saw plus
// the interrupted note, resets the live source, and the loop keeps serving
// the next message (interrupt aborts the turn, not the session).
func TestLoop_InterruptSealsPartialAndKeepsServing(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-interrupt"
	seedAgent(t, log, id)
	src := newMemSource()
	prov := &firstBlocksThenCompletes{partial: "Working on it", second: "Done.", streaming: make(chan struct{})}

	l := NewLoop(Config{Provider: prov}, log, src, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	time.Sleep(50 * time.Millisecond)
	appendUserMessage(t, log, id, "do a long thing")

	// Wait until the first turn has streamed its partial, then interrupt.
	select {
	case <-prov.streaming:
	case <-time.After(2 * time.Second):
		t.Fatal("provider never started streaming")
	}
	l.Interrupt()

	sealed := waitForAssistant(t, log, id, interruptedNote)
	if !strings.Contains(sealed, "Working on it") {
		t.Errorf("interrupted turn dropped the partial output: %q", sealed)
	}
	if got := src.Get(id); got != "" {
		t.Errorf("source not reset after interrupt: %q", got)
	}

	// The loop must still serve the next message.
	appendUserMessage(t, log, id, "now finish")
	if full := waitForAssistant(t, log, id, "Done."); !strings.Contains(full, "Done.") {
		t.Errorf("loop stopped serving after interrupt: %q", full)
	}
}

// TestLoop_InterruptBeforeFirstTokenSealsNote: interrupting after the stream
// opens but before any token (and with a clean-closing provider, nil err)
// must still seal a note-only "(interrupted)" turn, not erase the turn. This
// is the regression guard for keying the seal off the interrupt flag alone
// rather than off the streamed text or the provider error.
func TestLoop_InterruptBeforeFirstTokenSealsNote(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-pretoken"
	seedAgent(t, log, id)
	src := newMemSource()
	prov := &firstBlocksThenCompletes{partial: "", second: "x", streaming: make(chan struct{})}

	l := NewLoop(Config{Provider: prov}, log, src, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	time.Sleep(50 * time.Millisecond)
	appendUserMessage(t, log, id, "think hard")
	select {
	case <-prov.streaming:
	case <-time.After(2 * time.Second):
		t.Fatal("provider never opened the stream")
	}
	l.Interrupt()

	sealed := waitForAssistant(t, log, id, interruptedNote)
	if strings.TrimSpace(sealed) != interruptedNote {
		t.Errorf("pre-token interrupt should seal the note only, got %q", sealed)
	}
}

// TestLoop_InterruptIdleAndNilAreNoops: interrupting with no turn in flight,
// or on a nil Loop, returns false and must not panic.
func TestLoop_InterruptIdleAndNilAreNoops(t *testing.T) {
	l := NewLoop(Config{Provider: scriptedProvider("hi")}, openTestLog(t), newMemSource(), "x")
	if l.Interrupt() { // no turn in flight
		t.Error("Interrupt with no armed turn should return false")
	}

	var nilLoop *Loop
	if nilLoop.Interrupt() { // nil receiver
		t.Error("Interrupt on a nil loop should return false")
	}
}

// TestLoop_InterruptClosesUnbalancedCodeFence: a partial that ends inside an
// open ``` fence gets the fence closed before the (interrupted) note, so the
// note renders as text rather than being swallowed into the code block.
func TestLoop_InterruptClosesUnbalancedCodeFence(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-cg-fence"
	seedAgent(t, log, id)
	src := newMemSource()
	prov := &firstBlocksThenCompletes{partial: "```go\nfunc main() {", second: "x", streaming: make(chan struct{})}

	l := NewLoop(Config{Provider: prov}, log, src, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	time.Sleep(50 * time.Millisecond)
	appendUserMessage(t, log, id, "write some go")
	select {
	case <-prov.streaming:
	case <-time.After(2 * time.Second):
		t.Fatal("provider never streamed")
	}
	l.Interrupt()

	sealed := waitForAssistant(t, log, id, interruptedNote)
	if strings.Count(sealed, "```")%2 != 0 {
		t.Errorf("interrupted seal left an unbalanced code fence: %q", sealed)
	}
	// The note must sit OUTSIDE the code block (after the closing fence).
	if i := strings.LastIndex(sealed, "```"); i < 0 || strings.Contains(sealed[:i], interruptedNote) {
		t.Errorf("note should follow the closing fence, not precede it: %q", sealed)
	}
}
