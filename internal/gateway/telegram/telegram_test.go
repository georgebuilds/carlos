package telegram_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/gateway/telegram"
)

// fakeBotAPI is a tiny httptest-backed stub of the Telegram Bot API. It
// records every inbound request and replies according to per-method
// queues the test sets up.
type fakeBotAPI struct {
	t      *testing.T
	server *httptest.Server

	mu sync.Mutex

	// per-method captured request bodies (raw JSON). Tests inspect
	// these to assert on the wire shape.
	sentMessages    []json.RawMessage
	getUpdatesCalls []json.RawMessage
	answerCalls     []json.RawMessage

	// updates is the queue of getUpdates responses. Each Pop returns
	// the next entry; if the queue is empty, getUpdates blocks
	// briefly and returns an empty list to mimic long-poll idle.
	updates [][]map[string]any

	// sendBehavior controls how sendMessage responds. Pop-based queue
	// so each test sequences exactly the responses it expects.
	sendBehavior []sendResponse

	answerOK bool
}

type sendResponse struct {
	httpStatus  int
	apiResponse map[string]any
	// retryAfterHeader populates the Retry-After header on the wire.
	retryAfterHeader string
}

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	f := &fakeBotAPI{t: t, answerOK: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/", f.handle)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeBotAPI) BaseURL() string { return f.server.URL }

func (f *fakeBotAPI) Client() *http.Client { return f.server.Client() }

// QueueUpdates appends a batch the next getUpdates call returns.
func (f *fakeBotAPI) QueueUpdates(batch []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, batch)
}

// QueueSendResponse appends a sendMessage response.
func (f *fakeBotAPI) QueueSendResponse(r sendResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendBehavior = append(f.sendBehavior, r)
}

func (f *fakeBotAPI) SentMessages() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]json.RawMessage, len(f.sentMessages))
	copy(out, f.sentMessages)
	return out
}

func (f *fakeBotAPI) AnswerCalls() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]json.RawMessage, len(f.answerCalls))
	copy(out, f.answerCalls)
	return out
}

func (f *fakeBotAPI) handle(w http.ResponseWriter, r *http.Request) {
	// URL shape: /bot<token>/<method>
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) != 2 {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	method := parts[1]

	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()

	switch method {
	case "sendMessage":
		f.mu.Lock()
		f.sentMessages = append(f.sentMessages, body)
		var resp sendResponse
		if len(f.sendBehavior) > 0 {
			resp = f.sendBehavior[0]
			f.sendBehavior = f.sendBehavior[1:]
		} else {
			// Default: ok with synthetic message_id.
			resp = sendResponse{
				httpStatus: http.StatusOK,
				apiResponse: map[string]any{
					"ok": true,
					"result": map[string]any{
						"message_id": 100 + len(f.sentMessages),
						"chat":       map[string]any{"id": int64(123)},
					},
				},
			}
		}
		f.mu.Unlock()
		writeResponse(w, resp)
	case "getUpdates":
		f.mu.Lock()
		f.getUpdatesCalls = append(f.getUpdatesCalls, body)
		var batch []map[string]any
		if len(f.updates) > 0 {
			batch = f.updates[0]
			f.updates = f.updates[1:]
		}
		f.mu.Unlock()
		writeResponse(w, sendResponse{
			httpStatus: http.StatusOK,
			apiResponse: map[string]any{
				"ok":     true,
				"result": batch,
			},
		})
	case "answerCallbackQuery":
		f.mu.Lock()
		f.answerCalls = append(f.answerCalls, body)
		ok := f.answerOK
		f.mu.Unlock()
		writeResponse(w, sendResponse{
			httpStatus: http.StatusOK,
			apiResponse: map[string]any{
				"ok":     ok,
				"result": true,
			},
		})
	default:
		http.Error(w, "unknown method "+method, http.StatusNotFound)
	}
}

func writeResponse(w http.ResponseWriter, r sendResponse) {
	if r.retryAfterHeader != "" {
		w.Header().Set("Retry-After", r.retryAfterHeader)
	}
	w.Header().Set("Content-Type", "application/json")
	status := r.httpStatus
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(r.apiResponse)
}

