package oacompat

import (
	"encoding/json"
	"strings"
)

// Gemini's FunctionDeclaration validator accepts only a restricted subset
// of JSON Schema (an OpenAPI 3.0 dialect). Built-in carlos tools are kept
// inside that subset and a compile-time test enforces it
// (internal/tools/gemini_schema_test.go). MCP tools, however, arrive from
// arbitrary external servers whose schemas we don't control: a single
// `type: array` with no `items`, an `additionalProperties` map, a `$ref`,
// or an `anyOf` is enough for Google's Generative Language API to reject the
// whole request with an INVALID_ARGUMENT 400 ("field predicate failed ·
// $type = Type.ARRAY", and friends), which OpenRouter surfaces as "(via
// Google AI Studio)".
//
// sanitizeGeminiSchema rewrites one tool's parameter schema into the
// Gemini-supported subset so the request is accepted. It is intentionally
// fail-open: anything it can't parse is returned unchanged (better to let
// Google reject a genuinely broken schema than to swallow the tool).
//
// The transform, applied recursively to every node:
//
//   - collapse anyOf/oneOf/allOf onto the node (first typed member wins),
//     since Gemini's translation via OpenRouter chokes on combinators;
//   - normalize a JSON-Schema-7 type array (e.g. ["string","null"]) down to
//     a single type string;
//   - convert `const` to a single-value `enum`;
//   - strip keys Gemini rejects: additionalProperties, patternProperties,
//     $ref/$defs/definitions/$schema/$id/$comment, nullable, not, if/then/
//     else, and other unsupported structural keywords;
//   - drop `format` values outside Gemini's small allowlist;
//   - drop empty-string enum entries (a documented Gemini reject) and the
//     enum itself if it empties out;
//   - ensure every `type: array` carries an `items` (defaulting to a string
//     element) — the specific shape that produced the reported failure.
func sanitizeGeminiSchema(raw json.RawMessage) json.RawMessage {
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return raw
	}
	cleaned := sanitizeGeminiNode(node)
	out, err := json.Marshal(cleaned)
	if err != nil {
		return raw
	}
	return out
}

// geminiBlockedKeys are JSON-Schema keywords Gemini's validator does not
// accept; they are deleted from every object node. anyOf/oneOf/allOf and
// const are handled separately (collapsed / rewritten) before this runs.
var geminiBlockedKeys = []string{
	"additionalProperties", "patternProperties", "propertyNames",
	"$ref", "$defs", "definitions", "$schema", "$id", "$comment", "$anchor",
	"nullable", "not", "if", "then", "else",
	"dependentRequired", "dependentSchemas",
	"unevaluatedProperties", "unevaluatedItems",
	"exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"contentEncoding", "contentMediaType",
	"readOnly", "writeOnly", "deprecated", "examples",
}

// geminiAllowedFormats is the small set of `format` values Gemini documents
// as supported. Anything else is advisory JSON Schema that has tripped the
// validator before, so it is dropped.
var geminiAllowedFormats = map[string]bool{
	"date-time": true, "date": true, "time": true, "duration": true,
	"enum": true, "int32": true, "int64": true,
	"float": true, "double": true,
}

func sanitizeGeminiNode(node any) any {
	switch v := node.(type) {
	case map[string]any:
		collapseGeminiCombinators(v)

		if c, ok := v["const"]; ok {
			delete(v, "const")
			if _, has := v["enum"]; !has {
				v["enum"] = []any{c}
			}
		}

		for _, k := range geminiBlockedKeys {
			delete(v, k)
		}

		if f, ok := v["format"].(string); ok && !geminiAllowedFormats[f] {
			delete(v, "format")
		}

		normalizeGeminiType(v)
		dropTypeMismatchedKeywords(v)
		pruneGeminiEnum(v)

		for key, child := range v {
			v[key] = sanitizeGeminiNode(child)
		}

		// Add items AFTER recursion so the default isn't itself re-walked
		// (it's already clean) and so a stripped node still gets one.
		if geminiTypeIs(v, "array") {
			if _, ok := v["items"]; !ok {
				v["items"] = map[string]any{"type": "string"}
			}
		}
		return v
	case []any:
		for i := range v {
			v[i] = sanitizeGeminiNode(v[i])
		}
		return v
	default:
		return node
	}
}

