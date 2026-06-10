// Package approvals bridges the agent.ApprovalQueue to the gateway
// Broker. It's the load-bearing piece of G4 - the bit that turns a
// pending approval (Phase 4 contract) into an outbound envelope on
// whatever channels the user configured, and turns the Decision that
// comes back into an Accept / Reject on the same queue the TUI manages.
//
// # Shape
//
// The Router runs one polling loop:
//
//  1. Every PollInterval, query agent.ListPendingApprovals.
//  2. For each pending approval we haven't already sent for, build an
//     OutboundApprovalRequest envelope and hand it to broker.Send. The
//     broker fans it out to every channel in routing.approvals.
//  3. Subscribe to broker.SubscribeDecision(artifactID) in a goroutine;
//     when the first Decision lands, translate it into AcceptApproval
//     or RejectApproval on the event log.
//
// # Why polling instead of event-log subscribe
//
// The agent event log's Subscribe is keyed per agentID, but pending
// approvals span every sub-agent in the system. v0 polls because
// implementing "subscribe to type X across all agents" is a separate
// projection-shape change the rest of the broker doesn't need. Cadence
// is generous (30s default) - the user's phone notification is the
// thing they actually wait on, not the database tick.
//
// # First-write-wins
//
// We don't coordinate decisions ourselves. The broker's gate
// guarantees only one Decision per ArtifactID resolves to subscribers
// (subsequent decisions still land as audit rows). The Router just
// subscribes and acts on whatever fires.
//
// # Revise semantics (Phase 4 only knows Approve / Reject)
//
// The Phase-4 approval queue is binary. The HITL primitive we expose
// to channels is three-way (approve / revise / reject) because that's
// what users actually need. We map DecisionRevise → RejectApproval
// with the revision text glued into the reason ("user requested
// revision: <text>"). Post-G6 work would surface Revise as its own
// state so the producing agent can re-attempt; until that lands, the
// reject-with-context shape preserves the user intent in the event log
// and keeps the projection schema unchanged.
//
// # Restart behavior
//
// "Already sent" is tracked in-memory. On Router restart that map is
// empty, so the first poll re-sends every still-pending approval. The
// user sees a duplicate notification; the broker's inbound dedupe by
// (Source, GatewayEventID) keeps the decision side clean and the gate
// fires only once per ArtifactID anyway. This is the right tradeoff
// for a single-user daemon - durable bookkeeping for "did we send"
// would just be a slower way to re-prompt at the cost of a migration
// we don't need yet.
package approvals

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/gateway"
)

const (
	defaultPollInterval = 30 * time.Second
	defaultTitleMaxLen  = 80
)

// Config wires the Router to its dependencies. Log and Broker are
// required; everything else has sane defaults.
type Config struct {
	// Log is the SQLite event log the agent.Approval API reads/writes.
	// Required.
	Log *agent.SQLiteEventLog

	// Broker is the gateway broker the Router calls Send on and
	// subscribes for Decisions against. Required.
	Broker *gateway.Broker

	// PollInterval controls how often the Router scans for new pending
	// approvals. Default 30s. Tests may pass milliseconds to drive the
	// loop hot.
	PollInterval time.Duration

	// TitleMaxLen caps the outbound envelope Title length so platforms
	// with short headline limits (ntfy especially) don't truncate mid-
	// glyph. Default 80. Values <= 0 fall back to the default.
	TitleMaxLen int

	// BodyTemplate renders the outbound envelope Body for a pending
	// approval. Optional; defaults to a one-line summary that mentions
	// the artifact ID + path. Override when callers want richer copy
	// (e.g. embedding a diff snippet).
	BodyTemplate func(p agent.PendingApproval) string

	// Now is the clock for any time-stamping the Router does. Optional;
	// defaults to time.Now.
	Now func() time.Time

	// Logger receives Error-level records when the Router fails to
	// persist a decision to the event log. Optional; defaults to
	// slog.Default(). A failed AcceptApproval / RejectApproval call
	// means the user's tap was acknowledged on the channel but never
	// landed in the queue — the producing agent stays blocked, so we
	// want operators to see the failure even though the silent-success
	// path stays quiet.
	Logger *slog.Logger
}

