package oacompat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
)

func TestTargetsKimi(t *testing.T) {
	cases := map[string]bool{
		"moonshotai/kimi-k2.6":     true,
		"moonshotai/kimi-k2.7-code": true,
		"kimi-k2.6":                true,
		"z-ai/glm-5.2":             false,
		"x-ai/grok-build-0.1":      false,
		"google/gemini-3.5-flash":  false,
	}
	for model, want := range cases {
		if got := targetsKimi("openrouter", model); got != want {
			t.Errorf("targetsKimi(%q) = %v, want %v", model, got, want)
		}
	}
}

// The confirmed Kimi gotcha: legacy Draft-07 `definitions` + `#/definitions/`
// refs must become `$defs` + `#/$defs/`.
func TestSanitizeKimiSchema_RewritesRefsAndDefs(t *testing.T) {
	in := []byte(`{
		"type":"object",
		"properties":{"a":{"$ref":"#/definitions/Thing"}},
		"definitions":{"Thing":{"type":"object","properties":{"x":{"type":"string"}}}}
	}`)
	var got map[string]any
	if err := json.Unmarshal(sanitizeKimiSchema(in), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["definitions"]; ok {
		t.Error("legacy definitions block not renamed")
	}
	defs, ok := got["$defs"].(map[string]any)
	if !ok {
		t.Fatal("$defs block missing after rewrite")
	}
	if _, ok := defs["Thing"]; !ok {
		t.Error("$defs lost the Thing definition")
	}
	ref := got["properties"].(map[string]any)["a"].(map[string]any)["$ref"].(string)
	if ref != "#/$defs/Thing" {
		t.Errorf("$ref = %q, want #/$defs/Thing", ref)
	}
}

// Already-correct $defs/#/$defs schemas pass through unchanged.
func TestSanitizeKimiSchema_LeavesModernRefs(t *testing.T) {
	in := []byte(`{"properties":{"a":{"$ref":"#/$defs/T"}},"$defs":{"T":{"type":"string"}}}`)
	var got map[string]any
	if err := json.Unmarshal(sanitizeKimiSchema(in), &got); err != nil {
		t.Fatal(err)
	}
	ref := got["properties"].(map[string]any)["a"].(map[string]any)["$ref"].(string)
	if ref != "#/$defs/T" {
		t.Errorf("$ref was altered: %q", ref)
	}
}

// Unlike Gemini, Kimi accepts the broad dialect: additionalProperties, enum,
// and combinators must survive untouched.
func TestSanitizeKimiSchema_PreservesRichDialect(t *testing.T) {
	in := []byte(`{"type":"object","additionalProperties":false,` +
		`"properties":{"m":{"type":"string","enum":["a","b"]}}}`)
	var got map[string]any
	if err := json.Unmarshal(sanitizeKimiSchema(in), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["additionalProperties"]; !ok {
		t.Error("additionalProperties was wrongly stripped (Kimi accepts it)")
	}
	if _, ok := got["properties"].(map[string]any)["m"].(map[string]any)["enum"]; !ok {
		t.Error("enum was wrongly stripped")
	}
}

func TestSanitizeKimiSchema_FailOpen(t *testing.T) {
	bad := []byte(`{nope`)
	if !reflect.DeepEqual([]byte(sanitizeKimiSchema(bad)), bad) {
		t.Error("bad JSON should pass through unchanged")
	}
}

// Gating: the $ref rewrite happens for Kimi models and not for others.
func TestBuildRequest_SanitizesOnlyForKimi(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"a":{"$ref":"#/definitions/T"}},"definitions":{"T":{"type":"string"}}}`)
	mk := func(model string) providers.Request {
		return providers.Request{
			Model: model,
			Tools: []providers.ToolSpec{{Name: "f", Schema: schema}},
		}
	}

	out, err := BuildRequest(mk("moonshotai/kimi-k2.6"), "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out.Tools[0].Function.Parameters); !strings.Contains(s, "#/$defs/T") || strings.Contains(s, "#/definitions/") {
		t.Errorf("kimi path did not rewrite refs: %s", s)
	}

	out2, err := BuildRequest(mk("deepseek/deepseek-v4-pro"), "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]byte(out2.Tools[0].Function.Parameters), schema) {
		t.Errorf("non-kimi schema was altered:\ngot  %s\nwant %s", out2.Tools[0].Function.Parameters, schema)
	}
}
