// Package fake provides a deterministic in-memory gateway.Adapter used
// by broker tests and by downstream packages (approvals router,
// daemon-side integration) to exercise the gateway loop end-to-end
// without a real platform behind it.
//
// The fake records every outbound Send for later inspection and
// exposes Push to inject inbound envelopes; it is safe for concurrent
// use.
package fake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
)

// Adapter is the in-memory fake. The zero value is unusable; construct
// with New.
type Adapter struct {
	name    gateway.Source
	caps    gateway.OutboundCapabilities
	failFor map[string]error // envelope.ID → return error on Send
	delay   time.Duration

	mu        sync.Mutex
	sent      []gateway.OutboundEnvelope
	receipts  []gateway.DeliveryReceipt
	ingest    gateway.IngestFunc
	started   bool
	stopped   bool
	stopCh    chan struct{}
	startedCh chan struct{}
}

// Option configures a fake adapter at construction.
type Option func(*Adapter)

// WithCapabilities overrides the default fully-capable OutboundCapabilities.
// Tests that want to model a partial channel (e.g. ntfy-style with
// MaxActions=3) pass a custom struct.
func WithCapabilities(c gateway.OutboundCapabilities) Option {
	return func(a *Adapter) { a.caps = c }
}

// WithSendDelay introduces a deterministic wait inside Send. Useful for
// stress-testing concurrency without hitting real network jitter.
func WithSendDelay(d time.Duration) Option {
	return func(a *Adapter) { a.delay = d }
}

// New builds a fake adapter under the given Source name. Default
// OutboundCapabilities accept every outbound kind (Push + FixedChoiceHITL +
// FreeFormTextInbound + MaxActions=3). Override via WithCapabilities.
func New(name gateway.Source, opts ...Option) *Adapter {
	if !name.Valid() {
		// We allow SourceFake here even though real callers should
		// pick SourceFake for clarity; misuse is loud.
		name = gateway.SourceFake
	}
	a := &Adapter{
		name: name,
		caps: gateway.OutboundCapabilities{
			Push:                true,
			FixedChoiceHITL:     true,
			MaxActions:          3,
			FreeFormTextInbound: true,
			FileImageInbound:    true,
			DiffRichApproval:    true,
		},
		failFor:   map[string]error{},
		stopCh:    make(chan struct{}),
		startedCh: make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Name returns the Source assigned at construction.
func (a *Adapter) Name() gateway.Source { return a.name }

// OutboundCapabilities returns the configured OutboundCapabilities struct.
func (a *Adapter) OutboundCapabilities() gateway.OutboundCapabilities { return a.caps }

// SetFailure tells the adapter to return err from the next Send whose
// envelope.ID matches. Useful for testing the broker's retry loop
// without coordinating with a real platform.
func (a *Adapter) SetFailure(envelopeID string, err error) {
	a.mu.Lock()
	a.failFor[envelopeID] = err
	a.mu.Unlock()
}

// Send records env in the sent log and returns either an explicit
// failure (set via SetFailure) or a Delivered receipt with a synthetic
// ProviderRef.
func (a *Adapter) Send(ctx context.Context, env gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	if a.delay > 0 {
		select {
		case <-ctx.Done():
			return gateway.DeliveryReceipt{Source: a.name, Status: gateway.StatusFailed, Error: ctx.Err().Error()}, ctx.Err()
		case <-time.After(a.delay):
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, env)
	if err, ok := a.failFor[env.ID]; ok {
		delete(a.failFor, env.ID)
		r := gateway.DeliveryReceipt{Source: a.name, Status: gateway.StatusFailed, Error: err.Error()}
		a.receipts = append(a.receipts, r)
		return r, err
	}
	r := gateway.DeliveryReceipt{
		Source:      a.name,
		ProviderRef: fmt.Sprintf("%s-%d", a.name, len(a.sent)),
		Status:      gateway.StatusDelivered,
		DeliveredAt: time.Now().UTC(),
	}
	a.receipts = append(a.receipts, r)
	return r, nil
}

// Start records the broker-supplied IngestFunc and blocks until ctx is
// cancelled OR Stop is called. The adapter exposes Started to let tests
// wait for the Start goroutine to settle before injecting Push events.
func (a *Adapter) Start(ctx context.Context, ingest gateway.IngestFunc) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return errors.New("fake: already started")
	}
	a.started = true
	a.ingest = ingest
	a.mu.Unlock()
	select {
	case a.startedCh <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stopCh:
		return nil
	}
}

// Stop signals the Start goroutine to return. Idempotent.
func (a *Adapter) Stop(ctx context.Context) error {
	a.mu.Lock()
	if !a.stopped {
		a.stopped = true
		close(a.stopCh)
	}
	a.mu.Unlock()
	_ = ctx
	return nil
}

// Started returns a channel that fires once Start has captured the
// IngestFunc. Useful for tests that need to wait for the adapter to be
// ready before pushing inbound envelopes.
func (a *Adapter) Started() <-chan struct{} { return a.startedCh }

// Push injects env as if the platform had delivered it. Returns the
// broker's IngestFunc error verbatim (caller may want to assert on
// dedupe drops). Returns an error if Start has not yet been called.
func (a *Adapter) Push(ctx context.Context, env gateway.InboundEnvelope) error {
	a.mu.Lock()
	ingest := a.ingest
	a.mu.Unlock()
	if ingest == nil {
		return errors.New("fake: push before Start")
	}
	if env.Source == "" {
		env.Source = a.name
	}
	return ingest(ctx, env)
}

// Sent returns a snapshot of every OutboundEnvelope the adapter has
// seen via Send, in chronological order.
func (a *Adapter) Sent() []gateway.OutboundEnvelope {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]gateway.OutboundEnvelope, len(a.sent))
	copy(out, a.sent)
	return out
}

// Receipts returns a snapshot of every DeliveryReceipt the adapter has
// produced from Send, in chronological order.
func (a *Adapter) Receipts() []gateway.DeliveryReceipt {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]gateway.DeliveryReceipt, len(a.receipts))
	copy(out, a.receipts)
	return out
}

// Reset clears the recorded sent + receipts. Useful between subtests.
func (a *Adapter) Reset() {
	a.mu.Lock()
	a.sent = nil
	a.receipts = nil
	a.failFor = map[string]error{}
	a.mu.Unlock()
}
