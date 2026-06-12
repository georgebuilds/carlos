package gateway_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/gateway/fake"
)

func newLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func newBroker(t *testing.T, log *agent.SQLiteEventLog, routing gateway.RoutingConfig) *gateway.Broker {
	t.Helper()
	b, err := gateway.New(gateway.Options{
		Log:     log,
		Routing: routing,
		Retry:   gateway.RetryConfig{MaxAttempts: 3, BackoffInitial: time.Millisecond, BackoffMax: 2 * time.Millisecond},
		Sleep:   func(ctx context.Context, d time.Duration) error { return ctx.Err() },
	})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	return b
}

func TestBroker_New_RequiresLog(t *testing.T) {
	if _, err := gateway.New(gateway.Options{}); err == nil {
		t.Error("expected error on nil log")
	}
}

func TestBroker_Register_RejectsDuplicate(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	f := fake.New(gateway.SourceFake)
	if err := b.Register(f); err != nil {
		t.Fatal(err)
	}
	if err := b.Register(f); err == nil {
		t.Error("expected duplicate registration error")
	}
}

func TestBroker_Register_RejectsNil(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	if err := b.Register(nil); err == nil {
		t.Error("expected nil-adapter error")
	}
}

func TestBroker_Send_NoChannels(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	receipts, err := b.Send(context.Background(), gateway.OutboundEnvelope{
		Kind:  gateway.OutboundNotification,
		Title: "x",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(receipts) != 0 {
		t.Errorf("receipts: want 0 got %d", len(receipts))
	}
}

func TestBroker_Send_FanOut_Delivered(t *testing.T) {
	log := newLog(t)
	r := gateway.RoutingConfig{Notifications: []gateway.Source{gateway.SourceFake, gateway.SourceTelegram}}
	b := newBroker(t, log, r)
	f1 := fake.New(gateway.SourceFake)
	f2 := fake.New(gateway.SourceTelegram)
	if err := b.Register(f1); err != nil {
		t.Fatal(err)
	}
	if err := b.Register(f2); err != nil {
		t.Fatal(err)
	}

	receipts, err := b.Send(context.Background(), gateway.OutboundEnvelope{
		Kind:  gateway.OutboundNotification,
		Title: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 2 {
		t.Fatalf("receipts: want 2 got %d", len(receipts))
	}
	for _, r := range receipts {
		if r.Status != gateway.StatusDelivered {
			t.Errorf("receipt %v not delivered: %+v", r.Source, r)
		}
	}
	if len(f1.Sent()) != 1 || len(f2.Sent()) != 1 {
		t.Errorf("adapters: f1=%d f2=%d", len(f1.Sent()), len(f2.Sent()))
	}
}

func TestBroker_Send_ChannelWithoutAdapter(t *testing.T) {
	log := newLog(t)
	r := gateway.RoutingConfig{Notifications: []gateway.Source{gateway.SourceTelegram}}
	b := newBroker(t, log, r)
	receipts, err := b.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].Status != gateway.StatusFailed {
		t.Errorf("expected one failed receipt, got %+v", receipts)
	}
}

func TestBroker_Send_AdapterMissingCapability(t *testing.T) {
	log := newLog(t)
	r := gateway.RoutingConfig{Conversations: []gateway.Source{gateway.SourceFake}}
	b := newBroker(t, log, r)
	caps := gateway.OutboundCapabilities{Push: true} // no FreeFormTextInbound
	f := fake.New(gateway.SourceFake, fake.WithCapabilities(caps))
	_ = b.Register(f)
	receipts, err := b.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundConversationReply, Title: "x", Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].Status != gateway.StatusFailed {
		t.Errorf("expected failed for missing capability: %+v", receipts)
	}
	if len(f.Sent()) != 0 {
		t.Errorf("adapter should not have been called: %d sends", len(f.Sent()))
	}
}

func TestBroker_Send_TruncatesActionsToMax(t *testing.T) {
	log := newLog(t)
	r := gateway.RoutingConfig{Approvals: []gateway.Source{gateway.SourceFake}}
	b := newBroker(t, log, r)
	caps := gateway.OutboundCapabilities{Push: true, FixedChoiceHITL: true, MaxActions: 2}
	f := fake.New(gateway.SourceFake, fake.WithCapabilities(caps))
	_ = b.Register(f)
	receipts, err := b.Send(context.Background(), gateway.OutboundEnvelope{
		Kind:       gateway.OutboundApprovalRequest,
		Title:      "review",
		Body:       "x",
		ArtifactID: "art-1",
		Actions:    gateway.CanonicalActions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].Status != gateway.StatusDelivered {
		t.Fatalf("send: %+v", receipts)
	}
	if got := f.Sent()[0].Actions; len(got) != 2 {
		t.Errorf("actions truncated: want 2 got %d", len(got))
	}
}

func TestBroker_Send_RetriesOnFailure(t *testing.T) {
	log := newLog(t)
	r := gateway.RoutingConfig{Notifications: []gateway.Source{gateway.SourceFake}}
	b := newBroker(t, log, r)
	f := &counterAdapter{name: gateway.SourceFake, caps: gateway.OutboundCapabilities{Push: true, FixedChoiceHITL: true, MaxActions: 3, FreeFormTextInbound: true}}
	f.failTimes = 2
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
		t.Fatalf("want delivered after retries, got %+v", receipts)
	}
	if f.calls != 3 {
		t.Errorf("expected 3 send calls, got %d", f.calls)
	}
}

func TestBroker_Send_GivesUpAfterMaxAttempts(t *testing.T) {
	log := newLog(t)
	r := gateway.RoutingConfig{Notifications: []gateway.Source{gateway.SourceFake}}
	b := newBroker(t, log, r)
	f := &counterAdapter{name: gateway.SourceFake, caps: gateway.OutboundCapabilities{Push: true}, failTimes: 10}
	_ = b.Register(f)
	receipts, _ := b.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "x",
	})
	if len(receipts) != 1 || receipts[0].Status != gateway.StatusFailed {
		t.Errorf("want failed after exhausting retries: %+v", receipts)
	}
	if f.calls != 3 {
		t.Errorf("want 3 attempts (MaxAttempts=3), got %d", f.calls)
	}
}

