package approvals_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/gateway/approvals"
	"github.com/georgebuilds/carlos/internal/gateway/fake"
)

// newTestLog opens an in-tempdir SQLite event log for one test. Pure-Go
// modernc driver makes this cheap enough to use everywhere.
func newTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// newTestBroker builds a broker wired to send approvals through the
// SourceFake channel only. The retry config uses ~zero backoff so a
// stuck send doesn't add observable latency to the suite.
func newTestBroker(t *testing.T, log *agent.SQLiteEventLog) *gateway.Broker {
	t.Helper()
	b, err := gateway.New(gateway.Options{
		Log: log,
		Routing: gateway.RoutingConfig{
			Approvals: []gateway.Source{gateway.SourceFake},
		},
		Retry: gateway.RetryConfig{
			MaxAttempts:    2,
			BackoffInitial: time.Microsecond,
			BackoffMax:     time.Microsecond,
		},
		Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	return b
}

// startBrokerWithFake registers a fake adapter under SourceFake, starts
// the broker, waits for the fake's Start to settle, and returns the
// fake + a cancel func tests can defer.
func startBrokerWithFake(t *testing.T, b *gateway.Broker) (*fake.Adapter, context.Context, context.CancelFunc) {
	t.Helper()
	f := fake.New(gateway.SourceFake)
	if err := b.Register(f); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = b.Start(ctx) }()
	select {
	case <-f.Started():
	case <-time.After(time.Second):
		cancel()
		t.Fatal("fake adapter did not start")
	}
	return f, ctx, cancel
}

// mkRef builds an ArtifactRef the approval API will accept. Mirrors the
// agent_test helper for consistency.
func mkRef(id string) agent.ArtifactRef {
	return agent.ArtifactRef{
		ID:        id,
		AgentID:   "agent-1",
		Path:      "/tmp/fake/" + id,
		Kind:      "plan",
		SHA256:    "deadbeef",
		Size:      42,
		CreatedAt: time.Now().UTC(),
	}
}

// waitFor polls cond until it returns true or timeout elapses. Used to
// avoid sleep-based assertions against the polling router.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestRouter_New_RequiresLogAndBroker(t *testing.T) {
	if _, err := approvals.New(approvals.Config{}); err == nil {
		t.Error("expected error on missing log")
	}
	log := newTestLog(t)
	if _, err := approvals.New(approvals.Config{Log: log}); err == nil {
		t.Error("expected error on missing broker")
	}
	b := newTestBroker(t, log)
	if _, err := approvals.New(approvals.Config{Log: log, Broker: b}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRouter_SingleProposeFiresOneOutbound(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, err := approvals.New(approvals.Config{
		Log:          log,
		Broker:       b,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	runDone := make(chan struct{})
	go func() {
		_ = router.Run(runCtx)
		close(runDone)
	}()

	ref := mkRef("art-1")
	if _, err := agent.ProposeApproval(brokerCtx, log, "agent-1", "refactor parser", ref); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	got := f.Sent()[0]
	if got.Kind != gateway.OutboundApprovalRequest {
		t.Errorf("kind: want approval_request got %v", got.Kind)
	}
	if got.ArtifactID != ref.ID {
		t.Errorf("artifact id: want %q got %q", ref.ID, got.ArtifactID)
	}
	if got.AgentID != "agent-1" {
		t.Errorf("agent id: want agent-1 got %q", got.AgentID)
	}
	if got.Urgency != gateway.UrgencyHigh {
		t.Errorf("urgency: want high got %v", got.Urgency)
	}
	if len(got.Actions) != 3 {
		t.Errorf("actions: want 3 got %d", len(got.Actions))
	}
	if got.Title != "refactor parser" {
		t.Errorf("title: %q", got.Title)
	}
	if !strings.Contains(got.Body, ref.ID) {
		t.Errorf("body should mention ref id: %q", got.Body)
	}

	cancelRun()
	<-runDone
}

func TestRouter_DoesNotDoubleSendAcrossPolls(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{
		Log:          log,
		Broker:       b,
		PollInterval: time.Millisecond,
	})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	ref := mkRef("art-dup")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "x", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	// Allow many more polls to fire. If the dedupe is broken we'll see
	// the count grow.
	time.Sleep(50 * time.Millisecond)
	if got := len(f.Sent()); got != 1 {
		t.Errorf("expected 1 send across many polls, got %d", got)
	}
}

func TestRouter_DecisionApproveCallsAccept(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Millisecond})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	ref := mkRef("art-approve")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "approve me", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	// Inject an approve decision back through the fake.
	if err := f.Push(brokerCtx, gateway.InboundEnvelope{
		Source:         gateway.SourceFake,
		GatewayEventID: "tap-1",
		Kind:           gateway.InboundDecision,
		ArtifactID:     ref.ID,
		Decision:       &gateway.Decision{Kind: gateway.DecisionApprove},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		pending, _ := agent.ListPendingApprovals(brokerCtx, log)
		return len(pending) == 0
	})
}

func TestRouter_DecisionRejectCallsReject(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Millisecond})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	ref := mkRef("art-reject")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "reject me", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	if err := f.Push(brokerCtx, gateway.InboundEnvelope{
		Source:         gateway.SourceFake,
		GatewayEventID: "tap-2",
		Kind:           gateway.InboundDecision,
		ArtifactID:     ref.ID,
		Decision:       &gateway.Decision{Kind: gateway.DecisionReject, Revision: "no thanks"},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		pending, _ := agent.ListPendingApprovals(brokerCtx, log)
		return len(pending) == 0
	})

	// Sanity-check the rejection event landed with the user's note.
	events, _ := log.Read(brokerCtx, "user", 0)
	found := false
	for _, ev := range events {
		if ev.Type == agent.EvtApprovalRejected && strings.Contains(string(ev.Payload), "no thanks") {
			found = true
		}
	}
	if !found {
		t.Errorf("reject reason not persisted; events=%v", events)
	}
}

