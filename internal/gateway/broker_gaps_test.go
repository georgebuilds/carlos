package gateway_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/gateway/fake"
)

// TestBroker_New_RejectsInvalidRetry exercises the New() guard that
// rejects a structurally-broken (non-zero) RetryConfig. The zero value
// is allowed (defaults applied); a config with MaxAttempts<=0 but other
// fields set is not.
func TestBroker_New_RejectsInvalidRetry(t *testing.T) {
	_, err := gateway.New(gateway.Options{
		Log:   newLog(t),
		Retry: gateway.RetryConfig{MaxAttempts: 0, BackoffInitial: time.Second},
	})
	if err == nil {
		t.Fatal("expected error on invalid (non-zero) retry config")
	}
}

// TestBroker_New_ZeroRetryUsesDefaults confirms the zero-value retry
// config short-circuits the validation guard and Normalize fills in the
// defaults rather than erroring.
func TestBroker_New_ZeroRetryUsesDefaults(t *testing.T) {
	b, err := gateway.New(gateway.Options{Log: newLog(t)})
	if err != nil {
		t.Fatalf("zero retry should be accepted: %v", err)
	}
	if b == nil {
		t.Fatal("nil broker")
	}
}

// TestBroker_Register_RejectsInvalidSource covers the adapter.Name()
// validity gate in Register: an adapter that reports a bogus Source is
// refused before it lands in the map.
func TestBroker_Register_RejectsInvalidSource(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	if err := b.Register(&badNameAdapter{}); err == nil {
		t.Fatal("expected error for adapter reporting an invalid source")
	}
}

// TestBroker_SendTo_UnknownChannel covers the SendTo guard that rejects
// a channel name that is not a known Source.
func TestBroker_SendTo_UnknownChannel(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	_, err := b.SendTo(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "x",
	}, gateway.Source("bogus"))
	if err == nil {
		t.Fatal("expected unknown-channel error")
	}
}

// TestBroker_SendTo_InvalidEnvelope covers the SendTo envelope.Validate
// short-circuit.
func TestBroker_SendTo_InvalidEnvelope(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	_, err := b.SendTo(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, // no title/body
	}, gateway.SourceFake)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

// TestBroker_SendTo_NoAdapter covers the SendTo path where the channel
// is a valid Source but has no adapter registered: returns a failed
// receipt AND a typed error.
func TestBroker_SendTo_NoAdapter(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	receipt, err := b.SendTo(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "x",
	}, gateway.SourceTelegram)
	if err == nil {
		t.Fatal("expected no-adapter error")
	}
	if receipt.Status != gateway.StatusFailed {
		t.Errorf("want failed receipt, got %+v", receipt)
	}
	if receipt.Source != gateway.SourceTelegram {
		t.Errorf("receipt source: want telegram got %v", receipt.Source)
	}
}

// TestBroker_SendTo_UnsupportedKind covers the capability gate in
// SendTo: a registered adapter that does not support the envelope kind
// yields a failed receipt and a nil error (mirrors Send).
func TestBroker_SendTo_UnsupportedKind(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	caps := gateway.OutboundCapabilities{Push: true} // no FreeFormTextInbound
	f := fake.New(gateway.SourceFake, fake.WithCapabilities(caps))
	if err := b.Register(f); err != nil {
		t.Fatal(err)
	}
	receipt, err := b.SendTo(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundConversationReply, Title: "x", Body: "hi",
	}, gateway.SourceFake)
	if err != nil {
		t.Fatalf("unsupported kind should not be a hard error: %v", err)
	}
	if receipt.Status != gateway.StatusFailed {
		t.Errorf("want failed receipt for unsupported kind, got %+v", receipt)
	}
	if len(f.Sent()) != 0 {
		t.Errorf("adapter must not be called for unsupported kind: %d sends", len(f.Sent()))
	}
}

