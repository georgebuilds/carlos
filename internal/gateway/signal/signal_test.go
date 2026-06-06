package signal

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
)

func fixedNow() time.Time { return time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC) }

func TestNewDisabledDefaultsAreUsable(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatalf("New on zero Config: unexpected error %v", err)
	}
	if got := a.Name(); got != gateway.SourceSignal {
		t.Fatalf("Name() = %q, want %q", got, gateway.SourceSignal)
	}
}

func TestCapabilitiesShape(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	caps := a.OutboundCapabilities()
	want := gateway.OutboundCapabilities{
		Push:                true,
		FixedChoiceHITL:     true,
		MaxActions:          3,
		FreeFormTextInbound: true,
		FileImageInbound:    true,
		DiffRichApproval:    false,
		NeedsPublicEndpoint: false,
	}
	if caps != want {
		t.Fatalf("OutboundCapabilities() = %+v, want %+v", caps, want)
	}
}

func TestCapabilitiesSupportsKind(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	caps := a.OutboundCapabilities()
	cases := []struct {
		kind gateway.OutboundKind
		want bool
	}{
		{gateway.OutboundNotification, true},
		{gateway.OutboundApprovalRequest, true},
		{gateway.OutboundConversationReply, true},
		{gateway.OutboundKind("nonsense"), false},
	}
	for _, c := range cases {
		if got := caps.SupportsKind(c.kind); got != c.want {
			t.Errorf("SupportsKind(%q) = %v, want %v", c.kind, got, c.want)
		}
	}
}

func TestSendDisabledReturnsDisabledReceipt(t *testing.T) {
	a, err := New(Config{Now: fixedNow})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	receipt, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID:    "01HXYZ",
		Kind:  gateway.OutboundNotification,
		Title: "hi",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if receipt.Source != gateway.SourceSignal {
		t.Errorf("receipt.Source = %q, want %q", receipt.Source, gateway.SourceSignal)
	}
	if receipt.Status != gateway.StatusFailed {
		t.Errorf("receipt.Status = %q, want %q", receipt.Status, gateway.StatusFailed)
	}
	if !strings.Contains(receipt.Error, "disabled") {
		t.Errorf("receipt.Error = %q, want substring %q", receipt.Error, "disabled")
	}
	if !receipt.DeliveredAt.Equal(fixedNow()) {
		t.Errorf("receipt.DeliveredAt = %v, want %v", receipt.DeliveredAt, fixedNow())
	}
}

func TestStartDisabledReturnsImmediately(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan error, 1)
	ingest := func(context.Context, gateway.InboundEnvelope) error { return nil }
	go func() { done <- a.Start(context.Background(), ingest) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start (disabled) returned %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start (disabled) blocked; want immediate return")
	}
}

func TestStopDisabledNoOp(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop (disabled) = %v, want nil", err)
	}
	// idempotent
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop (disabled, second call) = %v, want nil", err)
	}
}

func TestNewEnabledMissingSocket(t *testing.T) {
	_, err := New(Config{Enabled: true, SenderNumber: "+15555555555"})
	if err == nil {
		t.Fatal("New(Enabled, no socket) = nil, want error")
	}
	if !strings.Contains(err.Error(), "socket") {
		t.Errorf("error = %q, want substring %q", err, "socket")
	}
}

func TestNewEnabledMissingSender(t *testing.T) {
	_, err := New(Config{Enabled: true, SignalCLISocket: "/tmp/socket"})
	if err == nil {
		t.Fatal("New(Enabled, no sender) = nil, want error")
	}
	if !strings.Contains(err.Error(), "sender") {
		t.Errorf("error = %q, want substring %q", err, "sender")
	}
}

