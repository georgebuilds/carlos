package oacompat

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
)

func TestTargetsXAI(t *testing.T) {
	cases := map[string]bool{
		"x-ai/grok-build-0.1": true,
		"x-ai/grok-4.20":      true,
		"grok-build-0.1":      true,
		"google/gemini-3.5-flash": false,
		"deepseek/deepseek-v4-pro": false,
		"gpt-4o":              false,
		"z-ai/glm-5.2":        false,
	}
	for model, want := range cases {
		if got := targetsXAI("openrouter", model); got != want {
			t.Errorf("targetsXAI(%q) = %v, want %v", model, got, want)
		}
	}
}

// The confirmed xAI gotcha: additionalProperties:false is stripped at every
// depth, including nested object properties (where MCP servers put it).
func TestSanitizeXAISchema_StripsAdditionalPropertiesFalse(t *testing.T) {
	in := []byte(`{
		"type":"object",
		"additionalProperties":false,
		"properties":{
			"opts":{"type":"object","additionalProperties":false,"properties":{"x":{"type":"string"}}},
			"tags":{"type":"array","items":{"type":"object","additionalProperties":false}}
		}
	}`)
	var got map[string]any
	if err := json.Unmarshal(sanitizeXAISchema(in), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["additionalProperties"]; ok {
		t.Error("root additionalProperties:false not stripped")
	}
	props := got["properties"].(map[string]any)
	opts := props["opts"].(map[string]any)
	if _, ok := opts["additionalProperties"]; ok {
		t.Error("nested object additionalProperties:false not stripped")
	}
	items := props["tags"].(map[string]any)["items"].(map[string]any)
	if _, ok := items["additionalProperties"]; ok {
		t.Error("array-items additionalProperties:false not stripped")
	}
	// The rest of the schema must survive untouched.
	if opts["properties"].(map[string]any)["x"].(map[string]any)["type"] != "string" {
		t.Error("nested property shape was damaged")
	}
}

// Only the boolean-false form is rejected by xAI; the object and true forms
// are valid and must be preserved.
func TestSanitizeXAISchema_KeepsValidAdditionalProperties(t *testing.T) {
	in := []byte(`{"type":"object","properties":{` +
		`"a":{"type":"object","additionalProperties":true},` +
		`"b":{"type":"object","additionalProperties":{"type":"string"}}}}`)
	var got map[string]any
	if err := json.Unmarshal(sanitizeXAISchema(in), &got); err != nil {
		t.Fatal(err)
	}
	props := got["properties"].(map[string]any)
	if props["a"].(map[string]any)["additionalProperties"] != true {
		t.Error("additionalProperties:true was wrongly stripped")
	}
	if _, ok := props["b"].(map[string]any)["additionalProperties"].(map[string]any); !ok {
		t.Error("object-valued additionalProperties was wrongly stripped")
	}
}

// Unlike the Gemini sanitizer, the xAI shim must NOT touch enum, nested
// objects, or combinators - Grok accepts those.
func TestSanitizeXAISchema_PreservesRichDialect(t *testing.T) {
	in := []byte(`{"type":"object","properties":{` +
		`"mode":{"type":"string","enum":["a","b"]},` +
		`"either":{"oneOf":[{"type":"object","properties":{"x":{"type":"number"}}},{"type":"string"}]}}}`)
	out := sanitizeXAISchema(in)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	props := got["properties"].(map[string]any)
	if _, ok := props["mode"].(map[string]any)["enum"]; !ok {
		t.Error("enum was stripped")
	}
	if _, ok := props["either"].(map[string]any)["oneOf"]; !ok {
		t.Error("oneOf combinator was stripped")
	}
}

// Fail-open: unparseable input is returned verbatim.
func TestSanitizeXAISchema_FailOpen(t *testing.T) {
	bad := []byte(`{not valid json`)
	if !reflect.DeepEqual([]byte(sanitizeXAISchema(bad)), bad) {
		t.Error("bad JSON should pass through unchanged")
	}
}

// TestBuildRequest_SanitizesOnlyForXAI pins the gating: an additionalProperties:false
// schema is stripped when the request targets a Grok model and left
// byte-identical for other providers.
func TestBuildRequest_SanitizesOnlyForXAI(t *testing.T) {
	schema := []byte(`{"type":"object","additionalProperties":false,"properties":{"q":{"type":"string"}}}`)
	mk := func(model string) providers.Request {
		return providers.Request{
			Model: model,
			Tools: []providers.ToolSpec{{Name: "search", Schema: schema}},
		}
	}

	// Grok: additionalProperties:false stripped.
	out, err := BuildRequest(mk("x-ai/grok-build-0.1"), "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	if err := json.Unmarshal(out.Tools[0].Function.Parameters, &params); err != nil {
		t.Fatal(err)
	}
	if _, ok := params["additionalProperties"]; ok {
		t.Errorf("grok path did not strip additionalProperties:false: %v", params)
	}

	// Non-xAI (and non-gemini): schema passes through byte-identical.
	out2, err := BuildRequest(mk("deepseek/deepseek-v4-pro"), "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]byte(out2.Tools[0].Function.Parameters), schema) {
		t.Errorf("non-xAI schema was altered:\ngot  %s\nwant %s", out2.Tools[0].Function.Parameters, schema)
	}
}
