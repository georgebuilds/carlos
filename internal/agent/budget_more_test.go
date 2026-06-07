package agent

// Whitebox tests for budget internals and artifact helpers.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewTrackerWithClock_UsesInjectedClock(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := func() time.Time { return t0 }
	tr := newTrackerWithClock(nil, clk)
	if got := tr.Elapsed(); got != 0 {
		t.Errorf("Elapsed should be 0 with fixed clock, got %v", got)
	}
}

func TestTracker_RemainingTimeAndZeroCap(t *testing.T) {
	calls := 0
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := func() time.Time {
		calls++
		// Advance the clock per call to simulate elapsed time.
		return now.Add(time.Duration(calls) * time.Second)
	}
	tr := newTrackerWithClock(nil, clk)
	tr.Add(0, 0, 0) // no spend

	// 5-second budget, real clock advanced 3 ticks → 2s remaining.
	tok, cost, wall, exceeded := tr.Remaining(Budget{
		MaxTokens: 100, MaxCostCents: 100, MaxWallClock: 5 * time.Second,
	})
	if exceeded {
		t.Errorf("expected not exceeded; got tok=%d cost=%d wall=%v", tok, cost, wall)
	}
	if wall <= 0 || wall > 5*time.Second {
		t.Errorf("wall remaining oddity: %v", wall)
	}
}

func TestTracker_RemainingExceededTime(t *testing.T) {
	calls := 0
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := func() time.Time {
		calls++
		return now.Add(time.Duration(calls) * time.Hour)
	}
	tr := newTrackerWithClock(nil, clk)
	_, _, wall, exceeded := tr.Remaining(Budget{MaxWallClock: time.Second})
	if !exceeded {
		t.Error("expected exceeded for elapsed >> cap")
	}
	if wall != 0 {
		t.Errorf("wall remaining should clamp at 0, got %v", wall)
	}
}

func TestTracker_CheckBudgetUnlimitedShortCircuits(t *testing.T) {
	tr := NewTracker(nil)
	tr.Add(1<<60, 0, 0)
	if err := tr.CheckBudget(Budget{}); err != nil {
		t.Errorf("unlimited budget should never fail: %v", err)
	}
}

func TestTracker_Snapshot_RoundsThroughClock(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := func() time.Time { return t0 }
	tr := newTrackerWithClock(nil, clk)
	tr.Add(10, 20, 5)
	s := tr.Snapshot()
	if s.Tokens != 30 || s.CostCents != 5 || s.Elapsed != 0 {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestEstimateCallCost_FloorsAtOne(t *testing.T) {
	// Anything below 10K should round to 1 cent (floor).
	if c := EstimateCallCost(0, 1); c != 1 {
		t.Errorf("min-body cost should floor at 1, got %d", c)
	}
}

func TestEstimateCallTokens_LinearScale(t *testing.T) {
	// 40 chars + 0 system = 10 tokens.
	if c := EstimateCallTokens(0, 40); c != 10 {
		t.Errorf("got %d want 10", c)
	}
	// systemBytes also count.
	if c := EstimateCallTokens(40, 40); c != 20 {
		t.Errorf("got %d want 20", c)
	}
}

// --- Artifact helpers.

func TestMkdirArtifactBase_EmptyErrors(t *testing.T) {
	if err := MkdirArtifactBase(""); err == nil {
		t.Fatal("empty basePath should error")
	}
}

func TestMkdirArtifactBase_CreatesAt0700(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "artifacts-test")
	if err := MkdirArtifactBase(nested); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("perm = %o want 0700", info.Mode().Perm())
	}
}

func TestArtifactBasePath_RespectsEnvOverride(t *testing.T) {
	custom := "/tmp/carlos-test-artifacts-override-" + time.Now().UTC().Format("150405")
	t.Setenv("CARLOS_ARTIFACT_BASE", custom)
	if got := ArtifactBasePath("/whatever"); got != custom {
		t.Errorf("got %q want %q", got, custom)
	}
}

func TestArtifactBasePath_FallsBackToRelative(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", "")
	t.Setenv("HOME", "")
	// On some CI environments HOME might still be set; we just test
	// that the function returns SOMETHING containing artifacts.
	got := ArtifactBasePath("/fake")
	if !strings.Contains(got, "artifacts") {
		t.Errorf("expected artifacts in path, got %q", got)
	}
}

