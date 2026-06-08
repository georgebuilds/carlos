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
//                "metadata": {"raw": "{...nested envelope...}",
//                             "provider_name": "Google AI Studio"}}}
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
func decodeEnvelope(body []byte) (msg string, provider string, ok bool) {
	var top struct {
		Error struct {
			Message  string `json:"message"`
			Metadata struct {
				Raw          string `json:"raw"`
				ProviderName string `json:"provider_name"`
			} `json:"metadata"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return "", "", false
	}
	outerMsg := strings.TrimSpace(top.Error.Message)
	if outerMsg == "" {
		outerMsg = strings.TrimSpace(top.Message)
	}
	provider = top.Error.Metadata.ProviderName
	if raw := strings.TrimSpace(top.Error.Metadata.Raw); raw != "" {
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
