package agent

// Whitebox coverage for computeDepth's defensive branches: the GetAgent
// error path, the parent-id cycle hop-budget exhaustion, and the
// maxHops floor clamp.

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newDepthSupervisor(t *testing.T) (*Supervisor, *SQLiteEventLog) {
	t.Helper()
	dir := t.TempDir()
	log, err := OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = CloseStateDB(log) })
	s := NewSupervisor(log, nil, nil)
	t.Cleanup(s.Shutdown)
	return s, log
}

// TestComputeDepth_GetAgentErrorSurfaces closes the DB so GetAgent fails,
// hitting the get-error wrap branch.
func TestComputeDepth_GetAgentErrorSurfaces(t *testing.T) {
	s, log := newDepthSupervisor(t)
	if err := CloseStateDB(log); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := s.computeDepth(context.Background(), "some-parent"); err == nil {
		t.Fatal("computeDepth on closed DB should error")
	}
}

// TestComputeDepth_CycleExhaustsHopBudget builds a 2-node parent_id cycle
// (a→b→a) directly in the projection table so the walk never finds a
// root and returns once the hop budget is spent.
func TestComputeDepth_CycleExhaustsHopBudget(t *testing.T) {
	s, log := newDepthSupervisor(t)
	ctx := context.Background()
	now := time.Now().UTC().UnixMilli()

	// Insert two rows that point at each other. We bypass InsertAgent's
	// FK ordering by inserting both with NULL parent first, then
	// updating parent_id to form the cycle.
	for _, id := range []string{"cyc-a", "cyc-b"} {
		if _, err := log.DB().ExecContext(ctx,
			`INSERT INTO agents(id, root_id, state, attempt, title, created_at, updated_at, last_heartbeat_at)
			 VALUES (?, ?, 'running', 1, ?, ?, ?, ?)`,
			id, id, id, now, now, now,
		); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	if _, err := log.DB().ExecContext(ctx, `UPDATE agents SET parent_id = 'cyc-b' WHERE id = 'cyc-a'`); err != nil {
		t.Fatalf("link a→b: %v", err)
	}
	if _, err := log.DB().ExecContext(ctx, `UPDATE agents SET parent_id = 'cyc-a' WHERE id = 'cyc-b'`); err != nil {
		t.Fatalf("link b→a: %v", err)
	}

	depth, err := s.computeDepth(ctx, "cyc-a")
	if err != nil {
		t.Fatalf("computeDepth: %v", err)
	}
	// The walk exhausts its hop budget (no root reached) and returns the
	// hop count — which must exceed the spawn cap so Spawn would refuse.
	if depth < s.maxSpawnDepth {
		t.Errorf("cycle walk depth = %d, want >= maxSpawnDepth %d", depth, s.maxSpawnDepth)
	}
}

// TestComputeDepth_MaxHopsFloorClamp sets maxSpawnDepth to 0 so the
// computed maxHops (0+2=2) is below the floor of 3 and gets clamped.
func TestComputeDepth_MaxHopsFloorClamp(t *testing.T) {
	s, log := newDepthSupervisor(t)
	s.SetMaxSpawnDepth(0)
	ctx := context.Background()
	now := time.Now().UTC()
	// A single root agent: parent_id empty → depth 1, returns before the
	// budget matters, but the clamp line still executes during setup.
	if err := log.InsertAgent(ctx, AgentRow{
		ID: "root-only", RootID: "root-only", State: StateRunning, Attempt: 1,
		Title: "root", Model: "fake", CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	depth, err := s.computeDepth(ctx, "root-only")
	if err != nil {
		t.Fatalf("computeDepth: %v", err)
	}
	if depth != 1 {
		t.Errorf("depth = %d, want 1", depth)
	}
}