func TestBroker_Send_ContextCancelStopsRetries(t *testing.T) {
	log := newLog(t)
	r := gateway.RoutingConfig{Notifications: []gateway.Source{gateway.SourceFake}}
	b := newBroker(t, log, r)
	f := &counterAdapter{name: gateway.SourceFake, caps: gateway.OutboundCapabilities{Push: true}, failTimes: 10}
	_ = b.Register(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	receipts, _ := b.Send(ctx, gateway.OutboundEnvelope{Kind: gateway.OutboundNotification, Title: "x"})
	if len(receipts) != 1 || receipts[0].Status != gateway.StatusFailed {
		t.Errorf("want failed under cancelled ctx: %+v", receipts)
	}
}

func TestBroker_Send_InvalidEnvelopeReturnsError(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	_, err := b.Send(context.Background(), gateway.OutboundEnvelope{Kind: gateway.OutboundNotification})
	if err == nil {
		t.Error("expected validation error")
	}
}

func TestBroker_Ingest_PersistsAndDedupes(t *testing.T) {
	log := newLog(t)
	b := newBroker(t, log, gateway.RoutingConfig{})
	ctx := context.Background()

	env := gateway.InboundEnvelope{
		Source:         gateway.SourceTelegram,
		GatewayEventID: "tg-1",
		Kind:           gateway.InboundMessage,
		Body:           "hi",
	}
	if err := b.Ingest(ctx, env); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	// Reset GatewayEventID stays same; broker should dedupe.
	if err := b.Ingest(ctx, env); err != nil {
		t.Errorf("dup ingest: %v", err)
	}
	events, err := log.Read(ctx, gateway.EventAgentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("after dedupe expected 1 inbound event, got %d", len(events))
	}
}

func TestBroker_Ingest_InvalidEnvelope(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	if err := b.Ingest(context.Background(), gateway.InboundEnvelope{}); err == nil {
		t.Error("expected validation error on empty envelope")
	}
}

func TestBroker_Ingest_DecisionRouting_FirstWins(t *testing.T) {
	log := newLog(t)
	b := newBroker(t, log, gateway.RoutingConfig{})
	ctx := context.Background()

	sub, unsub := b.SubscribeDecision("art-99")
	defer unsub()

	first := gateway.InboundEnvelope{
		Source:         gateway.SourceTelegram,
		GatewayEventID: "tg-99",
		Kind:           gateway.InboundDecision,
		ArtifactID:     "art-99",
		Decision:       &gateway.Decision{Kind: gateway.DecisionApprove, Revision: "go"},
	}
	if err := b.Ingest(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Second decision for same artifact via a different source.
	second := first
	second.Source = gateway.SourceNtfy
	second.GatewayEventID = "ntfy-99"
	second.Decision = &gateway.Decision{Kind: gateway.DecisionReject}
	if err := b.Ingest(ctx, second); err != nil {
		t.Fatal(err)
	}

	d, src, _, ok := b.DecisionFor("art-99")
	if !ok {
		t.Fatal("expected resolved decision")
	}
	if d.Kind != gateway.DecisionApprove {
		t.Errorf("first-wins violated: got %v", d)
	}
	if src != gateway.SourceTelegram {
		t.Errorf("winner source: want telegram got %v", src)
	}

	select {
	case got, ok := <-sub:
		if !ok {
			t.Error("subscriber channel closed without value")
		} else if got.Kind != gateway.DecisionApprove {
			t.Errorf("subscriber got %v", got)
		}
	case <-time.After(time.Second):
		t.Error("subscriber timed out")
	}

	// Both inbound events should be persisted (audit-only for the loser).
	events, _ := log.Read(ctx, gateway.EventAgentID, 0)
	if len(events) != 2 {
		t.Errorf("expected 2 inbound events, got %d", len(events))
	}
}

func TestBroker_SubscribeDecision_PreSeeds(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	ctx := context.Background()
	env := gateway.InboundEnvelope{
		Source: gateway.SourceFake, GatewayEventID: "fake-1",
		Kind: gateway.InboundDecision, ArtifactID: "art-x",
		Decision: &gateway.Decision{Kind: gateway.DecisionApprove},
	}
	if err := b.Ingest(ctx, env); err != nil {
		t.Fatal(err)
	}
	sub, unsub := b.SubscribeDecision("art-x")
	defer unsub()
	select {
	case got := <-sub:
		if got.Kind != gateway.DecisionApprove {
			t.Errorf("pre-seed: got %v", got)
		}
	case <-time.After(time.Second):
		t.Error("pre-seed never fired")
	}
}

func TestBroker_SubscribeDecision_Unsub(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	_, unsub := b.SubscribeDecision("art-unused")
	unsub()
	unsub() // second unsub is a no-op
}

// TestBroker_SubscribeDecision_RaceWithIngest_NoOrphans hammers the
// TOCTOU window where a subscriber registered between DecisionFor's
// "not resolved yet" probe and the subMu append could be orphaned by
// a concurrent Ingest that resolves and drains. Every subscriber must
// observe the winning decision via either pre-seed or fan-out.
//
// The pre-fix race window is narrow (a few instructions between
// gateMu.Unlock and subMu.Lock in SubscribeDecision), so the test
// uses a starting barrier and many trials to maximize the chance
// each goroutine lands inside the window on at least one trial.
func TestBroker_SubscribeDecision_RaceWithIngest_NoOrphans(t *testing.T) {
	const (
		trials = 200
		N      = 200
	)
	for trial := 0; trial < trials; trial++ {
		b := newBroker(t, newLog(t), gateway.RoutingConfig{})
		artifactID := "art-race"

		var (
			wg         sync.WaitGroup
			orphans    int32
			subscribed int32
			start      = make(chan struct{})
		)

		// Subscribers: line up on the barrier, then race the producer.
		// Each subscriber's SubscribeDecision call has a window between
		// DecisionFor's gateMu unlock and subMu.Lock where Ingest's
		// recordDecision can fan-out to an empty subscriber list and
		// strand the late registrant.
		for i := 0; i < N; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				sub, unsub := b.SubscribeDecision(artifactID)
				defer unsub()
				atomic.AddInt32(&subscribed, 1)
				select {
				case _, ok := <-sub:
					// Either the decision arrived or the channel was
					// closed without a value. Both are acceptable -
					// orphan means "blocked forever".
					_ = ok
				case <-time.After(2 * time.Second):
					atomic.AddInt32(&orphans, 1)
				}
			}()
		}

		// Producer: lined up on the same barrier. Different
		// GatewayEventID per trial so the dedupe table doesn't swallow
		// it (the broker dedupes on (Source, GatewayEventID)).
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			env := gateway.InboundEnvelope{
				Source:         gateway.SourceFake,
				GatewayEventID: fmt.Sprintf("fake-race-%d", trial),
				Kind:           gateway.InboundDecision,
				ArtifactID:     artifactID,
				Decision:       &gateway.Decision{Kind: gateway.DecisionApprove, Revision: "go"},
			}
			if err := b.Ingest(context.Background(), env); err != nil {
				t.Errorf("ingest: %v", err)
			}
		}()

		close(start) // fire the barrier - all goroutines race from here
		wg.Wait()
		if orphans != 0 {
			t.Fatalf("trial %d: %d/%d subscribers orphaned (subscribed=%d)", trial, orphans, N, subscribed)
		}
	}
}

