package gateway

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// Action is one user-tap option attached to an ApprovalRequest envelope.
// ntfy's three-button cap is the tightest constraint; broker truncates
// per-adapter via OutboundCapabilities.MaxActions.
//
// ID is the durable identifier the broker correlates back to a Decision
// inbound — adapters pass it through verbatim ("approve" stays
// "approve" across both legs). Label is what the user sees.
type Action struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// CanonicalActions returns the three approve / revise / reject buttons
// the approval queue wires for every pending artifact. Adapters with
// MaxActions < 3 truncate from the right (Revise drops first).
func CanonicalActions() []Action {
	return []Action{
		{ID: string(DecisionApprove), Label: "Approve"},
		{ID: string(DecisionRevise), Label: "Revise"},
		{ID: string(DecisionReject), Label: "Reject"},
	}
}

// Decision is the typed payload of an InboundDecision envelope. Kind is
// the three-way response; Revision carries the freeform text the user
// supplied when they tapped Revise (Telegram/Custom) or stays empty
// (ntfy — see spec § Approval-queue integration).
type Decision struct {
	Kind     DecisionKind `json:"kind"`
	Revision string       `json:"revision,omitempty"`
}

// OutboundEnvelope is the canonical broker→adapter shape. Adapters
// translate it into platform-native publishes and return a
// DeliveryReceipt. The broker stamps ID, CreatedAt before Send so the
// event log row is consistent regardless of which adapter wins.
type OutboundEnvelope struct {
	// ID is a ULID the broker mints. Used to correlate outbound event
	// rows across adapters when a single envelope fans out.
	ID string `json:"id"`

	// SessionID is the conversation context. May be empty for
	// daemon-originated notifications (scheduled-task results).
	SessionID string `json:"session_id,omitempty"`

	// AgentID is the producer. For approval routing it's the agent that
	// proposed the artifact; for daemon notifications it may be the
	// synthetic "daemon" identifier.
	AgentID string `json:"agent_id,omitempty"`

	// Kind selects rendering (notification / approval / conversation).
	Kind OutboundKind `json:"kind"`

	// Title is the short headline. ntfy uses it as the message title;
	// Telegram puts it in bold at the top of the body.
	Title string `json:"title,omitempty"`

	// Body is the markdown body. Adapters downgrade as needed —
	// Telegram supports MarkdownV2, ntfy treats it as plaintext.
	Body string `json:"body,omitempty"`

	// Actions are the buttons rendered with an ApprovalRequest.
	// Truncated per OutboundCapabilities.MaxActions before send.
	Actions []Action `json:"actions,omitempty"`

	// ArtifactID is the gateway's correlation key. Decision inbounds
	// must echo this back; the broker uses it to serialize first-
	// write-wins per artifact.
	ArtifactID string `json:"artifact_id,omitempty"`

	// Urgency drives per-adapter priority headers / silent flags.
	Urgency Urgency `json:"urgency,omitempty"`

	// CreatedAt is broker-stamped at Send time, UTC, ms-truncated.
	CreatedAt time.Time `json:"created_at"`
}

// Validate reports the first structural error in env. Called by Broker
// before persisting an outbound event, so a malformed envelope never
// hits the wire.
func (env OutboundEnvelope) Validate() error {
	switch env.Kind {
	case OutboundNotification, OutboundApprovalRequest, OutboundConversationReply:
	default:
		return fmt.Errorf("envelope: unknown OutboundKind %q", env.Kind)
	}
	if env.Title == "" && env.Body == "" {
		return errors.New("envelope: title and body both empty")
	}
	if env.Kind == OutboundApprovalRequest {
		if env.ArtifactID == "" {
			return errors.New("envelope: approval request requires artifact id")
		}
		if len(env.Actions) == 0 {
			return errors.New("envelope: approval request requires actions")
		}
	}
	return nil
}

