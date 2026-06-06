// Package ntfy implements gateway.Adapter against the ntfy.sh publish
// protocol. The package boundary is intentionally narrow: outbound
// publish + inbound action-button callbacks + nothing else. The broker
// owns routing, retries, dedupe, and persistence.
//
// # Outbound
//
// Each OutboundEnvelope becomes a single HTTPS POST to <Server>/ with
// a JSON body (see api.go). Notifications carry Title/Body/Priority.
// ApprovalRequests additionally carry up to three http action buttons.
// Each button's URL is the configured ActionEndpoint with a freshly
// signed token appended as ?t=<token>; the token binds envelope_id,
// artifact_id, action_id, and an expiry so subscribers cannot forge
// the inbound (see sign.go).
//
// Per the spec, ntfy is fire-and-forget at the transport level: we
// treat a JSON publish response with an `id` field as StatusDelivered
// and capture that id as ProviderRef; a 2xx with no parseable id maps
// to StatusUnknown. Non-2xx maps to StatusFailed with the body wrapped
// into the error so the broker's retry layer can decide what to do.
//
// # Inbound
//
// Inbound is the action-button callback path. The adapter exposes
// Handler() returning an http.Handler the daemon mounts on its public
// listener. Start does NOT bind a port; it only captures the
// IngestFunc the handler hands inbound envelopes to. This split keeps
// the adapter's contract clean — there is exactly one place that owns
// public TLS termination (the daemon, behind Tailscale Funnel) and
// the adapter is library code.
//
// # Capabilities
//
// ntfy's three-button cap and lack of any free-form inbound channel
// are surfaced via Capabilities so the broker can route correctly:
//
//	Push:                true
//	FixedChoiceHITL:     true
//	MaxActions:          3
//	FreeFormTextInbound: false
//	FileImageInbound:    false
//	DiffRichApproval:    false
//	NeedsPublicEndpoint: true
package ntfy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
)

// defaultTokenTTL is how long a signed action token remains valid when
// Config.TokenTTL is the zero value. 24h matches the spec's "tokens
// expire after this" example and is long enough that overnight pushes
// remain actionable when the user wakes up.
const defaultTokenTTL = 24 * time.Hour

// defaultHTTPTimeout caps an outbound publish round trip. ntfy is fast
// in the happy path; anything past 30s means the server is unreachable
// and the broker should see a Failed receipt rather than hang on Send.
const defaultHTTPTimeout = 30 * time.Second

// Config holds everything the adapter needs. Fields with non-empty
// defaults document them on the New constructor.
type Config struct {
	// Server is the ntfy publish base URL, e.g. "https://ntfy.sh" or
	// "https://carlos-cronus.ts.net". Required.
	Server string

	// Topic is the destination topic. Treat as a secret — anyone with
	// the topic name can subscribe to outbound messages on the public
	// ntfy.sh. Required.
	Topic string

	// Token is the bearer token for self-hosted ntfy with auth. Empty
	// when publishing to the public ntfy.sh.
	Token string

	// ActionEndpoint is the public URL the action buttons POST to —
	// typically the daemon's Tailscale Funnel'd /gateway/ntfy/action
	// route. Required when the broker ever sends an ApprovalRequest;
	// Notifications work without it.
	ActionEndpoint string

	// SigningKey is the HMAC key used to sign + verify action tokens.
	// Must be at least 32 bytes. Required for ApprovalRequest sends
	// and for handler verification.
	SigningKey []byte

	// PriorityMap maps Urgency.String() values to ntfy priority ints
	// (1..5). Missing entries fall back to 3 ("default").
	PriorityMap map[string]int

	// Headers are extra HTTP headers attached to every publish. Useful
	// for self-hosted setups that want X-Tags pre-baked or a custom
	// X-* trace header.
	Headers map[string]string

	// HTTPClient is the client used for publish. Tests inject an
	// httptest server's client; production leaves this nil and the
	// adapter constructs a sensibly-configured *http.Client.
	HTTPClient *http.Client

	// Now is the clock the adapter uses for token expiries and
	// receipt timestamps. Tests pin it; production leaves it nil and
	// time.Now is used.
	Now func() time.Time

	// TokenTTL is how long signed action tokens remain valid. Defaults
	// to defaultTokenTTL when zero.
	TokenTTL time.Duration
}

// Adapter is the ntfy gateway.Adapter. Construct via New. Safe for
// concurrent Send calls; Start may only be called once.
type Adapter struct {
	cfg Config

	httpClient *http.Client
	now        func() time.Time
	tokenTTL   time.Duration

	handler *actionHandler

	mu      sync.Mutex
	ingest  gateway.IngestFunc
	started bool
	stopped bool
	stopCh  chan struct{}
}