// TestBroker_SendTo_Delivered covers the happy SendTo path: ID +
// CreatedAt are minted, the adapter is invoked, and a delivered receipt
// is returned.
func TestBroker_SendTo_Delivered(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	f := fake.New(gateway.SourceFake)
	if err := b.Register(f); err != nil {
		t.Fatal(err)
	}
	receipt, err := b.SendTo(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "ping",
	}, gateway.SourceFake)
	if err != nil {
		t.Fatalf("send-to: %v", err)
	}
	if receipt.Status != gateway.StatusDelivered {
		t.Errorf("want delivered, got %+v", receipt)
	}
	sent := f.Sent()
	if len(sent) != 1 {
		t.Fatalf("adapter sends: want 1 got %d", len(sent))
	}
	if sent[0].ID == "" {
		t.Error("SendTo did not mint an envelope ID")
	}
	if sent[0].CreatedAt.IsZero() {
		t.Error("SendTo did not stamp CreatedAt")
	}
}

// TestBroker_SendTo_TruncatesActions covers the per-channel action
// truncation branch inside SendTo (caps.MaxActions enforcement).
func TestBroker_SendTo_TruncatesActions(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	caps := gateway.OutboundCapabilities{Push: true, FixedChoiceHITL: true, MaxActions: 2}
	f := fake.New(gateway.SourceFake, fake.WithCapabilities(caps))
	if err := b.Register(f); err != nil {
		t.Fatal(err)
	}
	receipt, err := b.SendTo(context.Background(), gateway.OutboundEnvelope{
		Kind:       gateway.OutboundApprovalRequest,
		Title:      "review",
		Body:       "x",
		ArtifactID: "art-sendto",
		Actions:    gateway.CanonicalActions(),
	}, gateway.SourceFake)
	if err != nil {
		t.Fatalf("send-to: %v", err)
	}
	if receipt.Status != gateway.StatusDelivered {
		t.Fatalf("want delivered, got %+v", receipt)
	}
	if got := f.Sent()[0].Actions; len(got) != 2 {
		t.Errorf("actions not truncated: want 2 got %d", len(got))
	}
}

// TestBroker_SendTo_PreservesCallerID confirms SendTo does not overwrite
// a caller-supplied envelope ID/CreatedAt.
func TestBroker_SendTo_PreservesCallerID(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	f := fake.New(gateway.SourceFake)
	if err := b.Register(f); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := b.SendTo(context.Background(), gateway.OutboundEnvelope{
		ID:        "caller-id",
		CreatedAt: when,
		Kind:      gateway.OutboundNotification,
		Title:     "x",
	}, gateway.SourceFake); err != nil {
		t.Fatal(err)
	}
	sent := f.Sent()[0]
	if sent.ID != "caller-id" {
		t.Errorf("ID overwritten: %q", sent.ID)
	}
	if !sent.CreatedAt.Equal(when) {
		t.Errorf("CreatedAt overwritten: %v", sent.CreatedAt)
	}
}

// TestBroker_Start_PropagatesAdapterError covers the firstErr branch in
// Start: a registered adapter whose Start returns a non-context error
// short-circuits and that error is returned to the caller.
func TestBroker_Start_PropagatesAdapterError(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	want := errors.New("adapter exploded")
	if err := b.Register(&errStartAdapter{name: gateway.SourceFake, err: want}); err != nil {
		t.Fatal(err)
	}
	err := b.Start(context.Background())
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("want adapter error propagated, got %v", err)
	}
}

// TestBroker_Start_AfterStopErrors covers the b.stopped guard in Start:
// once stopped, Start refuses to launch.
func TestBroker_Start_AfterStopErrors(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	if err := b.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background()); err == nil {
		t.Fatal("expected error starting an already-stopped broker")
	}
}

// TestBroker_Stop_PropagatesAdapterError covers the firstErr branch in
// Stop: an adapter whose Stop errors surfaces that error.
func TestBroker_Stop_PropagatesAdapterError(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	want := errors.New("stop boom")
	if err := b.Register(&errStopAdapter{name: gateway.SourceFake, err: want}); err != nil {
		t.Fatal(err)
	}
	err := b.Stop(context.Background())
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("want stop error propagated, got %v", err)
	}
}

