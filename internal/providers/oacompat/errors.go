package oacompat

import (
	"encoding/json"
	"strings"
)

// extractErrorMessage pulls the most user-readable string out of an
// OpenAI-compatible error envelope. Falls back to the raw trimmed body
// when nothing decodes.
//
// Shapes covered:
//
//   - OpenAI / Anthropic / Ollama / native Gemini:
//     {"error": {"message": "..."}}
//   - OpenRouter wrapping a downstream provider:
//     {"error": {"message": "Provider returned error",
//     "metadata": {"raw": "{...nested envelope...}",
//     "provider_name": "Google AI Studio"}}}
//   - Plain {"message": "..."} as a defensive fallback.
//
// The OpenRouter path walks one level into metadata.raw and tries to
// decode a nested envelope; otherwise it strips trailing whitespace
// and tags the provider name when present so the operator can see
// which upstream rejected the call.
func extractErrorMessage(body []byte) string {
	if len(body) == 0 {
		return "(empty body)"
	}
	if msg, provider, ok := decodeEnvelope(body); ok {
		if provider != "" {
			return msg + " (via " + provider + ")"
		}
		return msg
	}
	return strings.TrimSpace(string(body))
}

// decodeEnvelope returns the readable message and (when available) the
// upstream provider name. It first tries the outer {"error": ...}
// shape; if metadata.raw contains a nested envelope (OpenRouter), it
// recurses one level deeper and prefers the nested message.
//
// The `error` field is decoded flexibly because providers disagree on its
// shape: OpenAI/Anthropic/Gemini use an OBJECT ({message, metadata}), but
// xAI (Grok) and some others put the reason as a bare STRING
// ({"code":..., "error":"Schema validation failed: ..."}). Modelling it as
// only an object meant xAI's nested raw failed to decode, and the operator
// saw the useless outer "Provider returned error (via xAI)" instead of the
// real validation message. Accept both forms.
func decodeEnvelope(body []byte) (msg string, provider string, ok bool) {
	var top struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return "", "", false
	}

	var (
		outerMsg string
		raw      string
	)
	if len(top.Error) > 0 {
		// Object form: {message, metadata:{raw, provider_name}}.
		var obj struct {
			Message  string `json:"message"`
			Metadata struct {
				Raw          string `json:"raw"`
				ProviderName string `json:"provider_name"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(top.Error, &obj); err == nil {
			outerMsg = strings.TrimSpace(obj.Message)
			provider = obj.Metadata.ProviderName
			raw = strings.TrimSpace(obj.Metadata.Raw)
		} else {
			// String form: "error": "Schema validation failed: ...".
			var s string
			if err := json.Unmarshal(top.Error, &s); err == nil {
				outerMsg = strings.TrimSpace(s)
			}
		}
	}
	if outerMsg == "" {
		outerMsg = strings.TrimSpace(top.Message)
	}
	if raw != "" {
		if inner, innerProvider, innerOK := decodeEnvelope([]byte(raw)); innerOK {
			if innerProvider != "" {
				provider = innerProvider
			}
			return strings.TrimSpace(inner), provider, true
		}
	}
	if outerMsg == "" {
		return "", "", false
	}
	return outerMsg, provider, true
}
