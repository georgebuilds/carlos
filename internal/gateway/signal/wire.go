package signal

// This file sketches the signal-cli JSON-RPC wire envelope so a future
// implementer of slice G6 starts with the field names and types already
// pinned to the documented service shape - not a blank file.
//
// Source for the shapes below:
//
//	https://github.com/AsamK/signal-cli/wiki/JSON-RPC-service
//
// signal-cli ≥ 0.11 supports a JSON-RPC 2.0 service over a unix socket
// (and TCP, but unix-socket is what we target for the daemon-side
// integration on cronus). It accepts method calls and emits
// `receive` notifications for every inbound Signal message.
//
// IMPORTANT: nothing in this file is wired up yet. The structs exist
// only so:
//
//  1. The implementer of G6 has the correct JSON keys and types to
//     marshal/unmarshal against without re-deriving them from the wiki.
//  2. A compile-time test (signal_test.go) can verify the JSON tags
//     survive future refactors.
//
// If you're picking up G6: read the linked wiki, then start by feeding
// these structs to encoding/json and verifying round-trips against
// signal-cli output captured from a local daemon. Field names below
// follow signal-cli's documented spellings exactly.

import "encoding/json"

// rpcRequest is the JSON-RPC 2.0 envelope every method call uses. ID is
// the client-side correlation key signal-cli echoes back in the
// matching rpcResponse. Method is the signal-cli method name ("send",
// "subscribeReceive", "listGroups", …). Params is the method-specific
// payload - declared as RawMessage so each call site picks the
// concrete type (sendParams, etc.) without forcing a union here.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is the matching reply. Exactly one of Result and Error is
// set per the JSON-RPC 2.0 spec; signal-cli follows that contract.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError mirrors the JSON-RPC 2.0 error object. signal-cli uses Code
// to distinguish protocol errors (-326xx) from Signal-side failures
// (rate limit, unregistered recipient, etc.); Data carries the typed
// detail for the latter.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// rpcNotification is a server-initiated message (no ID). signal-cli
// uses these for `receive` events - one per inbound Signal message
// from any subscribed account. The Params payload decodes into
// receiveParams.
type rpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// sendParams is the payload of a `send` method call. The signal-cli
// service accepts either Recipient (a single E.164 number) or GroupID
// (a base64-encoded group identifier) - set exactly one. Message is
// the body; Attachments is an optional list of local file paths
// signal-cli reads from disk and ships as Signal attachments.
//
// Account is the registered sender number (the same value we pass as
// Config.SenderNumber). Without it signal-cli rejects the call when
// the daemon has multiple accounts registered.
type sendParams struct {
	Account     string   `json:"account"`
	Recipient   []string `json:"recipient,omitempty"`
	GroupID     string   `json:"group-id,omitempty"`
	Message     string   `json:"message"`
	Attachments []string `json:"attachments,omitempty"`
}

// sendResult is the result payload of a successful `send`. The
// per-recipient Timestamp is the signal-cli message identifier - we'll
// surface it as DeliveryReceipt.ProviderRef so the broker can
// correlate later.
type sendResult struct {
	Timestamp int64              `json:"timestamp"`
	Results   []sendResultDetail `json:"results,omitempty"`
}

// sendResultDetail is the per-recipient outcome inside sendResult.
// Type is signal-cli's documented enum ("SUCCESS",
// "UNREGISTERED_FAILURE", "NETWORK_FAILURE", …). RecipientAddress is
// the address the result applies to.
type sendResultDetail struct {
	RecipientAddress signalAddress `json:"recipientAddress"`
	Type             string        `json:"type"`
}

// signalAddress is the canonical recipient shape signal-cli uses
// across send/receive - either an E.164 phone number or a Signal UUID.
// Both are optional (one of the two is always populated) so we model
// them as omitempty-decorated strings rather than a union.
type signalAddress struct {
	Number string `json:"number,omitempty"`
	UUID   string `json:"uuid,omitempty"`
}

// receiveParams is the payload of a server-pushed `receive`
// notification. signal-cli wraps the actual content under Envelope.
// Account tells us which registered number the message arrived on
// (relevant when a future multi-account deploy lands).
type receiveParams struct {
	Envelope receiveEnvelope `json:"envelope"`
	Account  string          `json:"account,omitempty"`
}

