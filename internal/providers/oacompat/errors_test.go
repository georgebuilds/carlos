package oacompat

import (
	"strings"
	"testing"
)

func TestExtractErrorMessage_EmptyBody(t *testing.T) {
	if got := extractErrorMessage(nil); got != "(empty body)" {
		t.Errorf("nil body returned %q", got)
	}
	if got := extractErrorMessage([]byte{}); got != "(empty body)" {
		t.Errorf("empty body returned %q", got)
	}
}

func TestExtractErrorMessage_PlainNonJSONPassesThrough(t *testing.T) {
	got := extractErrorMessage([]byte("  Bad gateway\n"))
	if got != "Bad gateway" {
		t.Errorf("got %q, want trimmed plain text", got)
	}
}

func TestExtractErrorMessage_OpenAIShape(t *testing.T) {
	body := []byte(`{"error":{"message":"Invalid API key"}}`)
	got := extractErrorMessage(body)
	if got != "Invalid API key" {
		t.Errorf("got %q", got)
	}
}

func TestExtractErrorMessage_PlainMessageFallback(t *testing.T) {
	body := []byte(`{"message":"rate limited"}`)
	if got := extractErrorMessage(body); got != "rate limited" {
		t.Errorf("got %q", got)
	}
}

func TestExtractErrorMessage_OpenRouterNestedRaw(t *testing.T) {
	body := []byte(`{
		"error": {
			"message": "Provider returned error",
			"metadata": {
				"raw": "{\"error\":{\"code\":400,\"message\":\"GenerateContentRequest.tools[0].function_declarations[2].parameters.properties[section].enum[0]: cannot be empty\\n\",\"status\":\"INVALID_ARGUMENT\"}}",
				"provider_name": "Google AI Studio"
			}
		}
	}`)
	got := extractErrorMessage(body)
	if !strings.Contains(got, "enum[0]: cannot be empty") {
		t.Errorf("nested message not extracted: %q", got)
	}
	if !strings.Contains(got, "Google AI Studio") {
		t.Errorf("provider name not surfaced: %q", got)
	}
	if strings.Contains(got, `"error"`) {
		t.Errorf("raw JSON leaked into output: %q", got)
	}
}

func TestExtractErrorMessage_OpenRouterWrapsWithProvider(t *testing.T) {
	body := []byte(`{
		"error": {
			"message": "no provider available",
			"metadata": {
				"provider_name": "Anthropic"
			}
		}
	}`)
	got := extractErrorMessage(body)
	if !strings.Contains(got, "no provider available") {
		t.Errorf("outer message lost: %q", got)
	}
	if !strings.Contains(got, "Anthropic") {
		t.Errorf("provider name lost: %q", got)
	}
}

// TestExtractErrorMessage_XAIStringErrorNestedRaw covers the xAI/Grok
// shape: OpenRouter wraps the upstream body in metadata.raw, and xAI's
// body carries the reason as a bare STRING `"error"` (not the OpenAI
// object form). Before the flexible decode this fell back to the useless
// outer "Provider returned error (via xAI)".
func TestExtractErrorMessage_XAIStringErrorNestedRaw(t *testing.T) {
	body := []byte(`{
		"error": {
			"message": "Provider returned error",
			"metadata": {
				"raw": "{\"code\":\"Client specified an invalid argument\",\"error\":\"Schema validation failed: [standard_violation] /properties/foo/additionalProperties: property schema 'false' is not supported\"}",
				"provider_name": "xAI"
			}
		}
	}`)
	got := extractErrorMessage(body)
	if !strings.Contains(got, "Schema validation failed") {
		t.Errorf("nested xAI string error not surfaced: %q", got)
	}
	if !strings.Contains(got, "xAI") {
		t.Errorf("provider name not surfaced: %q", got)
	}
	if strings.Contains(got, "Provider returned error") {
		t.Errorf("should prefer the nested reason over the generic outer message: %q", got)
	}
}

// TestExtractErrorMessage_TopLevelStringError covers a provider that
// returns `{"error": "..."}` directly (no nesting, error as a string).
func TestExtractErrorMessage_TopLevelStringError(t *testing.T) {
	body := []byte(`{"error":"invalid request: bad tool schema"}`)
	got := extractErrorMessage(body)
	if got != "invalid request: bad tool schema" {
		t.Errorf("got %q, want the bare string error", got)
	}
}

func TestExtractErrorMessage_MalformedNestedRawFallsBackToOuter(t *testing.T) {
	body := []byte(`{"error":{"message":"outer","metadata":{"raw":"not-json-at-all"}}}`)
	got := extractErrorMessage(body)
	if got != "outer" {
		t.Errorf("got %q, want outer-only fallback", got)
	}
}

func TestExtractErrorMessage_EmptyOuterEmptyNestedReturnsRawBody(t *testing.T) {
	body := []byte(`{"foo":"bar"}`)
	got := extractErrorMessage(body)
	if !strings.Contains(got, "foo") {
		t.Errorf("unrecognised JSON should fall through to trimmed body: %q", got)
	}
}
