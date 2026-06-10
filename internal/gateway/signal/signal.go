// Package signal is the Signal-channel gateway.Adapter. It exists today
// as a stub for slice G6 of the gateway architecture: every method on
// the gateway.Adapter contract is implemented, the broker can register
// it, and the wire shape of the planned signal-cli JSON-RPC integration
// is sketched in wire.go - but no JSON-RPC traffic is exchanged.
//
// # Why a stub
//
// The gateway architecture spec § "Signal - same shape as Telegram,
// harder" defers Signal to its own slice because signal-cli is unofficial,
// the registration path (phone + captcha) is fiddly, and the trust model
// is materially different from Telegram. The stub lets the rest of the
// system - broker registration, capability advertisement, manage view -
// treat Signal as a first-class channel without forcing G6 onto the v1
// critical path.
//
// # What a real implementation needs to add
//
//  1. A signal-cli daemon: the user runs `signal-cli daemon --socket
//     /run/signal-cli.sock --json-rpc` against a registered number.
//  2. A unix-socket JSON-RPC client (see wire.go for the envelope shape).
//     Outbound Send becomes a `send` method call carrying recipient +
//     message body + (eventually) attachments. Approval-request Actions
//     have no inline-keyboard equivalent on Signal; render them as a
//     numbered list in the body and parse the user's reply text to map
//     back to a DecisionKind (same UX path Telegram falls back to when
//     the inline keyboard is stripped by a client that doesn't render it).
//  3. A subscribe loop: signal-cli emits `receive` JSON-RPC notifications
//     for every inbound message; the loop translates each into an
//     InboundEnvelope (Kind=InboundMessage for free text, Kind=InboundDecision
//     when the body parses as a decision keyword) and hands it to the
//     broker-supplied IngestFunc.
//  4. Dedupe: signal-cli messages carry a per-message `timestamp` (ms
//     since epoch + a random nonce); use that as GatewayEventID so the
//     broker's existing (Source, GatewayEventID) dedupe just works.
//
// The capability set advertised here is the post-implementation target
// from the spec's capability matrix - Push + FixedChoiceHITL (≤3,
// rendered as keyword replies) + FreeFormTextInbound + FileImageInbound.
// We expose it from the stub so routing config the user writes today
// keeps working when G6 lands.
package signal

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
)

// Config controls the Signal adapter. The zero value is a disabled
// adapter - Name + OutboundCapabilities still report meaningfully so the broker
// can register it, but Send returns a "disabled" failure receipt and
// Start exits immediately. Enabling the adapter requires a configured
// signal-cli socket path AND a sender E.164 number; both are validated
// at New time so a half-configured production deploy fails fast.
type Config struct {
	// Enabled gates the entire adapter. When false (the default), the
	// adapter is a no-op shell: Send fails with "disabled", Start
	// returns nil immediately, Stop is a no-op. This lets carlos ship
	// with Signal in the routing config without crashing before G6
	// lands.
	Enabled bool

	// SignalCLISocket is the unix-domain socket path the signal-cli
	// daemon listens on (e.g. /run/signal-cli/socket or
	// $XDG_RUNTIME_DIR/signal-cli/socket). Required when Enabled.
	SignalCLISocket string

	// SenderNumber is the E.164 phone number signal-cli has registered.
	// Used as the JSON-RPC `account` parameter on every outbound. We
	// don't validate the E.164 shape here - signal-cli rejects malformed
	// numbers loudly at first use, which is a clearer error surface than
	// us trying to mirror the grammar.
	SenderNumber string

	// Now is the clock the adapter uses to stamp receipts. Tests inject
	// a fixed clock; production leaves it nil and the adapter falls
	// back to time.Now.
	Now func() time.Time
}

// Adapter is the Signal gateway.Adapter stub. Build with New; the zero
// value is unusable.
type Adapter struct {
	cfg Config
	now func() time.Time

	stopCh   chan struct{}
	stopOnce sync.Once
}

