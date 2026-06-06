package ntfy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
)

// capturingServer wraps an httptest.Server that records every received
// request body + headers. Tests inspect the slice after Send to assert
// the adapter built the right publish.
type capturingServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []capturedRequest
	respond  func(w http.ResponseWriter, r *http.Request)
}

type capturedRequest struct {
	Method  string
	URL     *url.URL
	Headers http.Header
	Body    []byte
}

func newCapturingServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) *capturingServer {
	t.Helper()
	cs := &capturingServer{respond: respond}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cs.mu.Lock()
		cs.requests = append(cs.requests, capturedRequest{
			Method:  r.Method,
			URL:     r.URL,
			Headers: r.Header.Clone(),
			Body:    body,
		})
		cs.mu.Unlock()
		cs.respond(w, r)
	}))
	t.Cleanup(cs.Server.Close)
	return cs
}

func (c *capturingServer) snapshot() []capturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capturedRequest, len(c.requests))
	copy(out, c.requests)
	return out
}

func TestNew_ValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"missing server", Config{Topic: "t"}},
		{"missing topic", Config{Server: "https://x"}},
		{"short signing key", Config{Server: "https://x", Topic: "t", SigningKey: []byte("short")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.cfg); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

func TestNew_DefaultsApplied(t *testing.T) {
	a, err := New(Config{Server: "https://ntfy.example", Topic: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if a.httpClient == nil {
		t.Error("default http client missing")
	}
	if a.tokenTTL != defaultTokenTTL {
		t.Errorf("token ttl: %s", a.tokenTTL)
	}
	if a.now == nil {
		t.Error("default clock missing")
	}
}

func TestAdapter_NameAndCapabilities(t *testing.T) {
	a, err := New(Config{Server: "https://x", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name() != gateway.SourceNtfy {
		t.Errorf("name: %q", a.Name())
	}
	caps := a.Capabilities()
	want := gateway.Capabilities{
		Push: true, FixedChoiceHITL: true, MaxActions: 3,
		FreeFormTextInbound: false, FileImageInbound: false,
		DiffRichApproval: false, NeedsPublicEndpoint: true,
	}
	if caps != want {
		t.Errorf("caps:\n got %+v\nwant %+v", caps, want)
	}
}

func TestSend_Notification_JSONPublishWithIDReceipt(t *testing.T) {
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Echo a publish receipt with a known id.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ntfy-msg-123","time":1717593600,"event":"message","topic":"carlos-test"}`))
	})
	a, err := New(Config{
		Server:      cs.URL,
		Topic:       "carlos-test",
		HTTPClient:  cs.Client(),
		PriorityMap: map[string]int{"low": 1, "default": 3, "high": 5},
		Headers:     map[string]string{"X-Tags": "robot_face"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := gateway.OutboundEnvelope{
		ID:      "env-1",
		Kind:    gateway.OutboundNotification,
		Title:   "Hello",
		Body:    "world",
		Urgency: gateway.UrgencyHigh,
	}
	r, err := a.Send(context.Background(), env)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if r.Status != gateway.StatusDelivered {
		t.Errorf("status: %q", r.Status)
	}
	if r.ProviderRef != "ntfy-msg-123" {
		t.Errorf("provider ref: %q", r.ProviderRef)
	}
	reqs := cs.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("requests: %d want 1", len(reqs))
	}
	got := reqs[0]
	if got.Method != http.MethodPost {
		t.Errorf("method: %q", got.Method)
	}
	if ct := got.Headers.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: %q", ct)
	}
	if tags := got.Headers.Get("X-Tags"); tags != "robot_face" {
		t.Errorf("custom header X-Tags: %q", tags)
	}
	var body publishRequest
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("body parse: %v", err)
	}
	if body.Topic != "carlos-test" {
		t.Errorf("topic: %q", body.Topic)
	}
	if body.Title != "Hello" {
		t.Errorf("title: %q", body.Title)
	}
	if body.Message != "world" {
		t.Errorf("message: %q", body.Message)
	}
	if body.Priority != 5 {
		t.Errorf("priority: %d", body.Priority)
	}
	if len(body.Actions) != 0 {
		t.Errorf("notification should not carry actions: %+v", body.Actions)
	}
}

func TestSend_Notification_PlainBodyTreatedAsUnknown(t *testing.T) {
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// no body
	})
	a, err := New(Config{Server: cs.URL, Topic: "t", HTTPClient: cs.Client()})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "env-1", Kind: gateway.OutboundNotification, Title: "hi",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if r.Status != gateway.StatusUnknown {
		t.Errorf("status: %q want unknown", r.Status)
	}
	if r.ProviderRef != "" {
		t.Errorf("provider ref should be empty, got %q", r.ProviderRef)
	}
}

func TestSend_Notification_NonJSONBodyTreatedAsUnknown(t *testing.T) {
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	a, err := New(Config{Server: cs.URL, Topic: "t", HTTPClient: cs.Client()})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "env-1", Kind: gateway.OutboundNotification, Title: "hi",
	})
	if r.Status != gateway.StatusUnknown {
		t.Errorf("status: %q", r.Status)
	}
}