// TestBroker_Ingest_DecisionWithoutArtifactSkipsGate confirms an inbound
// decision missing an ArtifactID is persisted but never resolves a gate
// (the InboundDecision branch in Ingest requires ArtifactID != "").
//
// Note: InboundEnvelope.Validate requires an ArtifactID for decisions,
// so this branch is reached via the message path with a Decision set.
func TestBroker_Ingest_NonDecisionWithDecisionPayloadIgnored(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	ctx := context.Background()
	// A message-kind inbound that happens to carry a Decision pointer +
	// ArtifactID. Ingest only routes Kind==InboundDecision through the
	// gate, so DecisionFor must stay unresolved.
	env := gateway.InboundEnvelope{
		Source:         gateway.SourceFake,
		GatewayEventID: "msg-with-decision",
		Kind:           gateway.InboundMessage,
		Body:           "hi",
		ArtifactID:     "art-msg",
		Decision:       &gateway.Decision{Kind: gateway.DecisionApprove},
	}
	if err := b.Ingest(ctx, env); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := b.DecisionFor("art-msg"); ok {
		t.Error("message-kind inbound must not resolve a decision gate")
	}
}

// TestBroker_Ingest_DefaultClockStampsReceivedAt exercises the
// ReceivedAt-stamping branch when the caller leaves it zero, using a
// broker with the default clock.
func TestBroker_Ingest_StampsIDAndReceivedAt(t *testing.T) {
	log := newLog(t)
	b := newBroker(t, log, gateway.RoutingConfig{})
	ctx := context.Background()
	// ID and ReceivedAt left zero so Ingest mints/stamps both.
	env := gateway.InboundEnvelope{
		Source:         gateway.SourceFake,
		GatewayEventID: "stamp-1",
		Kind:           gateway.InboundMessage,
		Body:           "hello",
	}
	if err := b.Ingest(ctx, env); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// A second ingest with the same (source, event id) must dedupe even
	// though we never set an ID on the first call - proving the claim
	// was persisted under the broker-minted envelope ID.
	if err := b.Ingest(ctx, env); err != nil {
		t.Fatalf("dup ingest: %v", err)
	}
	events, err := log.Read(ctx, gateway.EventAgentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("want 1 inbound event after dedupe, got %d", len(events))
	}
}