// New constructs a Signal adapter from cfg. When cfg.Enabled is false
// the adapter is a no-op shell (any other Config fields are ignored).
// When cfg.Enabled is true both SignalCLISocket and SenderNumber must be
// set; missing either is an error.
func New(cfg Config) (*Adapter, error) {
	if cfg.Enabled {
		if cfg.SignalCLISocket == "" {
			return nil, errors.New("signal: signal-cli socket path required when enabled")
		}
		if cfg.SenderNumber == "" {
			return nil, errors.New("signal: sender number required when enabled")
		}
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Adapter{
		cfg:    cfg,
		now:    now,
		stopCh: make(chan struct{}),
	}, nil
}

// Name returns gateway.SourceSignal regardless of whether the adapter
// is enabled - the broker needs a stable name to key its adapter map
// even when the channel is gated off.
func (a *Adapter) Name() gateway.Source { return gateway.SourceSignal }

// OutboundCapabilities reports the post-implementation capability set from the
// spec's capability matrix. We advertise the target shape even from the
// stub so routing config written today keeps working unchanged when G6
// lands. The broker will short-circuit Send with a "disabled" receipt
// before any capability-driven render path runs, so over-advertising
// here is safe.
func (a *Adapter) OutboundCapabilities() gateway.OutboundCapabilities {
	return gateway.OutboundCapabilities{
		Push:                true,
		FixedChoiceHITL:     true,
		MaxActions:          3,
		FreeFormTextInbound: true,
		FileImageInbound:    true,
		DiffRichApproval:    false,
		NeedsPublicEndpoint: false,
	}
}

// Send is the broker→adapter outbound. In stub form it always returns
// a StatusFailed receipt - the body of the error distinguishes the two
// "not yet useful" modes:
//
//   - Disabled adapter: "signal adapter is disabled". The broker logs
//     this and moves on without retrying (the routing config should
//     not have selected Signal in the first place).
//   - Enabled-but-stub adapter: "signal adapter: not yet implemented".
//     The receipt is still StatusFailed because no message went out,
//     but the wording tells the operator the config is fine - what's
//     missing is the G6 implementation.
//
// A real implementation will translate env into a `send` JSON-RPC call
// (see wire.go); on success return StatusDelivered with ProviderRef set
// to the signal-cli message timestamp.
func (a *Adapter) Send(_ context.Context, _ gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	receipt := gateway.DeliveryReceipt{
		Source:      gateway.SourceSignal,
		Status:      gateway.StatusFailed,
		DeliveredAt: a.now().UTC(),
	}
	if !a.cfg.Enabled {
		receipt.Error = "signal adapter is disabled"
		return receipt, nil
	}
	receipt.Error = "signal adapter: not yet implemented"
	return receipt, nil
}

// Start is the broker→adapter inbound launch point. In stub form:
//
//   - Disabled adapter: returns nil immediately. The broker still calls
//     Start on every registered adapter; a disabled Signal must not
//     hang the startup goroutine.
//   - Enabled adapter: blocks until ctx is cancelled or Stop is called,
//     simulating the connected JSON-RPC subscribe loop. This is the
//     shape the real implementation will keep - replace the blocking
//     select with a `dial unix socket → loop on incoming JSON-RPC
//     notifications → translate to InboundEnvelope → ingest` body.
//
// ingest is accepted but never invoked in stub form; capturing it is
// pointless until there's something to push.
func (a *Adapter) Start(ctx context.Context, _ gateway.IngestFunc) error {
	if !a.cfg.Enabled {
		return nil
	}
	// Real implementation:
	//
	//   conn, err := net.Dial("unix", a.cfg.SignalCLISocket)
	//   if err != nil { return fmt.Errorf("signal: dial %s: %w", a.cfg.SignalCLISocket, err) }
	//   defer conn.Close()
	//   client := newRPCClient(conn)
	//   if err := client.Subscribe(ctx, a.cfg.SenderNumber); err != nil { ... }
	//   for note := range client.Receive() {
	//       env := translateReceive(note)
	//       if err := ingest(ctx, env); err != nil { ... }
	//   }
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stopCh:
		return nil
	}
}

// Stop signals an enabled Start to unwind. Idempotent and safe to call
// before Start. For a disabled adapter Stop is a strict no-op because
// Start never started anything.
func (a *Adapter) Stop(_ context.Context) error {
	if !a.cfg.Enabled {
		return nil
	}
	a.stopOnce.Do(func() { close(a.stopCh) })
	return nil
}

// compile-time check: *Adapter satisfies gateway.Adapter.
var _ gateway.Adapter = (*Adapter)(nil)