// collapseGeminiCombinators folds an anyOf/oneOf/allOf list onto the parent
// node: the first member carrying a concrete (non-null) type wins, and its
// keys are merged in without clobbering anything the parent already sets.
// This preserves the common optional-field shape ({anyOf:[{type:string},
// {type:null}]} -> {type:string}) that MCP servers emit constantly.
func collapseGeminiCombinators(v map[string]any) {
	for _, comb := range []string{"anyOf", "oneOf", "allOf"} {
		raw, ok := v[comb]
		if !ok {
			continue
		}
		delete(v, comb)
		members, ok := raw.([]any)
		if !ok {
			continue
		}
		chosen := pickGeminiMember(members)
		for k, val := range chosen {
			if _, exists := v[k]; !exists {
				v[k] = val
			}
		}
	}
}

// pickGeminiMember selects the most useful subschema from a combinator list:
// the first member that declares a non-null type, falling back to the first
// object member. Returns nil when no member is an object.
func pickGeminiMember(members []any) map[string]any {
	var first map[string]any
	for _, m := range members {
		obj, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if first == nil {
			first = obj
		}
		if t, ok := obj["type"].(string); ok && t != "" && t != "null" {
			return obj
		}
	}
	return first
}

// dropTypeMismatchedKeywords removes structural keywords that Gemini only
// permits on a specific type. Gemini's Schema validator enforces these as
// field predicates: e.g. `items` is rejected with "field predicate failed:
// $type == Type.ARRAY" when it appears on a non-array node. External MCP
// schemas violate this constantly - DigitalOcean ships a `type: "string"`
// property that also carries an `items` (with the real enum of allowed
// values) - so without this strip the whole request 400s.
//
// When a string property's allowed values are trapped inside a stray
// `items.enum`, they're hoisted onto the node as a real `enum` before the
// `items` is dropped, so the model still sees the valid choices.
//
// A node with no concrete type is left alone: we can't know which keywords
// are valid, and Gemini tolerates the absence.
func dropTypeMismatchedKeywords(v map[string]any) {
	t, ok := v["type"].(string)
	if !ok || t == "" {
		return
	}
	if t != "array" {
		if t == "string" {
			if items, ok := v["items"].(map[string]any); ok {
				if _, has := v["enum"]; !has {
					if e, ok := items["enum"].([]any); ok && len(e) > 0 {
						v["enum"] = e
					}
				}
			}
		}
		delete(v, "items")
		delete(v, "minItems")
		delete(v, "maxItems")
		delete(v, "uniqueItems")
	}
	if t != "object" {
		delete(v, "properties")
		delete(v, "required")
		delete(v, "minProperties")
		delete(v, "maxProperties")
	}
	if t != "string" {
		delete(v, "minLength")
		delete(v, "maxLength")
		delete(v, "pattern")
	}
	if t != "number" && t != "integer" {
		delete(v, "minimum")
		delete(v, "maximum")
	}
}

// normalizeGeminiType reduces a JSON-Schema-7 type array (["string","null"])
// to a single type string, which is all Gemini accepts. The first non-null
// entry wins; an all-null/empty list defaults to "string".
func normalizeGeminiType(v map[string]any) {
	arr, ok := v["type"].([]any)
	if !ok {
		return
	}
	picked := ""
	for _, e := range arr {
		s, _ := e.(string)
		if s != "" && s != "null" {
			picked = s
			break
		}
	}
	if picked == "" {
		picked = "string"
	}
	v["type"] = picked
}

// pruneGeminiEnum drops empty-string enum entries (a documented Gemini
// reject) and removes an enum that empties out entirely.
func pruneGeminiEnum(v map[string]any) {
	arr, ok := v["enum"].([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == 0 {
		delete(v, "enum")
		return
	}
	v["enum"] = kept
}

// geminiTypeIs reports whether the node's (already-normalized) type equals
// want.
func geminiTypeIs(v map[string]any, want string) bool {
	t, _ := v["type"].(string)
	return t == want
}

// targetsGemini reports whether a request bound for the given provider +
// model will be validated by Google's Gemini schema validator. True for the
// native gemini provider and for any model id mentioning "gemini" (e.g.
// OpenRouter's "google/gemini-3.5-flash"), so the sanitizer runs on exactly
// the paths that hit Google and leaves OpenAI/Anthropic/etc. untouched.
func targetsGemini(provider, model string) bool {
	if strings.EqualFold(provider, "gemini") {
		return true
	}
	return strings.Contains(strings.ToLower(model), "gemini")
}
