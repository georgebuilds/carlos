// Package ollama implements a pure-Go streaming client for the Ollama
// /api/chat endpoint. No SDK — we hit the HTTP endpoint directly so the
// capability map stays honest about what the API actually offers versus
// what an SDK papers over.
//
// Streaming is line-delimited JSON (one complete object per line), not
// SSE; the parser lives in jsonl.go. Wire-format adapters between
// providers.Request/Event and the Ollama schema live in messages.go.
// This file is the orchestrator: build request → POST → parse stream →
// map to canonical Event channel.
//
// Tool-use streaming caveat: unlike Anthropic and OpenAI, Ollama does
// NOT stream tool_call arguments incrementally — it emits the whole
// tool_call atomically (typically on the final chunk where the model
// decided to call a tool). We synthesize a ToolUseStart immediately
// followed by ToolUseEnd in that case so the canonical channel shape
// stays uniform. Ollama also doesn't supply a tool_call ID, so we
// synthesize one (the canonical providers.ToolUse.ID is non-empty by
// contract).
//
// Model compatibility: tool support is per-model on Ollama. Llama 3.1+,
// Mistral Nemo, Qwen 2.5+, and Firefunction-v2 are known-good. Older
// models silently ignore the `tools` parameter and respond with a text
// description of what they would do. The agent loop handles that case
// gracefully — there's nothing for us to detect or warn about here.
package ollama

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
)

// Client is the Ollama /api/chat client. Safe for concurrent use: each
// Stream call runs an independent HTTP request + goroutine.
//
// Auth: Ollama is local-only by design — no Authorization header is
// sent. If a downstream user proxies Ollama through an authenticating
// reverse proxy, that's their concern; the canonical local case is
// auth-free.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New constructs a client pointed at baseURL (defaulting to the standard
// loopback endpoint when empty). Like the Anthropic client, we set a
// per-dial header timeout but no overall HTTP timeout — streaming
// completions can legitimately take minutes and the caller's context is
// the right cancellation surface.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &Client{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

func (c *Client) Name() string { return "ollama" }

// Capabilities for Ollama. See package doc for the rationale on each.
//
//   - ParallelToolUse: false. Even when a model emits multiple tool_calls
//     in one response, the on-the-wire shape doesn't carry the parallel
//     semantics Anthropic does — we treat each call serially.
//   - PromptCaching: false. No built-in caching API surface.
//   - StructuredOut: true. Ollama supports `format: "json"` or a JSON
//     schema via the format field. carlos doesn't use it yet but the
//     capability is real.
//   - Vision: false. LLaVA-style multimodal works via an `images` field
//     on messages, but we punt that to a later slice (no canonical
//     image block exists yet).
func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		ParallelToolUse: false,
		PromptCaching:   false,
		StructuredOut:   true,
		Vision:          false,
	}
}

