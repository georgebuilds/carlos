package oacompat

import (
	"encoding/json"
	"strings"
)

// xAI's (Grok) function-calling validator rejects a tool's `parameters`
// schema when any object node carries `additionalProperties: false`,
// returning HTTP 400:
//
//	Schema validation failed: [standard_violation]
//	/properties/.../additionalProperties: property schema 'false' is not supported
//
// This is the xAI analog of the Gemini schema problem: built-in carlos tools
// never emit `additionalProperties` (so they are unaffected), but MCP tools
// arrive from arbitrary servers and very commonly stamp every object with
// `additionalProperties: false` (zod `.strict()`, pydantic, etc.), which then
// 400s the whole request on any x-ai/grok-* route via OpenRouter.
//
// Unlike Gemini, Grok accepts the broad JSON-Schema dialect (enum, nested
// objects, oneOf/anyOf with object branches, formats), so the fix is narrow:
// strip ONLY `additionalProperties: false`. The object-valued and `true`
// forms are left intact - xAI accepts those, and removing the `false` form
// simply relaxes the schema to its default (extra properties permitted),
// which is harmless for tool inputs.
//
// Fail-open: anything that doesn't parse is returned unchanged, so a
// genuinely broken schema is rejected by xAI rather than silently dropped.
func sanitizeXAISchema(raw json.RawMessage) json.RawMessage {
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return raw
	}
	cleaned := stripXAIRejectedKeys(node)
	out, err := json.Marshal(cleaned)
	if err != nil {
		return raw
	}
	return out
}

// stripXAIRejectedKeys recursively removes `additionalProperties: false`
// from every object node. A map or `true` value is preserved.
func stripXAIRejectedKeys(node any) any {
	switch v := node.(type) {
	case map[string]any:
		if ap, ok := v["additionalProperties"]; ok {
			if b, isBool := ap.(bool); isBool && !b {
				delete(v, "additionalProperties")
			}
		}
		for key, child := range v {
			v[key] = stripXAIRejectedKeys(child)
		}
		return v
	case []any:
		for i := range v {
			v[i] = stripXAIRejectedKeys(v[i])
		}
		return v
	default:
		return node
	}
}

// targetsXAI reports whether a request's model will be validated by xAI's
// (Grok) function-calling validator. carlos has no native xAI provider, so
// this matches purely on the model id - OpenRouter's "x-ai/grok-..." slugs -
// and leaves every other route untouched. The provider arg is accepted for
// symmetry with targetsGemini.
func targetsXAI(_, model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "x-ai/") || strings.Contains(m, "grok")
}
