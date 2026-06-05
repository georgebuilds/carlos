package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Broker composes adapters into a single send/receive surface for
// carlos. The contract:
//
//   - Send(env) fans out to every adapter in routing[env.Kind], retries
//     each per RetryConfig, persists EvtGatewayOutbound rows before AND
//     after each attempt (status flips Unknown → Delivered / Failed),
//     and returns the per-channel receipts.
//   - Ingest(env) is the IngestFunc adapters call. It dedupes on
//     (Source, GatewayEventID), validates, persists EvtGatewayInbound,
//     and (for Decision envelopes) routes through the per-ArtifactID
//     serialization gate so first-write-wins is enforced.
//   - DecisionsFor lets callers (the approvals router) subscribe to
//     the in-process stream of accepted Decision envelopes for a given
//     ArtifactID, with the "first-wins" guarantee held by the gate.
//
// The broker does NOT own the event-log connection — it borrows the
// *SQLiteEventLog from the daemon. Multiple brokers in the same
// process is not a supported config; the daemon constructs one.
type Broker struct {
	log     *agent.SQLiteEventLog
	routing RoutingConfig
	retry   RetryConfig
	clock   func() time.Time
	sleep   func(context.Context, time.Duration) error

	mu       sync.RWMutex
	adapters map[Source]Adapter
	started  bool
	stopped  bool

	// decisions serializes Decision inbounds per ArtifactID. Each gate
	// holds the winning Decision + a broadcast channel for late
	// subscribers; subsequent decisions for the same artifact get
	// persisted as audit rows but do not flip the gate.
	gateMu sync.Mutex
	gates  map[string]*decisionGate

	// subs are in-process subscribers for decision events, keyed by
	// ArtifactID. Used by the approvals router; never blocks Ingest.
	subMu sync.Mutex
	subs  map[string][]chan Decision
}

// decisionGate is the synchronization primitive for "first decision
// wins" per ArtifactID. The first Ingest call that lands a Decision for
// an artifact captures the Decision; later decisions read the
// already-resolved gate and call adapter.Send back via the broker so
// the user is told "already decided" (caller responsibility — broker
// just exposes Resolved + Decision).
type decisionGate struct {
	once     sync.Once
	resolved bool
	decision Decision
	source   Source
	at       time.Time
}

// Options configures a new Broker. All fields except Log have sane
// defaults.
type Options struct {
	// Log is the SQLite event log. Required.
	Log *agent.SQLiteEventLog
	// Routing maps OutboundKind → ordered channel list. Optional; an
	// empty RoutingConfig means "no fan-out" until the daemon hands one
	// in via SetRouting.
	Routing RoutingConfig
	// Retry controls send-attempt cadence. Optional; defaults applied
	// by Normalize().
	Retry RetryConfig
	// Now is the clock the broker reads. Optional; defaults to
	// time.Now. Tests pass a fake clock to make backoff deterministic.
	Now func() time.Time
	// Sleep is the cancellable wait between retries. Optional; defaults
	// to a select-on-ctx-Done implementation. Tests pass a fake to
	// avoid real waits.
	Sleep func(ctx context.Context, d time.Duration) error
}

// New constructs a Broker with the given options. Returns an error if
// Log is nil or RetryConfig is structurally invalid.
//
// New also runs the broker's idempotency-table migration against the
// shared SQLite database. Idempotent.
func New(opts Options) (*Broker, error) {
	if opts.Log == nil {
		return nil, errors.New("broker: Log is required")
	}
	if err := opts.Retry.Validate(); err != nil && opts.Retry != (RetryConfig{}) {
		return nil, fmt.Errorf("broker: retry: %w", err)
	}

	if err := migrateDedupe(context.Background(), brokerDB(opts.Log)); err != nil {
		return nil, err
	}

	b := &Broker{
		log:      opts.Log,
		routing:  opts.Routing,
		retry:    opts.Retry.Normalize(),
		adapters: map[Source]Adapter{},
		gates:    map[string]*decisionGate{},
		subs:     map[string][]chan Decision{},
		clock:    opts.Now,
		sleep:    opts.Sleep,
	}
	if b.clock == nil {
		b.clock = time.Now
	}
	if b.sleep == nil {
		b.sleep = ctxSleep
	}
	return b, nil
}