// newTestAdapter builds an Adapter wired to fake, with the rate limiter
// disabled so tests don't drag.
func newTestAdapter(t *testing.T, fake *fakeBotAPI, allowed ...int64) *telegram.Adapter {
	t.Helper()
	if len(allowed) == 0 {
		allowed = []int64{123}
	}
	a, err := telegram.New(telegram.Config{
		BotToken:       "test-token",
		APIBaseURL:     fake.BaseURL(),
		AllowedChatIDs: allowed,
		ParseMode:      "MarkdownV2",
		PollTimeoutSec: 1,
		HTTPClient:     fake.Client(),
		Now:            func() time.Time { return time.Unix(1700000000, 0).UTC() },
		Logger:         log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestNew_RequiresBotToken(t *testing.T) {
	_, err := telegram.New(telegram.Config{})
	if err == nil {
		t.Fatal("expected error for missing bot token")
	}
}

func TestNew_DefaultsApplied(t *testing.T) {
	a, err := telegram.New(telegram.Config{BotToken: "x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Name() != gateway.SourceTelegram {
		t.Errorf("Name: %q", a.Name())
	}
	caps := a.OutboundCapabilities()
	if !caps.Push || !caps.FixedChoiceHITL || !caps.FreeFormTextInbound || !caps.FileImageInbound {
		t.Errorf("OutboundCapabilities missing expected bits: %+v", caps)
	}
	if caps.DiffRichApproval {
		t.Errorf("OutboundCapabilities: DiffRichApproval should be false for Telegram")
	}
	if caps.MaxActions != 3 {
		t.Errorf("MaxActions = %d, want 3", caps.MaxActions)
	}
	if caps.NeedsPublicEndpoint {
		t.Errorf("OutboundCapabilities: NeedsPublicEndpoint should be false")
	}
}

func TestSend_Notification_OK(t *testing.T) {
	fake := newFakeBotAPI(t)
	a := newTestAdapter(t, fake)

	env := gateway.OutboundEnvelope{
		ID:        "env-1",
		Kind:      gateway.OutboundNotification,
		Title:     "carlos pinged!",
		Body:      "task complete.",
		Urgency:   gateway.UrgencyLow,
		CreatedAt: time.Unix(1700000000, 0).UTC(),
	}
	r, err := a.Send(context.Background(), env)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if r.Status != gateway.StatusDelivered {
		t.Errorf("Status = %q, want delivered", r.Status)
	}
	if r.ProviderRef == "" {
		t.Error("ProviderRef should be the message_id")
	}
	if r.Source != gateway.SourceTelegram {
		t.Errorf("Source = %q", r.Source)
	}

	// Inspect the wire payload.
	sent := fake.SentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	var payload map[string]any
	if err := json.Unmarshal(sent[0], &payload); err != nil {
		t.Fatalf("unmarshal sent: %v", err)
	}
	if payload["chat_id"].(float64) != 123 {
		t.Errorf("chat_id = %v, want 123", payload["chat_id"])
	}
	if payload["parse_mode"] != "MarkdownV2" {
		t.Errorf("parse_mode = %v", payload["parse_mode"])
	}
	if payload["disable_notification"] != true {
		t.Errorf("low urgency should disable notification, got %v", payload["disable_notification"])
	}
	text := payload["text"].(string)
	// Title bolded + body, both with periods escaped.
	if !strings.Contains(text, "*carlos pinged\\!*") {
		t.Errorf("text missing bolded escaped title: %q", text)
	}
	if !strings.Contains(text, "task complete\\.") {
		t.Errorf("text missing escaped body: %q", text)
	}
	if _, hasMarkup := payload["reply_markup"]; hasMarkup {
		t.Error("notification should not include reply_markup")
	}
}

func TestSend_ApprovalRequest_RendersThreeButtons(t *testing.T) {
	fake := newFakeBotAPI(t)
	a := newTestAdapter(t, fake)

	env := gateway.OutboundEnvelope{
		ID:         "env-2",
		Kind:       gateway.OutboundApprovalRequest,
		Title:      "review patch",
		Body:       "apply diff to main.go?",
		Actions:    gateway.CanonicalActions(),
		ArtifactID: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}
	r, err := a.Send(context.Background(), env)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if r.Status != gateway.StatusDelivered {
		t.Errorf("Status = %q", r.Status)
	}

	sent := fake.SentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	var payload map[string]any
	if err := json.Unmarshal(sent[0], &payload); err != nil {
		t.Fatalf("unmarshal sent: %v", err)
	}
	markup, ok := payload["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("missing reply_markup: %+v", payload)
	}
	kb := markup["inline_keyboard"].([]any)
	if len(kb) != 3 {
		t.Fatalf("want 3 keyboard rows got %d", len(kb))
	}
	// Each row has one button; the buttons should match the canonical
	// approve / revise / reject set with our artifact id baked into
	// callback_data.
	want := []string{"approve", "revise", "reject"}
	for i, row := range kb {
		btns := row.([]any)
		if len(btns) != 1 {
			t.Errorf("row %d has %d buttons, want 1", i, len(btns))
			continue
		}
		btn := btns[0].(map[string]any)
		data := btn["callback_data"].(string)
		action, artifact, derr := telegram.DecodeCallbackData(data)
		if derr != nil {
			t.Errorf("row %d decode: %v", i, derr)
			continue
		}
		if action != want[i] {
			t.Errorf("row %d action: want %q got %q", i, want[i], action)
		}
		if artifact != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
			t.Errorf("row %d artifact: got %q", i, artifact)
		}
	}
}

func TestSend_ConversationReply_NoKeyboard(t *testing.T) {
	fake := newFakeBotAPI(t)
	a := newTestAdapter(t, fake)

	env := gateway.OutboundEnvelope{
		ID:   "env-3",
		Kind: gateway.OutboundConversationReply,
		Body: "absolutely, here's that summary you asked for.",
	}
	r, err := a.Send(context.Background(), env)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if r.Status != gateway.StatusDelivered {
		t.Errorf("Status = %q", r.Status)
	}
	sent := fake.SentMessages()
	if len(sent) != 1 {
		t.Fatal("expected 1 message")
	}
	var payload map[string]any
	_ = json.Unmarshal(sent[0], &payload)
	if _, hasMarkup := payload["reply_markup"]; hasMarkup {
		t.Error("conversation reply should not include reply_markup")
	}
}

func TestSend_NoAllowedChat_Fails(t *testing.T) {
	fake := newFakeBotAPI(t)
	a, err := telegram.New(telegram.Config{
		BotToken:   "t",
		APIBaseURL: fake.BaseURL(),
		HTTPClient: fake.Client(),
		Logger:     log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind:  gateway.OutboundNotification,
		Title: "hi",
	})
	if err == nil {
		t.Fatal("expected error when no chat configured")
	}
	if r.Status != gateway.StatusFailed {
		t.Errorf("Status = %q", r.Status)
	}
}

func TestSend_APIError_SurfacesAsFailed(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueSendResponse(sendResponse{
		httpStatus: http.StatusOK,
		apiResponse: map[string]any{
			"ok":          false,
			"error_code":  400,
			"description": "Bad Request: chat not found",
		},
	})
	a := newTestAdapter(t, fake)

	r, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "hi",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if r.Status != gateway.StatusFailed {
		t.Errorf("Status = %q", r.Status)
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error missing api description: %v", err)
	}
}

func TestSend_RateLimited_429WithRetryAfter(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueSendResponse(sendResponse{
		httpStatus: http.StatusTooManyRequests,
		apiResponse: map[string]any{
			"ok":          false,
			"error_code":  429,
			"description": "Too Many Requests: retry after 7",
			"parameters":  map[string]any{"retry_after": 7},
		},
		retryAfterHeader: "7",
	})
	a := newTestAdapter(t, fake)
	r, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "hi",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if r.Status != gateway.StatusFailed {
		t.Errorf("Status = %q", r.Status)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should contain 429: %v", err)
	}
	if !strings.Contains(err.Error(), "retry after 7") {
		t.Errorf("error should surface retry-after: %v", err)
	}
}

func TestSend_RateLimited_HeaderFallback(t *testing.T) {
	// Telegram sometimes returns 429 without parameters.retry_after but
	// with the Retry-After header set. Make sure we still parse it.
	fake := newFakeBotAPI(t)
	fake.QueueSendResponse(sendResponse{
		httpStatus: http.StatusTooManyRequests,
		apiResponse: map[string]any{
			"ok":          false,
			"error_code":  429,
			"description": "Too Many Requests",
		},
		retryAfterHeader: "3",
	})
	a := newTestAdapter(t, fake)
	_, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "hi",
	})
	if err == nil || !strings.Contains(err.Error(), "retry after 3") {
		t.Errorf("expected retry-after 3 from header, got %v", err)
	}
}

func TestSend_RateBucket_SerializesCalls(t *testing.T) {
	// Two back-to-back sends with a 100ms bucket should take at least
	// the bucket interval. Exact timing is fragile in CI, so we assert
	// only a lower bound.
	fake := newFakeBotAPI(t)
	a, err := telegram.New(telegram.Config{
		BotToken:          "t",
		APIBaseURL:        fake.BaseURL(),
		AllowedChatIDs:    []int64{1},
		HTTPClient:        fake.Client(),
		RateLimitInterval: 50 * time.Millisecond,
		Logger:            log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	start := time.Now()
	_, _ = a.Send(context.Background(), gateway.OutboundEnvelope{Kind: gateway.OutboundNotification, Title: "1"})
	_, _ = a.Send(context.Background(), gateway.OutboundEnvelope{Kind: gateway.OutboundNotification, Title: "2"})
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Errorf("rate bucket did not delay: elapsed=%s", elapsed)
	}
}

func TestSend_RateBucket_RespectsCancel(t *testing.T) {
	fake := newFakeBotAPI(t)
	a, err := telegram.New(telegram.Config{
		BotToken:          "t",
		APIBaseURL:        fake.BaseURL(),
		AllowedChatIDs:    []int64{1},
		HTTPClient:        fake.Client(),
		RateLimitInterval: 5 * time.Second,
		Logger:            log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Burn one slot so the next one has to wait.
	_, _ = a.Send(context.Background(), gateway.OutboundEnvelope{Kind: gateway.OutboundNotification, Title: "1"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = a.Send(ctx, gateway.OutboundEnvelope{Kind: gateway.OutboundNotification, Title: "2"})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestSend_EmptyTextRejected(t *testing.T) {
	fake := newFakeBotAPI(t)
	a := newTestAdapter(t, fake)
	_, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification,
	})
	if err == nil {
		t.Fatal("expected error for empty title+body")
	}
}

func TestSend_ApprovalRequest_MissingArtifactRejected(t *testing.T) {
	fake := newFakeBotAPI(t)
	a := newTestAdapter(t, fake)
	_, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind:    gateway.OutboundApprovalRequest,
		Title:   "x",
		Actions: gateway.CanonicalActions(),
		// ArtifactID intentionally empty.
	})
	if err == nil {
		t.Fatal("expected error for missing artifact id")
	}
}

func TestSend_ApprovalRequest_TooLongArtifactRejected(t *testing.T) {
	fake := newFakeBotAPI(t)
	a := newTestAdapter(t, fake)
	_, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind:       gateway.OutboundApprovalRequest,
		Title:      "x",
		Actions:    gateway.CanonicalActions(),
		ArtifactID: strings.Repeat("a", 100),
	})
	if err == nil {
		t.Fatal("expected error for too-long artifact id")
	}
}

// --- inbound (Start) tests -------------------------------------------------

// runStart spins Start in a goroutine and returns a stop func + an
// ingest accumulator the test can drain.
func runStart(t *testing.T, a *telegram.Adapter) (stop func(), in *ingestSink) {
	t.Helper()
	in = newIngestSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Start(ctx, in.ingest)
	}()
	stop = func() {
		cancel()
		_ = a.Stop(context.Background())
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Start did not return")
		}
	}
	return stop, in
}

