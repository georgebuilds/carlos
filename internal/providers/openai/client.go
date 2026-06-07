// Package openai implements a pure-Go streaming client for the OpenAI
// Chat Completions API. No SDK - we hit the HTTP endpoint directly so the
// capability map stays honest about what the API actually offers versus
// what an SDK papers over.
//
// The wire-format work (SSE parsing, request building, stream-to-Event
// mapping) lives in `internal/providers/oacompat` and is shared with the
// openrouter client. This package is the OpenAI-specific adapter: the
// Client struct, the BaseURL default, and the OpenAI capability map.
package openai

import (
	"context"
	"net/http"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/oacompat"
)

// Client is the OpenAI Chat Completions client. Safe for concurrent use:
// each Stream call runs an independent HTTP request + goroutine.
//
// BaseURL is exposed so OpenAI-compatible servers (vLLM, Together, local
// llama.cpp, etc.) can reuse this client by pointing at a different host.
// We do NOT special-case any of them here - the wire format IS the contract.
// (OpenRouter has its own thin adapter because it needs attribution headers
// and exposes a slightly different capability set; the core wire handling
// is the same code in oacompat.)
type Client struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// New constructs a client with a 30-second response-header timeout. The
// per-request read deadline is governed by the caller's context (cancel
// ctx → cancel stream); we don't impose an overall deadline here because
// streaming completions can legitimately take minutes.
func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: "https://api.openai.com",
		HTTPClient: &http.Client{
			// No overall Timeout - that aborts in-flight streams.
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

func (c *Client) Name() string { return "openai" }

// Capabilities advertises what the OpenAI provider supports on its
// current-generation models (gpt-4o family + o-series). Older snapshot
// models (gpt-3.5) wouldn't all check every box, but carlos targets
// modern models for the agent loop and the capability map describes the
// provider, not any individual model.
func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		ParallelToolUse: true, // gpt-4o / o-series emit multiple tool_calls per turn
		PromptCaching:   true, // gpt-4o auto-caches the prefix at >1024 tokens
		StructuredOut:   true, // response_format json_object / json_schema
		Vision:          true, // gpt-4o image_url content blocks
	}
}

// Stream sends req to /v1/chat/completions with stream=true and returns a
// channel of canonical providers.Event values. The channel is closed when
// the stream ends (normal or error). Cancellation: cancel ctx to abort the
// in-flight HTTP request and goroutine.
//
// The wire-level work happens in oacompat.Stream; this method is just the
// OpenAI-specific Config (no extra headers, OpenAI finish_reason mapping).
func (c *Client) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	return oacompat.Stream(ctx, oacompat.Config{
		Name:            "openai",
		BaseURL:         c.BaseURL,
		Path:            "/v1/chat/completions",
		APIKey:          c.APIKey,
		HTTPClient:      c.HTTPClient,
		MapFinishReason: mapFinishReason,
	}, req)
}

// mapFinishReason converts OpenAI's finish_reason to the canonical stop
// vocabulary the agent loop branches on. The loop only special-cases
// "tool_use" - everything else is opaque routing for the projection layer.
//
//	tool_calls       → tool_use   (the loop's tool-dispatch branch)
//	stop             → end_turn   (Anthropic's idiom; mirrors clean termination)
//	length           → max_tokens (Anthropic's idiom for length-capped)
//	content_filter   → content_filter (no Anthropic equivalent; pass through)
//	"" / unknown     → "" (let the caller decide; usually means "still going")
func mapFinishReason(fr string) string {
	switch fr {
	case "tool_calls":
		return "tool_use"
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	default:
		return fr
	}
}
