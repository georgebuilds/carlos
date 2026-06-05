package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// fakeAdapter is a test double for ToolGroundedVerifier — records the
// args it was called with and returns the canned report/err.
type fakeAdapter struct {
	name   string
	report agent.VerificationReport
	err    error

	called   int
	lastDir  string
	lastBody []byte
}

func (f *fakeAdapter) Name() string { return f.name }
func (f *fakeAdapter) Verify(_ context.Context, workdir string, content []byte) (agent.VerificationReport, error) {
	f.called++
	f.lastDir = workdir
	f.lastBody = content
	return f.report, f.err
}

func TestDispatcher_RoutesByKind(t *testing.T) {
	a := &fakeAdapter{name: "a", report: agent.VerificationReport{Decision: agent.VerificationAccept, Score: 10}}
	b := &fakeAdapter{name: "b", report: agent.VerificationReport{Decision: agent.VerificationAccept, Score: 10}}
	d := agent.NewDispatcher()
	d.Register("plan", a)
	d.Register("research", b)

	reports, err := d.Verify(context.Background(), "/tmp", "plan", []byte("hello"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(reports) != 1 || a.called != 1 || b.called != 0 {
		t.Fatalf("expected plan to call a only; got reports=%d a=%d b=%d", len(reports), a.called, b.called)
	}
	if a.lastDir != "/tmp" || string(a.lastBody) != "hello" {
		t.Fatalf("adapter got wrong args: dir=%q body=%q", a.lastDir, a.lastBody)
	}

	if _, err := d.Verify(context.Background(), "/tmp", "research", []byte("x")); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if b.called != 1 {
		t.Fatalf("expected b to fire on research kind")
	}
}

func TestDispatcher_MultipleAdaptersPerKind(t *testing.T) {
	a := &fakeAdapter{name: "a"}
	b := &fakeAdapter{name: "b"}
	d := agent.NewDispatcher()
	d.Register("plan", a)
	d.Register("plan", b)

	reports, err := d.Verify(context.Background(), "/tmp", "plan", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(reports) != 2 || a.called != 1 || b.called != 1 {
		t.Fatalf("expected both adapters to fire; reports=%d a=%d b=%d", len(reports), a.called, b.called)
	}
}

func TestDispatcher_UnknownKindNoOp(t *testing.T) {
	d := agent.NewDispatcher()
	reports, err := d.Verify(context.Background(), "/tmp", "unknown", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if reports != nil {
		t.Fatalf("expected nil reports for unknown kind, got %v", reports)
	}
}

func TestDispatcher_AdapterErrorJoined(t *testing.T) {
	sentinelA := errors.New("a broke")
	sentinelB := errors.New("b broke")
	a := &fakeAdapter{name: "a", err: sentinelA}
	b := &fakeAdapter{name: "b", err: sentinelB}
	d := agent.NewDispatcher()
	d.Register("plan", a)
	d.Register("plan", b)

	reports, err := d.Verify(context.Background(), "/tmp", "plan", nil)
	if err == nil {
		t.Fatal("expected joined error")
	}
	if !errors.Is(err, sentinelA) || !errors.Is(err, sentinelB) {
		t.Fatalf("expected both sentinels in joined err: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("expected reports recorded even on error, got %d", len(reports))
	}
}

func TestDispatcher_RegisterEmptyKindSilent(t *testing.T) {
	d := agent.NewDispatcher()
	d.Register("", &fakeAdapter{name: "x"})
	d.Register("plan", nil)
	kinds := d.KindsRegistered()
	if len(kinds) != 0 {
		t.Fatalf("expected no kinds registered, got %v", kinds)
	}
}

func TestDispatcher_RegisterDefaults(t *testing.T) {
	d := agent.NewDispatcher()
	d.RegisterDefaults()
	kinds := d.KindsRegistered()
	want := map[string]bool{"plan": true, "diff": true, "research": true}
	if len(kinds) != len(want) {
		t.Fatalf("expected 3 kinds, got %d: %v", len(kinds), kinds)
	}
	for _, k := range kinds {
		if !want[k] {
			t.Fatalf("unexpected kind %q registered", k)
		}
	}
}

func TestDispatcher_NilReceiver(t *testing.T) {
	var d *agent.Dispatcher
	if _, err := d.Verify(context.Background(), "/tmp", "plan", nil); err == nil {
		t.Fatal("expected error on nil dispatcher")
	}
	// Nil-safe register / kinds
	d.Register("plan", &fakeAdapter{name: "x"})
	if got := d.KindsRegistered(); got != nil {
		t.Fatalf("expected nil kinds on nil dispatcher, got %v", got)
	}
}
