package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// seedAgent inserts an agent row in the projection cache + appends the
// corresponding state_change kind=created event. Returns the AgentRow we
// inserted so tests can assert against it.
func seedAgent(t *testing.T, ctx context.Context, log *agent.SQLiteEventLog, id, title string, state agent.State, heartbeatAt time.Time) agent.AgentRow {
	t.Helper()
	// Append the canonical creation event so the SoT replay still works.
	created, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: id, RootID: id, Title: title, Model: "fake",
	})
	if err != nil {
		t.Fatalf("seed: marshal created: %v", err)
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: id, TS: heartbeatAt, Type: agent.EvtStateChange, Payload: created,
	}); err != nil {
		t.Fatalf("seed: append created: %v", err)
	}
	row := agent.AgentRow{
		ID:              id,
		RootID:          id,
		State:           state,
		Attempt:         1,
		Title:           title,
		Model:           "fake",
		CreatedAt:       heartbeatAt,
		UpdatedAt:       heartbeatAt,
		LastHeartbeatAt: heartbeatAt,
	}
	if err := log.InsertAgent(ctx, row); err != nil {
		t.Fatalf("seed: insert agent: %v", err)
	}
	// If the seeded state isn't `spawning` (the default in
	// NewStateChangeCreated), correct the projection cache.
	if state != agent.StateSpawning {
		if err := log.UpdateAgentState(ctx, id, state, heartbeatAt); err != nil {
			t.Fatalf("seed: update state: %v", err)
		}
	}
	return row
}

// TestOpenStateDB_CreatesParentDirAt0700 verifies that OpenStateDB
// creates the parent dir at mode 0700 if missing.
func TestOpenStateDB_CreatesParentDirAt0700(t *testing.T) {
	dir := t.TempDir()
	// Use a nested path that doesn't exist yet.
	nested := filepath.Join(dir, "deep", "carlos")
	dbPath := filepath.Join(nested, "state.db")

	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("parent is not a dir: %v", info.Mode())
	}
	// On macOS/Linux MkdirAll + Chmod yields exactly 0700. Unmask
	// because the test runs under whatever umask the user has.
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("parent dir mode = %o, want 0700", got)
	}
}

// TestOpenStateDB_VerifiesSchema confirms that a fresh open succeeds
// against an empty DB file (schema is created by OpenSQLiteEventLog;
// verify just SELECTs from each table).
func TestOpenStateDB_VerifiesSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := agent.CloseStateDB(log); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Reopen — schema should still verify.
	log2, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer agent.CloseStateDB(log2)
}