type ingestSink struct {
	mu       sync.Mutex
	count    int32
	received []gateway.InboundEnvelope
	errOn    map[string]error
}

func newIngestSink() *ingestSink {
	return &ingestSink{errOn: map[string]error{}}
}

func (s *ingestSink) ingest(_ context.Context, env gateway.InboundEnvelope) error {
	atomic.AddInt32(&s.count, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.errOn[env.GatewayEventID]; ok {
		return err
	}
	s.received = append(s.received, env)
	return nil
}

func (s *ingestSink) Count() int { return int(atomic.LoadInt32(&s.count)) }

func (s *ingestSink) Received() []gateway.InboundEnvelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]gateway.InboundEnvelope, len(s.received))
	copy(out, s.received)
	return out
}

func (s *ingestSink) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.Received()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waited 2s for %d ingest, only got %d", n, len(s.Received()))
}

func TestStart_TextMessage_BecomesInboundMessage(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 42,
			"message": map[string]any{
				"message_id": 1,
				"text":       "hello carlos",
				"chat":       map[string]any{"id": 123, "type": "private"},
				"from":       map[string]any{"id": 999, "username": "george"},
			},
		},
	})
	a := newTestAdapter(t, fake)
	stop, sink := runStart(t, a)
	defer stop()

	sink.waitFor(t, 1)
	got := sink.Received()[0]
	if got.Kind != gateway.InboundMessage {
		t.Errorf("Kind = %q, want message", got.Kind)
	}
	if got.Body != "hello carlos" {
		t.Errorf("Body = %q", got.Body)
	}
	if got.GatewayEventID != "42" {
		t.Errorf("GatewayEventID = %q, want 42", got.GatewayEventID)
	}
	if got.Source != gateway.SourceTelegram {
		t.Errorf("Source = %q", got.Source)
	}
	if got.From != "123" {
		t.Errorf("From = %q, want chat id", got.From)
	}
}

