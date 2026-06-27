package oacompat

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
)

// decode is a tiny helper that unmarshals sanitized JSON back into a tree so
// tests can assert on structure rather than brittle string matching.
func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("sanitized schema is not valid JSON: %v\n%s", err, raw)
	}
	return m
}

// TestSanitizeGemini_ArrayGetsItems is the exact failure the user hit: a
// `type: array` property with no `items` makes Google reject the request
// with "field predicate failed · $type = Type.ARRAY".
func TestSanitizeGemini_ArrayGetsItems(t *testing.T) {
	in := json.RawMessage(`{
		"type": "object",
		"properties": {
			"tags": {"type": "array", "description": "labels"}
		}
	}`)
	got := decode(t, sanitizeGeminiSchema(in))
	tags := got["properties"].(map[string]any)["tags"].(map[string]any)
	items, ok := tags["items"].(map[string]any)
	if !ok {
		t.Fatalf("array did not get an items schema: %v", tags)
	}
	if items["type"] != "string" {
		t.Errorf("default items type = %v, want string", items["type"])
	}
}

func TestSanitizeGemini_PreservesExistingItems(t *testing.T) {
	in := json.RawMessage(`{"type":"array","items":{"type":"number"}}`)
	got := decode(t, sanitizeGeminiSchema(in))
	items := got["items"].(map[string]any)
	if items["type"] != "number" {
		t.Errorf("existing items clobbered: %v", items)
	}
}

func TestSanitizeGemini_StripsUnsupportedKeys(t *testing.T) {
	in := json.RawMessage(`{
		"type": "object",
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"additionalProperties": false,
		"properties": {
			"headers": {"type": "object", "additionalProperties": {"type": "string"}}
		}
	}`)
	got := decode(t, sanitizeGeminiSchema(in))
	if _, ok := got["$schema"]; ok {
		t.Error("$schema not stripped")
	}
	if _, ok := got["additionalProperties"]; ok {
		t.Error("top-level additionalProperties not stripped")
	}
	headers := got["properties"].(map[string]any)["headers"].(map[string]any)
	if _, ok := headers["additionalProperties"]; ok {
		t.Error("nested additionalProperties not stripped")
	}
}

func TestSanitizeGemini_CollapsesAnyOfToTypedMember(t *testing.T) {
	// The ubiquitous MCP "optional field" shape.
	in := json.RawMessage(`{"anyOf":[{"type":"string","description":"id"},{"type":"null"}]}`)
	got := decode(t, sanitizeGeminiSchema(in))
	if _, ok := got["anyOf"]; ok {
		t.Error("anyOf not collapsed")
	}
	if got["type"] != "string" {
		t.Errorf("collapsed type = %v, want string", got["type"])
	}
	if got["description"] != "id" {
		t.Errorf("collapsed description = %v, want id", got["description"])
	}
}

func TestSanitizeGemini_NormalizesTypeArray(t *testing.T) {
	in := json.RawMessage(`{"type":["string","null"]}`)
	got := decode(t, sanitizeGeminiSchema(in))
	if got["type"] != "string" {
		t.Errorf("type array not normalized: %v", got["type"])
	}
}

func TestSanitizeGemini_ConstBecomesEnum(t *testing.T) {
	in := json.RawMessage(`{"const":"v1"}`)
	got := decode(t, sanitizeGeminiSchema(in))
	if _, ok := got["const"]; ok {
		t.Error("const not removed")
	}
	enum, ok := got["enum"].([]any)
	if !ok || len(enum) != 1 || enum[0] != "v1" {
		t.Errorf("const not converted to single-value enum: %v", got["enum"])
	}
}

func TestSanitizeGemini_DropsEmptyEnumEntriesAndFormat(t *testing.T) {
	in := json.RawMessage(`{"type":"string","enum":["a","",""],"format":"uri"}`)
	got := decode(t, sanitizeGeminiSchema(in))
	enum := got["enum"].([]any)
	if len(enum) != 1 || enum[0] != "a" {
		t.Errorf("empty enum entries not pruned: %v", enum)
	}
	if _, ok := got["format"]; ok {
		t.Error("unsupported format not dropped")
	}
}

func TestSanitizeGemini_KeepsAllowedFormat(t *testing.T) {
	in := json.RawMessage(`{"type":"string","format":"date-time"}`)
	got := decode(t, sanitizeGeminiSchema(in))
	if got["format"] != "date-time" {
		t.Errorf("allowed format dropped: %v", got["format"])
	}
}

func TestSanitizeGemini_FailOpenOnInvalidJSON(t *testing.T) {
	in := json.RawMessage(`{not json`)
	got := sanitizeGeminiSchema(in)
	if string(got) != string(in) {
		t.Errorf("invalid JSON should pass through unchanged, got %s", got)
	}
}

func TestTargetsGemini(t *testing.T) {
	cases := []struct {
		provider, model string
		want            bool
	}{
		{"gemini", "gemini-3.5-flash", true},
		{"openrouter", "google/gemini-3.5-flash", true},
		{"openrouter", "GOOGLE/GEMINI-PRO", true},
		{"openai", "gpt-4o", false},
		{"anthropic", "claude-opus-4-8", false},
		{"openrouter", "anthropic/claude-3.5", false},
	}
	for _, tc := range cases {
		if got := targetsGemini(tc.provider, tc.model); got != tc.want {
			t.Errorf("targetsGemini(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

// TestBuildRequest_SanitizesOnlyForGemini pins the gating: an MCP-style
// array-without-items schema is repaired when the request targets Gemini and
// left byte-identical for other providers.
func TestBuildRequest_SanitizesOnlyForGemini(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"tags":{"type":"array"}}}`)
	mk := func(model string) providers.Request {
		return providers.Request{
			Model: model,
			Tools: []providers.ToolSpec{{Name: "do_list", Schema: schema}},
		}
	}

	// Gemini: array gets items.
	out, err := BuildRequest(mk("google/gemini-3.5-flash"), "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	if err := json.Unmarshal(out.Tools[0].Function.Parameters, &params); err != nil {
		t.Fatal(err)
	}
	tags := params["properties"].(map[string]any)["tags"].(map[string]any)
	if _, ok := tags["items"]; !ok {
		t.Errorf("gemini path did not repair array: %v", tags)
	}

	// Non-gemini: schema passes through byte-identical.
	out2, err := BuildRequest(mk("gpt-4o"), "openai")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]byte(out2.Tools[0].Function.Parameters), schema) {
		t.Errorf("non-gemini schema was altered:\ngot  %s\nwant %s", out2.Tools[0].Function.Parameters, schema)
	}
}
