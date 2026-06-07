package oacompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/georgebuilds/carlos/internal/providers"
)

// Config carries per-provider knobs into Stream. Each field has a
// behavior reason documented inline; provider clients populate this
// once in their Stream method and hand it to the shared driver.
type Config struct {
	// Name is the short provider identifier ("openai", "openrouter").
	// Used as a prefix in error messages and (effectively) nowhere else.
	Name string

	// BaseURL is the API root (e.g. "https://api.openai.com",
	// "https://openrouter.ai/api/v1"). Path is appended verbatim.
	BaseURL string

	// Path is the endpoint path (e.g. "/v1/chat/completions" for OpenAI,
	// "/chat/completions" for OpenRouter - the latter's BaseURL already
	// includes /api/v1).
	Path string

	// APIKey is sent as `Authorization: Bearer <APIKey>`.
	APIKey string

	// HTTPClient is the transport. Stream does not impose any timeouts;
	// the caller's *http.Client supplies the connect/header deadlines and
	// the request context governs the read side.
	HTTPClient *http.Client

	// ExtraHeaders are added to the outgoing HTTP request after the standard
	// Content-Type / Accept / Authorization triple. OpenRouter uses this to
	// inject HTTP-Referer and X-Title for attribution; OpenAI leaves it nil.
	ExtraHeaders map[string]string

	// MapFinishReason translates the provider's finish_reason vocabulary
	// into the canonical Stop name. OpenAI maps stop→end_turn etc.;
	// OpenRouter passes most values through and only translates tool_calls
	// / function_call → tool_use. If nil, finish_reason values are
	// forwarded verbatim.
	MapFinishReason func(string) string
}

// Stream sends req to cfg.BaseURL+cfg.Path with stream=true and returns a
// channel of canonical providers.Event values. The channel is closed when
// the stream ends (normal or error). Cancellation: cancel ctx to abort the
// in-flight HTTP request and the goroutine.
//
// This is the only entry point provider clients need; they construct a
// Config in their own Stream method and delegate here.
func Stream(ctx context.Context, cfg Config, req providers.Request) (<-chan providers.Event, error) {
	body, err := BuildRequest(req, cfg.Name)
	if err != nil {
		return nil, err
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal: %w", cfg.Name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.BaseURL+cfg.Path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", cfg.Name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	for k, v := range cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: HTTP: %w", cfg.Name, err)
	}
	if resp.StatusCode != http.StatusOK {
		// Drain + close before returning so the connection can be reused.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d: %s", cfg.Name, resp.StatusCode, strings.TrimSpace(string(b)))
	}

	out := make(chan providers.Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		ProcessStream(ctx, resp.Body, out, cfg.Name, cfg.MapFinishReason)
	}()
	return out, nil
}

