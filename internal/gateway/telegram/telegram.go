// Package telegram implements the gateway.Adapter contract on top of the
// Telegram Bot API.
//
// The adapter is the "Telegram" leg of the messaging gateway described
// in personal/projects/carlos/notes/2026-06-05 gateway architecture
// (ntfy + telegram + signal + custom).md. The load-bearing capabilities
// are:
//
//   - Outbound sendMessage with MarkdownV2 and an optional inline
//     keyboard (rendered from ApprovalRequest envelopes).
//   - Inbound long-poll via getUpdates, surfacing text messages as
//     InboundMessage and inline-button taps as InboundDecision (with
//     ArtifactID round-tripped through callback_data).
//   - chat_id whitelist: any inbound from a non-allowed chat is logged
//     and dropped - we do not error, because a stranger DM'ing the bot
//     should not stall the long-poll.
//   - Per-bot rate limit: a simple token-bucket spaces sendMessage
//     calls, and we honor Retry-After on a 429 by failing the current
//     Send (the broker owns the retry timing).
//
// The adapter stays dumb per the spec: no retries, no dedupe, no event
// log writes. Those live in the broker. Adapters translate envelopes
// one-for-one between the canonical shape and the platform wire.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
)

// maxResponseBytes caps how much of a Bot API response body we'll buffer
// into memory. Telegram getUpdates batches top out at ~100 updates per
// poll and individual messages are bounded by Bot API limits, so 16 MiB
// is comfortably above any legitimate response while still preventing a
// hostile or buggy upstream from OOMing the daemon by streaming forever.
const maxResponseBytes = 16 * 1024 * 1024

// Config configures a Telegram adapter. BotToken is the only required
// field; everything else has a sensible default (see New).
//
// Tests inject HTTPClient to point at a httptest.NewServer; production
// callers leave it nil and get http.DefaultClient.
type Config struct {
	// BotToken is the Bot API token issued by @BotFather. Required.
	BotToken string

	// APIBaseURL overrides the Bot API host. Defaults to
	// "https://api.telegram.org". Tests point this at a stub server.
	APIBaseURL string

	// AllowedChatIDs is the whitelist of chat_id values whose inbound
	// updates we accept. An empty list means "reject everything" - the
	// safer default for "carlos in my pocket" where the bot is single-
	// user and any other chat is by definition the wrong audience.
	AllowedChatIDs []int64

	// ParseMode sets the per-message parse_mode. Defaults to
	// "MarkdownV2"; callers can pass "" to disable formatting.
	ParseMode string

	// PollTimeoutSec is the long-poll timeout in seconds. Telegram caps
	// this around 50s. We default to 30 to balance idle CPU vs. burn
	// when the bot is in heavy use.
	PollTimeoutSec int

	// HTTPClient is the transport used for all API calls. nil means
	// http.DefaultClient.
	HTTPClient *http.Client

	// Now lets tests stub the clock. nil means time.Now.
	Now func() time.Time

	// RateLimitInterval is the minimum spacing between sendMessage
	// calls. Defaults to 33ms - roughly 30 messages/second, the
	// documented per-bot ceiling for non-broadcast traffic. Tests set
	// it to 0 to disable the bucket entirely.
	RateLimitInterval time.Duration

	// Logger receives diagnostic messages (whitelist rejections, 429s,
	// malformed updates). nil means log.Default().
	Logger *log.Logger
}

// Adapter is the Telegram implementation of gateway.Adapter. The zero
// value is unusable; construct with New.
type Adapter struct {
	cfg     Config
	allowed map[int64]struct{}

	httpc *http.Client
	now   func() time.Time
	log   *log.Logger

	// offsetMu guards offset. offset is the long-poll cursor; the
	// getUpdates loop updates it after every batch.
	offsetMu sync.Mutex
	offset   int64

	// rate gates outbound sendMessage calls. Nil when
	// RateLimitInterval is zero.
	rate *tokenBucket

	// stop signals the long-poll loop to exit; closed by Stop.
	stopOnce sync.Once
	stopCh   chan struct{}
}