func TestSend_500_ReturnsFailed(t *testing.T) {
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("db unreachable"))
	})
	a, err := New(Config{Server: cs.URL, Topic: "t", HTTPClient: cs.Client()})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "env-1", Kind: gateway.OutboundNotification, Title: "hi",
	})
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if r.Status != gateway.StatusFailed {
		t.Errorf("status: %q", r.Status)
	}
	if !strings.Contains(r.Error, "500") || !strings.Contains(r.Error, "db unreachable") {
		t.Errorf("error: %q", r.Error)
	}
}

func TestSend_ConversationReply_Rejected(t *testing.T) {
	a, err := New(Config{Server: "https://x", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "env-1", Kind: gateway.OutboundConversationReply, Title: "x", Body: "y",
	})
	if err == nil {
		t.Fatal("expected error for ConversationReply")
	}
	if r.Status != gateway.StatusFailed {
		t.Errorf("status: %q", r.Status)
	}
}

func TestSend_ApprovalRequest_AttachesSignedActions(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ap-1"}`))
	})
	a, err := New(Config{
		Server:         cs.URL,
		Topic:          "carlos-test",
		HTTPClient:     cs.Client(),
		ActionEndpoint: "https://carlos.example/gateway/ntfy/action",
		SigningKey:     testKey,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	env := gateway.OutboundEnvelope{
		ID:         "env-42",
		Kind:       gateway.OutboundApprovalRequest,
		Title:      "Approve diff?",
		Body:       "Refactor foo.go",
		ArtifactID: "art-7",
		Actions:    gateway.CanonicalActions(),
		Urgency:    gateway.UrgencyDefault,
	}
	r, err := a.Send(context.Background(), env)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if r.Status != gateway.StatusDelivered {
		t.Errorf("status: %q", r.Status)
	}
	if r.ProviderRef != "ap-1" {
		t.Errorf("provider ref: %q", r.ProviderRef)
	}

	reqs := cs.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("requests: %d", len(reqs))
	}
	var body publishRequest
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Actions) != 3 {
		t.Fatalf("actions: %d want 3", len(body.Actions))
	}
	for i, act := range body.Actions {
		if act.Action != "http" {
			t.Errorf("action %d: type %q", i, act.Action)
		}
		if act.Method != http.MethodPost {
			t.Errorf("action %d: method %q", i, act.Method)
		}
		u, err := url.Parse(act.URL)
		if err != nil {
			t.Fatal(err)
		}
		if u.Host != "carlos.example" {
			t.Errorf("action %d: host %q", i, u.Host)
		}
		tok := u.Query().Get("t")
		if tok == "" {
			t.Errorf("action %d: missing token", i)
		}
		// Round-trip the token through verifyToken to confirm it
		// binds the right envelope/artifact/action.
		p, err := verifyToken(testKey, tok, now)
		if err != nil {
			t.Errorf("action %d: verify: %v", i, err)
		}
		if p.EnvelopeID != env.ID {
			t.Errorf("action %d: envelope %q", i, p.EnvelopeID)
		}
		if p.ArtifactID != env.ArtifactID {
			t.Errorf("action %d: artifact %q", i, p.ArtifactID)
		}
		if p.ActionID != env.Actions[i].ID {
			t.Errorf("action %d: action id %q", i, p.ActionID)
		}
		if p.ExpUnixMs != now.Add(defaultTokenTTL).UnixMilli() {
			t.Errorf("action %d: exp %d", i, p.ExpUnixMs)
		}
	}
}

