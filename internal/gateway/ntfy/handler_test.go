package ntfy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
)

// newTestAdapter builds an Adapter wired with a fixed clock for
// deterministic token expiries. The httptest server is intentionally
// nil — handler tests don't publish; they invoke the inbound path.
func newTestAdapter(t *testing.T, now time.Time) *Adapter {
	t.Helper()
	a, err := New(Config{
		Server:         "https://ntfy.example",
		Topic:          "carlos-test",
		ActionEndpoint: "https://carlos.example/gateway/ntfy/action",
		SigningKey:     testKey,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	return a
}

// recordingIngest captures every InboundEnvelope handed to it. Used to
// assert the handler produced the right Decision envelope.
type recordingIngest struct {
	mu      sync.Mutex
	calls   []gateway.InboundEnvelope
	failNth int // 1-indexed; 0 = never fail
	failErr error
}

func (r *recordingIngest) fn(_ context.Context, env gateway.InboundEnvelope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, env)
	if r.failNth > 0 && len(r.calls) == r.failNth {
		return r.failErr
	}
	return nil
}

func (r *recordingIngest) snapshot() []gateway.InboundEnvelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]gateway.InboundEnvelope, len(r.calls))
	copy(out, r.calls)
	return out
}

// startAdapter spawns a.Start in a goroutine and waits for it to wire
// the ingest func. Returns a cancel callback that also calls Stop.
func startAdapter(t *testing.T, a *Adapter, ingest gateway.IngestFunc) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = a.Start(ctx, ingest)
		close(done)
	}()
	// poll for wiring with a short timeout — the Start goroutine
	// captures ingest under the mutex before blocking.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if a.currentIngest() != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if a.currentIngest() == nil {
		cancel()
		<-done
		t.Fatal("adapter never captured ingest")
	}
	return func() {
		_ = a.Stop(ctx)
		cancel()
		<-done
	}
}

func TestHandler_ValidToken_Ingests(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	rec := &recordingIngest{}
	defer startAdapter(t, a, rec.fn)()

	exp := now.Add(time.Hour).UnixMilli()
	tok, err := signToken(testKey, tokenPayload{
		EnvelopeID: "env-42",
		ArtifactID: "art-99",
		ActionID:   "approve",
		ExpUnixMs:  exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/gateway/ntfy/action?t="+url.QueryEscape(tok), nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status: got %d want 204; body=%q", w.Code, w.Body.String())
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("ingest called %d times, want 1", len(calls))
	}
	got := calls[0]
	if got.Kind != gateway.InboundDecision {
		t.Errorf("kind: %q", got.Kind)
	}
	if got.ArtifactID != "art-99" {
		t.Errorf("artifact id: %q", got.ArtifactID)
	}
	if got.GatewayEventID != "env-42:approve" {
		t.Errorf("gateway event id: %q", got.GatewayEventID)
	}
	if got.Source != gateway.SourceNtfy {
		t.Errorf("source: %q", got.Source)
	}
	if got.Decision == nil || got.Decision.Kind != gateway.DecisionApprove {
		t.Errorf("decision: %+v", got.Decision)
	}
}

func TestHandler_Replay_StillIngests(t *testing.T) {
	// Per the spec, the adapter does not dedupe — the broker does.
	// Two posts with the same token should each fire ingest; the
	// broker collapses on (Source, GatewayEventID).
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	rec := &recordingIngest{}
	defer startAdapter(t, a, rec.fn)()

	tok, err := signToken(testKey, tokenPayload{
		EnvelopeID: "env-1", ArtifactID: "art-1", ActionID: "approve",
		ExpUnixMs: now.Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/x?t="+url.QueryEscape(tok), nil)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Errorf("call %d: %d body=%q", i, w.Code, w.Body.String())
		}
	}
	if got := len(rec.snapshot()); got != 2 {
		t.Errorf("ingest count: got %d want 2", got)
	}
}

func TestHandler_InvalidHMAC_Returns401(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	rec := &recordingIngest{}
	defer startAdapter(t, a, rec.fn)()

	// Token signed with a different key.
	wrong := bytes32("OTHER-key-bytes-for-this-test-32")
	tok, err := signToken(wrong, tokenPayload{
		EnvelopeID: "e", ArtifactID: "a", ActionID: "approve",
		ExpUnixMs: now.Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/x?t="+url.QueryEscape(tok), nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got %d want 401", w.Code)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("ingest should not be called, got %d", got)
	}
}