// Router polls the approval queue and bridges decisions through the
// gateway broker.
//
// Construct with New, run with Run. Run blocks until its ctx cancels.
type Router struct {
	log          *agent.SQLiteEventLog
	broker       *gateway.Broker
	pollInterval time.Duration
	titleMaxLen  int
	bodyTemplate func(agent.PendingApproval) string
	now          func() time.Time
	logger       *slog.Logger

	// sent is the in-memory dedupe set keyed by ArtifactID. Entries are
	// added by dispatch (one envelope per pending) and removed by gc
	// when the artifact disappears from the pending list - which
	// happens when ANY surface (TUI click, scheduled auto-approve, or
	// the gateway router itself) resolved the approval. Without the
	// GC, a TUI-side accept leaves the watcher dangling forever.
	//
	// Invariant: r.watchers[id] exists ⇒ r.sent[id] exists. dispatch
	// installs the watcher entry under r.mu before releasing the lock
	// to call broker.Send, so a concurrent gc can never observe
	// "sent without watcher" and silently drop sent[id] while the
	// envelope is still in flight.
	mu       sync.Mutex
	sent     map[string]struct{}
	watchers map[string]context.CancelFunc
}

// New validates cfg and constructs a Router. Returns an error if Log
// or Broker is nil.
func New(cfg Config) (*Router, error) {
	if cfg.Log == nil {
		return nil, errors.New("approvals: Log is required")
	}
	if cfg.Broker == nil {
		return nil, errors.New("approvals: Broker is required")
	}
	r := &Router{
		log:          cfg.Log,
		broker:       cfg.Broker,
		pollInterval: cfg.PollInterval,
		titleMaxLen:  cfg.TitleMaxLen,
		bodyTemplate: cfg.BodyTemplate,
		now:          cfg.Now,
		logger:       cfg.Logger,
		sent:         map[string]struct{}{},
		watchers:     map[string]context.CancelFunc{},
	}
	if r.pollInterval <= 0 {
		r.pollInterval = defaultPollInterval
	}
	if r.titleMaxLen <= 0 {
		r.titleMaxLen = defaultTitleMaxLen
	}
	if r.bodyTemplate == nil {
		r.bodyTemplate = defaultBodyTemplate
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.logger == nil {
		r.logger = slog.Default()
	}
	return r, nil
}

// defaultBodyTemplate is the fallback Body renderer used when the
// caller doesn't supply one. Keeps the line short enough to read on a
// phone notification without aggressive truncation.
func defaultBodyTemplate(p agent.PendingApproval) string {
	if p.Ref.Path != "" {
		return fmt.Sprintf("Artifact %s ready for review at %s", p.Ref.ID, p.Ref.Path)
	}
	return fmt.Sprintf("Artifact %s ready for review", p.Ref.ID)
}

// Run blocks until ctx cancels, polling the approval queue on the
// configured interval. Returns nil on clean shutdown; any error
// returned represents an unrecoverable broker/event-log fault (today,
// nothing in the loop is fatal - the loop logs and continues - so
// this returns only ctx.Err()).
//
// In-flight decision watchers are cancelled when ctx cancels; their
// goroutines exit cleanly before Run returns.
func (r *Router) Run(ctx context.Context) error {
	// runCtx scopes every watcher goroutine; cancelling it on exit
	// unblocks any watcher still parked on its SubscribeDecision
	// channel.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	defer wg.Wait()

	// Tick once immediately so the first pending approval doesn't wait
	// a full interval to be picked up.
	r.poll(runCtx, &wg)

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.poll(runCtx, &wg)
		}
	}
}

// poll runs one scan of the approval queue. Each new pending approval
// gets a Send + a watcher goroutine; artifacts that have left the
// pending list since last tick get their watchers GC'd. Errors are
// logged-but-not-fatal; the next tick gets another shot.
func (r *Router) poll(ctx context.Context, wg *sync.WaitGroup) {
	pending, err := agent.ListPendingApprovals(ctx, r.log)
	if err != nil {
		// Transient DB error (e.g. ctx cancelled mid-query). The next
		// tick will retry. We don't have a structured logger here, so
		// we silently drop.
		return
	}
	stillPending := make(map[string]struct{}, len(pending))
	for _, p := range pending {
		stillPending[p.Ref.ID] = struct{}{}
	}
	r.gc(stillPending)
	for _, p := range pending {
		if r.markSent(p.Ref.ID) {
			continue
		}
		// dispatch fires the Send + spawns the decision watcher. We do
		// it inline so the marker stays consistent - if Send errors at
		// the envelope-validation layer we want to surface that
		// immediately rather than retrying every tick forever.
		r.dispatch(ctx, wg, p)
	}
}