func TestStart_CallbackQuery_BecomesInboundDecision(t *testing.T) {
	fake := newFakeBotAPI(t)
	cbData, err := telegram.EncodeCallbackData("approve", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 7,
			"callback_query": map[string]any{
				"id":   "cb-1",
				"from": map[string]any{"id": 999},
				"message": map[string]any{
					"message_id": 1,
					"chat":       map[string]any{"id": 123, "type": "private"},
				},
				"data": cbData,
			},
		},
	})
	a := newTestAdapter(t, fake)
	stop, sink := runStart(t, a)
	defer stop()

	sink.waitFor(t, 1)
	got := sink.Received()[0]
	if got.Kind != gateway.InboundDecision {
		t.Errorf("Kind = %q, want decision", got.Kind)
	}
	if got.ArtifactID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
		t.Errorf("ArtifactID = %q", got.ArtifactID)
	}
	if got.Decision == nil || got.Decision.Kind != gateway.DecisionApprove {
		t.Errorf("Decision = %+v", got.Decision)
	}
	if got.GatewayEventID != "7" {
		t.Errorf("GatewayEventID = %q", got.GatewayEventID)
	}

	// The adapter should also have fired answerCallbackQuery for the
	// tapped button.
	waitFor(t, 2*time.Second, func() bool {
		return len(fake.AnswerCalls()) > 0
	}, "answerCallbackQuery never fired")
	var ans map[string]any
	if err := json.Unmarshal(fake.AnswerCalls()[0], &ans); err != nil {
		t.Fatalf("unmarshal answer: %v", err)
	}
	if ans["callback_query_id"] != "cb-1" {
		t.Errorf("answer callback id = %v", ans["callback_query_id"])
	}
}

