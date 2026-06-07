// Package anthropic implements a pure-Go streaming client for the
// Anthropic Messages API. No SDK - we hit the HTTP endpoint directly so
// the capability map stays honest about what the API actually offers
// versus what an SDK papers over.
//
// Streaming uses server-sent events; the SSE parser lives in sse.go.
// Wire-format adapters between providers.Request/Event and the Anthropic
// schema live in messages.go. This file is the orchestrator: build
// request → POST → parse stream → map to canonical Event channel.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
)

// Client is the Anthropic Messages API client. Safe for concurrent use:
// each Stream call runs an independent HTTP request + goroutine.
type Client struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// New constructs a client with a 30-second connect timeout. The per-request
// read deadline is governed by the caller's context (cancel ctx → cancel
// stream); we don't impose an overall deadline here because streaming
// completions can legitimately take minutes.
func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: "https://api.anthropic.com",
		HTTPClient: &http.Client{
			// No overall Timeout - that aborts in-flight streams.
			// Per-dial timeout via Transport instead.
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

func (c *Client) Name() string { return "anthropic" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		ParallelToolUse: true,
		PromptCaching:   true,
		StructuredOut:   true,
		Vision:          true,
	}
}

// Stream sends req to /v1/messages with stream=true and returns a channel
// of canonical providers.Event values. The channel is closed when the
// stream ends (normal or error). Cancellation: cancel ctx to abort the
// in-flight HTTP request and goroutine.
//
// Tool-use streaming: Anthropic ships tool_use input as a sequence of
// `input_json_delta` events that concatenate into a JSON string. We
// buffer per content-block-index and emit a single EventToolUseEnd with
// the assembled input when the block stops.
func (c *Client) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	body, err := buildRequest(req)
	if err != nil {
		return nil, err
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: HTTP: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Drain + close before returning so the connection can be reused.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
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
		// Per-block tool-use input accumulator. Keyed by content block
		// index because Anthropic interleaves indices when parallel
		// tool_use is enabled.
		toolBlocks := map[int]*providers.ToolUse{}
		toolInputBuf := map[int]*strings.Builder{}

		err := parseSSE(resp.Body, func(f sseFrame) error {
			if f.Data == "" {
				return nil
			}
			var ev streamEvent
			if err := json.Unmarshal([]byte(f.Data), &ev); err != nil {
				// One malformed frame shouldn't tear down the stream;
				// surface as an error event and continue. Scrub any
				// model-name reveal so identity framing stays carlos's.
				emit(providers.Event{Kind: providers.EventError,
					Err: providers.ScrubModelName(fmt.Errorf("anthropic: parse frame %q: %w", f.Event, err))})
				return nil
			}
			switch ev.Type {
			case "message_start":
				// Nothing to surface; the message stub carries usage
				// + model fields we don't currently expose.
			case "content_block_start":
				if ev.ContentBlock == nil {
					return nil
				}
				switch ev.ContentBlock.Type {
				case "text":
					// Text content blocks don't emit a start event
					// to the canonical channel; deltas suffice.
				case "tool_use":
					tu := &providers.ToolUse{
						ID:   ev.ContentBlock.ID,
						Name: ev.ContentBlock.Name,
					}
					toolBlocks[ev.Index] = tu
					toolInputBuf[ev.Index] = &strings.Builder{}
					emit(providers.Event{
						Kind: providers.EventToolUseStart, ToolUse: tu,
					})
				}
			case "content_block_delta":
				if ev.Delta == nil {
					return nil
				}
				switch ev.Delta.Type {
				case "text_delta":
					emit(providers.Event{
						Kind: providers.EventTextDelta, Text: ev.Delta.Text,
					})
				case "input_json_delta":
					if buf, ok := toolInputBuf[ev.Index]; ok {
						buf.WriteString(ev.Delta.PartialJSON)
						// Also emit a streaming delta in case the
						// caller wants to render progressively.
						if tu, ok := toolBlocks[ev.Index]; ok {
							emit(providers.Event{
								Kind: providers.EventToolUseDelta,
								ToolUse: &providers.ToolUse{
									ID: tu.ID, Name: tu.Name,
									Input: []byte(ev.Delta.PartialJSON),
								},
							})
						}
					}
				}
			case "content_block_stop":
				if tu, ok := toolBlocks[ev.Index]; ok {
					full := toolInputBuf[ev.Index].String()
					if full == "" {
						full = "{}"
					}
					tu.Input = []byte(full)
					emit(providers.Event{
						Kind: providers.EventToolUseEnd, ToolUse: tu,
					})
					delete(toolBlocks, ev.Index)
					delete(toolInputBuf, ev.Index)
				}
			case "message_delta":
				if ev.Delta != nil && ev.Delta.StopReason != "" {
					emit(providers.Event{
						Kind: providers.EventStopReason, Stop: ev.Delta.StopReason,
					})
				}
			case "message_stop":
				// Anthropic sends this as the last event; consumer
				// already saw EventStopReason via message_delta.
			case "ping":
				// keepalive - ignore.
			case "error":
				if ev.Error != nil {
					emit(providers.Event{
						Kind: providers.EventError,
						Err:  providers.ScrubModelName(fmt.Errorf("anthropic %s: %s", ev.Error.Type, ev.Error.Message)),
					})
				}
			default:
				// Unknown event type - forward-compat: ignore.
			}
			return nil
		})
		if err != nil && !isContextCancellation(ctx, err) {
			emit(providers.Event{Kind: providers.EventError, Err: providers.ScrubModelName(err)})
		}
	}()
	return out, nil
}

func isContextCancellation(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	return err == context.Canceled || err == context.DeadlineExceeded
}
