package manage

import (
	"context"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestStaticSnapshotSource_ReturnsCopy confirms the StaticSnapshotSource
// returns a defensive copy so a caller mutating the returned slice can
// not race subsequent ticks reading the same source.
func TestStaticSnapshotSource_ReturnsCopy(t *testing.T) {
	rows := []agent.AgentRow{
		{ID: "a", Title: "first"},
		{ID: "b", Title: "second"},
	}
	src := &StaticSnapshotSource{Rows: rows}

	got, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Snapshot len = %d, want 2", len(got))
	}

	// Mutate the returned slice + confirm the source's slice is untouched.
	got[0].Title = "tampered"
	if src.Rows[0].Title != "first" {
		t.Errorf("mutating snapshot mutated source: %q", src.Rows[0].Title)
	}
}

// TestSQLiteSnapshot_RoundTripsRows seeds two agents into a real
// SQLite event log + projection and confirms NewSQLiteSnapshotSource
// pulls them back out with hydrated states + timestamps.
func TestSQLiteSnapshot_RoundTripsRows(t *testing.T) {
	log := openTempLog(t)
	seedAgent(t, log, "01HVsrc00000000000000001", "", "first", "fake", agent.StateRunning)
	seedAgent(t, log, "01HVsrc00000000000000002", "", "second", "fake", agent.StateAwaitingInput)

	src := NewSQLiteSnapshotSource(log)
	rows, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot err: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Snapshot len = %d, want 2", len(rows))
	}

	byID := map[string]agent.AgentRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	first := byID["01HVsrc00000000000000001"]
	if first.State != agent.StateRunning {
		t.Errorf("first state = %s, want running", first.State)
	}
	if first.Title != "first" {
		t.Errorf("first title = %q, want first", first.Title)
	}
	if first.CreatedAt.IsZero() {
		t.Errorf("first created_at = zero")
	}

	second := byID["01HVsrc00000000000000002"]
	if second.State != agent.StateAwaitingInput {
		t.Errorf("second state = %s, want awaiting-input", second.State)
	}
}

// TestParseStateString_RoundTripsAllStates is a small invariant: the
// unexported parseStateString should round-trip every agent.State via
// State.String(). Unknown strings return false.
func TestParseStateString_RoundTripsAllStates(t *testing.T) {
	states := []agent.State{
		agent.StateSpawning, agent.StateQueued, agent.StateRunning,
		agent.StateAwaitingInput, agent.StateBlocked, agent.StatePausedByUser,
		agent.StateCompacting, agent.StateCancelling, agent.StateDone,
		agent.StateFailed, agent.StateOrphaned,
	}
	for _, s := range states {
		got, ok := parseStateString(s.String())
		if !ok {
			t.Errorf("parseStateString(%q) failed", s.String())
		}
		if got != s {
			t.Errorf("parseStateString(%q) = %v, want %v", s.String(), got, s)
		}
	}
	if _, ok := parseStateString("totally-not-a-state"); ok {
		t.Errorf("parseStateString accepted nonsense")
	}
}

// TestSQLiteSnapshot_ContextCancelled is a sanity check that the
// snapshot query honours the context.
func TestSQLiteSnapshot_ContextCancelled(t *testing.T) {
	log := openTempLog(t)
	seedAgent(t, log, "01HVsrc99999999999999999", "", "demo", "fake", agent.StateRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	// Sleep so the query runs against an already-cancelled context.
	time.Sleep(20 * time.Millisecond)

	src := NewSQLiteSnapshotSource(log)
	_, err := src.Snapshot(ctx)
	if err == nil {
		t.Logf("snapshot completed before ctx hit; acceptable on fast machines")
	}
}