func TestSend_ApprovalRequest_TruncatesActions(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	a, err := New(Config{
		Server:         cs.URL,
		Topic:          "t",
		HTTPClient:     cs.Client(),
		ActionEndpoint: "https://carlos.example/x",
		SigningKey:     testKey,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	env := gateway.OutboundEnvelope{
		ID: "env-1", Kind: gateway.OutboundApprovalRequest, Title: "x",
		ArtifactID: "art", Actions: []gateway.Action{
			{ID: "approve", Label: "Approve"},
			{ID: "revise", Label: "Revise"},
			{ID: "reject", Label: "Reject"},
			{ID: "extra1", Label: "Extra1"},
			{ID: "extra2", Label: "Extra2"},
		},
	}
	if _, err := a.Send(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	reqs := cs.snapshot()
	var body publishRequest
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Actions) != 3 {
		t.Errorf("actions: %d want 3", len(body.Actions))
	}
}

func TestSend_ApprovalRequest_MissingActionEndpoint(t *testing.T) {
	a, err := New(Config{Server: "https://x", Topic: "t", SigningKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "e", Kind: gateway.OutboundApprovalRequest, Title: "x",
		ArtifactID: "a", Actions: gateway.CanonicalActions(),
	})
	if err == nil || !strings.Contains(err.Error(), "ActionEndpoint") {
		t.Errorf("expected ActionEndpoint error, got %v", err)
	}
}

func TestSend_ApprovalRequest_MissingSigningKey(t *testing.T) {
	a, err := New(Config{
		Server:         "https://x",
		Topic:          "t",
		ActionEndpoint: "https://y/x",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "e", Kind: gateway.OutboundApprovalRequest, Title: "x",
		ArtifactID: "a", Actions: gateway.CanonicalActions(),
	})
	if !errors.Is(err, ErrSigningKeyTooShort) {
		t.Errorf("expected ErrSigningKeyTooShort, got %v", err)
	}
}

func TestSend_ApprovalRequest_MissingEnvelopeID(t *testing.T) {
	a, err := New(Config{
		Server: "https://x", Topic: "t",
		ActionEndpoint: "https://y/x", SigningKey: testKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundApprovalRequest, Title: "x",
		ArtifactID: "a", Actions: gateway.CanonicalActions(),
	})
	if err == nil || !strings.Contains(err.Error(), "envelope ID") {
		t.Errorf("expected envelope ID error, got %v", err)
	}
}

func TestSend_InvalidEnvelope_Rejected(t *testing.T) {
	a, err := New(Config{Server: "https://x", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Send(context.Background(), gateway.OutboundEnvelope{
		// Notification with empty title+body — Validate rejects.
		ID: "e", Kind: gateway.OutboundNotification,
	})
	if err == nil {
		t.Error("expected error from envelope Validate")
	}
}

func TestSend_BearerToken_Attached(t *testing.T) {
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	a, err := New(Config{
		Server: cs.URL, Topic: "t", Token: "secret-token",
		HTTPClient: cs.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "e", Kind: gateway.OutboundNotification, Title: "x",
	}); err != nil {
		t.Fatal(err)
	}
	got := cs.snapshot()[0].Headers.Get("Authorization")
	if got != "Bearer secret-token" {
		t.Errorf("auth header: %q", got)
	}
}

func TestSend_PriorityMap_Default(t *testing.T) {
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	a, err := New(Config{Server: cs.URL, Topic: "t", HTTPClient: cs.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "e", Kind: gateway.OutboundNotification, Title: "x",
		Urgency: gateway.UrgencyHigh,
	}); err != nil {
		t.Fatal(err)
	}
	var body publishRequest
	if err := json.Unmarshal(cs.snapshot()[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	// PriorityMap nil → fallback 3.
	if body.Priority != 3 {
		t.Errorf("priority: %d want 3", body.Priority)
	}
}

func TestSend_PriorityMap_MissingKey(t *testing.T) {
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	a, err := New(Config{
		Server: cs.URL, Topic: "t", HTTPClient: cs.Client(),
		// Map omits "high" — adapter should fall back to 3.
		PriorityMap: map[string]int{"low": 1, "default": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "e", Kind: gateway.OutboundNotification, Title: "x",
		Urgency: gateway.UrgencyHigh,
	}); err != nil {
		t.Fatal(err)
	}
	var body publishRequest
	if err := json.Unmarshal(cs.snapshot()[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Priority != 3 {
		t.Errorf("priority: %d want 3 fallback", body.Priority)
	}
}

func TestSend_TransportError(t *testing.T) {
	// Build a server then close it so the next dial fails.
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {})
	a, err := New(Config{Server: cs.URL, Topic: "t", HTTPClient: cs.Client()})
	if err != nil {
		t.Fatal(err)
	}
	cs.Server.Close()
	r, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "e", Kind: gateway.OutboundNotification, Title: "x",
	})
	if err == nil {
		t.Fatal("expected transport error")
	}
	if r.Status != gateway.StatusFailed {
		t.Errorf("status: %q", r.Status)
	}
}

func TestSend_BadServerURL_FailsAtRequest(t *testing.T) {
	a, err := New(Config{Server: "https://valid.example", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	// Force a bad URL post-construction by swapping the field.
	a.cfg.Server = "://bad"
	r, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID: "e", Kind: gateway.OutboundNotification, Title: "x",
	})
	if err == nil {
		t.Fatal("expected error from http.NewRequest")
	}
	if r.Status != gateway.StatusFailed {
		t.Errorf("status: %q", r.Status)
	}
}

func TestSend_ContextCancelled(t *testing.T) {
	cs := newCapturingServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Slow respond so we hit ctx cancellation.
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	})
	a, err := New(Config{Server: cs.URL, Topic: "t", HTTPClient: cs.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := a.Send(ctx, gateway.OutboundEnvelope{
		ID: "e", Kind: gateway.OutboundNotification, Title: "x",
	})
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if r.Status != gateway.StatusFailed {
		t.Errorf("status: %q", r.Status)
	}
}

func TestStartStop_Lifecycle(t *testing.T) {
	a, err := New(Config{Server: "https://x", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- a.Start(ctx, func(context.Context, gateway.InboundEnvelope) error { return nil })
	}()
	// Wait briefly for the goroutine to enter Start.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if a.currentIngest() != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if a.currentIngest() == nil {
		t.Fatal("ingest not wired")
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Errorf("stop: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("start returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after Stop")
	}
	// Idempotent.
	if err := a.Stop(context.Background()); err != nil {
		t.Errorf("second stop: %v", err)
	}
}

func TestStart_DoubleStart_Errors(t *testing.T) {
	a, err := New(Config{Server: "https://x", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Start(ctx, func(context.Context, gateway.InboundEnvelope) error { return nil }) }()
	// Wait for first Start to register.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if a.currentIngest() != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := a.Start(ctx, func(context.Context, gateway.InboundEnvelope) error { return nil }); err == nil {
		t.Error("expected double-start error")
	}
}

func TestStart_ContextCancellation(t *testing.T) {
	a, err := New(Config{Server: "https://x", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- a.Start(ctx, func(context.Context, gateway.InboundEnvelope) error { return nil })
	}()
	// Wait for goroutine to enter Start.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if a.currentIngest() != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != ctx.Err() {
			t.Errorf("Start returned %v want ctx.Err", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return on ctx cancel")
	}
}

func TestWithTokenQuery_PreservesExistingQuery(t *testing.T) {
	got, err := withTokenQuery("https://carlos.example/x?foo=bar", "tok123")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("foo") != "bar" {
		t.Errorf("foo lost: %q", got)
	}
	if u.Query().Get("t") != "tok123" {
		t.Errorf("t missing: %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 100); got != "hello" {
		t.Errorf("no truncation: %q", got)
	}
	if got := truncate("hello world", 5); got != "hello..." {
		t.Errorf("truncation: %q", got)
	}
}
