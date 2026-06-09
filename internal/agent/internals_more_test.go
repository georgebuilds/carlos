package agent

// More whitebox tests for internals: parseState fallback, drainSteering
// nil channel and close paths, lifecycle / heartbeat constructor knobs.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
)

func TestParseState_UnknownString(t *testing.T) {
	if _, ok := parseState("totally-not-a-state"); ok {
		t.Error("unknown state should return ok=false")
	}
}

func TestParseState_EveryKnownStateRoundTrips(t *testing.T) {
	for _, st := range []State{
		StateSpawning, StateQueued, StateRunning, StateAwaitingInput,
		StateBlocked, StatePausedByUser, StateCompacting, StateCancelling,
		StateDone, StateFailed, StateOrphaned,
	} {
		got, ok := parseState(st.String())
		if !ok || got != st {
			t.Errorf("round-trip for %s failed: got %s ok=%v", st, got, ok)
		}
	}
}

func TestDrainSteering_NilChannelNoOp(t *testing.T) {
	msgs := []providers.Message{{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hello"}}}}
	got := drainSteering(nil, msgs)
	if len(got) != 1 {
		t.Errorf("nil channel should leave messages unchanged, got %d", len(got))
	}
}

func TestDrainSteering_DrainsAndAppends(t *testing.T) {
	ch := make(chan string, 3)
	ch <- "first nudge"
	ch <- "" // empty should be skipped
	ch <- "second nudge"
	close(ch)

	got := drainSteering(ch, nil)
	if len(got) != 2 {
		t.Errorf("expected 2 messages from drain, got %d", len(got))
	}
	if got[0].Role != "user" {
		t.Errorf("steer message should have role=user, got %q", got[0].Role)
	}
	if got[0].Content[0].Text != "[steer] first nudge" {
		t.Errorf("steer prefix missing, got %q", got[0].Content[0].Text)
	}
}

func TestDrainSteering_NoPendingReturnsImmediately(t *testing.T) {
	ch := make(chan string, 1) // empty + open
	got := drainSteering(ch, []providers.Message{})
	if len(got) != 0 {
		t.Errorf("no pending should return immediately, got %d", len(got))
	}
}

func TestNewHeartbeatTicker_NilClockDefaults(t *testing.T) {
	// Construct via the production constructor with nil clock to hit the
	// default-RealClock branch.
	hb := NewHeartbeatTicker(nil, nil, 0)
	if hb == nil {
		t.Fatal("ticker nil")
	}
	if hb.interval != HeartbeatInterval {
		t.Errorf("interval = %v, want default", hb.interval)
	}
}

func TestNewOrphanSweeper_NilClockAndZeroDefaults(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenSQLiteEventLog(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer log.Close()
	sw := NewOrphanSweeper(log, nil, 0, 0)
	if sw == nil {
		t.Fatal("sweeper nil")
	}
	if sw.interval != SweepInterval {
		t.Errorf("interval default missed: %v", sw.interval)
	}
	if sw.tolerance != StalenessTolerance {
		t.Errorf("tolerance default missed: %v", sw.tolerance)
	}
}

func TestComputeDepth_EmptyParentReturnsZero(t *testing.T) {
	s := NewSupervisor(nil, nil, nil)
	defer s.Shutdown()
	d, err := s.computeDepth(context.Background(), "")
	if err != nil {
		t.Fatalf("computeDepth: %v", err)
	}
	if d != 0 {
		t.Errorf("depth = %d want 0", d)
	}
}

func TestComputeDepth_NilLogErrors(t *testing.T) {
	s := NewSupervisor(nil, nil, nil)
	defer s.Shutdown()
	if _, err := s.computeDepth(context.Background(), "some-parent"); err == nil {
		t.Fatal("nil log should error")
	}
}

func TestSupervisor_TransitionNilLogErrors(t *testing.T) {
	s := NewSupervisor(nil, nil, nil)
	defer s.Shutdown()
	if err := s.transition(context.Background(), "x", StateRunning); err == nil {
		t.Fatal("nil log should error")
	}
}

func TestRetryCount_UnknownAgentReturnsZero(t *testing.T) {
	s := NewSupervisor(nil, nil, nil)
	defer s.Shutdown()
	s.mu.Lock()
	got := s.retryCount("never", time.Now())
	s.mu.Unlock()
	if got != 0 {
		t.Errorf("retryCount unknown = %d want 0", got)
	}
}

func TestRetryCount_TrimsStaleEntries(t *testing.T) {
	s := NewSupervisor(nil, nil, nil)
	defer s.Shutdown()
	s.restartMaxT = 100 * time.Millisecond
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.mu.Lock()
	s.recordRetry("agent", now.Add(-1*time.Second)) // stale
	s.recordRetry("agent", now.Add(-50*time.Millisecond))
	s.recordRetry("agent", now)
	got := s.retryCount("agent", now)
	s.mu.Unlock()
	if got != 2 {
		t.Errorf("retryCount = %d, want 2 (one stale entry trimmed)", got)
	}
}

// TestSteer_AuditAppendFailureSurfaces pins the contract that an audit
// log Append failure is a real storage error - distinct from the
// non-blocking runtime-delivery drop documented at supervisor.go:523.
// We register a fake child directly so we can drive Steer past the
// child-not-found check, then close the event log so the next Append
// fails. The earlier implementation swallowed this error with `_, _ =`;
// the fix surfaces it via a wrapped error.
func TestSteer_AuditAppendFailureSurfaces(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenSQLiteEventLog(dir + "/state.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s := NewSupervisor(log, nil, nil)
	defer s.Shutdown()

	// Register a fake child so Steer reaches the Append call instead of
	// returning ErrAgentNotFound.
	s.mu.Lock()
	s.children["fake"] = &runningChild{
		id:       "fake",
		cancel:   func() {},
		done:     make(chan struct{}),
		steering: make(chan string, 1),
	}
	s.mu.Unlock()

	// Close the log so the next Append fails with "database is closed".
	if err := log.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}

	err = s.Steer("fake", "this should not be silently dropped")
	if err == nil {
		t.Fatal("Steer with closed audit log should return an error, got nil")
	}
	if msg := err.Error(); !strings.Contains(msg, "append audit event") {
		t.Errorf("error %q missing 'append audit event' prefix", msg)
	}
}