// ProcessStream parses an OpenAI-compatible chat-completions SSE stream
// from r and emits canonical providers.Event values on out. Returns when
// the stream is exhausted (EOF or `data: [DONE]`), the context is
// cancelled, or an unrecoverable read error occurs.
//
// errPrefix is stamped into any wrapped error so log lines say
// "openai: parse chunk:" or "openrouter: parse chunk:" rather than
// leaking the shared package's name. mapFinishReason translates the
// provider's finish_reason vocabulary; pass nil to forward values
// verbatim.
//
// Tool-use streaming: OpenAI-compatible APIs ship tool_calls as a sequence
// of partial `function.arguments` strings keyed by `tool_calls[].index`.
// We buffer per index and emit a single EventToolUseEnd with the assembled
// JSON when:
//
//  1. The finish_reason for the choice becomes "tool_calls" (canonical),
//     OR
//  2. The stream terminates via `data: [DONE]` or EOF (defensive flush in
//     case finish_reason was missing - some compatible servers omit it).
//
// EventToolUseStart fires on the FIRST delta that carries BOTH id+name;
// some servers (notably certain Azure proxies) send name in chunk 1 but
// id in chunk 2, so we defer Start until both are known. Consumers can
// then rely on EventToolUseStart.ToolUse.ID being populated.
//
// EventToolUseEnd events are emitted in ascending Index order so logs +
// tests see a deterministic sequence; parallel tool_use semantics are
// order-insensitive at dispatch time but a stable emit order matters.
func ProcessStream(
	ctx context.Context,
	r io.Reader,
	out chan<- providers.Event,
	errPrefix string,
	mapFinishReason func(string) string,
) {
	emit := func(ev providers.Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// Per-tool-call accumulators, keyed by the OpenAI tool_calls[].index.
	// We track:
	//   - the live ToolUse (id+name+accumulated args) so we can emit
	//     incremental EventToolUseDelta events as args arrive,
	//   - whether we have emitted Start yet (Start fires once id+name
	//     are both known - see header doc),
	//   - the args buffer (kept separate so we can finalize empty as "{}").
	type accum struct {
		tu        *providers.ToolUse
		args      strings.Builder
		startSent bool
	}
	toolCalls := map[int]*accum{}

	// flushTool emits EventToolUseEnd for the given index if one is
	// pending. Called on finish_reason=tool_calls and on [DONE]/EOF.
	flushTool := func(idx int) {
		a, ok := toolCalls[idx]
		if !ok || a.tu == nil {
			return
		}
		full := a.args.String()
		if full == "" {
			full = "{}"
		}
		a.tu.Input = []byte(full)
		emit(providers.Event{Kind: providers.EventToolUseEnd, ToolUse: a.tu})
		delete(toolCalls, idx)
	}
	// flushAllTools emits in ascending Index order so downstream consumers
	// see a deterministic sequence regardless of map-iteration order.
	flushAllTools := func() {
		maxIdx := -1
		for i := range toolCalls {
			if i > maxIdx {
				maxIdx = i
			}
		}
		for i := 0; i <= maxIdx; i++ {
			flushTool(i)
		}
	}

	mapStop := func(fr string) string {
		if mapFinishReason == nil {
			return fr
		}
		return mapFinishReason(fr)
	}

	parseErr := ParseSSE(r, func(f SSEFrame) error {
		data := f.Data
		if data == "" {
			return nil
		}
		// OpenAI's end-of-stream sentinel. Flush any pending tool_use
		// accumulators (defensive: finish_reason should already have
		// done this) and signal end-of-stream by returning nil - the
		// scanner loop will continue but no further frames will arrive.
		if data == "[DONE]" {
			flushAllTools()
			return nil
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// One malformed frame shouldn't tear down the stream;
			// surface as an error event and continue. Scrub any
			// model-name reveal first so identity framing stays
			// carlos's.
			emit(providers.Event{
				Kind: providers.EventError,
				Err:  providers.ScrubModelName(fmt.Errorf("%s: parse chunk: %w", errPrefix, err)),
			})
			return nil
		}
		// Server-side error embedded in the stream. Both OpenAI and
		// OpenRouter use this when an upstream provider fails after
		// the connection has been accepted. Scrub before emit - these
		// envelopes have historically been the noisiest leak surface
		// (OpenRouter passes upstream provider strings through).
		if chunk.Error != nil {
			emit(providers.Event{
				Kind: providers.EventError,
				Err:  providers.ScrubModelName(fmt.Errorf("%s %s: %s", errPrefix, errorTag(chunk.Error), chunk.Error.Message)),
			})
			return nil
		}

		for _, ch := range chunk.Choices {
			if ch.Delta != nil {
				// Text content delta.
				if ch.Delta.Content != "" {
					emit(providers.Event{
						Kind: providers.EventTextDelta,
						Text: ch.Delta.Content,
					})
				}
				// Tool-call deltas. Each entry has a stable .Index
				// across chunks; we accumulate by that.
				for _, tc := range ch.Delta.ToolCalls {
					a, ok := toolCalls[tc.Index]
					if !ok {
						a = &accum{tu: &providers.ToolUse{}}
						toolCalls[tc.Index] = a
					}
					// id+name typically arrive in the first chunk
					// for this index; carry them forward. Don't
					// clobber existing non-empty values with empty
					// re-sends (some OSS upstreams via OpenRouter do
					// this).
					if tc.ID != "" {
						a.tu.ID = tc.ID
					}
					if tc.Function != nil && tc.Function.Name != "" {
						a.tu.Name = tc.Function.Name
					}
					// Emit Start once we have BOTH id and name (see
					// header doc). Consumers rely on Start carrying
					// non-empty ID + Name.
					if !a.startSent && a.tu.ID != "" && a.tu.Name != "" {
						a.startSent = true
						emit(providers.Event{
							Kind:    providers.EventToolUseStart,
							ToolUse: &providers.ToolUse{ID: a.tu.ID, Name: a.tu.Name},
						})
					}
					// Argument fragments. Optionally surface a Delta
					// so progressive renderers can show partial JSON.
					// Gated on startSent so Delta events always carry
					// a populated ID+Name (consumers can rely on it).
					if tc.Function != nil && tc.Function.Arguments != "" {
						a.args.WriteString(tc.Function.Arguments)
						if a.startSent {
							emit(providers.Event{
								Kind: providers.EventToolUseDelta,
								ToolUse: &providers.ToolUse{
									ID:    a.tu.ID,
									Name:  a.tu.Name,
									Input: []byte(tc.Function.Arguments),
								},
							})
						}
					}
				}
			}
			// finish_reason on the choice. When it's tool_calls (or
			// the legacy "function_call" some providers still emit)
			// we must flush ALL accumulated tool_call buffers BEFORE
			// emitting the stop reason - the agent loop expects to
			// see complete EventToolUseEnd events before it switches
			// on EventStopReason.
			if ch.FinishReason != "" {
				if ch.FinishReason == "tool_calls" || ch.FinishReason == "function_call" {
					flushAllTools()
				}
				emit(providers.Event{
					Kind: providers.EventStopReason,
					Stop: mapStop(ch.FinishReason),
				})
			}
		}
		return nil
	})

	// Defensive: if the server hung up without [DONE] or finish_reason,
	// flush whatever we have so partial tool_calls aren't lost.
	flushAllTools()

	if parseErr != nil && !isContextCancellation(ctx, parseErr) {
		emit(providers.Event{Kind: providers.EventError, Err: providers.ScrubModelName(parseErr)})
	}
}

// errorTag picks the most informative field from the streaming error
// envelope for the wrapped message. OpenAI tends to populate Type
// ("server_error", "invalid_request_error"); OpenRouter tends to populate
// Code ("upstream_error", a numeric HTTP code as a string).
func errorTag(e *StreamError) string {
	if e.Type != "" {
		return e.Type
	}
	return e.Code
}

func isContextCancellation(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	return err == context.Canceled || err == context.DeadlineExceeded
}