// ctxSleep waits for d or for ctx to cancel, whichever comes first.
func ctxSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Register adds an adapter to the broker. Returns an error if an
// adapter for the same Source is already registered, or if a is nil.
// Adapters may be Registered after Start; the broker calls Start on
// late registrants automatically (using a context derived from the
// running broker's context).
func (b *Broker) Register(a Adapter) error {
	if a == nil {
		return errors.New("broker: nil adapter")
	}
	name := a.Name()
	if !name.Valid() {
		return fmt.Errorf("broker: adapter reports invalid source %q", name)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, dup := b.adapters[name]; dup {
		return fmt.Errorf("broker: adapter for %s already registered", name)
	}
	b.adapters[name] = a
	return nil
}

// Adapters returns a snapshot of currently-registered Source keys, in
// insertion-stable order driven by source string sort. Useful for
// tests + the manage view.
func (b *Broker) Adapters() []Source {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Source, 0, len(b.adapters))
	for s := range b.adapters {
		out = append(out, s)
	}
	sortSources(out)
	return out
}

// SetRouting replaces the routing config. Useful for hot config reload
// (daemon SIGHUP). Concurrent with Send: a send already in flight uses
// the snapshot it captured at call time.
func (b *Broker) SetRouting(r RoutingConfig) {
	b.mu.Lock()
	b.routing = r
	b.mu.Unlock()
}

// Routing returns a copy of the current routing config.
func (b *Broker) Routing() RoutingConfig {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.routing
}

