// Package openrouter implements a pure-Go streaming client for the
// OpenRouter chat-completions endpoint. OpenRouter is a routing proxy that
// speaks the OpenAI Chat Completions wire format (request body, response
// shape, SSE chunking, tools/tool_calls accumulation - all identical to
// OpenAI's spec) and brokers requests across many upstream providers via
// namespaced model IDs ("anthropic/claude-3.5-sonnet", "openai/gpt-4o",
// "meta-llama/llama-3.3-70b", …).
//
// The wire-format work (SSE parsing, request building, stream-to-Event
// mapping) lives in `internal/providers/oacompat` and is shared with the
// openai client. This package is the OpenRouter-specific adapter: BaseURL,
// the recommended HTTP-Referer / X-Title attribution headers, an
// OpenRouter-flavored capability map, and the (mostly-passthrough)
// finish-reason translator.
package openrouter

import (
	"context"
	"net/http"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/oacompat"
)

// Recommended OpenRouter attribution headers. Both optional - OpenRouter
// uses them for analytics + leaderboard placement and to give the user a
// pretty name in their dashboard. They cost nothing to send so we always do.
const (
	httpReferer = "https://github.com/georgebuilds/carlos"
	xTitle      = "carlos"
)

// Client is the OpenRouter chat-completions client. Safe for concurrent use:
// each Stream call runs an independent HTTP request + goroutine.
type Client struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// New constructs a client with a 30-second response-header timeout. The
// per-request read deadline is governed by the caller's context (cancel
// ctx → cancel stream); we don't impose an overall deadline here because
// streaming completions - especially via OpenRouter, which adds a routing
// hop - can legitimately take minutes for long responses on slow upstreams.
func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: "https://openrouter.ai/api/v1",
		HTTPClient: &http.Client{
			// No overall Timeout - that aborts in-flight streams.
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

func (c *Client) Name() string { return "openrouter" }

// Capabilities advertises the SERVICE'S capabilities, not any single upstream
// model's. OpenRouter routes to hundreds of models with varying capabilities;
// the user may pick a model that doesn't support, say, vision - in which case
// the request fails at provider time. That's correct behavior given the
// current providers.Capabilities shape (one flag per provider). A future
// refinement is a per-model capability table; see slice notes for the
// handoff.
//
// Defaults below are conservative-but-honest:
//   - ParallelToolUse: true (most major upstreams support it).
//   - PromptCaching: false (the surface area varies wildly through the proxy;
//     opt-in via direct provider when caching matters).
//   - StructuredOut: true (OpenAI-style response_format works for most
//     upstreams).
//   - Vision: true (gpt-4o, claude-3.5-sonnet, gemini-2.0-flash all support
//     image content blocks via OpenRouter).
func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		ParallelToolUse: true,
		PromptCaching:   false,
		StructuredOut:   true,
		Vision:          true,
	}
}

// Stream sends req to /chat/completions with stream=true and returns a
// channel of canonical providers.Event values. The channel is closed when
// the stream ends (normal or error). Cancellation: cancel ctx to abort the
// in-flight HTTP request and goroutine.
//
// The wire-level work happens in oacompat.Stream; this method is just the
// OpenRouter-specific Config (attribution headers, OpenRouter finish_reason
// mapping which mostly passes through).
func (c *Client) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	return oacompat.Stream(ctx, oacompat.Config{
		Name:       "openrouter",
		BaseURL:    c.BaseURL,
		Path:       "/chat/completions",
		APIKey:     c.APIKey,
		HTTPClient: c.HTTPClient,
		ExtraHeaders: map[string]string{
			"HTTP-Referer": httpReferer,
			"X-Title":      xTitle,
		},
		MapFinishReason: mapFinishReason,
	}, req)
}

// mapFinishReason maps OpenAI/OpenRouter finish_reason values onto the
// canonical stop names the agent loop branches on. The loop specifically
// keys off "tool_use" (Anthropic's name) to decide whether to dispatch
// tools, so we translate OpenAI's "tool_calls" → "tool_use" (and the legacy
// "function_call" alias). Other reasons pass through unchanged so the TUI
// can surface them verbatim.
//
// Possible OpenAI/OpenRouter values: stop, length, tool_calls, content_filter,
// function_call (legacy, treated like tool_calls).
func mapFinishReason(reason string) string {
	switch reason {
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return reason
	}
}