func TestStart_NonWhitelistedChat_Dropped(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 99,
			"message": map[string]any{
				"message_id": 1,
				"text":       "stranger",
				"chat":       map[string]any{"id": 555}, // not whitelisted
			},
		},
	})
	a := newTestAdapter(t, fake) // allowed = {123}
	stop, sink := runStart(t, a)
	defer stop()

	// Wait a beat to let the poll consume the update.
	time.Sleep(200 * time.Millisecond)
	if c := sink.Count(); c != 0 {
		t.Errorf("ingest fired %d times, want 0", c)
	}
}

func TestStart_NonWhitelistedCallback_Dropped(t *testing.T) {
	fake := newFakeBotAPI(t)
	cb, _ := telegram.EncodeCallbackData("approve", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 100,
			"callback_query": map[string]any{
				"id":   "cb-stranger",
				"from": map[string]any{"id": 1},
				"message": map[string]any{
					"message_id": 1,
					"chat":       map[string]any{"id": 555},
				},
				"data": cb,
			},
		},
	})
	a := newTestAdapter(t, fake)
	stop, sink := runStart(t, a)
	defer stop()

	time.Sleep(200 * time.Millisecond)
	if c := sink.Count(); c != 0 {
		t.Errorf("ingest fired %d times, want 0", c)
	}
}

