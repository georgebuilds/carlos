// Slice 1g: live heartbeat + orphan sweep, on top of the 1h lifecycle.
//
// Two collaborating types live here:
//
//   - HeartbeatTicker: one goroutine PER agent. While the target agent is
//     in a non-terminal state, ticks every HeartbeatInterval (5s) and
//     appends an EvtHeartbeat event + updates the projection cache's
//     last_heartbeat_at column. Per-agent because (a) Start/Stop is
//     per-agent, (b) one shared ticker would have to wrangle a roster
//     that mutates under it; a per-agent goroutine has none of that
//     ceremony and is cheap (Go scheduler handles thousands).
//
//   - OrphanSweeper: ONE goroutine per process. Every SweepInterval (10s)
//     scans for non-terminal agents whose last_heartbeat_at is older than
//     2 x HeartbeatInterval. For each, applies Transition(curState,
//     EvHeartbeatLost) -> StateOrphaned and persists. Global because the
//     scan is a single SQL query; spinning up a goroutine per agent for
//     a 10-second poll would be wasteful.
//
// Why 5s heartbeat / 10s sweep / 2x staleness tolerance (design §
// Heartbeat + orphan detection):
//
//   - 5s heartbeat is loud enough to notice in a TUI without flooding
//     the event log: 720 heartbeat events/hour/agent ~= 30 KB/hour of
//     events (well under the WAL throughput we benchmarked).
//   - 10s sweep means worst-case orphan detection latency is ~20s
//     (last heartbeat + 2x interval + sweep cadence) - fast enough that
//     a hung agent doesn't drift for minutes, slow enough that a brief
//     GC pause or a Slow-Disk(tm) event doesn't false-orphan.
//   - 2x interval staleness threshold = one missed heartbeat is fine,
//     two missed in a row is "this agent is gone".
//
// Clock injection: production wires the real clock; tests inject a
// FakeClock that they Advance() manually so timing-sensitive assertions
// don't rely on real wall-clock sleeps.
package agent

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// HeartbeatInterval is how often a live HeartbeatTicker emits an
// EvtHeartbeat event for its target agent.
const HeartbeatInterval = 5 * time.Second

// SweepInterval is how often the OrphanSweeper scans the projection
// cache for stale non-terminal agents.
const SweepInterval = 10 * time.Second

// StalenessTolerance is how old `last_heartbeat_at` must be before the
// OrphanSweeper promotes a non-terminal agent to `orphaned`. Equals 2 x
// HeartbeatInterval per design.
const StalenessTolerance = 2 * HeartbeatInterval

// Clock is the minimal interface the heartbeat/sweep code needs from
// time. Production uses RealClock; tests use FakeClock and Advance the
// virtual time manually.
type Clock interface {
	Now() time.Time
	// After returns a channel that fires once after d virtual time has
	// elapsed. Equivalent to time.After for the real clock; for the fake
	// clock, the channel fires when Advance() pushes past d from the
	// time After was called.
	After(d time.Duration) <-chan time.Time
}

// RealClock is the production Clock backed by package time.
type RealClock struct{}

func (RealClock) Now() time.Time                         { return time.Now().UTC() }
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// HeartbeatTicker owns one goroutine per started agent that emits
// EvtHeartbeat events on a cadence. Stop is idempotent. On ctx.Done(),
// ALL tickers exit.
type HeartbeatTicker struct {
	log      *SQLiteEventLog
	clock    Clock
	interval time.Duration

	mu      sync.Mutex
	tickers map[string]*tickerState // agentID -> running state
}

type tickerState struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewHeartbeatTicker constructs a heartbeat manager that writes to log.
// Pass nil clock to use RealClock; pass 0 interval to use the default.
func NewHeartbeatTicker(log *SQLiteEventLog, clock Clock, interval time.Duration) *HeartbeatTicker {
	if clock == nil {
		clock = RealClock{}
	}
	if interval == 0 {
		interval = HeartbeatInterval
	}
	return &HeartbeatTicker{
		log:      log,
		clock:    clock,
		interval: interval,
		tickers:  map[string]*tickerState{},
	}
}

// Start launches a per-agent goroutine that emits heartbeat events
// every `interval` until the parent ctx is cancelled OR Stop(agentID) is
// called OR the agent reaches a terminal state. Double-Start for the
// same agent is a no-op (the existing ticker keeps running).
//
// The goroutine reads the agent's current state from the projection
// cache before each emit; if the row is missing OR in a terminal state,
// the ticker stops itself (no further events).
func (h *HeartbeatTicker) Start(parentCtx context.Context, agentID string) {
	h.mu.Lock()
	if _, exists := h.tickers[agentID]; exists {
		h.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parentCtx)
	st := &tickerState{cancel: cancel, done: make(chan struct{})}
	h.tickers[agentID] = st
	h.mu.Unlock()

	go h.run(ctx, agentID, st)
}

// run is the per-agent ticker loop.
func (h *HeartbeatTicker) run(ctx context.Context, agentID string, st *tickerState) {
	defer close(st.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.clock.After(h.interval):
			// Re-check state before emitting. If the agent is gone or
			// terminal, stop ourselves and exit.
			row, ok, err := h.log.GetAgent(ctx, agentID)
			if err != nil || !ok || row.State.IsTerminal() {
				return
			}
			ts := h.clock.Now()
			// Append the heartbeat event (SoT).
			ev := Event{
				AgentID: agentID,
				TS:      ts,
				Type:    EvtHeartbeat,
				Payload: []byte(`{}`),
			}
			if _, err := h.log.Append(ctx, ev); err != nil {
				// On append failure (shutting down DB, locked, etc),
				// don't spin: exit the ticker. The sweeper will catch a
				// stale agent on its next cycle.
				return
			}
			// Mirror into the projection cache so the next StaleAgents
			// scan sees the fresh heartbeat. Failures are non-fatal:
			// the event is the source of truth and a Replay would
			// rebuild the cache correctly.
			_ = h.log.UpdateHeartbeat(ctx, agentID, ts)
		}
	}
}