func TestRouter_DecisionReviseMapsToRejectWithNote(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Millisecond})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	ref := mkRef("art-revise")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "revise me", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	if err := f.Push(brokerCtx, gateway.InboundEnvelope{
		Source:         gateway.SourceFake,
		GatewayEventID: "tap-3",
		Kind:           gateway.InboundDecision,
		ArtifactID:     ref.ID,
		Decision:       &gateway.Decision{Kind: gateway.DecisionRevise, Revision: "make it idempotent"},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		pending, _ := agent.ListPendingApprovals(brokerCtx, log)
		return len(pending) == 0
	})

	events, _ := log.Read(brokerCtx, "user", 0)
	wantSub := "user requested revision: make it idempotent"
	found := false
	for _, ev := range events {
		if ev.Type == agent.EvtApprovalRejected && strings.Contains(string(ev.Payload), wantSub) {
			found = true
		}
	}
	if !found {
		t.Errorf("revise → reject note not persisted; events=%v", events)
	}
}

func TestRouter_DecisionReviseWithoutRevisionGetsDefaultNote(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Millisecond})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	ref := mkRef("art-revise-bare")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "revise me", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	if err := f.Push(brokerCtx, gateway.InboundEnvelope{
		Source:         gateway.SourceFake,
		GatewayEventID: "tap-bare",
		Kind:           gateway.InboundDecision,
		ArtifactID:     ref.ID,
		Decision:       &gateway.Decision{Kind: gateway.DecisionRevise},
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool {
		pending, _ := agent.ListPendingApprovals(brokerCtx, log)
		return len(pending) == 0
	})
	events, _ := log.Read(brokerCtx, "user", 0)
	found := false
	for _, ev := range events {
		if ev.Type == agent.EvtApprovalRejected && strings.Contains(string(ev.Payload), "user requested revision") {
			found = true
		}
	}
	if !found {
		t.Errorf("bare revise note missing; events=%v", events)
	}
}