// gc cancels any watchers whose artifact has left the pending list
// (resolved out-of-band, typically by a TUI click) and clears their
// sent-set entries. Without this the router would leak one goroutine +
// one broker subscriber per TUI-resolved approval for the lifetime of
// the daemon process. Cheap O(watchers); typical N is a handful.
func (r *Router) gc(stillPending map[string]struct{}) {
	r.mu.Lock()
	var dead []string
	for id, cancel := range r.watchers {
		if _, ok := stillPending[id]; ok {
			continue
		}
		dead = append(dead, id)
		// Cancel under the lock so a concurrent Run-exit doesn't race
		// the unsubscribe path.
		cancel()
	}
	for _, id := range dead {
		delete(r.watchers, id)
		delete(r.sent, id)
	}
	r.mu.Unlock()
}

// markSent atomically records artifactID as in-flight. Returns true if
// the artifact was already in the set (caller should skip), false if
// this is the first time we've seen it (caller should dispatch).
func (r *Router) markSent(artifactID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sent[artifactID]; ok {
		return true
	}
	r.sent[artifactID] = struct{}{}
	return false
}

// dispatch builds the outbound envelope, hands it to broker.Send, and
// launches a watcher goroutine for the matching decision. If
// broker.Send fails synchronously (envelope validation), the watcher
// is not started - there's no point waiting for a decision the user
// will never see - and we leave the artifact in the sent set so we
// don't loop on a malformed envelope every tick.
//
// Ordering note: the watcher entry is installed in r.watchers BEFORE
// broker.Send runs. broker.Send is IO-bound and can block for the full
// retry budget; if we installed the watcher only after Send returned,
// a poll tick firing during Send + an out-of-band resolution would let
// gc walk an empty watchers map (no entry yet for this id), miss the
// pairing, and corrupt the "watcher exists iff sent exists" invariant
// the next poll relies on. Installing under r.mu keeps gc and dispatch
// in lockstep.
func (r *Router) dispatch(ctx context.Context, wg *sync.WaitGroup, p agent.PendingApproval) {
	env := gateway.OutboundEnvelope{
		Kind:       gateway.OutboundApprovalRequest,
		Title:      truncateRunes(p.Title, r.titleMaxLen),
		Body:       truncateRunes(r.bodyTemplate(p), 4*r.titleMaxLen),
		ArtifactID: p.Ref.ID,
		Actions:    gateway.CanonicalActions(),
		Urgency:    gateway.UrgencyHigh,
		AgentID:    p.AgentID,
	}
	// SubscribeDecision before Send so a Decision that lands between
	// the broker writing the outbound row and us subscribing still
	// fires (Subscribe pre-seeds on already-resolved gates).
	decCh, unsub := r.broker.SubscribeDecision(p.Ref.ID)

	// Per-watcher cancel lets gc cull a watcher whose artifact was
	// resolved out-of-band (TUI click). Derived from ctx so a Run-
	// exit still tears everything down.
	watchCtx, cancel := context.WithCancel(ctx)

	// Install the watcher entry under r.mu BEFORE Send, so gc never
	// observes "sent[id] present, watchers[id] missing" for a dispatch
	// whose envelope is still in flight. The sent[id] check inside the
	// lock is idempotent against a parallel gc that already culled this
	// artifact (e.g. it resolved out-of-band between markSent and now).
	r.mu.Lock()
	if _, stillSent := r.sent[p.Ref.ID]; !stillSent {
		r.mu.Unlock()
		// gc has already decided this artifact is done. Drop the
		// subscription + the un-started watchCtx and bail; a future
		// poll won't see a pending row either, so there's nothing
		// to do here.
		cancel()
		unsub()
		return
	}
	r.watchers[p.Ref.ID] = cancel
	r.mu.Unlock()

	if _, err := r.broker.Send(ctx, env); err != nil {
		// Validation or mint failure. Drop the watcher; we'll never
		// see a decision because the user never saw the envelope.
		// Pull the early-installed entry back out so gc doesn't see
		// a watcher for a dispatch that never ran.
		r.mu.Lock()
		delete(r.watchers, p.Ref.ID)
		r.mu.Unlock()
		cancel()
		unsub()
		return
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer unsub()
		defer cancel()
		r.waitForDecision(watchCtx, p.Ref.ID, decCh)
		// We don't self-clean watcher / sent map entries here - the
		// next poll's gc handles it once the resolved artifact drops
		// out of ListPendingApprovals. Leaving a stale cancel in the
		// map between Decision-landed and next-gc costs one entry
		// (the cancel is harmless to call twice).
	}()
}