// Stream sends req to /api/chat with stream=true and returns a channel
// of canonical providers.Event values. The channel is closed when the
// stream ends (normal or error). Cancellation: cancel ctx to abort the
// in-flight HTTP request and goroutine.
//
// Event mapping (see package doc for the full contract):
//   - chunk.message.content non-empty  → EventTextDelta
//   - chunk.message.tool_calls present → EventToolUseStart then
//     EventToolUseEnd per call (atomic, no per-arg streaming)
//   - chunk.done=true                  → EventStopReason with the
//     mapped done_reason (or "tool_use" if tool_calls were present)
//   - HTTP non-200                     → error returned synchronously
//   - mid-stream error                 → EventError then close
func (c *Client) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	body, err := buildRequest(req)
	if err != nil {
		return nil, err
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/chat", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: HTTP: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode,
			strings.TrimSpace(string(b)))
	}

	out := make(chan providers.Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		emit := func(ev providers.Event) bool {
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}

		err := parseJSONL(resp.Body, func(line string) error {
			var chunk streamChunk
			if err := json.Unmarshal([]byte(line), &chunk); err != nil {
				// Malformed line shouldn't tear down the stream; surface
				// as error event and continue scanning. Same defensive
				// posture as the Anthropic client takes per SSE frame.
				// Scrub any model-name reveal so identity framing stays
				// carlos's.
				emit(providers.Event{
					Kind: providers.EventError,
					Err:  providers.ScrubModelName(fmt.Errorf("ollama: parse chunk: %w", err)),
				})
				return nil
			}

			// Mid-stream error: Ollama uses a top-level "error" string on
			// the response object (e.g. when the model is not found or
			// the server hit an internal error after streaming started).
			if chunk.Error != "" {
				emit(providers.Event{
					Kind: providers.EventError,
					Err:  providers.ScrubModelName(fmt.Errorf("ollama: %s", chunk.Error)),
				})
				return nil
			}

			// Text delta: the most common chunk shape. Skip empty
			// content to avoid spamming the channel with no-op events.
			if chunk.Message.Content != "" {
				if !emit(providers.Event{
					Kind: providers.EventTextDelta,
					Text: chunk.Message.Content,
				}) {
					return nil
				}
			}

			// Tool calls: atomic per call. Synthesize start+end events
			// back-to-back. ID synthesis: Ollama doesn't supply one, but
			// the canonical providers.ToolUse.ID is non-empty by
			// contract; the agent loop uses it to thread tool_results
			// back. "ollama-tu-" prefix keeps the synthesized origin
			// obvious in debug output.
			for _, tc := range chunk.Message.ToolCalls {
				args := tc.Function.Arguments
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				tu := &providers.ToolUse{
					ID:    synthesizeToolUseID(),
					Name:  tc.Function.Name,
					Input: []byte(args),
				}
				if !emit(providers.Event{
					Kind:    providers.EventToolUseStart,
					ToolUse: tu,
				}) {
					return nil
				}
				if !emit(providers.Event{
					Kind:    providers.EventToolUseEnd,
					ToolUse: tu,
				}) {
					return nil
				}
			}

			// Final chunk: surface stop reason. Mapping:
			//   tool_calls present in this chunk → "tool_use"
			//     (canonical Anthropic name; overrides done_reason
			//     because Ollama reports "stop" even when a tool was
			//     called, which is unhelpful for the agent loop)
			//   "stop"   → "end_turn"
			//   "length" → "max_tokens"
			//   other    → pass through verbatim
			if chunk.Done {
				stop := mapStopReason(chunk.DoneReason)
				if len(chunk.Message.ToolCalls) > 0 {
					stop = "tool_use"
				}
				emit(providers.Event{
					Kind: providers.EventStopReason,
					Stop: stop,
				})
			}
			return nil
		})
		if err != nil && !isContextCancellation(ctx, err) {
			emit(providers.Event{Kind: providers.EventError, Err: providers.ScrubModelName(err)})
		}
	}()
	return out, nil
}

// mapStopReason normalizes Ollama's done_reason vocabulary to the
// canonical (Anthropic-shaped) stop reason names the agent loop
// expects. Empty input (Ollama omitted the field) falls through as
// "end_turn" — a done=true chunk with no reason is the normal
// successful-completion path on older Ollama builds.
func mapStopReason(reason string) string {
	switch reason {
	case "", "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	default:
		return reason
	}
}

// synthesizeToolUseID generates a short random ID for a tool_call that
// arrived without one. Ollama doesn't supply IDs because its conceptual
// model is one-call-per-turn; the canonical providers.ToolUse.ID exists
// to thread N parallel calls' results back in Anthropic's protocol. We
// give every call a unique ID so the agent loop's bookkeeping works
// uniformly across providers.
//
// 8 random bytes (16 hex chars) is overkill for in-process uniqueness
// but cheap, and matches the human-readable token-length feel of
// Anthropic's `toolu_…` IDs.
func synthesizeToolUseID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand is documented to never fail in practice on the
		// platforms Go supports; if it does, a timestamp fallback keeps
		// us from returning an empty string (which would violate the
		// canonical contract).
		return fmt.Sprintf("ollama-tu-%d", time.Now().UnixNano())
	}
	return "ollama-tu-" + hex.EncodeToString(b[:])
}

func isContextCancellation(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	return err == context.Canceled || err == context.DeadlineExceeded
}