// receiveEnvelope is the per-message header signal-cli emits.
// SourceNumber + SourceUUID identify the sender; Timestamp is the
// message identifier we'll use as InboundEnvelope.GatewayEventID for
// dedupe (it's a ms-since-epoch + nonce, unique per message). Exactly
// one of DataMessage / SyncMessage / TypingMessage is populated per
// envelope; we only care about DataMessage for v1.
type receiveEnvelope struct {
	Source         string             `json:"source,omitempty"`
	SourceNumber   string             `json:"sourceNumber,omitempty"`
	SourceUUID     string             `json:"sourceUuid,omitempty"`
	SourceName     string             `json:"sourceName,omitempty"`
	Timestamp      int64              `json:"timestamp"`
	DataMessage    *receiveDataMsg    `json:"dataMessage,omitempty"`
	SyncMessage    json.RawMessage    `json:"syncMessage,omitempty"`
	TypingMessage  json.RawMessage    `json:"typingMessage,omitempty"`
	ReceiptMessage *receiveReceiptMsg `json:"receiptMessage,omitempty"`
}

// receiveDataMsg is the inbound message body. Message is the free-form
// text; Attachments carries any media the user sent (signal-cli writes
// them to disk and gives us paths). GroupInfo distinguishes group
// messages from 1:1 DMs - v1 treats both alike because the gateway is
// single-user, but G6+ will likely want to filter on it.
type receiveDataMsg struct {
	Timestamp   int64              `json:"timestamp"`
	Message     string             `json:"message"`
	ExpiresInS  int                `json:"expiresInSeconds,omitempty"`
	ViewOnce    bool               `json:"viewOnce,omitempty"`
	Attachments []receiveAttach    `json:"attachments,omitempty"`
	GroupInfo   *receiveGroupInfo  `json:"groupInfo,omitempty"`
	Quote       *receiveQuote      `json:"quote,omitempty"`
	Reactions   []receiveReactInfo `json:"reactions,omitempty"`
}

// receiveAttach is one media item attached to a receiveDataMsg.
// signal-cli writes the bytes to disk under its data directory; File
// is the resulting local path.
type receiveAttach struct {
	ContentType string `json:"contentType"`
	Filename    string `json:"filename,omitempty"`
	ID          string `json:"id,omitempty"`
	Size        int64  `json:"size,omitempty"`
	File        string `json:"file,omitempty"`
}

// receiveGroupInfo identifies a group context. GroupID is base64; Type
// is signal-cli's enum ("DELIVER", "UPDATE", "QUIT", …) describing why
// the message arrived (a member joining, a name change, etc.).
type receiveGroupInfo struct {
	GroupID string `json:"groupId"`
	Type    string `json:"type,omitempty"`
}

// receiveQuote is the reply-to header when the user quoted a previous
// message. Useful eventually for thread-style approval flows.
type receiveQuote struct {
	ID     int64  `json:"id"`
	Author string `json:"author,omitempty"`
	Text   string `json:"text,omitempty"`
}

// receiveReactInfo captures emoji reactions. Reactions on a previous
// approval message are a candidate UX for "approve" without typing -
// the user double-taps a thumbs-up - but that's well past G6.
type receiveReactInfo struct {
	Emoji     string `json:"emoji"`
	Author    string `json:"author,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// receiveReceiptMsg is the read/delivery receipt signal-cli emits when
// a previous outbound has been seen. We'll plumb it through to update
// the broker's DeliveryReceipt.Status from Unknown→Delivered once the
// real implementation lands; for now it's just here to keep parity
// with signal-cli's emitted shapes.
type receiveReceiptMsg struct {
	When       int64   `json:"when"`
	IsDelivery bool    `json:"isDelivery"`
	IsRead     bool    `json:"isRead"`
	IsViewed   bool    `json:"isViewed,omitempty"`
	Timestamps []int64 `json:"timestamps,omitempty"`
}

// subscribeReceiveParams is the payload of a `subscribeReceive` method
// call - the way a JSON-RPC client tells signal-cli "start streaming
// inbound for this account". Without it the daemon holds incoming
// messages and only releases them on an explicit `receive` poll.
type subscribeReceiveParams struct {
	Account string `json:"account"`
}