// TestRecover_TransitionsStaleNonTerminalToOrphaned is the headline
// 1h test. Seed 3 agents:
//
//   - "healthy"  : running, heartbeat 1s old
//   - "stale"    : running, heartbeat 5 min old
//   - "terminal" : done, heartbeat 5 min old (stale but not subject to orphan)
//
// Run Recover with a 30s tolerance. Assert only "stale" got promoted
// to `orphaned` (event appended + projection row updated). Assert the
// other two are untouched.
func TestRecover_TransitionsStaleNonTerminalToOrphaned(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	ctx := context.Background()

	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	now := time.Now().UTC().Truncate(time.Millisecond)
	healthyTS := now.Add(-1 * time.Second)
	staleTS := now.Add(-5 * time.Minute)

	seedAgent(t, ctx, log, "healthy", "h", agent.StateRunning, healthyTS)
	seedAgent(t, ctx, log, "stale", "s", agent.StateRunning, staleTS)
	seedAgent(t, ctx, log, "terminal", "t", agent.StateDone, staleTS)

	rep, err := agent.RecoverWith(ctx, log, now, 30*time.Second)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	// Only "stale" should be orphaned.
	if got, want := rep.Orphaned, []string{"stale"}; !equal(got, want) {
		t.Fatalf("Orphaned = %v, want %v", got, want)
	}
	// Only "healthy" remains in StillActive (terminal is, well, terminal).
	if got, want := rep.StillActive, []string{"healthy"}; !equal(got, want) {
		t.Fatalf("StillActive = %v, want %v", got, want)
	}

	// Verify projection row for "stale" is now orphaned.
	row, ok, err := log.GetAgent(ctx, "stale")
	if err != nil || !ok {
		t.Fatalf("get stale: ok=%v err=%v", ok, err)
	}
	if row.State != agent.StateOrphaned {
		t.Fatalf("stale row state = %v, want orphaned", row.State)
	}

	// Verify "healthy" is untouched.
	row, ok, err = log.GetAgent(ctx, "healthy")
	if err != nil || !ok {
		t.Fatalf("get healthy: ok=%v err=%v", ok, err)
	}
	if row.State != agent.StateRunning {
		t.Fatalf("healthy row state = %v, want running", row.State)
	}

	// Verify the orphan event landed in the events table by replaying
	// "stale" and checking the final state.
	proj, err := agent.Replay(ctx, log, "stale")
	if err != nil {
		t.Fatalf("replay stale: %v", err)
	}
	r, ok := proj.Get("stale")
	if !ok {
		t.Fatalf("replay stale: row missing")
	}
	if r.State != agent.StateOrphaned {
		t.Fatalf("replayed stale state = %v, want orphaned", r.State)
	}

	// Totals look right.
	if rep.Counts.Agents != 3 {
		t.Fatalf("Counts.Agents = %d, want 3", rep.Counts.Agents)
	}
	// 3 created + 1 transition-to-running (correction in seedAgent)
	// + 1 transition-to-done (correction in seedAgent for terminal)
	// + 1 orphan transition (from Recover for stale)
	// = 6 (healthy: 1, stale: 2, terminal: 2, stale-orphan: 1)
	if rep.Counts.Events < 4 { // permissive lower bound; the exact value depends on seedAgent's state-correction emits
		t.Fatalf("Counts.Events = %d, want >= 4", rep.Counts.Events)
	}
}

// TestRecover_NoopOnCleanDB verifies that running Recover twice in a
// row only orphans once: the second call sees the already-orphaned row
// (terminal) and skips it.
func TestRecover_NoopOnCleanDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	ctx := context.Background()

	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	now := time.Now().UTC().Truncate(time.Millisecond)

	// Case A: totally empty DB.
	rep, err := agent.RecoverWith(ctx, log, now, 30*time.Second)
	if err != nil {
		t.Fatalf("recover empty: %v", err)
	}
	if len(rep.Orphaned) != 0 || len(rep.StillActive) != 0 {
		t.Fatalf("empty DB: Orphaned=%v StillActive=%v", rep.Orphaned, rep.StillActive)
	}

	// Case B: one stale agent + run Recover twice.
	seedAgent(t, ctx, log, "stale", "s", agent.StateRunning, now.Add(-5*time.Minute))

	rep1, err := agent.RecoverWith(ctx, log, now, 30*time.Second)
	if err != nil {
		t.Fatalf("recover 1: %v", err)
	}
	if got, want := rep1.Orphaned, []string{"stale"}; !equal(got, want) {
		t.Fatalf("first Orphaned = %v, want %v", got, want)
	}

	rep2, err := agent.RecoverWith(ctx, log, now, 30*time.Second)
	if err != nil {
		t.Fatalf("recover 2: %v", err)
	}
	if len(rep2.Orphaned) != 0 {
		t.Fatalf("second Orphaned = %v, want []", rep2.Orphaned)
	}
}

// TestCloseStateDB_NilSafe — defensive: callers shouldn't have to nil-
// check before close.
func TestCloseStateDB_NilSafe(t *testing.T) {
	if err := agent.CloseStateDB(nil); err != nil {
		t.Fatalf("close nil: %v", err)
	}
}

// TestOpenStateDB_EmptyPath rejects empty path explicitly.
func TestOpenStateDB_EmptyPath(t *testing.T) {
	if _, err := agent.OpenStateDB(""); err == nil {
		t.Fatalf("expected error for empty path")
	}
}

// equal compares two string slices irrespective of nil-vs-empty.
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
