package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// EventAgentID is the synthetic agent_id under which all gateway events
// are written. Distinct from any real agent id so projection scans can
// filter cleanly and from "user" (the approval-queue resolver id) so
// the audit trail distinguishes "user clicked in TUI" from "user
// clicked from phone via gateway".
const EventAgentID = "gateway"

// OutboundPayload is the JSON body of an EvtGatewayOutbound event. One
// row is written per (envelope, channel) pair - a single Send call
// that fans out to ntfy + Telegram produces two rows, each with its
// own DeliveryReceipt. ArtifactID is duplicated from the envelope so
// projections can filter by artifact without unmarshaling the full
// envelope.
type OutboundPayload struct {
	Channel    Source           `json:"channel"`
	EnvelopeID string           `json:"envelope_id"`
	ArtifactID string           `json:"artifact_id,omitempty"`
	Envelope   OutboundEnvelope `json:"envelope"`
	Receipt    DeliveryReceipt  `json:"receipt"`
	Attempt    int              `json:"attempt"`
}

// InboundPayload is the JSON body of an EvtGatewayInbound event. One
// row per inbound after dedupe; an idempotent retry from the platform
// is silently dropped at the broker layer before reaching the log.
type InboundPayload struct {
	Channel    Source          `json:"channel"`
	EnvelopeID string          `json:"envelope_id"`
	ArtifactID string          `json:"artifact_id,omitempty"`
	Envelope   InboundEnvelope `json:"envelope"`
}

// appendOutbound writes an EvtGatewayOutbound row. Returns the seq the
// log assigned so callers (the broker's send path) can correlate with
// retry attempts.
func appendOutbound(ctx context.Context, log *agent.SQLiteEventLog, p OutboundPayload, ts time.Time) (int64, error) {
	if log == nil {
		return 0, errors.New("gateway events: nil log")
	}
	if p.EnvelopeID == "" {
		return 0, errors.New("gateway events: outbound envelope id required")
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return 0, fmt.Errorf("gateway events: marshal outbound: %w", err)
	}
	return log.Append(ctx, agent.Event{
		AgentID: EventAgentID,
		TS:      ts.UTC().Truncate(time.Millisecond),
		Type:    agent.EvtGatewayOutbound,
		Payload: payload,
	})
}

// appendInbound writes an EvtGatewayInbound row.
func appendInbound(ctx context.Context, log *agent.SQLiteEventLog, p InboundPayload, ts time.Time) (int64, error) {
	if log == nil {
		return 0, errors.New("gateway events: nil log")
	}
	if p.EnvelopeID == "" {
		return 0, errors.New("gateway events: inbound envelope id required")
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return 0, fmt.Errorf("gateway events: marshal inbound: %w", err)
	}
	return log.Append(ctx, agent.Event{
		AgentID: EventAgentID,
		TS:      ts.UTC().Truncate(time.Millisecond),
		Type:    agent.EvtGatewayInbound,
		Payload: payload,
	})
}

// DecodeOutboundPayload is exposed for projection consumers that want
// to walk the event log offline (manage view, audit tools). Returns a
// descriptive error if the row is corrupted rather than panicking - the
// queue tolerates a few malformed rows the same way ListPendingApprovals
// does.
func DecodeOutboundPayload(raw []byte) (OutboundPayload, error) {
	var p OutboundPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return OutboundPayload{}, fmt.Errorf("gateway events: decode outbound: %w", err)
	}
	return p, nil
}

// DecodeInboundPayload - the inbound analogue of DecodeOutboundPayload.
func DecodeInboundPayload(raw []byte) (InboundPayload, error) {
	var p InboundPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return InboundPayload{}, fmt.Errorf("gateway events: decode inbound: %w", err)
	}
	return p, nil
}
