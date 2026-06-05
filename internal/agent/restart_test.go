package agent_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestRetry_BreakerTripsAfterMaxR is the headline OTP restart-
// intensity assertion: MaxR+1 retries inside MaxT trips the breaker,
// which surfaces as ErrRestartIntensityExceeded AND flips
// IsCircuitBroken(id) to true.
//
// We tighten the cap (MaxR=2 inside 10s) so the test doesn't have to
// hammer 4 calls; the math is the same as the default 3-in-60s.
func TestRetry_BreakerTripsAfterMaxR(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()

	// MaxR=2, MaxT=10s: 3rd retry inside the window should trip.
	sup.SetRestartIntensity(2, 10*time.Second)

	const id = "agent-x"

	// Calls 1 and 2 are within budget.
	if _, err := sup.Retry(id); err != nil {
		t.Fatalf("retry 1: %v", err)
	}
	if sup.IsCircuitBroken(id) {
		t.Fatalf("breaker tripped early after 1 retry")
	}
	if _, err := sup.Retry(id); err != nil {
		t.Fatalf("retry 2: %v", err)
	}
	if sup.IsCircuitBroken(id) {
		t.Fatalf("breaker tripped early after 2 retries")
	}

	// Call 3 exceeds MaxR=2 → breaker trip.
	if _, err := sup.Retry(id); !errors.Is(err, agent.ErrRestartIntensityExceeded) {
		t.Fatalf("retry 3: err = %v, want ErrRestartIntensityExceeded", err)
	}
	if !sup.IsCircuitBroken(id) {
		t.Fatalf("expected IsCircuitBroken(%q) = true after breaker trip", id)
	}
}

// TestRetry_WindowResets verifies the sliding-window behavior: once
// MaxT has elapsed since the oldest attempt, that attempt no longer
// counts and the agent can retry again without tripping the breaker.
//
// We can't easily fake time inside the supervisor today (no clock
// injection); instead we use MaxT=20ms and sleep a bit.
func TestRetry_WindowResets(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()

	sup.SetRestartIntensity(2, 20*time.Millisecond)

	const id = "agent-fast"
	if _, err := sup.Retry(id); err != nil {
		t.Fatalf("retry 1: %v", err)
	}
	if _, err := sup.Retry(id); err != nil {
		t.Fatalf("retry 2: %v", err)
	}
	// Wait past MaxT — the two prior retries should age out.
	time.Sleep(40 * time.Millisecond)
	// Now we should be able to retry twice more without tripping.
	if _, err := sup.Retry(id); err != nil {
		t.Fatalf("retry 3 (post window): %v", err)
	}
	if _, err := sup.Retry(id); err != nil {
		t.Fatalf("retry 4 (post window): %v", err)
	}
	if sup.IsCircuitBroken(id) {
		t.Fatalf("breaker tripped even though window reset")
	}
}

// TestSpawn_RefusedWhenParentCircuitBroken verifies the Spawn-side
// breaker check: if the parent has tripped the breaker, a new Spawn
// under it is refused with ErrRestartIntensityExceeded rather than
// silently going through.
func TestSpawn_RefusedWhenParentCircuitBroken(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)
	ctx := context.Background()

	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()

	// Spawn a real root so the breaker target is a known agent_id.
	root, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "root"})
	if err != nil {
		t.Fatalf("spawn root: %v", err)
	}

	// Force the breaker to trip on the root.
	sup.SetRestartIntensity(0, time.Second)
	if _, err := sup.Retry(root.ID); !errors.Is(err, agent.ErrRestartIntensityExceeded) {
		t.Fatalf("force trip: err = %v, want ErrRestartIntensityExceeded", err)
	}
	if !sup.IsCircuitBroken(root.ID) {
		t.Fatalf("expected breaker tripped on root")
	}

	// Bump depth cap so the spawn would otherwise succeed.
	sup.SetMaxSpawnDepth(5)

	// New spawn under the broken root must be refused.
	if _, _, err := sup.Spawn(ctx, root.ID, agent.SpawnContract{Objective: "child of broken"}); !errors.Is(err, agent.ErrRestartIntensityExceeded) {
		t.Fatalf("spawn-under-broken: err = %v, want ErrRestartIntensityExceeded", err)
	}
}