// TestBroker_RecordDecision_DoubleCloseSafe asserts the close path is
// idempotent: calling recordDecision twice for the same artifact (via
// two Ingests with different GatewayEventIDs) does NOT panic on a
// double-close of the subscriber channel. First-write-wins still
// holds; the second decision is an audit-only no-op.
func TestBroker_RecordDecision_DoubleCloseSafe(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	ctx := context.Background()

	sub, unsub := b.SubscribeDecision("art-doubleclose")
	defer unsub()

	first := gateway.InboundEnvelope{
		Source:         gateway.SourceTelegram,
		GatewayEventID: "tg-double-1",
		Kind:           gateway.InboundDecision,
		ArtifactID:     "art-doubleclose",
		Decision:       &gateway.Decision{Kind: gateway.DecisionApprove, Revision: "go"},
	}
	if err := b.Ingest(ctx, first); err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	// First fan-out should have closed the channel; drain it.
	select {
	case got, ok := <-sub:
		if !ok {
			t.Error("subscriber channel closed without value")
		} else if got.Kind != gateway.DecisionApprove {
			t.Errorf("first decision: got %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first decision never fanned out")
	}

	// Second decision for same artifact - exercises recordDecision
	// again. The gate has already fired so winner=false, but defence-
	// in-depth: even if the close path were reached twice (via a
	// future caller, shutdown drain, etc.) it must not panic.
	second := first
	second.Source = gateway.SourceNtfy
	second.GatewayEventID = "ntfy-double-2"
	second.Decision = &gateway.Decision{Kind: gateway.DecisionReject}
	if err := b.Ingest(ctx, second); err != nil {
		t.Fatalf("second ingest: %v", err)
	}

	// Directly exercise safeClose on the already-closed sub channel
	// via a fresh subscribe-then-resolve cycle, to assert close is
	// idempotent in the only path that exposes it (the fan-out loop).
	sub2, unsub2 := b.SubscribeDecision("art-doubleclose")
	defer unsub2()
	select {
	case got, ok := <-sub2:
		// Pre-seeded path: decision was already resolved, channel
		// should carry the WINNING (first) decision then close.
		if !ok {
			t.Error("pre-seed channel closed without value")
		} else if got.Kind != gateway.DecisionApprove {
			t.Errorf("pre-seed: got %v, want approve (first-wins)", got)
		}
	case <-time.After(time.Second):
		t.Error("pre-seed never fired")
	}

	d, _, _, ok := b.DecisionFor("art-doubleclose")
	if !ok || d.Kind != gateway.DecisionApprove {
		t.Errorf("first-wins broken after double ingest: %+v ok=%v", d, ok)
	}
}

func TestBroker_StartStop_LifecycleIdempotent(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	f := fake.New(gateway.SourceFake)
	_ = b.Register(f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Start(ctx) }()
	select {
	case <-f.Started():
	case <-time.After(time.Second):
		t.Fatal("fake did not start")
	}
	if err := b.Start(ctx); err == nil {
		t.Error("double start should error")
	}
	if err := b.Stop(context.Background()); err != nil {
		t.Errorf("stop: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Start returned %v", err)
		}
	case <-time.After(time.Second):
		t.Error("Start did not return after cancel")
	}
	// double stop is idempotent.
	if err := b.Stop(context.Background()); err != nil {
		t.Errorf("double stop: %v", err)
	}
}