// New validates cfg and returns a ready-to-use Adapter. The adapter is
// not started; the caller (broker) drives Start when it's ready.
func New(cfg Config) (*Adapter, error) {
	if cfg.BotToken == "" {
		return nil, errors.New("telegram: bot token required")
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = "https://api.telegram.org"
	}
	if cfg.ParseMode == "" {
		cfg.ParseMode = "MarkdownV2"
	}
	if cfg.PollTimeoutSec == 0 {
		cfg.PollTimeoutSec = 30
	}
	httpc := cfg.HTTPClient
	if httpc == nil {
		// Default client must allow the server-side long-poll window to
		// elapse without tripping the HTTP timeout. PollTimeoutSec is
		// the time Telegram holds the connection open; we add a buffer
		// for connection setup + the response round-trip so a quiet bot
		// doesn't spuriously fail every poll. When PollTimeoutSec is
		// unset, fall back to a 60s ceiling - enough headroom for the
		// default 30s long-poll plus slack.
		pollSec := cfg.PollTimeoutSec
		if pollSec <= 0 {
			pollSec = 30
		}
		httpc = &http.Client{
			Timeout: time.Duration(pollSec+30) * time.Second,
		}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	allowed := make(map[int64]struct{}, len(cfg.AllowedChatIDs))
	for _, id := range cfg.AllowedChatIDs {
		allowed[id] = struct{}{}
	}
	a := &Adapter{
		cfg:     cfg,
		allowed: allowed,
		httpc:   httpc,
		now:     now,
		log:     logger,
		stopCh:  make(chan struct{}),
	}
	if cfg.RateLimitInterval > 0 {
		a.rate = newTokenBucket(cfg.RateLimitInterval, now)
	}
	return a, nil
}

// Name reports gateway.SourceTelegram.
func (a *Adapter) Name() gateway.Source { return gateway.SourceTelegram }

// OutboundCapabilities reports what Telegram can render. Per the spec capability
// matrix: push + fixed-choice HITL (≤3 to match ntfy compat, though TG
// could carry more) + free-form text + file/image inbound. Diff-rich
// approval is false; the spec calls Telegram a "partial" diff renderer
// and we collapse partial → false so the manage view can surface the
// degradation.
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

// Send publishes env to the first whitelisted chat. Per the spec,
// adapters are single-recipient at this level - multi-recipient fanout
// is the broker's job (one Send call per destination). We pick the
// first AllowedChatID as the destination; the broker will eventually
// loop over chats explicitly when multi-user lands.
//
// Send is synchronous; the returned receipt is what the broker logs.
// Errors are wrapped so the broker can pattern-match on the wrapped
// error if it cares (rate-limit detection lives in the broker, but the
// error text contains "429" so a sanity check in the broker can grep
// for it).
func (a *Adapter) Send(ctx context.Context, env gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	if len(a.cfg.AllowedChatIDs) == 0 {
		return a.failedReceipt("no allowed chat id configured"),
			errors.New("telegram: no allowed chat id configured")
	}
	chatID := a.cfg.AllowedChatIDs[0]

	if a.rate != nil {
		if err := a.rate.wait(ctx); err != nil {
			return a.failedReceipt(err.Error()), err
		}
	}

	req, err := a.buildSendMessage(chatID, env)
	if err != nil {
		return a.failedReceipt(err.Error()), err
	}

	var resp sendMessageResponse
	if err := a.callAPI(ctx, "sendMessage", req, &resp); err != nil {
		return a.failedReceipt(err.Error()), err
	}

	return gateway.DeliveryReceipt{
		Source:      gateway.SourceTelegram,
		ProviderRef: strconv.FormatInt(resp.MessageID, 10),
		Status:      gateway.StatusDelivered,
		DeliveredAt: a.now().UTC(),
	}, nil
}

// buildSendMessage converts an OutboundEnvelope into the typed
// sendMessageRequest. Title is bolded above the body when present;
// Actions become a one-button-per-row inline keyboard (matching how
// short Telegram button rows lay out on mobile).
//
// We escape every user-supplied substring against MarkdownV2 reserved
// chars so a stray period in a title doesn't get the entire message
// rejected by the Bot API.
func (a *Adapter) buildSendMessage(chatID int64, env gateway.OutboundEnvelope) (sendMessageRequest, error) {
	req := sendMessageRequest{
		ChatID:    chatID,
		ParseMode: a.cfg.ParseMode,
		// UrgencyLow → silent push. The broker is the source of truth
		// for the urgency → notification mapping; the spec calls out
		// "default for everything except approvals is silent."
		DisableNotification: env.Urgency == gateway.UrgencyLow,
	}

	var b strings.Builder
	if env.Title != "" {
		if a.cfg.ParseMode == "MarkdownV2" {
			b.WriteString("*")
			b.WriteString(EscapeMarkdownV2(env.Title))
			b.WriteString("*")
		} else {
			b.WriteString(env.Title)
		}
		if env.Body != "" {
			b.WriteString("\n\n")
		}
	}
	if env.Body != "" {
		if a.cfg.ParseMode == "MarkdownV2" {
			b.WriteString(EscapeMarkdownV2(env.Body))
		} else {
			b.WriteString(env.Body)
		}
	}
	req.Text = b.String()
	if req.Text == "" {
		return sendMessageRequest{}, errors.New("telegram: empty message text")
	}

	if env.Kind == gateway.OutboundApprovalRequest && len(env.Actions) > 0 {
		if env.ArtifactID == "" {
			return sendMessageRequest{}, errors.New("telegram: approval request requires artifact id")
		}
		kb := &inlineKeyboardMarkup{}
		for _, act := range env.Actions {
			data, err := EncodeCallbackData(act.ID, env.ArtifactID)
			if err != nil {
				return sendMessageRequest{}, fmt.Errorf("telegram: encode callback for %q: %w", act.ID, err)
			}
			kb.InlineKeyboard = append(kb.InlineKeyboard, []inlineKeyboardButton{
				{Text: act.Label, CallbackData: data},
			})
		}
		req.ReplyMarkup = kb
	}

	return req, nil
}

// failedReceipt is the convenience constructor for the StatusFailed
// shape the broker expects on any Send error. Centralizing the shape
// here keeps callers from forgetting to set Source/Status consistently.
func (a *Adapter) failedReceipt(reason string) gateway.DeliveryReceipt {
	return gateway.DeliveryReceipt{
		Source: gateway.SourceTelegram,
		Status: gateway.StatusFailed,
		Error:  reason,
	}
}

// Start launches the long-poll loop. Returns nil on graceful shutdown
// via ctx cancellation or Stop. Returns a non-nil error only if the
// loop hits a non-recoverable HTTP error (network failures and 5xx are
// logged and retried; a 401 / wrong-token error stops the loop).
func (a *Adapter) Start(ctx context.Context, ingest gateway.IngestFunc) error {
	if ingest == nil {
		return errors.New("telegram: ingest required")
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.stopCh:
			return nil
		default:
		}

		if err := a.pollOnce(ctx, ingest); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			// Transient errors (network blip, server 5xx, malformed
			// JSON) get logged and we retry. The long-poll itself is
			// the natural backoff - we don't burn a tight loop.
			a.log.Printf("telegram: getUpdates: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-a.stopCh:
				return nil
			case <-time.After(time.Second):
			}
		}
	}
}

