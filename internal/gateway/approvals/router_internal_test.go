// White-box regression tests for the approvals Router. Lives in
// `package approvals` (not `approvals_test`) so we can poke at the
// unexported sent/watchers maps and call waitForDecision directly —
// the two fixes covered here are about internal invariants that the
// public API doesn't surface cleanly.
package approvals

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
	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/gateway/fake"
)

// newInternalTestLog opens an in-tempdir SQLite event log for one test.
// Duplicated from router_test.go because that file is in the _test
// package and we can't import its helpers from here.
func newInternalTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// blockingAdapter wraps a fake.Adapter and lets a test gate the Send
// call on a channel the test owns. enteredCh fires once when Send is
// entered; the goroutine blocks until releaseCh is closed (or ctx
// cancels).
type blockingAdapter struct {
	*fake.Adapter
	enteredCh chan struct{}
	releaseCh chan struct{}
	once      sync.Once
}

func newBlockingAdapter(name gateway.Source) *blockingAdapter {
	return &blockingAdapter{
		Adapter:   fake.New(name),
		enteredCh: make(chan struct{}, 1),
		releaseCh: make(chan struct{}),
	}
}

func (b *blockingAdapter) Send(ctx context.Context, env gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	// Signal the test exactly once, on the first Send call.
	b.once.Do(func() {
		select {
		case b.enteredCh <- struct{}{}:
		default:
		}
		select {
		case <-b.releaseCh:
		case <-ctx.Done():
		}
	})
	return b.Adapter.Send(ctx, env)
}

func (b *blockingAdapter) release() {
	select {
	case <-b.releaseCh:
		// already closed
	default:
		close(b.releaseCh)
	}
}