// Stop cancels the per-agent ticker. Idempotent: stopping an agent that
// was never Started or already Stopped is a no-op.
//
// Stop returns after the goroutine has exited (so tests can assert "no
// further events after Stop" deterministically).
func (h *HeartbeatTicker) Stop(agentID string) {
	h.mu.Lock()
	st, ok := h.tickers[agentID]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(h.tickers, agentID)
	h.mu.Unlock()
	st.cancel()
	<-st.done
}

// StopAll cancels every active ticker and waits for them to drain.
// Called on supervisor shutdown.
func (h *HeartbeatTicker) StopAll() {
	h.mu.Lock()
	all := make([]*tickerState, 0, len(h.tickers))
	for id, st := range h.tickers {
		all = append(all, st)
		delete(h.tickers, id)
	}
	h.mu.Unlock()
	for _, st := range all {
		st.cancel()
	}
	for _, st := range all {
		<-st.done
	}
}

// Active returns the count of currently-running per-agent tickers.
// Useful for tests / introspection; not used by production code paths.
func (h *HeartbeatTicker) Active() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.tickers)
}

// OnOrphanFunc is invoked once per agent the sweeper transitions to
// orphaned. The callback runs synchronously on the sweep goroutine;
// keep it fast or hand off to a worker.
type OnOrphanFunc func(agentID string)

// OrphanSweeper is the single per-process goroutine that polls for stale
// non-terminal agents and transitions them to `orphaned`.
type OrphanSweeper struct {
	log       *SQLiteEventLog
	clock     Clock
	interval  time.Duration
	tolerance time.Duration

	mu       sync.Mutex
	started  bool
	stopOnce sync.Once
	cancel   context.CancelFunc
	done     chan struct{}

	OnOrphan OnOrphanFunc // optional callback; nil = silent
}

// NewOrphanSweeper constructs a sweeper that polls log every `interval`
// and orphans non-terminal agents whose heartbeat is older than
// `tolerance`. Pass 0 for either to use the defaults.
func NewOrphanSweeper(log *SQLiteEventLog, clock Clock, interval, tolerance time.Duration) *OrphanSweeper {
	if clock == nil {
		clock = RealClock{}
	}
	if interval == 0 {
		interval = SweepInterval
	}
	if tolerance == 0 {
		tolerance = StalenessTolerance
	}
	return &OrphanSweeper{
		log:       log,
		clock:     clock,
		interval:  interval,
		tolerance: tolerance,
	}
}

// Start launches the sweep goroutine. Calling Start a second time is a
// no-op (the existing goroutine keeps running). The goroutine exits on
// ctx.Done() OR Stop().
func (s *OrphanSweeper) Start(parentCtx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(parentCtx)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.mu.Unlock()

	go s.run(ctx)
}

// Stop cancels the sweep goroutine and waits for it to drain. Idempotent.
func (s *OrphanSweeper) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		cancel := s.cancel
		done := s.done
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
	})
}

// Sweep runs exactly ONE sweep cycle synchronously. Exposed so tests
// can deterministically trigger a sweep without juggling the goroutine
// loop. Production code uses Start; Sweep is for tests + manual flushes.
func (s *OrphanSweeper) Sweep(ctx context.Context) error {
	now := s.clock.Now()
	threshold := now.Add(-s.tolerance)
	staleIDs, err := s.log.StaleAgents(ctx, threshold)
	if err != nil {
		return fmt.Errorf("sweeper: stale scan: %w", err)
	}
	for _, id := range staleIDs {
		row, ok, err := s.log.GetAgent(ctx, id)
		if err != nil {
			return fmt.Errorf("sweeper: get agent %s: %w", id, err)
		}
		if !ok || row.State.IsTerminal() {
			// Race: between scan and now, agent finished. Skip.
			continue
		}
		// Apply the state-machine transition. EvHeartbeatLost is legal
		// from every non-terminal state per state.go's universal
		// dispatch, so this always succeeds for our filter.
		next, err := Transition(row.State, EvHeartbeatLost)
		if err != nil {
			// Defensive: if we ever broaden the state set such that
			// some non-terminal state rejects EvHeartbeatLost, surface
			// it rather than silently skip.
			return fmt.Errorf("sweeper: transition %s from %s: %w", id, row.State, err)
		}
		payload, err := NewStateChangeTransition(next)
		if err != nil {
			return fmt.Errorf("sweeper: marshal transition for %s: %w", id, err)
		}
		ev := Event{
			AgentID: id,
			TS:      now,
			Type:    EvtStateChange,
			Payload: payload,
		}
		if _, err := s.log.Append(ctx, ev); err != nil {
			return fmt.Errorf("sweeper: append orphan event for %s: %w", id, err)
		}
		if err := s.log.UpdateAgentState(ctx, id, next, now); err != nil {
			return fmt.Errorf("sweeper: update orphan row for %s: %w", id, err)
		}
		if s.OnOrphan != nil {
			s.OnOrphan(id)
		}
	}
	return nil
}

func (s *OrphanSweeper) run(ctx context.Context) {
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(s.interval):
			if err := s.Sweep(ctx); err != nil {
				// Production: nowhere to log yet (no logger pkg).
				// Best-effort continue; next sweep will retry.
				_ = err
			}
		}
	}
}
