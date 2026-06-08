package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAllToolSchemas_GeminiCompatible asserts that every tool in the
// default registry uses only the JSON-Schema subset that Gemini's
// FunctionDeclaration validator accepts. The bug this guards against
// is documented at:
//
//	https://ai.google.dev/api/caching#Schema
//
// Gemini's validator surfaces violations as INVALID_ARGUMENT errors
// (HTTP 400 from the Generative Language API, which OpenRouter forwards
// when routing OpenAI-compat requests to a google/gemini-* model). The
// first regression - empty-string enum values - tanked every `carlos
// please` invocation against Gemini Flash (commit 0a6a40d). The second
// - additionalProperties on the http_request tool's `headers` field -
// did the same on first-message-after-onboarding.
//
// We blocklist the JSON-Schema features Gemini documents as unsupported.
// New tools that need richer typing should either constrain their
// inputs at execute time OR pick a Gemini-supported substitute (an
// array of {name,value} objects in lieu of a free-form map, etc.).
func TestAllToolSchemas_GeminiCompatible(t *testing.T) {
	r := NewDefaultRegistry()
	for _, tool := range r.All() {
		tool := tool
		t.Run(tool.Name(), func(t *testing.T) {
			raw := tool.Schema()
			var schema any
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("schema is not valid JSON: %v\n%s", err, raw)
			}
			walkSchemaForGemini(t, "$", schema)
		})
	}
}

// walkSchemaForGemini recursively descends into the schema and fails
// the test on any feature key Gemini rejects. The path argument carries
// the JSON Pointer to the offending node so test failures point to the
// exact field.
func walkSchemaForGemini(t *testing.T, path string, node any) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if reason := geminiRejects(key, child); reason != "" {
				t.Errorf("%s.%s: %s", path, key, reason)
			}
			walkSchemaForGemini(t, path+"."+key, child)
		}
	case []any:
		for i, item := range v {
			walkSchemaForGemini(t, jsonPathIndex(path, i), item)
		}
	}
}

// geminiRejects returns a non-empty reason when the (key, value) pair
// is one of Gemini's known-unsupported schema features. Returns "" when
// the field is benign or merely advisory.
//
// References:
//   - https://ai.google.dev/api/caching#Schema (the supported subset)
//   - https://cloud.google.com/vertex-ai/generative-ai/docs/multimodal/function-calling#function_declarations
func geminiRejects(key string, value any) string {
	switch key {
	case "additionalProperties", "patternProperties":
		return "Gemini Schema does not support property wildcards; use a typed schema or constrain at execute time"
	case "$ref", "$defs", "definitions":
		return "Gemini Schema does not support references; inline the type"
	case "oneOf", "anyOf", "allOf":
		return "Gemini Schema does not support combinator keywords"
	case "const":
		return "Gemini Schema does not support `const`; use a single-value enum"
	case "format":
		// Gemini's Schema accepts a tiny set: date-time, enum, int32,
		// int64, etc. Most JSON Schema "format" values are advisory.
		// We treat ANY format value as suspect because OpenRouter's
		// translation has bitten us here before. Tools that genuinely
		// need a Gemini-supported format can opt-out by widening this
		// allowlist with the documented values.
		if s, _ := value.(string); s != "" {
			return "Gemini Schema accepts only a small `format` allowlist; drop or constrain"
		}
	case "nullable":
		return "Gemini Schema does not honor JSON-Schema 7's `nullable`; remove the field"
	}
	// "enum" is supported, but empty-string entries trip Gemini. Catch
	// them here so the regression that motivated 0a6a40d can't recur.
	if key == "enum" {
		if arr, ok := value.([]any); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok && strings.TrimSpace(s) == "" {
					return "Gemini Schema rejects empty-string enum values"
				}
			}
		}
	}
	return ""
}

// jsonPathIndex formats a path with an array index in JSON-Pointer-ish
// shape so failures read as "$.properties.headers.items[0]".
func jsonPathIndex(path string, i int) string {
	return path + "[" + itoa(i) + "]"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
