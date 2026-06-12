package agent_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// seedRootAgent inserts a top-level agent row (parent_id NULL, self
// root_id) - the shape a chat thread takes in the agents projection.
// Depth tests spawn under it so the chain starts from an explicit,
// named root, exactly as the Agent tool does now that it threads the
// calling thread's id through ctx (WithSpawnParent).
func seedRootAgent(t *testing.T, log *agent.SQLiteEventLog, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID: id, RootID: id, State: agent.StateRunning, Attempt: 1,
		Title: id, CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("seed root %s: %v", id, err)
	}
}

// TestSpawn_DepthCapDefault verifies the default maxSpawnDepth=1
// rejects a grandchild spawn:
//
//	root thread (depth 0) → child A (depth 1, OK) → child B (depth 2, REJECT)
//
// The root is an explicit top-level agent row (a chat thread), matching
// the lineage the Agent tool records since spawn-parent ctx plumbing
// landed. The rejection must be ErrSpawnDepthExceeded so the Agent tool
// can surface it to the model verbatim.
func TestSpawn_DepthCapDefault(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()

	seedRootAgent(t, log, "thread-root")

	// depth 1: child of the explicit root thread - allowed at cap 1.
	a, _, err := sup.Spawn(ctx, "thread-root", agent.SpawnContract{Objective: "depth-1"})
	if err != nil {
		t.Fatalf("spawn depth-1: %v", err)
	}

	// depth 2: grandchild — must be refused at the default cap of 1.
	_, _, err = sup.Spawn(ctx, a.ID, agent.SpawnContract{Objective: "depth-2"})
	if !errors.Is(err, agent.ErrSpawnDepthExceeded) {
		t.Fatalf("spawn depth-2: err = %v, want ErrSpawnDepthExceeded", err)
	}
}

// TestSpawn_DepthCapTopLevelAlwaysAllowed pins the parentID == ""
// fallback: a top-level spawn (headless `please`, daemon fires - any
// caller without a spawn-parent in ctx) is depth 0 and never trips the
// cap, preserving the legacy behaviour.
func TestSpawn_DepthCapTopLevelAlwaysAllowed(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()

	if _, _, err := sup.Spawn(context.Background(), "", agent.SpawnContract{Objective: "top-level"}); err != nil {
		t.Fatalf("top-level spawn: %v", err)
	}
}

// TestSpawn_DepthCapConfigurable verifies SetMaxSpawnDepth lets the
// chain go deeper. We bump to 3 and spawn three levels under an
// explicit root thread; level 4 must be refused.
func TestSpawn_DepthCapConfigurable(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	sup.SetMaxSpawnDepth(3)
	defer sup.Shutdown()

	seedRootAgent(t, log, "thread-root")

	a, _, err := sup.Spawn(ctx, "thread-root", agent.SpawnContract{Objective: "d1"})
	if err != nil {
		t.Fatalf("spawn d1: %v", err)
	}
	b, _, err := sup.Spawn(ctx, a.ID, agent.SpawnContract{Objective: "d2"})
	if err != nil {
		t.Fatalf("spawn d2: %v", err)
	}
	c, _, err := sup.Spawn(ctx, b.ID, agent.SpawnContract{Objective: "d3"})
	if err != nil {
		t.Fatalf("spawn d3: %v", err)
	}
	// Depth 4 is over the cap of 3.
	if _, _, err := sup.Spawn(ctx, c.ID, agent.SpawnContract{Objective: "d4"}); !errors.Is(err, agent.ErrSpawnDepthExceeded) {
		t.Fatalf("spawn d4: err = %v, want ErrSpawnDepthExceeded", err)
	}
}

// TestSpawn_DepthCapUnknownParent verifies that spawning with a
// parentID that the supervisor doesn't know about surfaces an error
// (not a silent treat-as-root). This catches a caller bug early.
func TestSpawn_DepthCapUnknownParent(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()

	if _, _, err := sup.Spawn(ctx, "ghost-parent", agent.SpawnContract{Objective: "lost"}); err == nil {
		t.Fatalf("expected error for unknown parent, got nil")
	}
}