func TestSendEnabledReturnsNotImplemented(t *testing.T) {
	a, err := New(Config{
		Enabled:         true,
		SignalCLISocket: "/tmp/signal-cli.sock",
		SenderNumber:    "+15555550100",
		Now:             fixedNow,
	})
	if err != nil {
		t.Fatalf("New(enabled): %v", err)
	}
	receipt, err := a.Send(context.Background(), gateway.OutboundEnvelope{
		ID:    "01HXYZ",
		Kind:  gateway.OutboundNotification,
		Title: "hi",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if receipt.Status != gateway.StatusFailed {
		t.Errorf("receipt.Status = %q, want %q", receipt.Status, gateway.StatusFailed)
	}
	if !strings.Contains(receipt.Error, "not yet implemented") {
		t.Errorf("receipt.Error = %q, want substring %q", receipt.Error, "not yet implemented")
	}
	if strings.Contains(receipt.Error, "disabled") {
		t.Errorf("receipt.Error = %q, must not say disabled for enabled adapter", receipt.Error)
	}
}

func TestStartEnabledBlocksUntilContextCancel(t *testing.T) {
	a, err := New(Config{
		Enabled:         true,
		SignalCLISocket: "/tmp/signal-cli.sock",
		SenderNumber:    "+15555550100",
	})
	if err != nil {
		t.Fatalf("New(enabled): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- a.Start(ctx, func(context.Context, gateway.InboundEnvelope) error { return nil })
	}()

	// confirm Start is still running
	select {
	case err := <-done:
		t.Fatalf("Start (enabled) returned %v before ctx cancel; want block", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != context.Canceled.Error() {
			t.Fatalf("Start (enabled, cancelled) = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start (enabled) did not return after ctx cancel")
	}
}

func TestStopEnabledUnblocksStart(t *testing.T) {
	a, err := New(Config{
		Enabled:         true,
		SignalCLISocket: "/tmp/signal-cli.sock",
		SenderNumber:    "+15555550100",
	})
	if err != nil {
		t.Fatalf("New(enabled): %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- a.Start(context.Background(), func(context.Context, gateway.InboundEnvelope) error { return nil })
	}()

	// wait for Start to actually start blocking
	time.Sleep(20 * time.Millisecond)

	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop (enabled): %v", err)
	}
	// Idempotent: a second Stop must not panic on close-of-closed-channel.
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop (enabled, second): %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start (enabled, stopped) = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start (enabled) did not return after Stop")
	}
}

func TestSatisfiesAdapterInterface(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var _ gateway.Adapter = a
}

func TestConcurrentStopSafety(t *testing.T) {
	a, err := New(Config{
		Enabled:         true,
		SignalCLISocket: "/tmp/signal-cli.sock",
		SenderNumber:    "+15555550100",
	})
	if err != nil {
		t.Fatalf("New(enabled): %v", err)
	}
	startDone := make(chan error, 1)
	go func() {
		startDone <- a.Start(context.Background(), func(context.Context, gateway.InboundEnvelope) error { return nil })
	}()
	time.Sleep(20 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.Stop(context.Background())
		}()
	}
	wg.Wait()
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Stop did not unblock Start")
	}
}

// TestWireRoundTrips is the compile-time + tag-time check requested for
// wire.go: marshal a populated instance of each wire struct and verify
// every documented JSON key shows up. If a future refactor strips a
// json tag this test fails loudly rather than silently breaking the
// G6 implementation.
func TestWireRoundTrips(t *testing.T) {
	cases := []struct {
		name     string
		value    any
		wantKeys []string
	}{
		{
			name:     "rpcRequest",
			value:    rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "send", Params: json.RawMessage(`{}`)},
			wantKeys: []string{"jsonrpc", "id", "method", "params"},
		},
		{
			name:     "rpcResponse",
			value:    rpcResponse{JSONRPC: "2.0", ID: json.RawMessage(`1`), Result: json.RawMessage(`{}`)},
			wantKeys: []string{"jsonrpc", "id", "result"},
		},
		{
			name:     "rpcError",
			value:    rpcError{Code: -32000, Message: "boom", Data: json.RawMessage(`"x"`)},
			wantKeys: []string{"code", "message", "data"},
		},
		{
			name:     "rpcNotification",
			value:    rpcNotification{JSONRPC: "2.0", Method: "receive", Params: json.RawMessage(`{}`)},
			wantKeys: []string{"jsonrpc", "method", "params"},
		},
		{
			name: "sendParams",
			value: sendParams{
				Account:     "+15555550100",
				Recipient:   []string{"+15555550101"},
				GroupID:     "abc",
				Message:     "hi",
				Attachments: []string{"/tmp/x.png"},
			},
			wantKeys: []string{"account", "recipient", "group-id", "message", "attachments"},
		},
		{
			name: "sendResult",
			value: sendResult{
				Timestamp: 1717_000_000_000,
				Results: []sendResultDetail{{
					RecipientAddress: signalAddress{Number: "+15555550101", UUID: "abc"},
					Type:             "SUCCESS",
				}},
			},
			wantKeys: []string{"timestamp", "results", "recipientAddress", "type", "number", "uuid"},
		},
		{
			name: "receiveParams",
			value: receiveParams{
				Account: "+15555550100",
				Envelope: receiveEnvelope{
					Source: "+15555550101", SourceNumber: "+15555550101", SourceUUID: "u",
					SourceName: "Bob", Timestamp: 1717_000_000_000,
					DataMessage: &receiveDataMsg{Timestamp: 1717_000_000_000, Message: "yo"},
				},
			},
			wantKeys: []string{"envelope", "account", "source", "sourceNumber", "sourceUuid", "sourceName", "timestamp", "dataMessage", "message"},
		},
		{
			name: "receiveDataMsg",
			value: receiveDataMsg{
				Timestamp: 1, Message: "m", ExpiresInS: 5, ViewOnce: true,
				Attachments: []receiveAttach{{ContentType: "image/png", Filename: "x", ID: "y", Size: 1, File: "/tmp/x"}},
				GroupInfo:   &receiveGroupInfo{GroupID: "g", Type: "DELIVER"},
				Quote:       &receiveQuote{ID: 1, Author: "a", Text: "t"},
				Reactions:   []receiveReactInfo{{Emoji: "👍", Author: "a", Timestamp: 2}},
			},
			wantKeys: []string{
				"timestamp", "message", "expiresInSeconds", "viewOnce",
				"attachments", "groupInfo", "quote", "reactions",
				"contentType", "filename", "id", "size", "file",
				"groupId", "type", "author", "text", "emoji",
			},
		},
		{
			name:     "receiveReceiptMsg",
			value:    receiveReceiptMsg{When: 1, IsDelivery: true, IsRead: true, IsViewed: true, Timestamps: []int64{1, 2}},
			wantKeys: []string{"when", "isDelivery", "isRead", "isViewed", "timestamps"},
		},
		{
			name:     "subscribeReceiveParams",
			value:    subscribeReceiveParams{Account: "+15555550100"},
			wantKeys: []string{"account"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", c.name, err)
			}
			s := string(b)
			if s == "" || s == "{}" {
				t.Fatalf("marshal %s produced empty JSON: %q", c.name, s)
			}
			for _, k := range c.wantKeys {
				needle := `"` + k + `":`
				if !strings.Contains(s, needle) {
					t.Errorf("marshal %s missing key %q in %s", c.name, k, s)
				}
			}
		})
	}
}