// New constructs an Adapter from cfg. Required fields are validated up
// front so a misconfigured deployment fails at startup, not at first
// publish.
func New(cfg Config) (*Adapter, error) {
	if cfg.Server == "" {
		return nil, errors.New("ntfy: Server required")
	}
	if _, err := url.Parse(cfg.Server); err != nil {
		return nil, fmt.Errorf("ntfy: Server invalid: %w", err)
	}
	if cfg.Topic == "" {
		return nil, errors.New("ntfy: Topic required")
	}
	// SigningKey is required for ApprovalRequest sends; we enforce
	// strict length here so the adapter cannot accept a too-short key
	// silently and then fail every approval at sign time.
	if len(cfg.SigningKey) > 0 && len(cfg.SigningKey) < minSigningKeyLen {
		return nil, ErrSigningKeyTooShort
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultHTTPTimeout}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	ttl := cfg.TokenTTL
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	a := &Adapter{
		cfg:        cfg,
		httpClient: hc,
		now:        now,
		tokenTTL:   ttl,
		stopCh:     make(chan struct{}),
	}
	a.handler = &actionHandler{
		key:    cfg.SigningKey,
		ingest: a.currentIngest,
		now:    now,
	}
	return a, nil
}

// Name returns SourceNtfy.
func (a *Adapter) Name() gateway.Source { return gateway.SourceNtfy }

// Capabilities reports ntfy's per-channel capability matrix. See the
// package doc for the rationale behind each flag.
func (a *Adapter) Capabilities() gateway.Capabilities {
	return gateway.Capabilities{
		Push:                true,
		FixedChoiceHITL:     true,
		MaxActions:          3,
		FreeFormTextInbound: false,
		FileImageInbound:    false,
		DiffRichApproval:    false,
		NeedsPublicEndpoint: true,
	}
}

// Handler returns the http.Handler the daemon mounts on its public
// listener at the configured action endpoint path. The adapter does
// not bind a port itself; this method is the seam.
func (a *Adapter) Handler() http.Handler { return a.handler }

// Send publishes env to ntfy. Returns a DeliveryReceipt and an error
// per the gateway.Adapter contract:
//
//   - On success with a JSON receipt: Status=Delivered, ProviderRef=id.
//   - On success with a non-JSON 2xx: Status=Delivered, ProviderRef="".
//   - On non-2xx OR transport error: Status=Failed, Error=msg.
//
// ConversationReply envelopes are rejected outright (StatusFailed)
// because ntfy has no free-form inbound channel and the broker would
// be stuck waiting for a reply that can never arrive.
func (a *Adapter) Send(ctx context.Context, env gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	if env.Kind == gateway.OutboundConversationReply {
		err := errors.New("ntfy: ConversationReply not supported")
		return gateway.DeliveryReceipt{
			Source: gateway.SourceNtfy,
			Status: gateway.StatusFailed,
			Error:  err.Error(),
		}, err
	}
	req, err := a.buildPublishRequest(env)
	if err != nil {
		return gateway.DeliveryReceipt{
			Source: gateway.SourceNtfy,
			Status: gateway.StatusFailed,
			Error:  err.Error(),
		}, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return gateway.DeliveryReceipt{
			Source: gateway.SourceNtfy,
			Status: gateway.StatusFailed,
			Error:  err.Error(),
		}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.Server, bytes.NewReader(body))
	if err != nil {
		return gateway.DeliveryReceipt{
			Source: gateway.SourceNtfy,
			Status: gateway.StatusFailed,
			Error:  err.Error(),
		}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Accept JSON so the server returns a publish receipt with the id
	// we map to ProviderRef. ntfy honors Accept when the publish body
	// is JSON; older servers may ignore it (we fall back to Unknown).
	httpReq.Header.Set("Accept", "application/json")
	if a.cfg.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	}
	for k, v := range a.cfg.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return gateway.DeliveryReceipt{
			Source: gateway.SourceNtfy,
			Status: gateway.StatusFailed,
			Error:  err.Error(),
		}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("ntfy: publish failed: %d %s: %s", resp.StatusCode, resp.Status, truncate(string(respBody), 256))
		return gateway.DeliveryReceipt{
			Source: gateway.SourceNtfy,
			Status: gateway.StatusFailed,
			Error:  wrapped.Error(),
		}, wrapped
	}

	receipt := gateway.DeliveryReceipt{
		Source:      gateway.SourceNtfy,
		Status:      gateway.StatusDelivered,
		DeliveredAt: a.now().UTC(),
	}
	// Try to parse the JSON receipt for ProviderRef. If the body is
	// empty or not JSON we still consider the publish delivered (the
	// 2xx is the authoritative signal) but downgrade ProviderRef to
	// empty and Status to Unknown — the broker uses Unknown to mean
	// "we tried and the platform didn't tell us anything more."
	if len(bytes.TrimSpace(respBody)) == 0 {
		receipt.Status = gateway.StatusUnknown
		return receipt, nil
	}
	var pr publishResponse
	if err := json.Unmarshal(respBody, &pr); err != nil || pr.ID == "" {
		receipt.Status = gateway.StatusUnknown
		return receipt, nil
	}
	receipt.ProviderRef = pr.ID
	return receipt, nil
}

