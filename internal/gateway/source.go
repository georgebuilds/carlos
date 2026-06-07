// Package gateway provides the messaging-gateway broker and adapter
// contract that lets carlos talk to ntfy, Telegram, Signal, and a future
// custom HITL side-app over the same canonical envelope.
//
// Design lives at:
//
//	personal/projects/carlos/notes/2026-06-05 gateway architecture
//	(ntfy + telegram + signal + custom).md
//
// In short:
//
//   - Adapters are dumb I/O. They translate one OutboundEnvelope into one
//     platform-native publish, and one platform-native inbound into one
//     InboundEnvelope they hand to the broker via IngestFunc.
//   - The Broker owns retry/backoff, inbound dedupe, decision serialization
//     per ArtifactID, and the event-log writes (EvtGatewayOutbound /
//     EvtGatewayInbound).
//   - The agent loop sees gateway inbound exactly like a TUI click -
//     same approval queue, same resolver, no parallel control path.
//
// The CLI Gateway (the in-process TUI surface) is unrelated to this
// package's Source set; it predates the broker and keeps living in
// gateway.go.
package gateway

// Source identifies which adapter produced an inbound envelope or
// delivered an outbound. The set is closed today; new channels add a
// constant here and a sub-block to gateway config.
type Source string

const (
	// SourceNtfy - fire-and-forget HTTP publish + ≤3 action buttons.
	SourceNtfy Source = "ntfy"
	// SourceTelegram - Bot API long-poll with inline keyboards.
	SourceTelegram Source = "telegram"
	// SourceSignal - signal-cli JSON-RPC. Post-v1; the adapter exists
	// as a stub so the contract is exercised in tests.
	SourceSignal Source = "signal"
	// SourceCustom - Tailscale-only WebSocket from a custom phone app.
	// Post-v1; not implemented in this round.
	SourceCustom Source = "custom"
	// SourceFake - used by tests to stand a deterministic adapter in
	// for any real channel. Never enabled in production config.
	SourceFake Source = "fake"
)

// String satisfies fmt.Stringer + lets the type sit in error messages
// without an explicit cast.
func (s Source) String() string { return string(s) }

// Valid reports whether s is a Source the broker knows how to route to.
// Unknown sources arrive only from corrupt config or a forged inbound;
// callers should drop the envelope rather than panic.
func (s Source) Valid() bool {
	switch s {
	case SourceNtfy, SourceTelegram, SourceSignal, SourceCustom, SourceFake:
		return true
	}
	return false
}

// Urgency maps to per-adapter priority semantics: ntfy's `Priority`
// header (1..5), Telegram's `disable_notification` flag, signal-cli's
// notification flag, the custom app's importance hint.
//
// The mapping is deliberately coarse; per-adapter config (priority_map)
// translates these three buckets into platform-specific numbers.
type Urgency int8

const (
	// UrgencyLow is silent push: status updates, daemon results, cost
	// pings the user opts into seeing. Default for everything except
	// approvals.
	UrgencyLow Urgency = 0
	// UrgencyDefault is a normal notification: heard but not insistent.
	UrgencyDefault Urgency = 1
	// UrgencyHigh is heads-up: HITL approval requests, scheduled-task
	// failures, anything where notification fatigue is better than
	// missing the ping.
	UrgencyHigh Urgency = 2
)

// String emits the lowercase form used in config + event payloads.
func (u Urgency) String() string {
	switch u {
	case UrgencyLow:
		return "low"
	case UrgencyDefault:
		return "default"
	case UrgencyHigh:
		return "high"
	default:
		return "default"
	}
}

// ParseUrgency parses the string form back into the typed value. An
// unknown string maps to UrgencyDefault so a typo in config is not
// catastrophic - it's just middle-of-the-road.
func ParseUrgency(s string) Urgency {
	switch s {
	case "low":
		return UrgencyLow
	case "default", "":
		return UrgencyDefault
	case "high":
		return UrgencyHigh
	}
	return UrgencyDefault
}

// OutboundKind categorizes a broker→adapter envelope. Adapters that
// can't render a kind faithfully degrade - e.g. ntfy renders an
// ApprovalRequest by surfacing up to three Action buttons; ConversationReply
// over ntfy is invalid and the broker drops the envelope for that channel
// (the routing config should not point ConversationReply at ntfy in the
// first place).
type OutboundKind string

const (
	// OutboundNotification is a one-shot info ping. No buttons, no
	// response expected. Maps to a plain push on every channel.
	OutboundNotification OutboundKind = "notification"
	// OutboundApprovalRequest carries Actions + an ArtifactID and waits
	// (in the broker) for a matching Decision inbound.
	OutboundApprovalRequest OutboundKind = "approval_request"
	// OutboundConversationReply is the agent talking back during free-
	// form chat. Only channels with FreeFormTextInbound should ever see
	// this kind.
	OutboundConversationReply OutboundKind = "conversation_reply"
)

// InboundKind categorizes an adapter→broker envelope.
type InboundKind string

const (
	// InboundMessage is free-form text from the user - the start (or
	// continuation) of a conversation. Routes to the agent loop as a
	// new user message event.
	InboundMessage InboundKind = "message"
	// InboundDecision answers an outstanding ApprovalRequest. ArtifactID
	// must be set; Decision must be non-nil.
	InboundDecision InboundKind = "decision"
	// InboundCommand is a slash-style instruction (e.g. "/cancel") that
	// the agent loop should parse rather than treat as conversation.
	// Reserved for post-G2 work; today's adapters do not emit this.
	InboundCommand InboundKind = "command"
)

// DecisionKind is the three-way HITL response the approval queue
// understands. `revise` is the only one that may carry a free-form
// Revision body; ntfy can emit revise without text because it lacks a
// rich inbound channel, in which case the agent prompts via the next
// outbound message.
type DecisionKind string

const (
	DecisionApprove DecisionKind = "approve"
	DecisionRevise  DecisionKind = "revise"
	DecisionReject  DecisionKind = "reject"
)

// Valid reports whether d is one of the three accepted decision kinds.
func (d DecisionKind) Valid() bool {
	switch d {
	case DecisionApprove, DecisionRevise, DecisionReject:
		return true
	}
	return false
}

// DeliveryStatus is the receipt the broker logs alongside an outbound
// envelope. The asymmetry is deliberate: not every channel can
// acknowledge delivery (ntfy is fire-and-forget), so the broker records
// what it knows and moves on.
type DeliveryStatus string

const (
	// StatusUnknown means the broker tried the send but cannot confirm
	// delivery (ntfy publish with no handler echo, network mid-flight
	// when the adapter returned). The event log records the attempt so
	// a restart sees the same row.
	StatusUnknown DeliveryStatus = "unknown"
	// StatusDelivered means the channel acknowledged receipt with a
	// platform-native id (Telegram message_id, etc.).
	StatusDelivered DeliveryStatus = "delivered"
	// StatusFailed means the adapter returned an error; broker has
	// already attempted retries to exhaustion.
	StatusFailed DeliveryStatus = "failed"
)