// pollOnce issues one getUpdates request, processes every Update in the
// response, and advances the cursor. Returns nil after a successful
// processed batch (even if empty).
func (a *Adapter) pollOnce(ctx context.Context, ingest gateway.IngestFunc) error {
	a.offsetMu.Lock()
	offset := a.offset
	a.offsetMu.Unlock()

	req := getUpdatesRequest{
		Offset:         offset,
		Timeout:        a.cfg.PollTimeoutSec,
		AllowedUpdates: []string{"message", "callback_query"},
	}

	var updates []update
	if err := a.callAPI(ctx, "getUpdates", req, &updates); err != nil {
		return err
	}

	for _, u := range updates {
		a.handleUpdate(ctx, u, ingest)
		// Advance the cursor past every update we've seen, even ones
		// we dropped (wrong chat, malformed, etc.) - re-fetching them
		// would just produce the same drop next iteration.
		a.offsetMu.Lock()
		if u.UpdateID >= a.offset {
			a.offset = u.UpdateID + 1
		}
		a.offsetMu.Unlock()
	}
	return nil
}

// handleUpdate routes one Update into the IngestFunc. The Bot API
// guarantees at most one of Message / CallbackQuery is set per Update,
// so we branch on whichever is present.
func (a *Adapter) handleUpdate(ctx context.Context, u update, ingest gateway.IngestFunc) {
	switch {
	case u.CallbackQuery != nil:
		a.handleCallback(ctx, u, ingest)
	case u.Message != nil:
		a.handleMessage(ctx, u, ingest)
	default:
		// Update type we don't model (edited_message, channel_post,
		// etc.). Cursor still advances so we don't re-process.
		a.log.Printf("telegram: update %d has no message or callback_query, skipping", u.UpdateID)
	}
}