func TestBroker_StartStop_NoAdapters(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Start(ctx) }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("Start did not return")
	}
}

func TestBroker_SetRouting_HotSwap(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	b.SetRouting(gateway.RoutingConfig{Notifications: []gateway.Source{gateway.SourceFake}})
	got := b.Routing()
	if len(got.Notifications) != 1 || got.Notifications[0] != gateway.SourceFake {
		t.Errorf("routing not updated: %+v", got)
	}
}

func TestBroker_Adapters_Snapshot(t *testing.T) {
	b := newBroker(t, newLog(t), gateway.RoutingConfig{})
	_ = b.Register(fake.New(gateway.SourceFake))
	_ = b.Register(fake.New(gateway.SourceTelegram))
	got := b.Adapters()
	if len(got) != 2 {
		t.Fatalf("adapters: want 2 got %d", len(got))
	}
	// sorted
	if got[0] > got[1] {
		t.Errorf("adapters not sorted: %v", got)
	}
}

func TestBroker_ConcurrentSendsAreSafe(t *testing.T) {
	log := newLog(t)
	r := gateway.RoutingConfig{Notifications: []gateway.Source{gateway.SourceFake}}
	b := newBroker(t, log, r)
	f := fake.New(gateway.SourceFake)
	_ = b.Register(f)

	const N = 20
	var wg sync.WaitGroup
	var failures int32
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipts, err := b.Send(context.Background(), gateway.OutboundEnvelope{
				Kind:  gateway.OutboundNotification,
				Title: "n",
				Body:  string(rune('a' + i%26)),
			})
			if err != nil || len(receipts) != 1 || receipts[0].Status != gateway.StatusDelivered {
				atomic.AddInt32(&failures, 1)
			}
		}()
	}
	wg.Wait()
	if failures != 0 {
		t.Errorf("%d concurrent sends failed", failures)
	}
	if len(f.Sent()) != N {
		t.Errorf("sends: want %d got %d", N, len(f.Sent()))
	}
}

