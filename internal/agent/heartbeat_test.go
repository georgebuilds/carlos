package agent_test

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// fakeClock is a deterministic test clock. Now() returns the virtual
// wall-clock; After returns a channel that fires when Advance() pushes
// the virtual clock past the requested duration. Multiple After()
// callers race; the implementation buffers all pending fires until
// Advance crosses their threshold.
//
// Concurrency: After() must be safe to call from the goroutine under
// test while Advance() is called from the test goroutine. We guard
// with a mutex and a slice of pending timers.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*pendingTimer
}

type pendingTimer struct {
	fireAt time.Time
	ch     chan time.Time
	fired  bool
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start.UTC()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &pendingTimer{
		fireAt: c.now.Add(d),
		ch:     make(chan time.Time, 1),
	}
	c.pending = append(c.pending, t)
	// Race: if d <= 0 fire immediately so the consumer doesn't deadlock.
	if !t.fired && !c.now.Before(t.fireAt) {
		t.ch <- c.now
		t.fired = true
	}
	return t.ch
}

// Pending returns the number of timers that have been registered via
// After() and not yet fired. Tests use this to synchronize with a
// goroutine: wait until Pending() > 0 before calling Advance, so the
// Advance actually crosses a registered fireAt.
func (c *fakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.pending {
		if !t.fired {
			n++
		}
	}
	return n
}

// Advance moves virtual time forward by d. Fires any pending timers
// whose fireAt is <= the new now. Returns after all such timers have
// been sent.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	pending := c.pending
	c.mu.Unlock()
	for _, t := range pending {
		if t.fired {
			continue
		}
		if !now.Before(t.fireAt) {
			// Non-blocking send (channel cap 1, so first send always
			// succeeds for an unfired timer).
			select {
			case t.ch <- now:
				t.fired = true
			default:
			}
		}
	}
}