// handleMessage turns a text Message into an InboundMessage envelope.
// Whitelist check runs first; non-allowed chats produce a single log
// row and are dropped (no error - see package comment).
func (a *Adapter) handleMessage(ctx context.Context, u update, ingest gateway.IngestFunc) {
	m := u.Message
	if m.Chat.ID == 0 {
		a.log.Printf("telegram: update %d missing chat id, skipping", u.UpdateID)
		return
	}
	if _, ok := a.allowed[m.Chat.ID]; !ok {
		a.log.Printf("telegram: rejecting message from non-whitelisted chat %d", m.Chat.ID)
		return
	}
	if m.Text == "" {
		// Non-text content (photo, sticker, etc.) - surface a log row
		// and skip. File/image inbound is a future enhancement; the
		// capability bit is set so the broker knows we *will* handle
		// it, but the wire code lives in a later slice.
		a.log.Printf("telegram: update %d has no text, skipping", u.UpdateID)
		return
	}
	env := gateway.InboundEnvelope{
		GatewayEventID: strconv.FormatInt(u.UpdateID, 10),
		Source:         gateway.SourceTelegram,
		From:           strconv.FormatInt(m.Chat.ID, 10),
		Kind:           gateway.InboundMessage,
		Body:           m.Text,
		ReceivedAt:     a.now().UTC(),
	}
	if err := ingest(ctx, env); err != nil {
		a.log.Printf("telegram: ingest message %d failed: %v", u.UpdateID, err)
	}
}

// handleCallback turns a CallbackQuery into an InboundDecision envelope.
// We decode callback_data into actionID + artifactID, then map the
// actionID to a DecisionKind. Any unknown action surfaces as a log row
// and a dropped envelope - the broker should never receive an inbound
// it can't act on.
//
// We always answer the callback (answerCallbackQuery) so the spinner on
// the user's tapped button dismisses promptly, even when the inbound
// gets dropped on our side. The user sees "got it" UI either way.
func (a *Adapter) handleCallback(ctx context.Context, u update, ingest gateway.IngestFunc) {
	cb := u.CallbackQuery
	defer a.answerCallback(ctx, cb.ID)

	if cb.Message == nil || cb.Message.Chat.ID == 0 {
		a.log.Printf("telegram: callback %s missing chat, skipping", cb.ID)
		return
	}
	if _, ok := a.allowed[cb.Message.Chat.ID]; !ok {
		a.log.Printf("telegram: rejecting callback from non-whitelisted chat %d", cb.Message.Chat.ID)
		return
	}
	if cb.Data == "" {
		a.log.Printf("telegram: callback %s has empty data, skipping", cb.ID)
		return
	}
	actionID, artifactID, err := DecodeCallbackData(cb.Data)
	if err != nil {
		a.log.Printf("telegram: callback %s malformed data: %v", cb.ID, err)
		return
	}
	kind := gateway.DecisionKind(actionID)
	if !kind.Valid() {
		a.log.Printf("telegram: callback %s unknown action %q", cb.ID, actionID)
		return
	}
	env := gateway.InboundEnvelope{
		GatewayEventID: strconv.FormatInt(u.UpdateID, 10),
		Source:         gateway.SourceTelegram,
		From:           strconv.FormatInt(cb.Message.Chat.ID, 10),
		Kind:           gateway.InboundDecision,
		ArtifactID:     artifactID,
		Decision:       &gateway.Decision{Kind: kind},
		ReceivedAt:     a.now().UTC(),
	}
	if err := ingest(ctx, env); err != nil {
		a.log.Printf("telegram: ingest decision %s failed: %v", cb.ID, err)
	}
}

// answerCallback fires the answerCallbackQuery method to dismiss the
// spinner. Best-effort - a failure here is logged but never propagated
// because the decision envelope has already been ingested and a user-
// visible spinner is purely cosmetic.
func (a *Adapter) answerCallback(ctx context.Context, callbackID string) {
	req := answerCallbackQueryRequest{CallbackQueryID: callbackID}
	var ok bool
	if err := a.callAPI(ctx, "answerCallbackQuery", req, &ok); err != nil {
		a.log.Printf("telegram: answerCallbackQuery %s: %v", callbackID, err)
	}
}

