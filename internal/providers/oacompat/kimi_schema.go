package oacompat

import (
	"encoding/json"
	"strings"
)

// Moonshot's (Kimi) function-calling validator enforces a "Moonshot-flavored
// JSON schema" in which every `$ref` must resolve against `#/$defs/...`. A
// tool whose `parameters` use the legacy Draft-07 `#/definitions/...` form
// (and a top-level `definitions` block) is rejected with HTTP 400:
//
//	Invalid request: tools.function.parameters is not a valid moonshot
//	flavored json schema, details: <At path '...$ref': references must
//	start with #/$defs/>
//
// Built-in carlos tools don't use `$ref`, but MCP tools arrive from arbitrary
// servers and many JSON-Schema generators (zod-to-json-schema in its default
// mode, older pydantic) emit `definitions` + `#/definitions/` refs, which then
// 400s the whole request on any moonshotai/kimi-* route.
//
// sanitizeKimiSchema rewrites a tool's parameter schema into the Moonshot
// dialect: it renames a `definitions` block to `$defs` and repoints every
// `#/definitions/` ref at `#/$defs/`. Everything else is left intact - Kimi
// accepts the broad JSON-Schema dialect (enum, nested objects, combinators,
// additionalProperties), so unlike Gemini this is a narrow rewrite, not a
// subset projection.
//
// Fail-open: anything that doesn't parse is returned unchanged.
func sanitizeKimiSchema(raw json.RawMessage) json.RawMessage {
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return raw
	}
	cleaned := rewriteKimiRefs(node)
	out, err := json.Marshal(cleaned)
	if err != nil {
		return raw
	}
	return out
}

// rewriteKimiRefs recursively renames `definitions` to `$defs` and rewrites
// `#/definitions/` ref prefixes to `#/$defs/`.
func rewriteKimiRefs(node any) any {
	switch v := node.(type) {
	case map[string]any:
		// Move definitions -> $defs before ranging so the map is stable
		// during iteration (and so the moved subtree is itself recursed).
		if defs, ok := v["definitions"]; ok {
			if _, has := v["$defs"]; !has {
				v["$defs"] = defs
			}
			delete(v, "definitions")
		}
		if ref, ok := v["$ref"].(string); ok {
			// A JSON-pointer $ref is anchored at "#/", so only the leading
			// "#/definitions/" is the dialect marker. Rewrite that prefix
			// alone; never touch a "definitions" segment deeper in the path.
			if rest, found := strings.CutPrefix(ref, "#/definitions/"); found {
				v["$ref"] = "#/$defs/" + rest
			}
		}
		for k, child := range v {
			v[k] = rewriteKimiRefs(child)
		}
		return v
	case []any:
		for i := range v {
			v[i] = rewriteKimiRefs(v[i])
		}
		return v
	default:
		return node
	}
}

// targetsKimi reports whether a request's model is validated by Moonshot's
// Kimi schema validator. carlos has no native Moonshot provider, so this
// matches purely on the model id - OpenRouter's "moonshotai/kimi-*" slugs.
func targetsKimi(_, model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "moonshot") || strings.Contains(m, "kimi")
}