// TestRouter_DispatchInstallsWatcherBeforeSend exercises Fix 1.
// Without the fix, dispatch ordered work as: markSent → broker.Send
// (slow) → install r.watchers[id]. That left a window where r.sent[id]
// existed but r.watchers[id] did not — gc walked an empty watchers map
// during that window and could not pair sent ↔ watcher correctly.
//
// With the fix the watcher entry is installed under r.mu BEFORE Send,
// so we can assert the invariant directly: while Send is still
// blocked, r.watchers[ref.ID] is already populated and r.sent[ref.ID]
// is too.
func TestRouter_DispatchInstallsWatcherBeforeSend(t *testing.T) {
	log := newInternalTestLog(t)
	b, err := gateway.New(gateway.Options{
		Log: log,
		Routing: gateway.RoutingConfig{
			Approvals: []gateway.Source{gateway.SourceFake},
		},
		Retry: gateway.RetryConfig{
			MaxAttempts:    1,
			BackoffInitial: time.Microsecond,
			BackoffMax:     time.Microsecond,
		},
		Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}

	ba := newBlockingAdapter(gateway.SourceFake)
	if err := b.Register(ba); err != nil {
		t.Fatalf("register: %v", err)
	}
	brokerCtx, cancelBroker := context.WithCancel(context.Background())
	defer cancelBroker()
	go func() { _ = b.Start(brokerCtx) }()
	select {
	case <-ba.Adapter.Started():
	case <-time.After(time.Second):
		t.Fatal("blocking adapter did not start")
	}

	router, err := New(Config{
		Log:          log,
		Broker:       b,
		PollInterval: 2 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	runCtx, cancelRun := context.WithCancel(brokerCtx)
	runDone := make(chan struct{})
	go func() {
		_ = router.Run(runCtx)
		close(runDone)
	}()

	ref := agent.ArtifactRef{
		ID:        "art-race",
		AgentID:   "agent-1",
		Path:      "/tmp/art-race",
		Kind:      "plan",
		SHA256:    "deadbeef",
		Size:      42,
		CreatedAt: time.Now().UTC(),
	}
	if _, err := agent.ProposeApproval(brokerCtx, log, "agent-1", "x", ref); err != nil {
		t.Fatal(err)
	}

	// Wait until our blocking adapter's Send is parked on releaseCh.
	// At this moment dispatch is blocked inside broker.Send; the watcher
	// must already be installed if Fix 1 is in place.
	select {
	case <-ba.enteredCh:
	case <-time.After(2 * time.Second):
		ba.release()
		cancelRun()
		t.Fatal("blocking adapter Send never reached")
	}

	router.mu.Lock()
	_, sentOK := router.sent[ref.ID]
	_, watcherOK := router.watchers[ref.ID]
	router.mu.Unlock()

	if !sentOK {
		t.Errorf("sent[%s] missing while Send is in flight; dispatch lost dedupe state", ref.ID)
	}
	if !watcherOK {
		t.Errorf("watchers[%s] missing while Send is in flight; the invariant"+
			" (watcher exists iff sent exists) is violated and gc cannot pair them", ref.ID)
	}

	// Concurrently fire a gc with stillPending=empty — simulates the
	// out-of-band-resolved-during-slow-Send case. With Fix 1 the
	// watcher entry IS present so gc cancels it and deletes sent[id].
	router.gc(map[string]struct{}{})

	router.mu.Lock()
	_, sentAfter := router.sent[ref.ID]
	_, watcherAfter := router.watchers[ref.ID]
	router.mu.Unlock()
	if sentAfter || watcherAfter {
		t.Errorf("after gc with empty stillPending, both maps should be cleared;"+
			" sent=%v watcher=%v", sentAfter, watcherAfter)
	}

	// Release the blocked Send and let the dispatch unwind. cancel run
	// and confirm we don't leak the watcher goroutine — Run's wg.Wait
	// would hang otherwise.
	ba.release()
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel — watcher goroutine leaked")
	}
}

// capturingHandler is a tiny slog.Handler that collects records for
// test inspection. slog ships slogtest for behaviour assertions but no
// stock in-memory capturing handler, so we roll our own.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}
func (h *capturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *capturingHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

// TestRouter_WaitForDecisionLogsAcceptFailure exercises Fix 2.
// Closing the log before waitForDecision processes the Decision makes
// agent.AcceptApproval fail with "sql: database is closed". The pre-
// fix code dropped that error silently (`_, _ = agent.AcceptApproval`),
// so the user's tap was acked on the channel but the producing agent
// never saw the resolution. Fix 2 emits a slog.Error record on each
// failure so operators have a trail.
func TestRouter_WaitForDecisionLogsAcceptFailure(t *testing.T) {
	log := newInternalTestLog(t)

	cap := &capturingHandler{}
	logger := slog.New(cap)

	// Construct broker + router against the still-open log, then close
	// the log to force every subsequent agent.AcceptApproval /
	// RejectApproval call to return "sql: database is closed". We
	// don't drive the broker side of the test — waitForDecision only
	// touches r.log + r.logger — so the broker handle is effectively a
	// placeholder.
	b, err := gateway.New(gateway.Options{
		Log: log,
		Routing: gateway.RoutingConfig{
			Approvals: []gateway.Source{gateway.SourceFake},
		},
	})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	r, err := New(Config{
		Log:    log,
		Broker: b,
		Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := log.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}

	cases := []struct {
		name      string
		decision  gateway.Decision
		wantMsg   string
		wantKind  string
		artifactID string
	}{
		{
			name:       "approve failure",
			decision:   gateway.Decision{Kind: gateway.DecisionApprove, Revision: "yes"},
			wantMsg:    "approvals: accept failed",
			wantKind:   string(gateway.DecisionApprove),
			artifactID: "art-approve",
		},
		{
			name:       "reject failure",
			decision:   gateway.Decision{Kind: gateway.DecisionReject, Revision: "no"},
			wantMsg:    "approvals: reject failed",
			wantKind:   string(gateway.DecisionReject),
			artifactID: "art-reject",
		},
		{
			name:       "revise failure",
			decision:   gateway.Decision{Kind: gateway.DecisionRevise, Revision: "redo"},
			wantMsg:    "approvals: revise-to-reject failed",
			wantKind:   string(gateway.DecisionRevise),
			artifactID: "art-revise",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(cap.snapshot())

			decCh := make(chan gateway.Decision, 1)
			decCh <- tc.decision
			close(decCh)

			// waitForDecision should observe the decision, call into
			// agent.AcceptApproval / RejectApproval (which errors
			// because the log is closed), and emit an Error record.
			r.waitForDecision(context.Background(), tc.artifactID, decCh)

			records := cap.snapshot()[before:]
			if len(records) != 1 {
				t.Fatalf("want 1 log record, got %d", len(records))
			}
			rec := records[0]
			if rec.Level != slog.LevelError {
				t.Errorf("level: want Error, got %v", rec.Level)
			}
			if rec.Message != tc.wantMsg {
				t.Errorf("msg: want %q, got %q", tc.wantMsg, rec.Message)
			}
			attrs := collectAttrs(rec)
			if got := attrs["artifact_id"]; got != tc.artifactID {
				t.Errorf("artifact_id attr: want %q, got %q", tc.artifactID, got)
			}
			if got := attrs["decision"]; got != tc.wantKind {
				t.Errorf("decision attr: want %q, got %q", tc.wantKind, got)
			}
			if attrs["err"] == "" {
				t.Errorf("err attr missing; raw attrs=%v", attrs)
			}
			// modernc/sqlite phrases the error as "sql: database is
			// closed"; we just probe a stable fragment so a driver
			// upgrade doesn't churn the test.
			if !strings.Contains(attrs["err"], "closed") &&
				!strings.Contains(attrs["err"], "database") {
				t.Errorf("err attr should mention the closed log, got %q", attrs["err"])
			}
		})
	}
}

// TestRouter_WaitForDecisionSilentSuccess pins the no-log-record path:
// a successful AcceptApproval/RejectApproval must not emit any record.
// Without this guard a future change that logs INFO on every decision
// would slip through unnoticed and spam the production log.
func TestRouter_WaitForDecisionSilentSuccess(t *testing.T) {
	log := newInternalTestLog(t)
	cap := &capturingHandler{}
	logger := slog.New(cap)

	b, err := gateway.New(gateway.Options{
		Log: log,
		Routing: gateway.RoutingConfig{
			Approvals: []gateway.Source{gateway.SourceFake},
		},
	})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	r, err := New(Config{Log: log, Broker: b, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}

	// Propose so the artifact actually exists in the log; otherwise
	// resolveApproval still succeeds (it just appends an event), but
	// having a real pending row is closer to the production path.
	ref := agent.ArtifactRef{
		ID:        "art-quiet",
		AgentID:   "agent-1",
		Path:      "/tmp/art-quiet",
		Kind:      "plan",
		SHA256:    "deadbeef",
		Size:      42,
		CreatedAt: time.Now().UTC(),
	}
	if _, err := agent.ProposeApproval(context.Background(), log, "agent-1", "x", ref); err != nil {
		t.Fatal(err)
	}

	decCh := make(chan gateway.Decision, 1)
	decCh <- gateway.Decision{Kind: gateway.DecisionApprove}
	close(decCh)

	r.waitForDecision(context.Background(), ref.ID, decCh)

	if got := cap.snapshot(); len(got) != 0 {
		var buf bytes.Buffer
		for _, rec := range got {
			buf.WriteString(rec.Message)
			buf.WriteString(";")
		}
		t.Errorf("success path emitted %d records (want 0): %s", len(got), buf.String())
	}
}

// collectAttrs flattens record attributes into a string->string map
// for easy assertions. slog stores them as a callback-driven list so
// the test has to walk them once.
func collectAttrs(r slog.Record) map[string]string {
	out := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.String()
		return true
	})
	return out
}
