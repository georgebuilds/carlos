// Package gemini implements a pure-Go streaming client for Google's
// Generative Language API via its OpenAI-compatible endpoint at
// https://generativelanguage.googleapis.com/v1beta/openai/. Google
// added this path so any client speaking OpenAI's Chat Completions
// wire format works against Gemini without a separate SDK; carlos's
// oacompat layer is exactly that client.
//
// The wire-format work (SSE parsing, request building, stream-to-Event
// mapping) lives in `internal/providers/oacompat` and is shared with
// the openai and openrouter clients. This package is the Gemini-
// specific adapter: BaseURL, the capability map, and finish-reason
// translation.
//
// Why a separate provider instead of reusing openrouter or openai with
// a custom base URL:
//
//   - Capability map differs: Gemini's vision + structured output story
//     is distinct, prompt caching surface is its own thing.
//   - The provider Name() is the routing key the rest of carlos uses to
//     pick model defaults, capability checks, and (when we add them)
//     per-provider tool-use quirks. Carrying a real Name() is cheaper
//     than threading a "really an OpenAI-compatible alias" flag.
//   - User-facing config: a separate `gemini:` entry in
//     ~/.carlos/config.yaml is the obvious place to put the API key,
//     not a sub-config of openai.
package gemini

import (
	"context"
	"net/http"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/oacompat"
)

// Client is the Gemini chat-completions client. Safe for concurrent
// use: each Stream call runs an independent HTTP request + goroutine.
type Client struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// New constructs a client with a 30-second response-header timeout.
// The per-request read deadline is governed by the caller's context;
// no overall timeout because streaming completions can legitimately
// take minutes.
func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

// Name is the routing key the rest of carlos uses to identify this
// provider. Must stay stable across releases — `default_provider:
// gemini` in user configs hangs off this string.
func (c *Client) Name() string { return "gemini" }

// Capabilities describes what Gemini's current production models
// (Gemini 3.x flash + pro families) support through the OpenAI-
// compatible endpoint.
//
// ParallelToolUse: Gemini emits multiple tool_calls per turn via the
// OpenAI shape — confirmed against Google's docs for the compatible
// endpoint.
//
// PromptCaching: Gemini's context caching exists but is exposed
// through native /generateContent endpoints, not the OpenAI shim. Set
// false until we wire native caching as a separate code path.
//
// StructuredOut: Gemini supports response_format with json_schema on
// the OpenAI-compatible endpoint.
//
// Vision: Gemini Flash + Pro accept image_url content blocks via the
// OpenAI shape.
func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{
		ParallelToolUse: true,
		PromptCaching:   false,
		StructuredOut:   true,
		Vision:          true,
	}
}

// Stream sends req to /chat/completions with stream=true and returns
// a channel of canonical providers.Event values. The channel is
// closed when the stream ends (normal or error). Cancellation: cancel
// ctx to abort the in-flight HTTP request and goroutine.
func (c *Client) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	return oacompat.Stream(ctx, oacompat.Config{
		Name:            "gemini",
		BaseURL:         c.BaseURL,
		Path:            "/chat/completions",
		APIKey:          c.APIKey,
		HTTPClient:      c.HTTPClient,
		MapFinishReason: mapFinishReason,
	}, req)
}

// mapFinishReason normalizes Gemini's finish_reason values onto the
// canonical "tool_use" the agent loop branches on. Gemini's OpenAI
// shim emits the same vocabulary OpenAI does (stop, length,
// tool_calls, content_filter) so the translation is a straight
// remap of tool_calls + the legacy function_call alias.
func mapFinishReason(reason string) string {
	switch reason {
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return reason
	}
}