// Start records the broker's IngestFunc so the action handler can
// route inbound envelopes back. It blocks until ctx is cancelled or
// Stop is called — matches the fake adapter's lifecycle shape so the
// daemon can run all adapters under a single errgroup.
func (a *Adapter) Start(ctx context.Context, ingest gateway.IngestFunc) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return errors.New("ntfy: already started")
	}
	a.started = true
	a.ingest = ingest
	a.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stopCh:
		return nil
	}
}

// Stop signals Start to return. Idempotent.
func (a *Adapter) Stop(ctx context.Context) error {
	a.mu.Lock()
	if !a.stopped {
		a.stopped = true
		close(a.stopCh)
	}
	a.mu.Unlock()
	_ = ctx
	return nil
}

// currentIngest exposes the IngestFunc to the handler closure under
// the same mutex Start updates. Returning the func by value (nil if
// not started) lets the handler emit a 503 rather than panic on a
// pre-Start tap.
func (a *Adapter) currentIngest() gateway.IngestFunc {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ingest
}

// buildPublishRequest translates an OutboundEnvelope into the ntfy
// JSON publish shape. ApprovalRequests have their Actions truncated
// to MaxActions and each surviving action is rendered into a signed
// http button URL.
func (a *Adapter) buildPublishRequest(env gateway.OutboundEnvelope) (publishRequest, error) {
	if err := env.Validate(); err != nil {
		return publishRequest{}, err
	}
	pr := publishRequest{
		Topic:    a.cfg.Topic,
		Title:    env.Title,
		Message:  env.Body,
		Priority: a.priorityFor(env.Urgency),
	}
	if env.Kind != gateway.OutboundApprovalRequest {
		return pr, nil
	}
	if a.cfg.ActionEndpoint == "" {
		return publishRequest{}, errors.New("ntfy: ActionEndpoint required for ApprovalRequest")
	}
	if len(a.cfg.SigningKey) < minSigningKeyLen {
		return publishRequest{}, ErrSigningKeyTooShort
	}
	// signToken needs envelope ID to bind the token; the broker stamps
	// it before Send but we fail loudly rather than emit unforgeable-
	// looking-but-actually-broken tokens.
	if env.ID == "" {
		return publishRequest{}, errors.New("ntfy: envelope ID required for ApprovalRequest")
	}
	actions := env.Actions
	if len(actions) > 3 {
		actions = actions[:3]
	}
	exp := a.now().Add(a.tokenTTL).UnixMilli()
	pr.Actions = make([]publishAction, 0, len(actions))
	for _, act := range actions {
		token, err := signToken(a.cfg.SigningKey, tokenPayload{
			EnvelopeID: env.ID,
			ArtifactID: env.ArtifactID,
			ActionID:   act.ID,
			ExpUnixMs:  exp,
		})
		if err != nil {
			return publishRequest{}, fmt.Errorf("ntfy: sign action %q: %w", act.ID, err)
		}
		actionURL, err := withTokenQuery(a.cfg.ActionEndpoint, token)
		if err != nil {
			return publishRequest{}, fmt.Errorf("ntfy: build action url: %w", err)
		}
		pr.Actions = append(pr.Actions, publishAction{
			Action: "http",
			Label:  act.Label,
			URL:    actionURL,
			Method: http.MethodPost,
			Clear:  true,
		})
	}
	return pr, nil
}

// priorityFor maps the canonical Urgency to ntfy's 1..5 scale via
// Config.PriorityMap. Missing entries fall back to 3 ("default"),
// which renders as a normal-priority notification on every client.
func (a *Adapter) priorityFor(u gateway.Urgency) int {
	if a.cfg.PriorityMap == nil {
		return 3
	}
	if v, ok := a.cfg.PriorityMap[u.String()]; ok {
		return v
	}
	return 3
}

// withTokenQuery appends ?t=<token> to base, preserving any pre-
// existing query parameters. Returns the base unmodified if it cannot
// be parsed; callers detect this via the returned error.
func withTokenQuery(base, token string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(actionTokenQueryParam, token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// truncate clips s to n bytes, appending an ellipsis when truncation
// happens. Used to keep error messages bounded — a verbose ntfy 5xx
// body should not blow up the event log.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
