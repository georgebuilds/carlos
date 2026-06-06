package usershell

import (
	"context"
	"testing"
	"time"
)

func TestState_String(t *testing.T) {
	cases := map[State]string{
		StatePending:   "pending",
		StateRunning:   "running",
		StateDone:      "done",
		StateFailed:    "failed",
		StateCancelled: "cancelled",
		State(99):      "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestState_IsTerminal(t *testing.T) {
	terminal := map[State]bool{
		StatePending:   false,
		StateRunning:   false,
		StateDone:      true,
		StateFailed:    true,
		StateCancelled: true,
	}
	for s, want := range terminal {
		if got := s.IsTerminal(); got != want {
			t.Errorf("State(%v).IsTerminal() = %v, want %v", s, got, want)
		}
	}
}

func TestMode_String(t *testing.T) {
	if Foreground.String() != "foreground" {
		t.Errorf("Foreground.String() = %q", Foreground.String())
	}
	if Background.String() != "background" {
		t.Errorf("Background.String() = %q", Background.String())
	}
	if Mode(99).String() != "unknown" {
		t.Errorf("Mode(99).String() = %q", Mode(99).String())
	}
}

// TestJob_TransitionTable pins every (from, to) pair as legal or
// illegal. The state machine is small enough to exhaustively check
// at test time — and the cost of a missed move is silent corruption.
func TestJob_TransitionTable(t *testing.T) {
	all := []State{StatePending, StateRunning, StateDone, StateFailed, StateCancelled}
	legal := map[State]map[State]bool{
		StatePending:   {StateRunning: true, StateCancelled: true},
		StateRunning:   {StateDone: true, StateFailed: true, StateCancelled: true},
		StateDone:      {},
		StateFailed:    {},
		StateCancelled: {},
	}
	for _, from := range all {
		for _, to := range all {
			t.Run(from.String()+"_to_"+to.String(), func(t *testing.T) {
				j := NewJob("test", "echo hi", "/tmp", Foreground, nil)
				j.state = from
				err := j.transition(to)
				if legal[from][to] {
					if err != nil {
						t.Errorf("legal transition %v→%v errored: %v", from, to, err)
					}
					if j.State() != to {
						t.Errorf("state didn't advance: want %v got %v", to, j.State())
					}
				} else {
					if err == nil {
						t.Errorf("illegal transition %v→%v should have errored", from, to)
					}
					if j.State() != from {
						t.Errorf("illegal transition mutated state: was %v now %v", from, j.State())
					}
				}
			})
		}
	}
}

func TestJob_TransitionStampsTimestamps(t *testing.T) {
	j := NewJob("t1", "echo hi", "/tmp", Foreground, nil)
	if !j.StartedAt.IsZero() {
		t.Error("StartedAt should be zero before run")
	}
	if err := j.transition(StateRunning); err != nil {
		t.Fatal(err)
	}
	if j.StartedAt.IsZero() {
		t.Error("StartedAt should be stamped on pending→running")
	}
	if !j.EndedAt.IsZero() {
		t.Error("EndedAt should still be zero")
	}
	if err := j.transition(StateDone); err != nil {
		t.Fatal(err)
	}
	if j.EndedAt.IsZero() {
		t.Error("EndedAt should be stamped on running→terminal")
	}
}

func TestJob_MarkBackgrounded(t *testing.T) {
	j := NewJob("t", "echo", "/tmp", Foreground, nil)
	// Cannot background a pending job.
	if err := j.markBackgrounded(true); err == nil {
		t.Error("expected error on bg-ing pending job")
	}
	_ = j.transition(StateRunning)
	if err := j.markBackgrounded(true); err != nil {
		t.Fatalf("bg of running job: %v", err)
	}
	if !j.Backgrounded {
		t.Error("Backgrounded flag not set")
	}
	// Idempotent.
	if err := j.markBackgrounded(true); err != nil {
		t.Errorf("idempotent bg: %v", err)
	}
	// Cannot background a terminal job.
	_ = j.transition(StateDone)
	if err := j.markBackgrounded(false); err == nil {
		t.Error("expected error on bg flag change after terminal")
	}
}

func TestJob_Snapshot(t *testing.T) {
	j := NewJob("j-snap", "cargo test", "/repo", Background, nil)
	_ = j.transition(StateRunning)
	_ = j.markBackgrounded(true)
	j.ExitCode = 0
	// Sleep > the ms truncation window so EndedAt - StartedAt is positive.
	time.Sleep(2 * time.Millisecond)
	_ = j.transition(StateDone)
	snap := j.Snapshot()
	if snap.ID != "j-snap" || snap.Command != "cargo test" || snap.Cwd != "/repo" {
		t.Errorf("snapshot fields: %+v", snap)
	}
	if snap.Mode != Background {
		t.Errorf("snapshot mode: %v", snap.Mode)
	}
	if !snap.Backgrounded {
		t.Error("snapshot Backgrounded flag lost")
	}
	if snap.State != StateDone {
		t.Errorf("snapshot state: %v", snap.State)
	}
	if snap.Duration() <= 0 {
		t.Errorf("snapshot duration should be positive after end: started=%v ended=%v", snap.StartedAt, snap.EndedAt)
	}
}

func TestSnapshot_Duration_StillRunning(t *testing.T) {
	j := NewJob("j", "cmd", "/tmp", Foreground, nil)
	_ = j.transition(StateRunning)
	time.Sleep(2 * time.Millisecond)
	if j.Snapshot().Duration() == 0 {
		t.Error("running job should have non-zero duration")
	}
}

func TestSnapshot_Duration_Pending(t *testing.T) {
	j := NewJob("j", "cmd", "/tmp", Foreground, nil)
	if j.Snapshot().Duration() != 0 {
		t.Error("pending job should have zero duration")
	}
}

func TestNewJob_SetsFields(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j := NewJob("id1", "ls -la", "/var/log", Background, cancel)
	if j.ID != "id1" || j.Command != "ls -la" || j.Cwd != "/var/log" {
		t.Errorf("NewJob fields: %+v", j)
	}
	if j.Mode != Background {
		t.Errorf("NewJob mode: %v", j.Mode)
	}
	if j.State() != StatePending {
		t.Errorf("NewJob initial state: %v", j.State())
	}
	if j.cancel == nil {
		t.Error("NewJob did not capture cancel")
	}
	if j.SubmittedAt.IsZero() {
		t.Error("NewJob did not stamp SubmittedAt")
	}
	_ = ctx
}