// counterAdapter is a tiny in-test adapter that fails the first
// `failTimes` Send calls then succeeds. Captures call count for
// retry assertions.
type counterAdapter struct {
	name      gateway.Source
	caps      gateway.OutboundCapabilities
	calls     int
	failTimes int
	mu        sync.Mutex
	stop      chan struct{}
	stopOnce  sync.Once
}

func (a *counterAdapter) Name() gateway.Source                               { return a.name }
func (a *counterAdapter) OutboundCapabilities() gateway.OutboundCapabilities { return a.caps }
func (a *counterAdapter) Send(_ context.Context, env gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.calls <= a.failTimes {
		return gateway.DeliveryReceipt{Source: a.name, Status: gateway.StatusFailed, Error: "boom"}, errors.New("boom")
	}
	return gateway.DeliveryReceipt{Source: a.name, ProviderRef: "ok", Status: gateway.StatusDelivered}, nil
}
func (a *counterAdapter) Start(ctx context.Context, _ gateway.IngestFunc) error {
	a.mu.Lock()
	if a.stop == nil {
		a.stop = make(chan struct{})
	}
	stop := a.stop
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-stop:
		return nil
	}
}
func (a *counterAdapter) Stop(context.Context) error {
	a.mu.Lock()
	if a.stop == nil {
		a.stop = make(chan struct{})
	}
	stop := a.stop
	a.mu.Unlock()
	a.stopOnce.Do(func() { close(stop) })
	return nil
}