// TestBroker_DefaultSleep_TimerFiresBetweenRetries drives the real
// (default) ctxSleep timer-fired branch. The broker is built WITHOUT a
// Sleep override so the production ctxSleep runs; a short positive
// backoff means attempt 1 fails, ctxSleep arms a timer that elapses
// (the <-t.C branch), and attempt 2 succeeds.
func TestBroker_DefaultSleep_TimerFiresBetweenRetries(t *testing.T) {
	b, err := gateway.New(gateway.Options{
		Log:     newLog(t),
		Routing: gateway.RoutingConfig{Notifications: []gateway.Source{gateway.SourceFake}},
		// Short positive backoff so ctxSleep arms a timer that fires.
		Retry: gateway.RetryConfig{MaxAttempts: 3, BackoffInitial: time.Millisecond, BackoffMax: 2 * time.Millisecond},
		// No Sleep override: exercises the default ctxSleep timer path.
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &counterAdapter{name: gateway.SourceFake, caps: gateway.OutboundCapabilities{Push: true}, failTimes: 1}
	if err := b.Register(f); err != nil {
		t.Fatal(err)
	}
	receipts, err := b.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].Status != gateway.StatusDelivered {
		t.Fatalf("want delivered after a real backoff sleep, got %+v", receipts)
	}
	if f.calls != 2 {
		t.Errorf("want 2 attempts (1 fail + 1 success through ctxSleep), got %d", f.calls)
	}
}

// TestBroker_DefaultSleep_ContextCancelAbortsRetry drives ctxSleep's
// <-ctx.Done() branch. A long positive backoff means attempt 2's
// ctxSleep blocks on the timer; a goroutine cancels the context after
// attempt 1 fails, so ctxSleep returns ctx.Err() and the receipt names
// the cancellation.
func TestBroker_DefaultSleep_ContextCancelAbortsRetry(t *testing.T) {
	b, err := gateway.New(gateway.Options{
		Log:     newLog(t),
		Routing: gateway.RoutingConfig{Notifications: []gateway.Source{gateway.SourceFake}},
		// Long backoff so ctxSleep blocks on its timer until we cancel.
		Retry: gateway.RetryConfig{MaxAttempts: 5, BackoffInitial: time.Hour, BackoffMax: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := newAlwaysFailAdapter(gateway.SourceFake)
	if err := b.Register(f); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// firstAttempt closes inside attempt 1's Send. sendOne then runs
		// its (still-live) ctx.Err() check at the end of attempt 1 -
		// ctx is NOT yet cancelled there - and loops into attempt 2's
		// ctxSleep, which parks on a 1h timer. A short delay guarantees
		// sendOne is inside that select before we cancel, so the
		// <-ctx.Done() branch wins deterministically.
		<-f.firstAttempt
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	receipts, _ := b.Send(ctx, gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "x",
	})
	if len(receipts) != 1 || receipts[0].Status != gateway.StatusFailed {
		t.Fatalf("want failed receipt after cancel during backoff, got %+v", receipts)
	}
	if got := f.attempts(); got != 1 {
		t.Errorf("want exactly 1 send attempt (cancelled in backoff), got %d", got)
	}
}

// --- in-test adapters ---

// badNameAdapter reports a Source that fails Valid(); used to cover the
// Register invalid-source guard.
type badNameAdapter struct{}

func (badNameAdapter) Name() gateway.Source { return gateway.Source("not-a-real-source") }
func (badNameAdapter) OutboundCapabilities() gateway.OutboundCapabilities {
	return gateway.OutboundCapabilities{}
}
func (badNameAdapter) Send(context.Context, gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	return gateway.DeliveryReceipt{}, nil
}
func (badNameAdapter) Start(context.Context, gateway.IngestFunc) error { return nil }
func (badNameAdapter) Stop(context.Context) error                     { return nil }

// errStartAdapter returns a non-context error from Start.
type errStartAdapter struct {
	name gateway.Source
	err  error
}

func (a *errStartAdapter) Name() gateway.Source { return a.name }
func (a *errStartAdapter) OutboundCapabilities() gateway.OutboundCapabilities {
	return gateway.OutboundCapabilities{Push: true}
}
func (a *errStartAdapter) Send(context.Context, gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	return gateway.DeliveryReceipt{Source: a.name, Status: gateway.StatusDelivered}, nil
}
func (a *errStartAdapter) Start(context.Context, gateway.IngestFunc) error { return a.err }
func (a *errStartAdapter) Stop(context.Context) error                     { return nil }

// errStopAdapter returns an error from Stop.
type errStopAdapter struct {
	name gateway.Source
	err  error
}

func (a *errStopAdapter) Name() gateway.Source { return a.name }
func (a *errStopAdapter) OutboundCapabilities() gateway.OutboundCapabilities {
	return gateway.OutboundCapabilities{Push: true}
}
func (a *errStopAdapter) Send(context.Context, gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	return gateway.DeliveryReceipt{Source: a.name, Status: gateway.StatusDelivered}, nil
}
func (a *errStopAdapter) Start(ctx context.Context, _ gateway.IngestFunc) error {
	<-ctx.Done()
	return ctx.Err()
}
func (a *errStopAdapter) Stop(context.Context) error { return a.err }

// alwaysFailAdapter fails every Send and signals the first attempt so
// tests can cancel the surrounding context mid-backoff.
type alwaysFailAdapter struct {
	name         gateway.Source
	mu           sync.Mutex
	calls        int
	firstOnce    sync.Once
	firstAttempt chan struct{}
}

func newAlwaysFailAdapter(name gateway.Source) *alwaysFailAdapter {
	return &alwaysFailAdapter{name: name, firstAttempt: make(chan struct{})}
}

func (a *alwaysFailAdapter) Name() gateway.Source { return a.name }
func (a *alwaysFailAdapter) OutboundCapabilities() gateway.OutboundCapabilities {
	return gateway.OutboundCapabilities{Push: true}
}
func (a *alwaysFailAdapter) Send(context.Context, gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	a.firstOnce.Do(func() { close(a.firstAttempt) })
	return gateway.DeliveryReceipt{Source: a.name, Status: gateway.StatusFailed, Error: "boom"}, errors.New("boom")
}
func (a *alwaysFailAdapter) Start(ctx context.Context, _ gateway.IngestFunc) error {
	<-ctx.Done()
	return ctx.Err()
}
func (a *alwaysFailAdapter) Stop(context.Context) error { return nil }
func (a *alwaysFailAdapter) attempts() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}
