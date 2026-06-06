package gateway

import "context"

// IngestFunc is the broker-supplied callback adapters use to deliver an
// inbound envelope. The broker is responsible for dedupe + persistence;
// adapters just hand off the typed envelope.
//
// Returning an error means the broker failed to ingest (e.g. the event
// log is down). Adapters should propagate the error to whatever loop
// produced the envelope so the platform's retry semantics decide what
// happens next — Telegram leaves update_id un-acked so the next poll
// picks it up; ntfy returns a 5xx so the action button retries.
type IngestFunc func(ctx context.Context, env InboundEnvelope) error

// Adapter is the contract every gateway channel implements.
//
// # Lifecycle
//
// The broker calls Start exactly once at process startup with the
// broker's IngestFunc. The adapter is expected to spin its own inbound
// machinery (Telegram long-poll, ntfy HTTP handler, signal-cli IPC) in
// a background goroutine that respects ctx — when ctx is cancelled,
// the adapter shuts down and Stop returns.
//
// Stop is called for graceful shutdown. Adapters must drain any in-
// flight inbound through IngestFunc before returning; the broker won't
// invoke IngestFunc after Stop returns.
//
// # Concurrency
//
// Send may be called concurrently from many goroutines (one per fan-
// out target). Adapters must serialize their own platform clients
// internally if the platform requires it (e.g. Telegram's per-bot rate
// limit).
//
// # Why the interface is small
//
// Per the spec § Adapter interface: adapters stay dumb so the broker
// owns retry, dedupe, decision serialization, and routing. Don't add
// methods here without first asking whether the new responsibility
// could live in the broker — the answer is usually yes.
type Adapter interface {
	// Name returns the Source constant the broker uses to key the
	// adapters map and to route per-channel events.
	Name() Source

	// OutboundCapabilities reports what the adapter can render. See
	// OutboundCapabilities for the field-level contract.
	OutboundCapabilities() OutboundCapabilities

	// Send publishes env to the channel. The adapter does NOT retry on
	// failure — broker owns that. The returned DeliveryReceipt's
	// Status reflects what the adapter learned synchronously
	// (Delivered if the platform acknowledged with an id, Unknown if
	// the API is fire-and-forget, Failed if Send errored).
	Send(ctx context.Context, env OutboundEnvelope) (DeliveryReceipt, error)

	// Start launches the adapter's inbound machinery. Long-running;
	// returns nil on graceful shutdown via ctx cancellation.
	Start(ctx context.Context, ingest IngestFunc) error

	// Stop signals the adapter to wind down. Idempotent. Adapters that
	// run all of Start under the provided ctx may implement Stop as a
	// no-op and rely on the broker's outer ctx cancellation.
	Stop(ctx context.Context) error
}