func TestRouter_DecisionRejectWithoutReasonGetsDefault(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Millisecond})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	ref := mkRef("art-reject-bare")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "reject bare", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	if err := f.Push(brokerCtx, gateway.InboundEnvelope{
		Source:         gateway.SourceFake,
		GatewayEventID: "tap-reject-bare",
		Kind:           gateway.InboundDecision,
		ArtifactID:     ref.ID,
		Decision:       &gateway.Decision{Kind: gateway.DecisionReject},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool {
		pending, _ := agent.ListPendingApprovals(brokerCtx, log)
		return len(pending) == 0
	})
	events, _ := log.Read(brokerCtx, "user", 0)
	found := false
	for _, ev := range events {
		if ev.Type == agent.EvtApprovalRejected && strings.Contains(string(ev.Payload), "user rejected") {
			found = true
		}
	}
	if !found {
		t.Errorf("default reject reason missing; events=%v", events)
	}
}

func TestRouter_ConcurrentPendingsResolveIndependently(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Millisecond})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	refA := mkRef("art-a")
	refB := mkRef("art-b")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "a", refA)
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "b", refB)

	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 2 })

	// Push the two decisions sequentially. The broker's Ingest path
	// shares a global ULID-monotonic entropy source that races under
	// truly concurrent calls (pre-existing condition in the gateway
	// package — see TestBroker_ConcurrentSendsAreSafe). What this test
	// actually proves is that the Router watches both ArtifactIDs and
	// resolves each independently — sequential injection from the
	// adapter is plenty for that.
	if err := f.Push(brokerCtx, gateway.InboundEnvelope{
		Source: gateway.SourceFake, GatewayEventID: "ev-a",
		Kind: gateway.InboundDecision, ArtifactID: refA.ID,
		Decision: &gateway.Decision{Kind: gateway.DecisionApprove},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.Push(brokerCtx, gateway.InboundEnvelope{
		Source: gateway.SourceFake, GatewayEventID: "ev-b",
		Kind: gateway.InboundDecision, ArtifactID: refB.ID,
		Decision: &gateway.Decision{Kind: gateway.DecisionReject, Revision: "nope"},
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool {
		pending, _ := agent.ListPendingApprovals(brokerCtx, log)
		return len(pending) == 0
	})

	// Verify A was accepted and B rejected — different event types in
	// the user namespace.
	events, _ := log.Read(brokerCtx, "user", 0)
	var accepts, rejects int
	for _, ev := range events {
		switch ev.Type {
		case agent.EvtApprovalAccepted:
			accepts++
		case agent.EvtApprovalRejected:
			rejects++
		}
	}
	if accepts != 1 || rejects != 1 {
		t.Errorf("accepts=%d rejects=%d (want 1/1)", accepts, rejects)
	}
}

func TestRouter_CancelledContextReturnsPromptly(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	_, _, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- router.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error on cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRouter_CancelDuringInFlightWatcherExitsCleanly(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Millisecond})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	done := make(chan error, 1)
	go func() { done <- router.Run(runCtx) }()

	ref := mkRef("art-stuck")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "stuck", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	// No decision injected — the watcher should park, then exit when
	// Run's ctx is cancelled.
	cancelRun()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel with in-flight watcher")
	}
}

func TestRouter_LongTitleIsTruncated(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{
		Log:          log,
		Broker:       b,
		PollInterval: time.Millisecond,
		TitleMaxLen:  10,
	})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	longTitle := strings.Repeat("a", 100)
	ref := mkRef("art-long")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", longTitle, ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	got := f.Sent()[0].Title
	if len([]rune(got)) != 10 {
		t.Errorf("title rune count: want 10 got %d (%q)", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("title should end with ellipsis: %q", got)
	}
}

func TestRouter_ShortTitleIsNotTruncated(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{
		Log:          log,
		Broker:       b,
		PollInterval: time.Millisecond,
		TitleMaxLen:  80,
	})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	ref := mkRef("art-short")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "short", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	if got := f.Sent()[0].Title; got != "short" {
		t.Errorf("short title mutated: %q", got)
	}
}

