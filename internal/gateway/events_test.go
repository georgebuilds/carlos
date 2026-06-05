package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

func newEventsLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func TestAppendOutbound_Persists(t *testing.T) {
	log := newEventsLog(t)
	ctx := context.Background()
	env := OutboundEnvelope{
		ID:        "env-1",
		Kind:      OutboundNotification,
		Title:     "hi",
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	p := OutboundPayload{
		Channel:    SourceTelegram,
		EnvelopeID: env.ID,
		Envelope:   env,
		Receipt:    DeliveryReceipt{Source: SourceTelegram, Status: StatusDelivered},
		Attempt:    1,
	}
	seq, err := appendOutbound(ctx, log, p, time.Now())
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if seq <= 0 {
		t.Errorf("seq: want >0 got %d", seq)
	}

	events, err := log.Read(ctx, EventAgentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events len: want 1 got %d", len(events))
	}
	if events[0].Type != agent.EvtGatewayOutbound {
		t.Errorf("event type: want %q got %q", agent.EvtGatewayOutbound, events[0].Type)
	}
	dec, err := DecodeOutboundPayload(events[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != SourceTelegram {
		t.Errorf("channel: want telegram got %q", dec.Channel)
	}
	if dec.EnvelopeID != "env-1" {
		t.Errorf("envelope id: want env-1 got %q", dec.EnvelopeID)
	}
}

func TestAppendInbound_Persists(t *testing.T) {
	log := newEventsLog(t)
	ctx := context.Background()
	env := InboundEnvelope{
		ID:             "env-1",
		Source:         SourceTelegram,
		GatewayEventID: "tg-100",
		Kind:           InboundMessage,
		Body:           "hello",
		ReceivedAt:     time.Now().UTC().Truncate(time.Millisecond),
	}
	p := InboundPayload{
		Channel:    env.Source,
		EnvelopeID: env.ID,
		Envelope:   env,
	}
	if _, err := appendInbound(ctx, log, p, env.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	events, _ := log.Read(ctx, EventAgentID, 0)
	if len(events) != 1 || events[0].Type != agent.EvtGatewayInbound {
		t.Fatalf("expected one inbound event, got %+v", events)
	}
	dec, err := DecodeInboundPayload(events[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Envelope.Body != "hello" {
		t.Errorf("body roundtrip: want hello got %q", dec.Envelope.Body)
	}
}

func TestAppendOutbound_NilLog(t *testing.T) {
	if _, err := appendOutbound(context.Background(), nil, OutboundPayload{EnvelopeID: "x"}, time.Now()); err == nil {
		t.Error("expected error on nil log")
	}
}

func TestAppendOutbound_EmptyEnvelopeID(t *testing.T) {
	log := newEventsLog(t)
	if _, err := appendOutbound(context.Background(), log, OutboundPayload{}, time.Now()); err == nil {
		t.Error("expected error on empty envelope id")
	}
}

func TestAppendInbound_EmptyEnvelopeID(t *testing.T) {
	log := newEventsLog(t)
	if _, err := appendInbound(context.Background(), log, InboundPayload{}, time.Now()); err == nil {
		t.Error("expected error on empty envelope id")
	}
}

func TestDecodeOutboundPayload_Malformed(t *testing.T) {
	if _, err := DecodeOutboundPayload([]byte("not json")); err == nil {
		t.Error("expected decode error")
	}
}

func TestDecodeInboundPayload_Malformed(t *testing.T) {
	if _, err := DecodeInboundPayload([]byte("not json")); err == nil {
		t.Error("expected decode error")
	}
}

func TestPayload_RoundTripsThroughEventLog(t *testing.T) {
	// Belt-and-braces: marshal a payload directly and decode it, no
	// SQLite involved, so the wire shape doesn't drift if the schema
	// table evolves later.
	in := OutboundPayload{
		Channel:    SourceNtfy,
		EnvelopeID: "x",
		ArtifactID: "art-9",
		Envelope: OutboundEnvelope{
			ID:    "x",
			Kind:  OutboundApprovalRequest,
			Title: "Review",
			Body:  "approve?",
			Actions: []Action{
				{ID: "approve", Label: "Yes"},
				{ID: "reject", Label: "No"},
			},
			ArtifactID: "art-9",
			Urgency:    UrgencyHigh,
		},
		Receipt: DeliveryReceipt{Source: SourceNtfy, Status: StatusUnknown},
		Attempt: 2,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOutboundPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Channel != in.Channel || got.Envelope.Title != in.Envelope.Title ||
		len(got.Envelope.Actions) != 2 || got.Attempt != 2 {
		t.Errorf("roundtrip mismatch: got %+v", got)
	}
}
