package miniyaml

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestUnmarshal_FlatMap — the most common shape in carlos's config.
func TestUnmarshal_FlatMap(t *testing.T) {
	in := []byte("user_name: George\ndefault_provider: anthropic\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"user_name":        "George",
		"default_provider": "anthropic",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}

// TestUnmarshal_NestedMap exercises the 3-level recursion the Config
// type relies on (providers → anthropic → api_key).
func TestUnmarshal_NestedMap(t *testing.T) {
	in := []byte("providers:\n  anthropic:\n    api_key: sk-x\n    default_model: claude-opus\n  openai:\n    api_key: sk-y\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("want map, got %T", got)
	}
	providers, ok := m["providers"].(map[string]any)
	if !ok {
		t.Fatalf("providers wrong type: %T", m["providers"])
	}
	anth, ok := providers["anthropic"].(map[string]any)
	if !ok {
		t.Fatalf("anthropic wrong type: %T", providers["anthropic"])
	}
	if anth["api_key"] != "sk-x" {
		t.Errorf("anthropic.api_key = %v", anth["api_key"])
	}
	if anth["default_model"] != "claude-opus" {
		t.Errorf("anthropic.default_model = %v", anth["default_model"])
	}
}

// TestUnmarshal_BlockList covers `- item` scalar lists.
func TestUnmarshal_BlockList(t *testing.T) {
	in := []byte("tags:\n  - alpha\n  - beta\n  - gamma\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	tags, ok := m["tags"].([]any)
	if !ok {
		t.Fatalf("tags wrong type: %T", m["tags"])
	}
	if len(tags) != 3 {
		t.Fatalf("want 3 tags, got %d", len(tags))
	}
	if tags[0] != "alpha" {
		t.Errorf("tags[0] = %v", tags[0])
	}
}

// TestUnmarshal_ListOfMaps covers the Schedule block in config.yaml.
func TestUnmarshal_ListOfMaps(t *testing.T) {
	in := []byte("schedules:\n  - name: morning\n    spec: \"0 9 * * 1-5\"\n    prompt: hi\n  - name: nightly\n    spec: \"0 0 * * *\"\n    prompt: bye\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m := got.(map[string]any)
	sch, ok := m["schedules"].([]any)
	if !ok {
		t.Fatalf("schedules wrong type: %T", m["schedules"])
	}
	if len(sch) != 2 {
		t.Fatalf("want 2 schedules, got %d", len(sch))
	}
	first := sch[0].(map[string]any)
	if first["name"] != "morning" {
		t.Errorf("first.name = %v", first["name"])
	}
	if first["spec"] != "0 9 * * 1-5" {
		t.Errorf("first.spec = %v", first["spec"])
	}
}

// TestUnmarshal_Scalars: types are inferred correctly.
func TestUnmarshal_Scalars(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"a: hello\n", "hello"},
		{"a: 42\n", int64(42)},
		{"a: -17\n", int64(-17)},
		{"a: 3.14\n", 3.14},
		{"a: true\n", true},
		{"a: false\n", false},
		{"a: yes\n", true},
		{"a: NO\n", false},
		{"a: null\n", nil},
		{"a: ~\n", nil},
		{"a:\n", nil},
		{"a: \"\"\n", ""},
		{"a: \"hello world\"\n", "hello world"},
		{"a: 'it''s fine'\n", "it's fine"},
		{"a: \"line1\\nline2\"\n", "line1\nline2"},
	}
	for _, c := range cases {
		got, err := Unmarshal([]byte(c.in))
		if err != nil {
			t.Errorf("%q: err %v", c.in, err)
			continue
		}
		m := got.(map[string]any)
		if !reflect.DeepEqual(m["a"], c.want) {
			t.Errorf("%q: got %#v (%T), want %#v (%T)", c.in, m["a"], m["a"], c.want, c.want)
		}
	}
}

// TestUnmarshal_FlowSequence: `tags: [a, b, c]` survives parsing as
// []any of strings.  Required because the notes testdata uses this.
func TestUnmarshal_FlowSequence(t *testing.T) {
	in := []byte("tags: [project, agent, tui]\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	tags := got.(map[string]any)["tags"].([]any)
	want := []any{"project", "agent", "tui"}
	if !reflect.DeepEqual(tags, want) {
		t.Errorf("got %#v want %#v", tags, want)
	}
}

// TestUnmarshal_FlowSequenceEmpty: `[]` parses as an empty []any.
func TestUnmarshal_FlowSequenceEmpty(t *testing.T) {
	in := []byte("tags: []\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	tags := got.(map[string]any)["tags"].([]any)
	if len(tags) != 0 {
		t.Errorf("want empty list, got %d entries", len(tags))
	}
}

// TestUnmarshal_Comments — `# foo` to EOL is stripped, comments
// inside quoted strings are preserved.
func TestUnmarshal_Comments(t *testing.T) {
	in := []byte("# top comment\nkey: value # trailing\nquoted: \"a # b\"\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["key"] != "value" {
		t.Errorf("key = %v", m["key"])
	}
	if m["quoted"] != "a # b" {
		t.Errorf("quoted = %v", m["quoted"])
	}
}

// TestUnmarshal_EmptyDocument: parsing empty or whitespace input yields
// (nil, nil).
func TestUnmarshal_EmptyDocument(t *testing.T) {
	for _, in := range [][]byte{nil, []byte(""), []byte("\n"), []byte("   \n\n  \n"), []byte("# only comment\n")} {
		got, err := Unmarshal(in)
		if err != nil {
			t.Errorf("err for %q: %v", in, err)
		}
		if got != nil {
			t.Errorf("want nil for %q, got %#v", in, got)
		}
	}
}

// TestUnmarshal_UnsupportedAnchor: `&anchor` → ErrUnsupportedSyntax.
func TestUnmarshal_UnsupportedAnchor(t *testing.T) {
	_, err := Unmarshal([]byte("a: &base hello\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("err should mention line number: %v", err)
	}
}

// TestUnmarshal_UnsupportedAlias.
func TestUnmarshal_UnsupportedAlias(t *testing.T) {
	_, err := Unmarshal([]byte("a:\n  b: 1\nc: *a\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestUnmarshal_UnsupportedMergeKey.
func TestUnmarshal_UnsupportedMergeKey(t *testing.T) {
	_, err := Unmarshal([]byte("base: 1\nchild:\n  <<: *base\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestUnmarshal_UnsupportedTypeTag.
func TestUnmarshal_UnsupportedTypeTag(t *testing.T) {
	_, err := Unmarshal([]byte("a: !!str 5\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestUnmarshal_UnsupportedFlowMap.
func TestUnmarshal_UnsupportedFlowMap(t *testing.T) {
	_, err := Unmarshal([]byte("a: {b: 1, c: 2}\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestUnmarshal_UnsupportedBlockScalar.
func TestUnmarshal_UnsupportedBlockScalar(t *testing.T) {
	_, err := Unmarshal([]byte("a: |\n  line1\n  line2\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestUnmarshal_UnsupportedTabIndent — yaml forbids tabs in indent.
func TestUnmarshal_UnsupportedTabIndent(t *testing.T) {
	_, err := Unmarshal([]byte("a:\n\tb: 1\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestMarshal_RoundTripsScalars confirms a Marshal+Unmarshal cycle
// preserves the canonical Go types.
func TestMarshal_RoundTripsScalars(t *testing.T) {
	input := map[string]any{
		"s":  "hello",
		"i":  int64(42),
		"f":  3.14,
		"b":  true,
		"n":  nil,
		"qs": "value with: special # chars",
	}
	data, err := Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("re-parse %q: %v", data, err)
	}
	bm := back.(map[string]any)
	for k, want := range input {
		if !reflect.DeepEqual(bm[k], want) {
			t.Errorf("%s: got %#v want %#v", k, bm[k], want)
		}
	}
}

// TestMarshal_DeterministicKeyOrder pins that two calls produce
// byte-identical output when the input is the same (alphabetical sort).
func TestMarshal_DeterministicKeyOrder(t *testing.T) {
	in := map[string]any{"z": 1, "a": 2, "m": 3}
	a, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("non-deterministic: %s vs %s", a, b)
	}
	if !strings.HasPrefix(string(a), "a:") {
		t.Errorf("not sorted: %s", a)
	}
}

// TestMarshal_NestedRoundTrip confirms nested maps and lists round-trip.
func TestMarshal_NestedRoundTrip(t *testing.T) {
	in := map[string]any{
		"providers": map[string]any{
			"anthropic": map[string]any{
				"api_key":       "sk-x",
				"default_model": "claude",
			},
		},
		"schedules": []any{
			map[string]any{"name": "a", "spec": "0 9 * * *"},
			map[string]any{"name": "b", "spec": "30 8 * * 1-5"},
		},
	}
	data, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("re-parse:\n%s\nerr: %v", data, err)
	}
	if !reflect.DeepEqual(back, in) {
		t.Errorf("not equal\nin   %#v\nback %#v\nbytes:\n%s", in, back, data)
	}
}

// TestMarshal_EmptyContainersDropped: empty sub-maps and sub-lists are
// dropped from the output entirely so non-pointer struct fields with
// no content don't leave a stray `theme: {}` / `tags: []` line in the
// on-disk file.  This is the YAML-layer companion to json's omitempty.
func TestMarshal_EmptyContainersDropped(t *testing.T) {
	in := map[string]any{
		"empty_map":  map[string]any{},
		"empty_list": []any{},
		"kept":       "value",
	}
	data, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "empty_map") {
		t.Errorf("empty_map should be dropped: %s", s)
	}
	if strings.Contains(s, "empty_list") {
		t.Errorf("empty_list should be dropped: %s", s)
	}
	if !strings.Contains(s, "kept: value") {
		t.Errorf("kept should remain: %s", s)
	}
}

// TestUnmarshal_EmptyFlowMapValue: a `key: {}` line parses as an empty
// map.  Other libraries emit this for zero-valued structs so we must
// tolerate it on the input path.
func TestUnmarshal_EmptyFlowMapValue(t *testing.T) {
	in := []byte("theme: {}\nuser_name: George\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if _, ok := m["theme"].(map[string]any); !ok {
		t.Errorf("theme should be empty map, got %T", m["theme"])
	}
	if m["user_name"] != "George" {
		t.Errorf("user_name = %v", m["user_name"])
	}
}

// TestMarshal_QuoteAmbiguousStrings — a string that looks like a
// number / bool / null must be quoted.
func TestMarshal_QuoteAmbiguousStrings(t *testing.T) {
	in := map[string]any{
		"a": "42",     // looks numeric
		"b": "true",   // looks bool
		"c": "null",   // looks null
		"d": "normal", // bare ok
	}
	data, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `a: "42"`) {
		t.Errorf("'42' should be quoted: %s", s)
	}
	if !strings.Contains(s, `b: "true"`) {
		t.Errorf("'true' should be quoted: %s", s)
	}
	if !strings.Contains(s, `c: "null"`) {
		t.Errorf("'null' should be quoted: %s", s)
	}
	if !strings.Contains(s, "d: normal") {
		t.Errorf("'normal' should be bare: %s", s)
	}
}

// TestSplitFrontmatter_Happy: the canonical Obsidian shape.
func TestSplitFrontmatter_Happy(t *testing.T) {
	in := []byte("---\ntitle: hello\ntags: [a, b]\n---\n\n# body\n")
	fm, body, found, err := SplitFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found")
	}
	if string(fm) != "title: hello\ntags: [a, b]\n" {
		t.Errorf("frontmatter = %q", fm)
	}
	if !strings.HasPrefix(string(body), "\n# body") {
		t.Errorf("body = %q", body)
	}
}

// TestSplitFrontmatter_NoOpener: a file with no `---` returns
// (nil, data, false, nil).
func TestSplitFrontmatter_NoOpener(t *testing.T) {
	in := []byte("# just markdown\n")
	fm, body, found, err := SplitFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("should not find frontmatter")
	}
	if fm != nil {
		t.Errorf("fm should be nil, got %q", fm)
	}
	if string(body) != string(in) {
		t.Errorf("body should equal in, got %q", body)
	}
}

// TestSplitFrontmatter_Unterminated: an opener with no closer is an
// error.
func TestSplitFrontmatter_Unterminated(t *testing.T) {
	in := []byte("---\ntitle: hello\n(no closer)\n")
	_, _, _, err := SplitFrontmatter(in)
	if err == nil {
		t.Fatal("want unterminated error")
	}
}

// TestSplitFrontmatter_Empty: `---\n---\n` is found with empty
// frontmatter.
func TestSplitFrontmatter_Empty(t *testing.T) {
	in := []byte("---\n---\nbody\n")
	fm, body, found, err := SplitFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected found")
	}
	if len(fm) != 0 {
		t.Errorf("fm should be empty, got %q", fm)
	}
	if string(body) != "body\n" {
		t.Errorf("body = %q", body)
	}
}

// TestSplitFrontmatter_CRLF: tolerate CRLF line endings.
func TestSplitFrontmatter_CRLF(t *testing.T) {
	in := []byte("---\r\ntitle: x\r\n---\r\nbody\r\n")
	fm, body, found, err := SplitFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected found")
	}
	if !strings.Contains(string(fm), "title: x") {
		t.Errorf("fm = %q", fm)
	}
	if !strings.HasPrefix(string(body), "body") {
		t.Errorf("body = %q", body)
	}
}

// TestSplitFrontmatter_BOM: tolerate a leading UTF-8 BOM.
func TestSplitFrontmatter_BOM(t *testing.T) {
	in := []byte("\xEF\xBB\xBF---\ntitle: x\n---\nbody\n")
	_, _, found, err := SplitFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected found")
	}
}

// TestUnmarshalStruct_RoundTrip: a Config-shaped struct survives a
// Marshal+Unmarshal cycle.
func TestUnmarshalStruct_RoundTrip(t *testing.T) {
	type Provider struct {
		APIKey       string `json:"api_key,omitempty"`
		DefaultModel string `json:"default_model,omitempty"`
	}
	type Config struct {
		UserName  string              `json:"user_name"`
		Providers map[string]Provider `json:"providers,omitempty"`
	}
	in := Config{
		UserName: "George",
		Providers: map[string]Provider{
			"anthropic": {APIKey: "sk-x", DefaultModel: "claude"},
		},
	}
	data, err := MarshalStruct(&in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := UnmarshalStruct(data, &out); err != nil {
		t.Fatalf("decode: %v\nbytes:\n%s", err, data)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("not equal\nin  %#v\nout %#v\nbytes:\n%s", in, out, data)
	}
}

// TestUnmarshalStruct_OmitemptyMatchesJSON: a zero-value field with
// json omitempty is absent from the output, matching json.Marshal.
func TestUnmarshalStruct_OmitemptyMatchesJSON(t *testing.T) {
	type S struct {
		A string `json:"a,omitempty"`
		B string `json:"b"`
	}
	data, err := MarshalStruct(&S{B: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "a:") {
		t.Errorf("omitempty should drop empty 'a': %s", s)
	}
	if !strings.Contains(s, "b: hi") {
		t.Errorf("'b' should be present: %s", s)
	}
}

// TestMarshal_Float_HasDecimal — a whole-number float must emit a
// trailing `.0` so the parser re-parses it as float, not int.
func TestMarshal_Float_HasDecimal(t *testing.T) {
	data, err := Marshal(map[string]any{"f": 5.0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "f: 5.0") {
		t.Errorf("expected `f: 5.0`, got %q", data)
	}
}

// TestMarshal_StringSliceField — a []string in a Frontmatter map
// renders as a block sequence.
func TestMarshal_StringSliceField(t *testing.T) {
	in := map[string]any{"tags": []any{"alpha", "beta"}}
	data, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := "tags:\n  - alpha\n  - beta\n"
	if string(data) != want {
		t.Errorf("got\n%s\nwant\n%s", data, want)
	}
}

// TestUnmarshal_SameIndentListValue pins the YAML idiom that lets a
// list value sit at the same indent as its parent key (common in
// hand-written config files).
func TestUnmarshal_SameIndentListValue(t *testing.T) {
	in := []byte("schedules:\n- name: a\n  spec: \"0 9 * * *\"\n- name: b\n  spec: \"30 8 * * *\"\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	sch := got.(map[string]any)["schedules"].([]any)
	if len(sch) != 2 {
		t.Errorf("want 2 schedules, got %d", len(sch))
	}
	if sch[0].(map[string]any)["name"] != "a" {
		t.Errorf("first.name = %v", sch[0].(map[string]any)["name"])
	}
}

// TestRoundTrip_TimeField — time.Time fields via the JSON path serialize
// as RFC3339 strings and parse back into time.Time.  Skills use this for
// Created/Updated.
func TestRoundTrip_TimeField(t *testing.T) {
	type S struct {
		Created string `json:"created"`
		Name    string `json:"name"`
	}
	in := S{Created: "2026-06-05T12:00:00Z", Name: "test"}
	data, err := MarshalStruct(&in)
	if err != nil {
		t.Fatal(err)
	}
	var out S
	if err := UnmarshalStruct(data, &out); err != nil {
		t.Fatalf("decode: %v\nbytes:\n%s", err, data)
	}
	if out != in {
		t.Errorf("not equal\nin  %+v\nout %+v", in, out)
	}
}