// waitFor polls cond up to budget; returns true if cond ever returned
// true. Used to bridge the "the ticker goroutine has observed the fake
// clock's tick" handoff without an arbitrary real sleep.
func waitFor(budget time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// countEventsOfType returns the number of events of the given type for
// agentID in the log.
func countEventsOfType(t *testing.T, ctx context.Context, log *agent.SQLiteEventLog, agentID string, typ agent.EventType) int {
	t.Helper()
	evs, err := log.Read(ctx, agentID, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// TestHeartbeatTicker_AppendsEventAndUpdatesProjection drives one
// per-agent ticker with a fake clock: start, advance past one
// interval, assert one EvtHeartbeat appended + last_heartbeat_at
// bumped. Stop, advance more, assert no further events.
func TestHeartbeatTicker_AppendsEventAndUpdatesProjection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	t0 := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(t0)
	seedAgent(t, ctx, log, "alpha", "a", agent.StateRunning, t0)

	hb := agent.NewHeartbeatTicker(log, clk, agent.HeartbeatInterval)
	hb.Start(ctx, "alpha")

	// Wait for the ticker goroutine to register its first After() call.
	if !waitFor(time.Second, func() bool { return clk.Pending() >= 1 }) {
		t.Fatalf("ticker did not register After() within budget")
	}
	// Advance one interval. Expect one heartbeat event + cache update.
	clk.Advance(agent.HeartbeatInterval)
	if !waitFor(time.Second, func() bool {
		return countEventsOfType(t, ctx, log, "alpha", agent.EvtHeartbeat) >= 1
	}) {
		t.Fatalf("heartbeat event not appended after one interval; got %d", countEventsOfType(t, ctx, log, "alpha", agent.EvtHeartbeat))
	}
	row, ok, err := log.GetAgent(ctx, "alpha")
	if err != nil || !ok {
		t.Fatalf("get alpha: ok=%v err=%v", ok, err)
	}
	if !row.LastHeartbeatAt.After(t0) {
		t.Fatalf("LastHeartbeatAt = %v, want > %v", row.LastHeartbeatAt, t0)
	}

	// Wait for the ticker to register its second After() before
	// advancing again (otherwise the timer for the next interval may
	// not yet exist).
	if !waitFor(time.Second, func() bool { return clk.Pending() >= 1 }) {
		t.Fatalf("ticker did not register second After() within budget")
	}
	// Advance another interval. Expect a second heartbeat.
	clk.Advance(agent.HeartbeatInterval)
	if !waitFor(time.Second, func() bool {
		return countEventsOfType(t, ctx, log, "alpha", agent.EvtHeartbeat) >= 2
	}) {
		t.Fatalf("second heartbeat not appended; got %d", countEventsOfType(t, ctx, log, "alpha", agent.EvtHeartbeat))
	}

	// Stop the ticker. Advance more virtual time. Assert no further
	// heartbeats. (Stop waits for the goroutine to drain.)
	hb.Stop("alpha")
	before := countEventsOfType(t, ctx, log, "alpha", agent.EvtHeartbeat)
	clk.Advance(10 * agent.HeartbeatInterval)
	time.Sleep(50 * time.Millisecond) // give any stray goroutine a window to misbehave
	after := countEventsOfType(t, ctx, log, "alpha", agent.EvtHeartbeat)
	if after != before {
		t.Fatalf("heartbeats fired after Stop: before=%d after=%d", before, after)
	}
	if hb.Active() != 0 {
		t.Fatalf("Active after Stop = %d, want 0", hb.Active())
	}
}

// TestHeartbeatTicker_StopIdempotent verifies double-Stop is safe.
func TestHeartbeatTicker_StopIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	ctx := context.Background()
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	clk := newFakeClock(time.Now().UTC())
	seedAgent(t, ctx, log, "alpha", "a", agent.StateRunning, clk.Now())

	hb := agent.NewHeartbeatTicker(log, clk, agent.HeartbeatInterval)
	hb.Start(ctx, "alpha")
	hb.Stop("alpha")
	hb.Stop("alpha") // must not panic
	hb.Stop("never-started")
}

// TestHeartbeatTicker_StopsOnTerminalState verifies that when an
// agent's projection row is in a terminal state, the ticker exits
// itself without emitting another heartbeat.
func TestHeartbeatTicker_StopsOnTerminalState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	ctx := context.Background()
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	clk := newFakeClock(time.Now().UTC())
	seedAgent(t, ctx, log, "alpha", "a", agent.StateRunning, clk.Now())

	hb := agent.NewHeartbeatTicker(log, clk, agent.HeartbeatInterval)
	hb.Start(ctx, "alpha")

	// Move agent to a terminal state externally (simulates "agent finished").
	if err := log.UpdateAgentState(ctx, "alpha", agent.StateDone, clk.Now()); err != nil {
		t.Fatalf("update state: %v", err)
	}

	clk.Advance(agent.HeartbeatInterval)
	// Allow ticker to observe terminal state and exit. Active() may
	// still return 1 momentarily; we just assert no NEW heartbeats.
	time.Sleep(50 * time.Millisecond)
	clk.Advance(agent.HeartbeatInterval)
	time.Sleep(50 * time.Millisecond)

	n := countEventsOfType(t, ctx, log, "alpha", agent.EvtHeartbeat)
	if n > 0 {
		t.Fatalf("heartbeat fired after terminal state; got %d", n)
	}
	hb.StopAll()
}