func TestHandler_ExpiredToken_Returns401(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	rec := &recordingIngest{}
	defer startAdapter(t, a, rec.fn)()

	// exp is in the past
	tok, err := signToken(testKey, tokenPayload{
		EnvelopeID: "e", ArtifactID: "a", ActionID: "approve",
		ExpUnixMs: now.Add(-time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/x?t="+url.QueryEscape(tok), nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got %d want 401", w.Code)
	}
}

func TestHandler_MissingToken_Returns400(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	defer startAdapter(t, a, (&recordingIngest{}).fn)()

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400", w.Code)
	}
}

func TestHandler_MalformedToken_Returns400(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	defer startAdapter(t, a, (&recordingIngest{}).fn)()

	req := httptest.NewRequest(http.MethodPost, "/x?t=not-a-token", nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400; body=%q", w.Code, w.Body.String())
	}
}

func TestHandler_UnknownActionID_Returns400(t *testing.T) {
	// A well-signed token with an action_id that isn't one of the
	// canonical Decision constants. The handler must reject before
	// calling ingest so the broker never sees a malformed Decision.
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	rec := &recordingIngest{}
	defer startAdapter(t, a, rec.fn)()

	tok, err := signToken(testKey, tokenPayload{
		EnvelopeID: "e", ArtifactID: "a", ActionID: "snooze",
		ExpUnixMs: now.Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/x?t="+url.QueryEscape(tok), nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400", w.Code)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("ingest should not be called, got %d", got)
	}
}

func TestHandler_WrongMethod_Returns405(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	defer startAdapter(t, a, (&recordingIngest{}).fn)()

	req := httptest.NewRequest(http.MethodGet, "/x?t=anything", nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d want 405", w.Code)
	}
	if got := w.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow header: %q", got)
	}
}

func TestHandler_BeforeStart_Returns503(t *testing.T) {
	// Construct an adapter but do NOT call Start.
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	tok, err := signToken(testKey, tokenPayload{
		EnvelopeID: "e", ArtifactID: "a", ActionID: "approve",
		ExpUnixMs: now.Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/x?t="+url.QueryEscape(tok), nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d want 503", w.Code)
	}
}

func TestHandler_IngestError_Returns500(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	rec := &recordingIngest{failNth: 1, failErr: errString("broker down")}
	defer startAdapter(t, a, rec.fn)()

	tok, err := signToken(testKey, tokenPayload{
		EnvelopeID: "e", ArtifactID: "a", ActionID: "approve",
		ExpUnixMs: now.Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/x?t="+url.QueryEscape(tok), nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("got %d want 500", w.Code)
	}
}

// errString is a tiny error type used in tests where errors.New
// allocates more than we want to type.
type errString string

func (e errString) Error() string { return string(e) }

func TestHandler_KeyTooShort_Returns500(t *testing.T) {
	// Bypass New's validation by mutating the handler's key directly
	// after construction — simulates a deployment that swapped the
	// signing key for something too short at runtime (shouldn't
	// happen, but the handler must not panic).
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	a.handler.key = []byte("short")
	defer startAdapter(t, a, (&recordingIngest{}).fn)()

	req := httptest.NewRequest(http.MethodPost, "/x?t=anything.anything", nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("got %d want 500", w.Code)
	}
}

func TestHandler_AllCanonicalActions(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	a := newTestAdapter(t, now)
	rec := &recordingIngest{}
	defer startAdapter(t, a, rec.fn)()

	for _, act := range []gateway.DecisionKind{
		gateway.DecisionApprove, gateway.DecisionRevise, gateway.DecisionReject,
	} {
		tok, err := signToken(testKey, tokenPayload{
			EnvelopeID: "env-" + string(act), ArtifactID: "art", ActionID: string(act),
			ExpUnixMs: now.Add(time.Hour).UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/x?t="+url.QueryEscape(tok), nil)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Errorf("%s: status %d", act, w.Code)
		}
	}
	calls := rec.snapshot()
	if len(calls) != 3 {
		t.Fatalf("ingest called %d times, want 3", len(calls))
	}
	for i, want := range []gateway.DecisionKind{
		gateway.DecisionApprove, gateway.DecisionRevise, gateway.DecisionReject,
	} {
		if calls[i].Decision.Kind != want {
			t.Errorf("call %d: kind %q want %q", i, calls[i].Decision.Kind, want)
		}
		if !strings.HasSuffix(calls[i].GatewayEventID, ":"+string(want)) {
			t.Errorf("call %d: gateway_event_id %q lacks action suffix", i, calls[i].GatewayEventID)
		}
	}
}