// Start launches every registered adapter's inbound machinery. The
// provided ctx is propagated to each adapter; cancelling ctx triggers
// shutdown. Start blocks until ctx is cancelled OR an adapter's Start
// returns an error; the first error short-circuits and the broker
// invokes Stop on every other adapter.
//
// Late Register calls after Start return without auto-starting today;
// the daemon is expected to Register everything before Start.
func (b *Broker) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return errors.New("broker: already started")
	}
	if b.stopped {
		b.mu.Unlock()
		return errors.New("broker: already stopped")
	}
	b.started = true
	snap := make([]Adapter, 0, len(b.adapters))
	for _, a := range b.adapters {
		snap = append(snap, a)
	}
	b.mu.Unlock()

	if len(snap) == 0 {
		// No adapters: just block on ctx so the daemon's main loop can
		// still depend on Start returning at shutdown.
		<-ctx.Done()
		return ctx.Err()
	}

	ingest := func(c context.Context, env InboundEnvelope) error {
		return b.Ingest(c, env)
	}

	errCh := make(chan error, len(snap))
	var wg sync.WaitGroup
	for _, a := range snap {
		a := a
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.Start(ctx, ingest); err != nil && !errors.Is(err, context.Canceled) {
				errCh <- fmt.Errorf("adapter %s: %w", a.Name(), err)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	var firstErr error
	for err := range errCh {
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// Stop signals every registered adapter to shut down. Idempotent.
func (b *Broker) Stop(ctx context.Context) error {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return nil
	}
	b.stopped = true
	snap := make([]Adapter, 0, len(b.adapters))
	for _, a := range b.adapters {
		snap = append(snap, a)
	}
	b.mu.Unlock()
	var firstErr error
	for _, a := range snap {
		if err := a.Stop(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("adapter %s: %w", a.Name(), err)
		}
	}
	return firstErr
}

// Send fans envelope out to every channel in
// routing[env.Kind] that has a registered adapter. Each channel runs
// the full retry loop independently; per-attempt rows land in the
// event log.
//
// Returns the receipts in the same order as the routing list (skipping
// channels with no registered adapter). An error is returned only if
// the envelope itself fails Validate — partial send failures surface as
// receipts with StatusFailed.
func (b *Broker) Send(ctx context.Context, env OutboundEnvelope) ([]DeliveryReceipt, error) {
	if err := env.Validate(); err != nil {
		return nil, err
	}
	now := b.clock().UTC().Truncate(time.Millisecond)
	if env.ID == "" {
		id, err := newEnvelopeID(now)
		if err != nil {
			return nil, fmt.Errorf("broker: mint envelope id: %w", err)
		}
		env.ID = id
	}
	if env.CreatedAt.IsZero() {
		env.CreatedAt = now
	}

	b.mu.RLock()
	channels := append([]Source(nil), b.routing.ChannelsFor(env.Kind)...)
	adapters := make(map[Source]Adapter, len(b.adapters))
	for k, v := range b.adapters {
		adapters[k] = v
	}
	retry := b.retry
	b.mu.RUnlock()

	var receipts []DeliveryReceipt
	for _, ch := range channels {
		a, ok := adapters[ch]
		if !ok {
			// Routing references a channel with no adapter — log a
			// failed receipt so the manage view can show the gap, but
			// don't fail the send.
			receipts = append(receipts, DeliveryReceipt{
				Source: ch,
				Status: StatusFailed,
				Error:  "no adapter registered",
			})
			continue
		}
		caps := a.Capabilities()
		if !caps.SupportsKind(env.Kind) {
			receipts = append(receipts, DeliveryReceipt{
				Source: ch,
				Status: StatusFailed,
				Error:  fmt.Sprintf("adapter does not support kind %q", env.Kind),
			})
			continue
		}
		// Per-channel envelope copy — Actions may need truncation.
		chEnv := env
		if caps.MaxActions > 0 && len(chEnv.Actions) > caps.MaxActions {
			chEnv.Actions = append([]Action(nil), chEnv.Actions[:caps.MaxActions]...)
		}
		receipt := b.sendOne(ctx, a, chEnv, retry)
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

// sendOne runs the retry loop for a single (adapter, envelope) pair.
// Persists one EvtGatewayOutbound row per attempt; the final row carries
// the terminal receipt.
func (b *Broker) sendOne(ctx context.Context, a Adapter, env OutboundEnvelope, retry RetryConfig) DeliveryReceipt {
	var last DeliveryReceipt
	for attempt := 1; attempt <= retry.MaxAttempts; attempt++ {
		// Wait before retry (no wait on attempt 1).
		if attempt > 1 {
			if err := b.sleep(ctx, retry.backoffFor(attempt-1)); err != nil {
				return DeliveryReceipt{
					Source: a.Name(),
					Status: StatusFailed,
					Error:  fmt.Sprintf("ctx cancelled before attempt %d: %v", attempt, err),
				}
			}
		}

		// Pre-attempt row — "we tried, status unknown" per spec.
		preReceipt := DeliveryReceipt{Source: a.Name(), Status: StatusUnknown}
		_, _ = appendOutbound(ctx, b.log, OutboundPayload{
			Channel:    a.Name(),
			EnvelopeID: env.ID,
			ArtifactID: env.ArtifactID,
			Envelope:   env,
			Receipt:    preReceipt,
			Attempt:    attempt,
		}, b.clock())

		receipt, err := a.Send(ctx, env)
		if err == nil && receipt.Source == "" {
			receipt.Source = a.Name()
		}
		if err != nil {
			receipt = DeliveryReceipt{
				Source: a.Name(),
				Status: StatusFailed,
				Error:  err.Error(),
			}
		}

		// Post-attempt row — terminal status for this attempt.
		_, _ = appendOutbound(ctx, b.log, OutboundPayload{
			Channel:    a.Name(),
			EnvelopeID: env.ID,
			ArtifactID: env.ArtifactID,
			Envelope:   env,
			Receipt:    receipt,
			Attempt:    attempt,
		}, b.clock())

		last = receipt
		if receipt.Status == StatusDelivered || receipt.Status == StatusUnknown {
			// Unknown is "we tried, the platform didn't say no" —
			// don't retry fire-and-forget channels. The spec is
			// explicit about this for ntfy.
			return receipt
		}
		if ctx.Err() != nil {
			return last
		}
	}
	return last
}

// Ingest is the IngestFunc the broker hands to adapters. Persists a
// deduped EvtGatewayInbound row and (for Decision envelopes) runs the
// first-write-wins gate per ArtifactID.
//
// Returns nil on dedupe hit (caller's already-seen event is a no-op,
// not a failure). Returns an error on validation failures or DB errors;
// adapters surface the error to their underlying platform so the next
// retry picks up the event.
func (b *Broker) Ingest(ctx context.Context, env InboundEnvelope) error {
	now := b.clock().UTC().Truncate(time.Millisecond)
	if env.ID == "" {
		id, err := newEnvelopeID(now)
		if err != nil {
			return fmt.Errorf("broker: mint inbound id: %w", err)
		}
		env.ID = id
	}
	if env.ReceivedAt.IsZero() {
		env.ReceivedAt = now
	}
	if err := env.Validate(); err != nil {
		return err
	}

	claimed, err := claimIngest(ctx, brokerDB(b.log), env.Source, env.GatewayEventID, env.ID, env.ReceivedAt.UnixMilli())
	if err != nil {
		return err
	}
	if !claimed {
		// Duplicate — silent drop.
		return nil
	}

	if _, err := appendInbound(ctx, b.log, InboundPayload{
		Channel:    env.Source,
		EnvelopeID: env.ID,
		ArtifactID: env.ArtifactID,
		Envelope:   env,
	}, env.ReceivedAt); err != nil {
		return err
	}

	if env.Kind == InboundDecision && env.ArtifactID != "" && env.Decision != nil {
		b.recordDecision(env.ArtifactID, *env.Decision, env.Source, env.ReceivedAt)
	}
	return nil
}

// recordDecision drives the first-write-wins gate + fan-out to in-
// process subscribers. The audit row landed already (in Ingest); this
// only flips the gate AND notifies subscribers.
func (b *Broker) recordDecision(artifactID string, d Decision, src Source, at time.Time) {
	b.gateMu.Lock()
	g, ok := b.gates[artifactID]
	if !ok {
		g = &decisionGate{}
		b.gates[artifactID] = g
	}
	winner := false
	g.once.Do(func() {
		g.resolved = true
		g.decision = d
		g.source = src
		g.at = at
		winner = true
	})
	b.gateMu.Unlock()

	if !winner {
		return
	}

	// Fan out to subscribers; non-blocking.
	b.subMu.Lock()
	chans := b.subs[artifactID]
	delete(b.subs, artifactID)
	b.subMu.Unlock()
	for _, c := range chans {
		select {
		case c <- d:
		default:
		}
		close(c)
	}
}

// DecisionFor returns the resolved Decision for artifactID, if any. The
// second return reports whether the gate has fired (false → still
// pending or no decision ever recorded).
func (b *Broker) DecisionFor(artifactID string) (Decision, Source, time.Time, bool) {
	b.gateMu.Lock()
	defer b.gateMu.Unlock()
	g, ok := b.gates[artifactID]
	if !ok || !g.resolved {
		return Decision{}, "", time.Time{}, false
	}
	return g.decision, g.source, g.at, true
}

// SubscribeDecision returns a channel that receives the winning Decision
// for artifactID exactly once. If the decision has already landed, the
// channel is pre-seeded and closed before returning. The unsubscribe
// func is safe to call after the channel fires; the caller MUST call
// it to free the slot if they choose not to wait.
//
// Used by the approvals router to bridge gateway Decisions to
// agent.Approval state without polling.
func (b *Broker) SubscribeDecision(artifactID string) (<-chan Decision, func()) {
	ch := make(chan Decision, 1)
	// Already resolved? Pre-seed.
	if d, _, _, ok := b.DecisionFor(artifactID); ok {
		ch <- d
		close(ch)
		return ch, func() {}
	}
	b.subMu.Lock()
	b.subs[artifactID] = append(b.subs[artifactID], ch)
	b.subMu.Unlock()
	unsub := func() {
		b.subMu.Lock()
		defer b.subMu.Unlock()
		list := b.subs[artifactID]
		for i, c := range list {
			if c == ch {
				b.subs[artifactID] = append(list[:i], list[i+1:]...)
				if len(b.subs[artifactID]) == 0 {
					delete(b.subs, artifactID)
				}
				return
			}
		}
	}
	return ch, unsub
}

// sortSources is a small helper used by Adapters(). Sorts by string
// form for stable test output.
func sortSources(s []Source) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