// TestOrphanSweeper_OnlyOrphansStaleNonTerminal seeds 3 agents:
//   - fresh heartbeat  (running, hb just now)            -> untouched
//   - stale heartbeat  (running, hb 5 min ago)           -> orphaned
//   - terminal agent   (done,    hb 5 min ago)           -> untouched
//
// Runs ONE sweep cycle. Asserts only the stale-non-terminal one
// transitioned.
func TestOrphanSweeper_OnlyOrphansStaleNonTerminal(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	ctx := context.Background()
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(now)

	seedAgent(t, ctx, log, "fresh", "f", agent.StateRunning, now.Add(-1*time.Second))
	seedAgent(t, ctx, log, "stale", "s", agent.StateRunning, now.Add(-5*time.Minute))
	seedAgent(t, ctx, log, "terminal", "t", agent.StateDone, now.Add(-5*time.Minute))

	var orphaned []string
	sw := agent.NewOrphanSweeper(log, clk, agent.SweepInterval, agent.StalenessTolerance)
	sw.OnOrphan = func(id string) { orphaned = append(orphaned, id) }

	if err := sw.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if !equal(orphaned, []string{"stale"}) {
		t.Fatalf("OnOrphan calls = %v, want [stale]", orphaned)
	}
	// Verify projection.
	row, ok, err := log.GetAgent(ctx, "stale")
	if err != nil || !ok {
		t.Fatalf("get stale: ok=%v err=%v", ok, err)
	}
	if row.State != agent.StateOrphaned {
		t.Fatalf("stale state = %v, want orphaned", row.State)
	}
	row, _, _ = log.GetAgent(ctx, "fresh")
	if row.State != agent.StateRunning {
		t.Fatalf("fresh state = %v, want running", row.State)
	}
	row, _, _ = log.GetAgent(ctx, "terminal")
	if row.State != agent.StateDone {
		t.Fatalf("terminal state = %v, want done", row.State)
	}

	// Verify event landed in events table (SoT replay sees orphan).
	proj, err := agent.Replay(ctx, log, "stale")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	r, _ := proj.Get("stale")
	if r.State != agent.StateOrphaned {
		t.Fatalf("replayed stale state = %v, want orphaned", r.State)
	}
}

// TestOrphanSweeper_StartStopIdempotent verifies the long-running
// goroutine wiring: Start, Stop, repeat without panic.
func TestOrphanSweeper_StartStopIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	clk := newFakeClock(time.Now().UTC())
	sw := agent.NewOrphanSweeper(log, clk, agent.SweepInterval, agent.StalenessTolerance)

	ctx, cancel := context.WithCancel(context.Background())
	sw.Start(ctx)
	sw.Start(ctx) // no-op
	cancel()
	sw.Stop() // also stops via Stop()
	sw.Stop() // idempotent
}