func TestStart_MalformedCallbackData_Dropped(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 50,
			"callback_query": map[string]any{
				"id":   "cb-bad",
				"from": map[string]any{"id": 1},
				"message": map[string]any{
					"message_id": 1,
					"chat":       map[string]any{"id": 123},
				},
				"data": "no-separator-here",
			},
		},
	})
	a := newTestAdapter(t, fake)
	stop, sink := runStart(t, a)
	defer stop()

	time.Sleep(200 * time.Millisecond)
	if c := sink.Count(); c != 0 {
		t.Errorf("ingest fired %d times, want 0 for malformed callback", c)
	}
	// But the answer should still fire.
	waitFor(t, 2*time.Second, func() bool {
		return len(fake.AnswerCalls()) > 0
	}, "answer didn't fire on malformed callback")
}

func TestStart_UnknownActionInCallback_Dropped(t *testing.T) {
	fake := newFakeBotAPI(t)
	// Encode a non-canonical action; the decoder accepts it but the
	// adapter must drop it because it's not a Decision kind.
	cb, err := telegram.EncodeCallbackData("noop", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 60,
			"callback_query": map[string]any{
				"id":   "cb-unknown",
				"from": map[string]any{"id": 1},
				"message": map[string]any{
					"message_id": 1,
					"chat":       map[string]any{"id": 123},
				},
				"data": cb,
			},
		},
	})
	a := newTestAdapter(t, fake)
	stop, sink := runStart(t, a)
	defer stop()

	time.Sleep(200 * time.Millisecond)
	if c := sink.Count(); c != 0 {
		t.Errorf("ingest fired %d times, want 0", c)
	}
}

func TestStart_EmptyCallbackData_Dropped(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 61,
			"callback_query": map[string]any{
				"id":   "cb-empty",
				"from": map[string]any{"id": 1},
				"message": map[string]any{
					"message_id": 1,
					"chat":       map[string]any{"id": 123},
				},
				"data": "",
			},
		},
	})
	a := newTestAdapter(t, fake)
	stop, sink := runStart(t, a)
	defer stop()
	time.Sleep(200 * time.Millisecond)
	if c := sink.Count(); c != 0 {
		t.Errorf("ingest fired %d times, want 0 for empty data", c)
	}
}

func TestStart_MessageMissingChatID_Dropped(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 70,
			"message": map[string]any{
				"message_id": 1,
				"text":       "hi",
				// chat omitted entirely.
			},
		},
	})
	a := newTestAdapter(t, fake)
	stop, sink := runStart(t, a)
	defer stop()
	time.Sleep(200 * time.Millisecond)
	if c := sink.Count(); c != 0 {
		t.Errorf("ingest fired %d times, want 0", c)
	}
}

func TestStart_EmptyTextMessage_Dropped(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 71,
			"message": map[string]any{
				"message_id": 1,
				"text":       "",
				"chat":       map[string]any{"id": 123},
			},
		},
	})
	a := newTestAdapter(t, fake)
	stop, sink := runStart(t, a)
	defer stop()
	time.Sleep(200 * time.Millisecond)
	if c := sink.Count(); c != 0 {
		t.Errorf("ingest fired %d times, want 0 for empty text", c)
	}
}

func TestStart_UnknownUpdateType_Dropped(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 80,
			// neither message nor callback_query.
		},
	})
	a := newTestAdapter(t, fake)
	stop, sink := runStart(t, a)
	defer stop()
	time.Sleep(200 * time.Millisecond)
	if c := sink.Count(); c != 0 {
		t.Errorf("ingest fired %d times, want 0", c)
	}
}

func TestStart_OffsetAdvances(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 200,
			"message": map[string]any{
				"message_id": 1,
				"text":       "first",
				"chat":       map[string]any{"id": 123},
			},
		},
	})
	a := newTestAdapter(t, fake)
	stop, sink := runStart(t, a)
	defer stop()
	sink.waitFor(t, 1)

	// Wait for the next getUpdates call after the one that returned the
	// update — its offset should be 201.
	waitFor(t, 2*time.Second, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.getUpdatesCalls) >= 2
	}, "did not see second getUpdates call")

	fake.mu.Lock()
	last := fake.getUpdatesCalls[len(fake.getUpdatesCalls)-1]
	fake.mu.Unlock()
	var req map[string]any
	if err := json.Unmarshal(last, &req); err != nil {
		t.Fatalf("unmarshal getUpdates: %v", err)
	}
	if off := req["offset"]; off == nil || off.(float64) != 201 {
		t.Errorf("offset on second call = %v, want 201", off)
	}
}