// InboundEnvelope is the canonical adapter→broker shape. Adapters fill
// Source, GatewayEventID (platform-native dedupe key), From, Kind, Body,
// optional ArtifactID + Decision. The broker stamps ID + ReceivedAt at
// Ingest and persists the event.
type InboundEnvelope struct {
	// ID is a ULID the broker mints at Ingest. Used as the event-log
	// envelope_id for the gateway_inbound payload.
	ID string `json:"id"`

	// GatewayEventID is the platform-native idempotency key. Telegram
	// uses update_id, ntfy uses the action click ID, signal-cli uses a
	// per-message timestamp. The broker dedupes on
	// (Source, GatewayEventID) before persisting.
	GatewayEventID string `json:"gateway_event_id"`

	// Source is the adapter that received this inbound.
	Source Source `json:"source"`

	// From is the platform-native identity (chat_id, ntfy click subject,
	// signal phone number). v0 collapses all From values to "the user"
	// for routing; the field is preserved for the future multi-user
	// gateway_identities mapping.
	From string `json:"from,omitempty"`

	// Kind selects parsing (message / decision / command).
	Kind InboundKind `json:"kind"`

	// Body is the user's free-form text (when present). Empty for
	// pure Decision envelopes from ntfy.
	Body string `json:"body,omitempty"`

	// ArtifactID is required for Kind=Decision; matches the
	// OutboundEnvelope.ArtifactID the user is answering.
	ArtifactID string `json:"artifact_id,omitempty"`

	// Decision is required for Kind=Decision; the typed three-way
	// response.
	Decision *Decision `json:"decision,omitempty"`

	// ReceivedAt is broker-stamped at Ingest, UTC, ms-truncated.
	ReceivedAt time.Time `json:"received_at"`
}

// Validate reports the first structural error in env. Adapters call
// this before handing the envelope to IngestFunc so a broken adapter
// fails loudly during development rather than silently dropping rows.
func (env InboundEnvelope) Validate() error {
	if !env.Source.Valid() {
		return fmt.Errorf("envelope: unknown Source %q", env.Source)
	}
	if env.GatewayEventID == "" {
		return errors.New("envelope: gateway_event_id required")
	}
	switch env.Kind {
	case InboundMessage:
		if env.Body == "" {
			return errors.New("envelope: message requires body")
		}
	case InboundDecision:
		if env.ArtifactID == "" {
			return errors.New("envelope: decision requires artifact id")
		}
		if env.Decision == nil {
			return errors.New("envelope: decision requires decision payload")
		}
		if !env.Decision.Kind.Valid() {
			return fmt.Errorf("envelope: unknown DecisionKind %q", env.Decision.Kind)
		}
	case InboundCommand:
		if env.Body == "" {
			return errors.New("envelope: command requires body")
		}
	default:
		return fmt.Errorf("envelope: unknown InboundKind %q", env.Kind)
	}
	return nil
}

// DeliveryReceipt is what an adapter returns from Send. The broker
// persists it inside the EvtGatewayOutbound payload so the manage view
// + restart-reconcile path can render per-channel outcomes.
type DeliveryReceipt struct {
	Source      Source         `json:"source"`
	ProviderRef string         `json:"provider_ref,omitempty"`
	Status      DeliveryStatus `json:"status"`
	DeliveredAt time.Time      `json:"delivered_at,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// envelopeULIDEntropy is the monotonic-random source the broker uses to
// stamp envelope IDs. ULID gives sortable IDs without needing a global
// counter; monotonic-entropy guarantees uniqueness within the same
// millisecond.
//
// MonotonicEntropy is NOT safe for concurrent calls (its Read mutates
// shared state without internal locking). The broker can be called
// from many goroutines (one per fan-out adapter, one per inbound
// callback), so we serialize through envelopeULIDMu — cheap because
// minting one ULID is ~microseconds.
var (
	envelopeULIDMu      sync.Mutex
	envelopeULIDEntropy = ulid.Monotonic(rand.Reader, 0)
)

// newEnvelopeID mints a fresh ULID-encoded ID using the package
// entropy. Exposed as a package-internal helper so the broker and the
// adapters that pre-stamp inbound IDs share one source.
func newEnvelopeID(now time.Time) (string, error) {
	envelopeULIDMu.Lock()
	defer envelopeULIDMu.Unlock()
	u, err := ulid.New(uint64(now.UnixMilli()), envelopeULIDEntropy)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