// TestSupervisorIntegration_HeartbeatAndSweep is the wire-it-all-up
// test: open db, NewSupervisor, Spawn (heartbeat ticker starts), do
// NOT advance the clock past any heartbeat (so no heartbeat fires),
// advance past the stale threshold, run one Sweep, assert the spawned
// agent transitioned to orphaned.
//
// We use a fakeClock + invoke Sweep() directly rather than relying on
// the live sweep goroutine cadence, to keep the assertion deterministic.
func TestSupervisorIntegration_HeartbeatAndSweep(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer agent.CloseStateDB(log)

	// Construct the supervisor with the production constructor — this
	// exercises NewSupervisor's wiring of heartbeat + sweeper fields.
	// We don't call Run because that uses RealClock for the loop; we
	// instead drive Sweep manually on a fakeClock for determinism.
	//
	// Slice 3a: Spawn now actually runs a child loop, so we pass a
	// fake provider that hangs forever (no scripted events) — that
	// keeps the child in `running` long enough for the orphan sweep
	// to transition it to `orphaned`.
	sup := agent.NewSupervisor(log, newHangingProvider(), nil)
	defer sup.Shutdown()

	sub, _, err := sup.Spawn(ctx, "", agent.SpawnContract{Objective: "do the thing"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if sub.ID == "" {
		t.Fatalf("spawn returned empty ID")
	}

	// Confirm the projection row landed. The worker goroutine will
	// have transitioned us to `running` almost immediately; poll the
	// projection cache briefly to ride past the race.
	var row agent.AgentRow
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, ok, err := log.GetAgent(ctx, sub.ID)
		if err != nil || !ok {
			t.Fatalf("get spawned: ok=%v err=%v", ok, err)
		}
		row = r
		if row.State == agent.StateRunning || row.State == agent.StateSpawning {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if row.State != agent.StateRunning && row.State != agent.StateSpawning {
		t.Fatalf("spawned state = %v, want spawning or running", row.State)
	}

	// Build a fake-clock sweeper that thinks "now" is well past the
	// staleness tolerance, and run one sweep. The agent's
	// last_heartbeat_at was set at real Now() inside Spawn, so we just
	// pretend a long time has passed.
	clk := newFakeClock(time.Now().UTC().Add(10 * time.Minute))
	sw := agent.NewOrphanSweeper(log, clk, agent.SweepInterval, agent.StalenessTolerance)
	var orphaned []string
	sw.OnOrphan = func(id string) { orphaned = append(orphaned, id) }
	if err := sw.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !equal(orphaned, []string{sub.ID}) {
		t.Fatalf("OnOrphan = %v, want [%s]", orphaned, sub.ID)
	}
	row, _, _ = log.GetAgent(ctx, sub.ID)
	if row.State != agent.StateOrphaned {
		t.Fatalf("post-sweep state = %v, want orphaned", row.State)
	}
}

// TestSupervisor_NilLogSpawnRejected verifies the defensive check in
// Spawn when constructed without a log (no liveness possible).
func TestSupervisor_NilLogSpawnRejected(t *testing.T) {
	sup := agent.NewSupervisor(nil, nil, nil)
	if _, _, err := sup.Spawn(context.Background(), "", agent.SpawnContract{}); err == nil {
		t.Fatalf("expected error from Spawn with nil log")
	}
}

// TestOrphanSweeper_RunGoroutineLogsSweepErrors pins fix #5: the
// per-process sweep goroutine now pipes Sweep errors through slog
// instead of silently swallowing them. We trigger an error by
// closing the underlying *sql.DB before the sweep fires (StaleAgents
// fails on a closed connection), capture stderr-bound slog output
// in a bytes.Buffer, and assert the run loop logged the failure.
//
// Synchronisation is channel-based: the fakeClock's After() fires
// when Advance() crosses the registered duration, so we know the
// sweep goroutine has registered its timer before we trigger the
// error. We then poll the buffer with a deadline (no fixed sleep).
func TestOrphanSweeper_RunGoroutineLogsSweepErrors(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	clk := newFakeClock(time.Now().UTC())
	sw := agent.NewOrphanSweeper(log, clk, agent.SweepInterval, agent.StalenessTolerance)

	var buf bytes.Buffer
	// Mutex on the buffer because slog's text handler may emit
	// concurrently with the test goroutine's Read - the race
	// detector would otherwise complain on a plain Buffer.
	var mu sync.Mutex
	syncedW := &syncedBuffer{buf: &buf, mu: &mu}
	handler := slog.NewTextHandler(syncedW, &slog.HandlerOptions{Level: slog.LevelDebug})
	sw.SetLogger(slog.New(handler))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sw.Start(ctx)
	defer sw.Stop()

	// Wait for the run goroutine to register its After() before we
	// close the DB; otherwise we might race the goroutine's first
	// loop entry.
	if !waitFor(time.Second, func() bool { return clk.Pending() >= 1 }) {
		t.Fatalf("sweep goroutine did not register After() within budget")
	}

	// Close the DB so the next Sweep returns an error from
	// StaleAgents (sqlite "database is closed").
	if err := agent.CloseStateDB(log); err != nil {
		t.Fatalf("close db: %v", err)
	}

	// Advance virtual time so the sweep fires.
	clk.Advance(agent.SweepInterval)

	// Wait for the log line. waitFor polls the buffer until it
	// observes the expected error attribute or the budget elapses.
	got := waitFor(2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(buf.String(), "orphan sweep failed")
	})
	if !got {
		mu.Lock()
		dump := buf.String()
		mu.Unlock()
		t.Fatalf("expected slog Error log for sweep failure, captured: %q", dump)
	}
	// Tighter check: the log line is at Error level.
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("sweep error log should be at ERROR level, got: %q", out)
	}
}

// syncedBuffer wraps a bytes.Buffer with a mutex so concurrent
// goroutine writes + test-side reads stay race-free.
type syncedBuffer struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (b *syncedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
