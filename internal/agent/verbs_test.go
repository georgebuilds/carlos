package agent_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/tools"
)

// TestSteer_AppendsEventAndDeliversToLoop spawns a child running on a
// "slow" provider (events trickled with deliberate delay so the loop
// is reliably in-flight when Steer fires). Asserts: (a) Steer returns
// nil; (b) an EvtSteering event lands in the log; (c) the second
// provider Stream call's request includes a "[steer] " user message —
// proving the loop drained the steering channel between iterations.
func TestSteer_AppendsEventAndDeliversToLoop(t *testing.T) {
	sup, log, p, cleanup := newVerbHarness(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sub, resultCh, err := sup.Spawn(ctx, "", agent.SpawnContract{
		Objective: "test", OutputFormat: "x", ToolAllowlist: []string{}, MaxTurns: 4,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Block until the first Stream call has been observed (the
	// supervisor goroutine is now mid-loop), then Steer.
	p.waitForCall(t, 1, 2*time.Second)
	if err := sup.Steer(sub.ID, "please be brief"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	// Release the first Stream so the loop advances to iteration 2.
	p.release()
	// Wait for second Stream call to be issued so we can assert the
	// request shape includes the drained steer.
	p.waitForCall(t, 2, 2*time.Second)
	p.release()

	res := <-resultCh
	_ = res

	// Audit: the EvtSteering row landed. Use a fresh ctx — the loop
	// may have exhausted the one passed to Spawn while we waited on
	// resultCh.
	evs, readErr := log.Read(context.Background(), sub.ID, 0)
	if readErr != nil {
		t.Fatalf("Read: %v", readErr)
	}
	var sawSteer bool
	types := make([]string, 0, len(evs))
	for _, e := range evs {
		types = append(types, string(e.Type))
		if e.Type == agent.EvtSteering {
			sawSteer = true
		}
	}
	if !sawSteer {
		t.Errorf("no steering event recorded in log\nseen types: %v\nsub.ID=%q", types, sub.ID)
	}

	// Runtime: the loop drained the steer into iteration 2's request.
	if !p.everSawSteer() {
		t.Error("loop did not drain steering channel into the next provider request")
	}
}

func TestSteer_UnknownAgentReturnsErrAgentNotFound(t *testing.T) {
	sup := newQuietSupervisor(t)
	defer sup.Shutdown()
	if err := sup.Steer("nope", "hi"); !errors.Is(err, agent.ErrAgentNotFound) {
		t.Errorf("want ErrAgentNotFound, got %v", err)
	}
}

func TestSteer_EmptyMessageRejected(t *testing.T) {
	sup := newQuietSupervisor(t)
	defer sup.Shutdown()
	if err := sup.Steer("any", "   "); err == nil {
		t.Error("expected error on empty message")
	}
}

func TestInterrupt_CancelsRunningChild(t *testing.T) {
	sup, _, p, cleanup := newVerbHarness(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, resultCh, err := sup.Spawn(ctx, "", agent.SpawnContract{
		Objective: "x", OutputFormat: "y", ToolAllowlist: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.waitForCall(t, 1, 2*time.Second)
	if err := sup.Interrupt(sub.ID); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case <-resultCh:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Interrupt did not cause the child to exit within 2s")
	}
}

func TestStop_CancelsRunningChild(t *testing.T) {
	sup, _, p, cleanup := newVerbHarness(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, resultCh, _ := sup.Spawn(ctx, "", agent.SpawnContract{
		Objective: "x", OutputFormat: "y", ToolAllowlist: []string{},
	})
	p.waitForCall(t, 1, 2*time.Second)
	if err := sup.Stop(sub.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cause the child to exit within 2s")
	}
}

func TestKill_AliasesStop(t *testing.T) {
	sup := newQuietSupervisor(t)
	defer sup.Shutdown()
	if !errors.Is(sup.Kill("nope"), agent.ErrAgentNotFound) {
		t.Error("Kill on unknown id should return ErrAgentNotFound")
	}
	if !errors.Is(sup.Stop("nope"), agent.ErrAgentNotFound) {
		t.Error("Stop on unknown id should return ErrAgentNotFound")
	}
}

// --- helpers ---------------------------------------------------------------

func newQuietSupervisor(t *testing.T) *agent.Supervisor {
	t.Helper()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return agent.NewSupervisor(log, fake.New("q", fake.Script{}), tools.NewRegistry())
}

// newVerbHarness gives a freshly-spawned Supervisor + log + a
// gatedProvider that hangs each Stream call until release() is called.
// Used by verb tests that need a reliably-in-flight target.
func newVerbHarness(t *testing.T) (*agent.Supervisor, *agent.SQLiteEventLog, *gatedProvider, func()) {
	t.Helper()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	p := newGatedProvider()
	sup := agent.NewSupervisor(log, p, tools.NewRegistry())
	return sup, log, p, func() {
		sup.Shutdown()
		_ = log.Close()
	}
}

// gatedProvider is a test-local providers.Provider that blocks each
// Stream call on a release signal. Lets verb tests synchronize: spawn
// → waitForCall → fire the verb → release → wait for next call.
//
// The script emitted per call is a one-shot tool_use that the loop
// handles (unknown tool → tool_result) and loops on. Tests observe
// requests via sawSteer to verify drainSteering ran.
type gatedProvider struct {
	gate     chan struct{} // released per call
	calls    chan int      // emits the new call count on each Stream
	totalMu  *time.Time    // unused; keeping struct simple
	sawSteer bool
	count    int
}

func newGatedProvider() *gatedProvider {
	return &gatedProvider{
		gate:  make(chan struct{}, 10), // buffered so release-ahead is fine
		calls: make(chan int, 10),
	}
}

func (p *gatedProvider) Name() string                         { return "gated" }
func (p *gatedProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }

func (p *gatedProvider) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	p.count++
	myCall := p.count
	// Notify watchers that a Stream call is in flight.
	select {
	case p.calls <- myCall:
	default:
	}
	// Inspect request for a "[steer]" marker on any subsequent call.
	if myCall >= 2 {
		for _, m := range req.Messages {
			for _, b := range m.Content {
				if strings.Contains(b.Text, "[steer]") {
					p.sawSteer = true
				}
			}
		}
	}
	ch := make(chan providers.Event)
	go func() {
		defer close(ch)
		// Block until release (or ctx cancel).
		select {
		case <-p.gate:
		case <-ctx.Done():
			return
		}
		// Emit a tool_use that the loop will fail to execute (empty
		// registry) — that gives us the iteration boundary we need.
		evs := []providers.Event{
			{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "t", Name: "ghost"}},
			{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "t", Name: "ghost", Input: []byte(`{}`)}},
			{Kind: providers.EventStopReason, Stop: "tool_use"},
		}
		for _, ev := range evs {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// release lets one pending Stream call emit its events.
func (p *gatedProvider) release() {
	select {
	case p.gate <- struct{}{}:
	default:
	}
}

// waitForCall blocks until the Nth Stream call has been observed.
func (p *gatedProvider) waitForCall(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case got := <-p.calls:
			if got >= n {
				return
			}
		case <-deadline:
			t.Fatalf("waitForCall(%d): timed out (sawSteer=%t)", n, p.sawSteer)
		}
	}
}

func (p *gatedProvider) everSawSteer() bool { return p.sawSteer }