func TestStart_StopReturnsCleanly(t *testing.T) {
	fake := newFakeBotAPI(t)
	a := newTestAdapter(t, fake)
	in := newIngestSink()
	ctx := context.Background()
	done := make(chan error, 1)
	go func() {
		done <- a.Start(ctx, in.ingest)
	}()
	// Give Start a moment to land in its first poll.
	time.Sleep(50 * time.Millisecond)
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
	// Stop is idempotent.
	if err := a.Stop(context.Background()); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestStart_CtxCancelStops(t *testing.T) {
	fake := newFakeBotAPI(t)
	a := newTestAdapter(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- a.Start(ctx, func(context.Context, gateway.InboundEnvelope) error { return nil })
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned %v on cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

func TestStart_NilIngestRejected(t *testing.T) {
	fake := newFakeBotAPI(t)
	a := newTestAdapter(t, fake)
	if err := a.Start(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil ingest")
	}
}

func TestStart_MalformedJSONResponse_Recovers(t *testing.T) {
	// Stand a server that returns garbage on the first getUpdates and
	// then well-formed data. The adapter should log the first failure
	// and keep polling.
	hits := int64(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "getUpdates") {
			if n == 1 {
				_, _ = w.Write([]byte("{not json"))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": []map[string]any{
					{
						"update_id": 1,
						"message": map[string]any{
							"message_id": 1,
							"text":       "recovered",
							"chat":       map[string]any{"id": 123},
						},
					},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	defer server.Close()

	a, err := telegram.New(telegram.Config{
		BotToken:       "t",
		APIBaseURL:     server.URL,
		AllowedChatIDs: []int64{123},
		HTTPClient:     server.Client(),
		PollTimeoutSec: 1,
		Logger:         log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stop, sink := runStart(t, a)
	defer stop()

	sink.waitFor(t, 1)
}

func TestSend_NetworkError_SurfacesAsFailed(t *testing.T) {
	// Point the adapter at an unreachable address; the http client
	// should surface a dial error which the adapter wraps.
	a, err := telegram.New(telegram.Config{
		BotToken:       "t",
		APIBaseURL:     "http://127.0.0.1:1", // refused
		AllowedChatIDs: []int64{1},
		HTTPClient:     &http.Client{Timeout: 200 * time.Millisecond},
		Logger:         log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "hi",
	})
	if err == nil {
		t.Fatal("expected network error")
	}
	if r.Status != gateway.StatusFailed {
		t.Errorf("Status = %q", r.Status)
	}
}

func TestSend_NoParseMode_PlainText(t *testing.T) {
	fake := newFakeBotAPI(t)
	a, err := telegram.New(telegram.Config{
		BotToken:       "t",
		APIBaseURL:     fake.BaseURL(),
		AllowedChatIDs: []int64{123},
		ParseMode:      "none-of-the-above", // not MarkdownV2
		HTTPClient:     fake.Client(),
		Logger:         log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "hi.", Body: "body.",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	var payload map[string]any
	_ = json.Unmarshal(fake.SentMessages()[0], &payload)
	text := payload["text"].(string)
	// No escaping should happen because parse_mode is not MarkdownV2.
	if !strings.Contains(text, "hi.") || strings.Contains(text, "hi\\.") {
		t.Errorf("expected plain text, got %q", text)
	}
}

func TestStart_IngestErrorLogged_LoopContinues(t *testing.T) {
	fake := newFakeBotAPI(t)
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 1,
			"message": map[string]any{
				"message_id": 1,
				"text":       "first",
				"chat":       map[string]any{"id": 123},
			},
		},
	})
	fake.QueueUpdates([]map[string]any{
		{
			"update_id": 2,
			"message": map[string]any{
				"message_id": 2,
				"text":       "second",
				"chat":       map[string]any{"id": 123},
			},
		},
	})
	a := newTestAdapter(t, fake)
	in := newIngestSink()
	in.errOn["1"] = errors.New("dropped")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Start(ctx, in.ingest) }()

	// The first update produces a count++ but ingestion returns an
	// error and the envelope is NOT appended to received. The second
	// update should still make it through.
	waitFor(t, 3*time.Second, func() bool {
		return len(in.Received()) >= 1 && in.Count() >= 2
	}, "second update never ingested")
	_ = a.Stop(context.Background())
}

// TestSend_OversizedResponse_RejectedCleanly stands up a server that
// streams a body well above the 16 MiB cap and asserts the adapter
// fails the Send instead of buffering the whole stream into memory.
// Without the io.LimitReader wrap a hostile/buggy upstream could OOM
// the daemon by writing forever.
func TestSend_OversizedResponse_RejectedCleanly(t *testing.T) {
	// 17 MiB of filler - just above the 16 MiB cap. We use a fixed
	// size (not infinite) so the test still terminates if the cap
	// regresses; the adapter must still error because it sees > cap
	// bytes before EOF.
	const bodyBytes = 17 * 1024 * 1024
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Write a giant blob; valid JSON wrapper is irrelevant because
		// the adapter should error on the size check before decoding.
		buf := make([]byte, 64*1024)
		for i := range buf {
			buf[i] = 'a'
		}
		written := 0
		for written < bodyBytes {
			n, err := w.Write(buf)
			if err != nil {
				return
			}
			written += n
		}
	}))
	defer server.Close()

	a, err := telegram.New(telegram.Config{
		BotToken:       "t",
		APIBaseURL:     server.URL,
		AllowedChatIDs: []int64{1},
		HTTPClient:     server.Client(),
		Logger:         log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		Kind: gateway.OutboundNotification, Title: "hi",
	})
	if err == nil {
		t.Fatal("expected error from oversized response, got nil")
	}
	if r.Status != gateway.StatusFailed {
		t.Errorf("Status = %q, want failed", r.Status)
	}
	if !strings.Contains(err.Error(), "cap") && !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention the cap, got %v", err)
	}
}

// TestNew_DefaultHTTPClient_NotDefaultClient asserts the adapter does
// not adopt http.DefaultClient (which has Timeout=0 and would let a
// hung TCP connection wedge the long-poll goroutine forever) when the
// caller leaves cfg.HTTPClient nil. We test the property indirectly:
// point Send at a raw TCP listener that accepts but never writes, then
// confirm Send returns via the request-scoped context deadline rather
// than blocking past it. With the old http.DefaultClient code the
// request would only end when the client *transport* gave up - which
// for a 0-Timeout client effectively means "never" unless the per-call
// context is honored, but historically httpc.Do respects ctx so this
// test is really pinning the layered defense: even if a future refactor
// drops the per-call context, the client-level Timeout must kick in.
func TestNew_DefaultHTTPClient_NotDefaultClient(t *testing.T) {
	// Raw TCP listener: accept connections and hold them open without
	// ever writing a response. Unlike httptest.Server, Close() here
	// doesn't wait for handler goroutines to drain.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the conn; the deferred ln.Close will trip Accept and
			// these conns leak briefly but are cleaned up at process exit.
			_ = c
		}
	}()

	a, err := telegram.New(telegram.Config{
		BotToken:       "x",
		APIBaseURL:     "http://" + ln.Addr().String(),
		AllowedChatIDs: []int64{1},
		PollTimeoutSec: 1,
		Logger:         log.New(io.Discard, "", 0),
		// HTTPClient intentionally nil - we're testing the default.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Bound Send with a short context. The assertion is that Send
	// returns rather than blocking; a regression that wires up
	// http.DefaultClient + drops ctx honoring would hang past the
	// outer 3s deadline below.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.Send(ctx, gateway.OutboundEnvelope{
			Kind: gateway.OutboundNotification, Title: "hi",
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from hanging server")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Send blocked past context deadline - default client likely missing Timeout")
	}
}

// waitFor polls cond every 10ms until it returns true or deadline
// elapses. Used because the long-poll loop runs concurrently and we
// can't synchronize on it cleanly.
func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

