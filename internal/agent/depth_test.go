package agent_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestSpawn_DepthCapDefault verifies the default maxSpawnDepth=1
// rejects a grandchild spawn:
//
//   root (depth 0) → child A (depth 1, OK) → child B (depth 2, REJECT)
//
// The rejection must be ErrSpawnDepthExceeded so the eventual Agent
// tool (Slice 3e) can surface it to the model verbatim.
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

	// depth 1: child of the root agent (parent ID == "" means root).
	a, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "depth-1"})
	if err != nil {
		t.Fatalf("spawn depth-1: %v", err)
	}

	// depth 2: grandchild — must be refused at the default cap of 1.
	_, _, err = sup.Spawn(ctx, a.ID, agent.SpawnContract{Objective: "depth-2"})
	if !errors.Is(err, agent.ErrSpawnDepthExceeded) {
		t.Fatalf("spawn depth-2: err = %v, want ErrSpawnDepthExceeded", err)
	}
}

// TestSpawn_DepthCapConfigurable verifies SetMaxSpawnDepth lets the
// chain go deeper. We bump to 3 and spawn three levels.
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

	a, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "d1"})
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
