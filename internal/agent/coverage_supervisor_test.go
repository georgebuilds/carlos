package agent_test

// Coverage for Supervisor.Spawn mode-cap refusal branches and the
// Append-created storage-error path on a root spawn.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/tools"
)

func newSpawnSupervisor(t *testing.T) (*agent.Supervisor, *agent.SQLiteEventLog) {
	t.Helper()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sup := agent.NewSupervisor(log, newHangingProvider(), tools.NewRegistry())
	t.Cleanup(func() {
		sup.Shutdown()
		_ = log.Close()
	})
	return sup, log
}

// TestSpawn_SoloModeRefuses pins the solo-mode cap (0) → ErrSpawnRefusedSolo.
func TestSpawn_SoloModeRefuses(t *testing.T) {
	sup, _ := newSpawnSupervisor(t)
	sup.SetMode(frame.ModeSolo)
	_, _, err := sup.Spawn(context.Background(), "", agent.SpawnContract{Objective: "x"})
	if !errors.Is(err, agent.ErrSpawnRefusedSolo) {
		t.Fatalf("solo spawn err = %v, want ErrSpawnRefusedSolo", err)
	}
}

// TestSpawn_TightModeBusyWhenCapZeroed sets tight mode and lowers the
// concurrency knob to 0 so effectiveSpawnCapLocked's non-orchestrator
// min() branch returns 0 → ErrSpawnBusyTight on the first spawn.
func TestSpawn_TightModeBusyWhenCapZeroed(t *testing.T) {
	sup, _ := newSpawnSupervisor(t)
	sup.SetMode(frame.ModeTight)
	sup.SetMaxConcurrentChildren(0) // forces min(0, tightCap) = 0
	_, _, err := sup.Spawn(context.Background(), "", agent.SpawnContract{Objective: "x"})
	if !errors.Is(err, agent.ErrSpawnBusyTight) {
		t.Fatalf("tight spawn err = %v, want ErrSpawnBusyTight", err)
	}
}

// TestSpawn_RunBudgetAndOverrideRegistry spawns a root child with a
// run-wide Tracker installed (so the per-subtree Tracker branch fires)
// and an OverrideRegistry on the contract (so the override-registry
// branch fires). The hanging provider keeps the child running; we cancel
// via Shutdown in cleanup.
func TestSpawn_RunBudgetAndOverrideRegistry(t *testing.T) {
	sup, _ := newSpawnSupervisor(t)
	sup.SetRunBudget(agent.NewTracker(nil)) // installs parentTracker

	override := tools.NewRegistry()
	sub, _, err := sup.Spawn(context.Background(), "", agent.SpawnContract{
		Objective:        "with override",
		OverrideRegistry: override,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if sub.ID == "" {
		t.Fatal("spawn returned empty agent id")
	}
	// The child is hanging; the in-flight accounting should see it.
	if got := sup.ActiveChildren(""); got != 1 {
		t.Errorf("ActiveChildren = %d, want 1", got)
	}
}

// TestSpawn_RootAppendFailsOnClosedLog closes the log so the first
// event-log write in Spawn (the state_change created event) fails,
// covering Spawn's append-created error branch. A root spawn
// (parentID="") skips the depth GetAgent, so Append is the first DB op.
func TestSpawn_RootAppendFailsOnClosedLog(t *testing.T) {
	sup, log := newSpawnSupervisor(t)
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, _, err := sup.Spawn(context.Background(), "", agent.SpawnContract{Objective: "x"})
	if err == nil {
		t.Fatal("Spawn against a closed log should error on the created-event append")
	}
}