func TestArtifactBasePath_EmptyHomeUsesOSUserHomeDir(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", "")
	// Passing "" lets the function call os.UserHomeDir; we just verify
	// no panic and a non-empty path.
	got := ArtifactBasePath("")
	if got == "" {
		t.Error("expected non-empty default path")
	}
}

func TestReadArtifact_EmptyBasePathErrors(t *testing.T) {
	if _, err := ReadArtifact("", "deadbeef"); err == nil {
		t.Fatal("empty basePath should error")
	}
}

func TestReadArtifact_EmptyHashErrors(t *testing.T) {
	if _, err := ReadArtifact("/tmp", ""); err == nil {
		t.Fatal("empty sha should error")
	}
}

func TestReadArtifact_MissingFileWrapsErrArtifactNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadArtifact(dir, "nonexistent-hash")
	if err == nil {
		t.Fatal("expected error on missing file")
	}
	if !errors.Is(err, ErrArtifactNotFound) {
		t.Errorf("err = %v want wrapping ErrArtifactNotFound", err)
	}
}

func TestRandomSuffix_ReturnsHex(t *testing.T) {
	s, err := randomSuffix()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(s) != 16 {
		t.Errorf("suffix length = %d want 16", len(s))
	}
}

func TestWriteArtifact_NilLogErrors(t *testing.T) {
	if _, err := WriteArtifact(context.Background(), nil, "a", "kind", []byte("x")); err == nil {
		t.Fatal("nil log should error")
	}
}

func TestWriteArtifact_EmptyAgentIDErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", dir)
	log := openWhiteboxLog(t)
	if _, err := WriteArtifact(context.Background(), log, "", "kind", []byte("x")); err == nil {
		t.Fatal("empty agentID should error")
	}
}

func TestWriteArtifact_EmptyKindErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", dir)
	log := openWhiteboxLog(t)
	if _, err := WriteArtifact(context.Background(), log, "a", "", []byte("x")); err == nil {
		t.Fatal("empty kind should error")
	}
}

func TestWriteArtifact_DeduplicatesContent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(dir, "artifacts"))
	log := openWhiteboxLog(t)
	seedWhiteboxAgent(t, log, "child-1")

	content := []byte("dedup test bytes")
	ref1, err := WriteArtifact(context.Background(), log, "child-1", ArtifactKindText, content)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	ref2, err := WriteArtifact(context.Background(), log, "child-1", ArtifactKindText, content)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if ref1.SHA256 != ref2.SHA256 {
		t.Errorf("sha mismatch: %q vs %q", ref1.SHA256, ref2.SHA256)
	}
	if ref1.Path != ref2.Path {
		t.Errorf("path mismatch: %q vs %q", ref1.Path, ref2.Path)
	}
	if ref1.ID == ref2.ID {
		t.Error("IDs should be distinct ULIDs even on dedupe")
	}
}

func openWhiteboxLog(t *testing.T) *SQLiteEventLog {
	t.Helper()
	dir := t.TempDir()
	log, err := OpenSQLiteEventLog(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func seedWhiteboxAgent(t *testing.T, log *SQLiteEventLog, id string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	created, _ := NewStateChangeCreated(AgentCreated{ID: id, RootID: id, Title: id, Model: "fake"})
	if _, err := log.Append(context.Background(), Event{
		AgentID: id, TS: now, Type: EvtStateChange, Payload: created,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := log.InsertAgent(context.Background(), AgentRow{
		ID: id, RootID: id, State: StateRunning, Attempt: 1,
		Title: id, Model: "fake", CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

// Restart / circuit breaker: cover markCircuitBroken on a brand-new
// retry record (the existing tests only hit it via Retry).
func TestMarkCircuitBroken_FreshAgentWorks(t *testing.T) {
	s := NewSupervisor(nil, nil, nil)
	defer s.Shutdown()
	s.mu.Lock()
	s.markCircuitBroken("brand-new")
	s.mu.Unlock()
	if !s.IsCircuitBroken("brand-new") {
		t.Error("markCircuitBroken on a fresh id should trip the breaker")
	}
	// Double-trip is idempotent.
	s.mu.Lock()
	s.markCircuitBroken("brand-new")
	s.mu.Unlock()
	if !s.IsCircuitBroken("brand-new") {
		t.Error("double markCircuitBroken should remain tripped")
	}
}