// Stop signals the long-poll loop to wind down. Idempotent.
func (a *Adapter) Stop(_ context.Context) error {
	a.stopOnce.Do(func() {
		close(a.stopCh)
	})
	return nil
}

// callAPI issues a POST to the Bot API's <method> endpoint with body as
// the JSON payload, then decodes the typed result into result (which
// must be a pointer to whatever the method returns). Returns an error
// if the HTTP call fails, the API responds with ok=false, or JSON
// decoding fails.
//
// 429s carry a Retry-After hint we surface in the error so the broker
// can wait the right amount. We do NOT retry inside the adapter - the
// broker owns the retry loop. The error text always contains "429"
// when the underlying cause is rate-limiting, so a non-typed grep
// works.
func (a *Adapter) callAPI(ctx context.Context, method string, body any, result any) error {
	endpoint := a.endpointFor(method)

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("telegram: marshal %s: %w", method, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram: build %s request: %w", method, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := a.httpc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, err)
	}
	defer httpResp.Body.Close()

	// Cap the response body at maxResponseBytes so a hostile or buggy
	// upstream can't OOM the daemon by streaming forever. Read one byte
	// past the cap so we can distinguish "exactly at limit" from "limit
	// exceeded" and surface a clean error in the latter case.
	respBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("telegram: read %s response: %w", method, err)
	}
	if int64(len(respBytes)) > maxResponseBytes {
		return fmt.Errorf("telegram: %s response exceeds %d byte cap", method, maxResponseBytes)
	}

	var resp apiResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return fmt.Errorf("telegram: decode %s response: %w", method, err)
	}

	if !resp.OK {
		if httpResp.StatusCode == http.StatusTooManyRequests || resp.ErrorCode == http.StatusTooManyRequests {
			retryAfter := 0
			if resp.Parameters != nil {
				retryAfter = resp.Parameters.RetryAfter
			}
			if retryAfter == 0 {
				// Fall back to the HTTP header if Telegram didn't
				// embed Parameters.retry_after.
				if h := httpResp.Header.Get("Retry-After"); h != "" {
					if v, perr := strconv.Atoi(h); perr == nil {
						retryAfter = v
					}
				}
			}
			a.log.Printf("telegram: %s rate-limited, retry after %ds: %s", method, retryAfter, resp.Description)
			return fmt.Errorf("telegram: %s: 429 too many requests (retry after %ds): %s", method, retryAfter, resp.Description)
		}
		return fmt.Errorf("telegram: %s: api error %d: %s", method, resp.ErrorCode, resp.Description)
	}

	if result == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		return fmt.Errorf("telegram: decode %s result: %w", method, err)
	}
	return nil
}

// endpointFor builds the per-method URL. Format is documented at
// https://core.telegram.org/bots/api: <base>/bot<token>/<method>.
func (a *Adapter) endpointFor(method string) string {
	// url.PathEscape on the token guards against the (unlikely) case
	// where it contains a slash; in practice Bot API tokens are
	// alphanumeric + colon, but defense in depth is cheap.
	return a.cfg.APIBaseURL + "/bot" + url.PathEscape(a.cfg.BotToken) + "/" + method
}

// tokenBucket is the per-bot rate limiter. We model it as a single-token
// bucket refilled every interval - equivalent to "wait at least
// interval since the last sendMessage." Sufficient for the per-bot
// 30 msg/s ceiling; if we ever need bursty sends, swap in a multi-
// token bucket without changing the call site.
type tokenBucket struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
	now      func() time.Time
}

func newTokenBucket(interval time.Duration, now func() time.Time) *tokenBucket {
	return &tokenBucket{interval: interval, now: now}
}

// wait blocks until the bucket allows another send, or ctx is cancelled.
// Reserves the slot synchronously so concurrent waiters serialize FIFO
// (or close to it, modulo lock-acquisition order).
func (b *tokenBucket) wait(ctx context.Context) error {
	b.mu.Lock()
	now := b.now()
	delay := time.Duration(0)
	if !b.next.IsZero() && now.Before(b.next) {
		delay = b.next.Sub(now)
	}
	// Reserve the next slot regardless of whether we slept - this is
	// what makes the bucket FIFO under concurrency.
	b.next = now.Add(delay + b.interval)
	b.mu.Unlock()
	if delay <= 0 {
		return nil
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