func TestRouter_BodyTemplateIsInvokedWhenProvided(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	var called int
	var seenID string
	router, _ := approvals.New(approvals.Config{
		Log:          log,
		Broker:       b,
		PollInterval: time.Millisecond,
		BodyTemplate: func(p agent.PendingApproval) string {
			called++
			seenID = p.Ref.ID
			return "custom: " + p.Title
		},
	})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	ref := mkRef("art-tpl")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "tpl", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	if called == 0 {
		t.Error("body template not invoked")
	}
	if seenID != ref.ID {
		t.Errorf("template saw ref %q want %q", seenID, ref.ID)
	}
	if got := f.Sent()[0].Body; got != "custom: tpl" {
		t.Errorf("body: %q", got)
	}
}

func TestRouter_DefaultBodyMentionsArtifactAndPath(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Millisecond})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	ref := mkRef("art-default-body")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "x", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	body := f.Sent()[0].Body
	if !strings.Contains(body, ref.ID) || !strings.Contains(body, ref.Path) {
		t.Errorf("default body missing ref/path: %q", body)
	}
}

func TestRouter_DefaultBodyOmitsPathWhenEmpty(t *testing.T) {
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Millisecond})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	ref := mkRef("art-no-path")
	ref.Path = ""
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "x", ref)
	waitFor(t, time.Second, func() bool { return len(f.Sent()) == 1 })

	body := f.Sent()[0].Body
	if !strings.Contains(body, ref.ID) || strings.Contains(body, " at ") {
		t.Errorf("default body should skip path when empty: %q", body)
	}
}

func TestRouter_DefaultsAppliedWhenConfigZero(t *testing.T) {
	// Sanity: with PollInterval=0 the constructor should still build a
	// usable router. Drive it briefly to confirm Run doesn't hot-loop
	// on a zero ticker.
	log := newTestLog(t)
	b := newTestBroker(t, log)
	_, _, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	router, err := approvals.New(approvals.Config{Log: log, Broker: b})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := router.Run(ctx); err != nil {
		t.Errorf("Run: %v", err)
	}
}

func TestRouter_PreResolvedDecisionStillTriggersAccept(t *testing.T) {
	// Race coverage: if the broker sees a Decision land BEFORE the
	// router's watcher subscribes (e.g. the user taps approve before
	// the first poll fires), SubscribeDecision pre-seeds and the
	// router still resolves correctly.
	log := newTestLog(t)
	b := newTestBroker(t, log)
	f, brokerCtx, cancelBroker := startBrokerWithFake(t, b)
	defer cancelBroker()

	ref := mkRef("art-prelanded")
	_, _ = agent.ProposeApproval(brokerCtx, log, "agent-1", "preland", ref)

	// Inject the decision before the router runs. The broker holds the
	// gate; when the router subscribes during dispatch, it'll fire
	// immediately.
	if err := b.Ingest(brokerCtx, gateway.InboundEnvelope{
		Source: gateway.SourceFake, GatewayEventID: "early-1",
		Kind: gateway.InboundDecision, ArtifactID: ref.ID,
		Decision: &gateway.Decision{Kind: gateway.DecisionApprove},
	}); err != nil {
		t.Fatal(err)
	}

	router, _ := approvals.New(approvals.Config{Log: log, Broker: b, PollInterval: time.Millisecond})
	runCtx, cancelRun := context.WithCancel(brokerCtx)
	defer cancelRun()
	go func() { _ = router.Run(runCtx) }()

	waitFor(t, time.Second, func() bool {
		pending, _ := agent.ListPendingApprovals(brokerCtx, log)
		return len(pending) == 0
	})
	// The Send still happens — we re-publish the outbound on the next
	// poll even though the decision is already in. That's the spec
	// shape: "the user gets a duplicate but the broker dedupes the
	// inbound decision by ArtifactID via the gate."
	if len(f.Sent()) == 0 {
		t.Error("expected at least one outbound even when decision pre-landed")
	}
}