// waitForDecision blocks until ctx cancels or a Decision arrives on
// decCh. On Decision, translates to the matching agent.Approval API
// call.
//
// Event-log errors are NOT retried here - the broker already deduped
// the decision and any retry would just be against the same local
// SQLite that just failed. But the failure mode matters: the user has
// already tapped Approve / Reject on their phone and the channel has
// acknowledged it; if we drop the write here silently, the producing
// agent stays blocked on a pending approval that will never resolve
// and operators have no trace. We log at Error so the failure is
// visible; the silent-success path (happy case) stays quiet.
//
// We deliberately do NOT delete r.sent[artifactID] on a failed write.
// That would let the next poll re-fire the envelope, re-prompt the
// user, and likely fail the same way against the same degraded
// SQLite. Leaving the state alone keeps the failure visible in logs
// without amplifying it. (Possible follow-up: surface a manual retry
// path once we have a structured ops-action API.)
func (r *Router) waitForDecision(ctx context.Context, artifactID string, decCh <-chan gateway.Decision) {
	var d gateway.Decision
	select {
	case <-ctx.Done():
		return
	case got, ok := <-decCh:
		if !ok {
			// Channel closed without a value - shouldn't happen with
			// the current broker, but it's not worth panicking over.
			return
		}
		d = got
	}

	// Resolve. The agent.Approval API uses a synthetic "user" agent
	// for resolution events; we don't pass an agentID through here.
	switch d.Kind {
	case gateway.DecisionApprove:
		if _, err := agent.AcceptApproval(ctx, r.log, artifactID, d.Revision); err != nil {
			r.logger.Error("approvals: accept failed",
				"artifact_id", artifactID,
				"decision", string(d.Kind),
				"err", err,
			)
		}
	case gateway.DecisionReject:
		reason := d.Revision
		if reason == "" {
			reason = "user rejected"
		}
		if _, err := agent.RejectApproval(ctx, r.log, artifactID, reason); err != nil {
			r.logger.Error("approvals: reject failed",
				"artifact_id", artifactID,
				"decision", string(d.Kind),
				"err", err,
			)
		}
	case gateway.DecisionRevise:
		// Phase 4 has no Revise state; map to Reject with annotated
		// reason so the producing agent has signal to act on. Post-G6
		// work surfaces Revise as a first-class state.
		reason := "user requested revision"
		if d.Revision != "" {
			reason = "user requested revision: " + d.Revision
		}
		if _, err := agent.RejectApproval(ctx, r.log, artifactID, reason); err != nil {
			r.logger.Error("approvals: revise-to-reject failed",
				"artifact_id", artifactID,
				"decision", string(d.Kind),
				"err", err,
			)
		}
	}
}

// truncateRunes returns s clipped to at most max runes, appending an
// ellipsis when truncation actually trimmed something. max <= 0
// returns s unchanged so callers can disable truncation by passing 0
// (though Config.TitleMaxLen normalizes that to the default before we
// see it).
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	// Count runes without allocating until we know we need to trim.
	count := 0
	for range s {
		count++
		if count > max {
			break
		}
	}
	if count <= max {
		return s
	}
	// Walk again to find the byte index of the (max-1)'th rune so we
	// leave room for the ellipsis.
	if max == 1 {
		return "…"
	}
	keep := max - 1
	i := 0
	idx := 0
	for byteIdx := range s {
		if i == keep {
			idx = byteIdx
			break
		}
		i++
	}
	return s[:idx] + "…"
}
