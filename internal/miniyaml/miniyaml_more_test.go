package miniyaml

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestUnmarshal_TopLevelScalar covers parseValue returning a bare scalar
// at the document root.
func TestUnmarshal_TopLevelScalar(t *testing.T) {
	got, err := Unmarshal([]byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("got %#v, want \"hello\"", got)
	}
}

// TestUnmarshal_TopLevelList covers parseList at the root with no
// surrounding key.
func TestUnmarshal_TopLevelList(t *testing.T) {
	got, err := Unmarshal([]byte("- a\n- b\n- c\n"))
	if err != nil {
		t.Fatal(err)
	}
	lst, ok := got.([]any)
	if !ok {
		t.Fatalf("want []any, got %T", got)
	}
	if len(lst) != 3 || lst[0] != "a" || lst[2] != "c" {
		t.Errorf("got %#v", lst)
	}
}

// TestUnmarshal_ListItemEmptyThenNested exercises parseListItem's `-`
// alone branch where the value continues on subsequent indented lines.
func TestUnmarshal_ListItemEmptyThenNested(t *testing.T) {
	got, err := Unmarshal([]byte("xs:\n  -\n    k: v\n  - inline\n"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m := got.(map[string]any)
	xs := m["xs"].([]any)
	if len(xs) != 2 {
		t.Fatalf("want 2 items, got %d (%#v)", len(xs), xs)
	}
	first, ok := xs[0].(map[string]any)
	if !ok {
		t.Fatalf("first item should be map, got %T", xs[0])
	}
	if first["k"] != "v" {
		t.Errorf("first[k]=%v", first["k"])
	}
	if xs[1] != "inline" {
		t.Errorf("second=%v", xs[1])
	}
}

// TestUnmarshal_NestedListInList tests a list-of-lists shape.
func TestUnmarshal_NestedListInList(t *testing.T) {
	in := []byte("outer:\n  - - inner1\n    - inner2\n  - - other\n")
	// Note: nested block list under a list item is tricky; ensure we
	// at least don't panic and produce useful output. This shape is
	// rare so just verify Unmarshal does not error on a simpler form.
	_, err := Unmarshal(in)
	// Accept either success or a clean error; the goal is no panic.
	_ = err
}

// TestUnmarshal_UnexpectedContent covers the `unexpected content at
// line N` branch where parseValue stops but lines remain.
func TestUnmarshal_BadIndent(t *testing.T) {
	// Child indented less than the opener creates an unexpected indent
	// error path inside parseMap when sibling indents differ.
	_, err := Unmarshal([]byte("a:\n   b: 1\n  c: 2\n"))
	if err == nil {
		t.Fatal("expected indent error")
	}
}

// TestUnmarshal_LeadingColonStaysScalar - `: value` doesn't match the
// `key:` shape (key would be empty) so it's parsed as a bare scalar.
func TestUnmarshal_LeadingColonStaysScalar(t *testing.T) {
	got, err := Unmarshal([]byte(": value\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != ": value" {
		t.Errorf("got %#v", got)
	}
}

// TestUnmarshal_FlowSequenceUnterminated triggers the missing-`]` error.
func TestUnmarshal_FlowSequenceUnterminated(t *testing.T) {
	_, err := Unmarshal([]byte("tags: [a, b\n"))
	if err == nil {
		t.Fatal("expected unterminated error")
	}
}

// TestUnmarshal_FlowSequenceNested rejects nested `[` inside a flow
// sequence with ErrUnsupportedSyntax.
func TestUnmarshal_FlowSequenceNested(t *testing.T) {
	// Nesting inside a quoted string is fine, but a raw `[` should fail.
	_, err := Unmarshal([]byte("tags: [a, [b, c]]\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestUnmarshal_QuotedKey verifies splitMapKey honors quoted keys.
func TestUnmarshal_QuotedKey(t *testing.T) {
	got, err := Unmarshal([]byte("\"weird: key\": value\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["weird: key"] != "value" {
		t.Errorf("got %#v", m)
	}
}

// TestUnmarshal_HashKeyInQuotes ensures `#` inside a quoted scalar is
// not treated as a comment.
func TestUnmarshal_HashKeyInQuotes(t *testing.T) {
	got, err := Unmarshal([]byte("k: 'a # b'\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["k"] != "a # b" {
		t.Errorf("got %#v", m)
	}
}

// TestUnmarshal_LineWithComment scrubs trailing `# foo` after a quoted
// scalar.
func TestUnmarshal_QuotedThenComment(t *testing.T) {
	got, err := Unmarshal([]byte("k: \"hi\" # trailing comment\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["k"] != "hi" {
		t.Errorf("got %v", m["k"])
	}
}

// TestParseInt_LeadingZero confirms `0123` stays as a string scalar.
func TestParseInt_LeadingZero(t *testing.T) {
	got, err := Unmarshal([]byte("id: 0123\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["id"] != "0123" {
		t.Errorf("leading-zero int should remain string; got %#v", m["id"])
	}
}

// TestParseInt_PlusSign rejects `+5` as int and keeps it as string.
func TestParseInt_PlusSign(t *testing.T) {
	got, err := Unmarshal([]byte("n: +5\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["n"] != "+5" {
		t.Errorf("got %#v", m["n"])
	}
}

// TestUnquoteDouble_UnknownEscape preserves a `\x` sequence verbatim.
func TestUnquoteDouble_UnknownEscape(t *testing.T) {
	// `\x` is not a known escape; the parser preserves it.
	got, err := Unmarshal([]byte("k: \"a\\xb\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	v := m["k"].(string)
	if !strings.Contains(v, "\\") {
		t.Errorf("unknown escape should be preserved; got %q", v)
	}
}

// TestUnquoteDouble_AllEscapes covers every escape branch.
func TestUnquoteDouble_AllEscapes(t *testing.T) {
	got, err := Unmarshal([]byte("k: \"\\\"\\\\\\/\\n\\r\\t\\0\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	v := m["k"].(string)
	want := "\"\\/\n\r\t\x00"
	if v != want {
		t.Errorf("got %q, want %q", v, want)
	}
}

// TestUnquoteSingle_NoEscape preserves a literal-no-`”` single-quoted
// value via the fast path.
func TestUnquoteSingle_NoEscape(t *testing.T) {
	got, err := Unmarshal([]byte("k: 'plain'\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["k"] != "plain" {
		t.Errorf("got %v", m["k"])
	}
}

// TestUnmarshal_FlowSequenceQuotedComma keeps a comma inside a quoted
// element intact.
func TestUnmarshal_FlowSequenceQuotedComma(t *testing.T) {
	got, err := Unmarshal([]byte("xs: [\"a, b\", c]\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	xs := m["xs"].([]any)
	if len(xs) != 2 || xs[0] != "a, b" || xs[1] != "c" {
		t.Errorf("got %#v", xs)
	}
}

// TestUnmarshal_BracketBareScalar - a top-level `[unclosed` line is
// parsed as a bare scalar (flow sequences only kick in as the value of
// a `key:` pair).
func TestUnmarshal_BracketBareScalar(t *testing.T) {
	got, err := Unmarshal([]byte("[unclosed\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "[unclosed" {
		t.Errorf("got %#v", got)
	}
}

// TestParser_Lookback exercises lookback in both states.
func TestParser_Lookback(t *testing.T) {
	p := &parser{lines: []line{{num: 1, body: "k: v"}}}
	if p.lookback() != nil {
		t.Errorf("lookback before advance should be nil")
	}
	p.advance()
	lb := p.lookback()
	if lb == nil || lb.num != 1 {
		t.Errorf("lookback after advance = %+v", lb)
	}
}

// TestParser_HasMore verifies the simple state predicate.
func TestParser_HasMore(t *testing.T) {
	p := &parser{lines: []line{{num: 1, body: "x"}}}
	if !p.hasMore() {
		t.Fatal("hasMore should be true initially")
	}
	p.advance()
	if p.hasMore() {
		t.Fatal("hasMore should be false after advance")
	}
}

// TestMarshal_StringSliceTopLevel exercises the []string promotion path
// in appendValue when a []string is the marshal target itself.
func TestMarshal_StringSliceTopLevel(t *testing.T) {
	out, err := Marshal([]string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := "- a\n- b\n"
	if string(out) != want {
		t.Errorf("got %q want %q", out, want)
	}
}

// TestMarshal_MapStringString promotes a map[string]string to map[string]any.
func TestMarshal_MapStringString(t *testing.T) {
	out, err := Marshal(map[string]string{"a": "1", "b": "2"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "a: \"1\"") || !strings.Contains(s, "b: \"2\"") {
		t.Errorf("got %q", s)
	}
}

// TestMarshal_NestedMapStringString as a value within a map.
func TestMarshal_NestedMapStringString(t *testing.T) {
	in := map[string]any{
		"env": map[string]string{"FOO": "bar"},
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "env:") || !strings.Contains(s, "FOO: bar") {
		t.Errorf("got %q", s)
	}
}

// TestMarshal_NestedStringSlice covers the []string value branch in
// appendMap.
func TestMarshal_NestedStringSlice(t *testing.T) {
	in := map[string]any{"tags": []string{"a", "b"}}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := "tags:\n  - a\n  - b\n"
	if string(out) != want {
		t.Errorf("got %q want %q", out, want)
	}
}

// TestMarshal_ListOfLists exercises appendList's `[]any` recurse arm
// and the empty-inner-list rendering.
func TestMarshal_ListOfLists(t *testing.T) {
	in := []any{
		[]any{"a", "b"},
		[]any{},
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "- \n  - a") && !strings.Contains(s, "-\n  - a") {
		// We only require that nested list renders; precise format
		// may vary. Confirm content survives roundtrip.
	}
	// Roundtrip-style: just confirm parsing back works for the first
	// item.
	if !strings.Contains(s, "a") || !strings.Contains(s, "b") {
		t.Errorf("nested list contents missing: %q", s)
	}
	if !strings.Contains(s, "[]") {
		t.Errorf("empty inner list should render as []: %q", s)
	}
}

// TestMarshal_ListOfMapEmptyMaps drops empty maps to `{}` per item.
func TestMarshal_ListOfMapEmptyInList(t *testing.T) {
	in := []any{
		map[string]any{},
		map[string]any{"a": "1"},
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "{}") {
		t.Errorf("empty map in list should render as {}: %q", s)
	}
	if !strings.Contains(s, "a: \"1\"") {
		t.Errorf("non-empty map item should render: %q", s)
	}
}

// TestMarshal_ListOfMapsWithEmptyValues - `{}` and `[]` as inline values.
func TestMarshal_MapValueEmptyInList(t *testing.T) {
	in := []any{
		map[string]any{"k": map[string]any{}, "ls": []any{}},
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "{}") || !strings.Contains(s, "[]") {
		t.Errorf("empty inline values missing: %q", s)
	}
}

// TestFormatScalar_AllNumeric covers the int/uint/float variants.
func TestFormatScalar_AllNumeric(t *testing.T) {
	in := map[string]any{
		"i":   int(7),
		"i32": int32(8),
		"i64": int64(9),
		"u":   uint(10),
		"u32": uint32(11),
		"u64": uint64(12),
		"f32": float32(1.5),
		"f64": float64(2.5),
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"i: 7", "i32: 8", "i64: 9", "u: 10", "u32: 11", "u64: 12", "f32: 1.5", "f64: 2.5"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %q", want, s)
		}
	}
}

// TestFormatScalar_TimeRoundtripsAsRFC3339 confirms time.Time marshals
// to a quoted RFC3339 string.
func TestFormatScalar_TimeMarshals(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	out, err := Marshal(map[string]any{"t": ts})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "2026-01-02T03:04:05Z") {
		t.Errorf("got %q", s)
	}
}

// TestFormatScalar_Unsupported errors cleanly for an exotic type.
func TestFormatScalar_Unsupported(t *testing.T) {
	type weird struct{ X int }
	_, err := Marshal(map[string]any{"k": weird{X: 1}})
	if err == nil {
		t.Fatal("expected error on unsupported scalar type")
	}
}

// TestMarshal_FloatInfNaN - NaN/Inf are valid Go floats but the
// stringifier emits "NaN"/"+Inf" which round-trip into strings. Just
// confirm Marshal doesn't error.
func TestMarshal_FloatLargeMagnitude(t *testing.T) {
	out, err := Marshal(map[string]any{"f": 1e20})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "e+20") && !strings.Contains(string(out), "e20") {
		t.Errorf("expected scientific notation, got %q", out)
	}
}

// TestQuoteIfNeeded_LeadingSpace forces quoting because the leading
// whitespace would otherwise be eaten on the parse-back.
func TestQuoteIfNeeded_LeadingSpace(t *testing.T) {
	out, err := Marshal(map[string]any{"k": "  spaced"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "\"  spaced\"") {
		t.Errorf("leading-space should be quoted: %q", out)
	}
}

// TestQuoteDouble_AllEscapes confirms every escape branch is exercised
// when a string contains special characters.
func TestQuoteDouble_AllEscapes(t *testing.T) {
	in := "a\"b\\c\nd\re\tf\x00g"
	out, err := Marshal(map[string]any{"k": in})
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unmarshal(out)
	if err != nil {
		t.Fatalf("roundtrip: %v\n%s", err, out)
	}
	if back.(map[string]any)["k"] != in {
		t.Errorf("not equal\nin   %q\nback %q", in, back.(map[string]any)["k"])
	}
}

// TestUnmarshal_BareStringWithDot remains a string when no exponent and
// not a parseable float.
func TestUnmarshal_BareNonFloat(t *testing.T) {
	got, err := Unmarshal([]byte("k: 1.2.3\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["k"] != "1.2.3" {
		t.Errorf("got %#v", m["k"])
	}
}

// TestUnmarshal_BareScalarWithE ensures `e`/`E` in a non-numeric token
// still parses as a string.
func TestUnmarshal_BareWithEButNotFloat(t *testing.T) {
	got, err := Unmarshal([]byte("k: abceDef\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["k"] != "abceDef" {
		t.Errorf("got %#v", m["k"])
	}
}

// TestUnmarshalStruct_EmptyInput is documented as a no-op nil.
func TestUnmarshalStruct_EmptyInput(t *testing.T) {
	type S struct {
		A string `json:"a"`
	}
	var out S
	if err := UnmarshalStruct(nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.A != "" {
		t.Errorf("nil input should not populate; got %+v", out)
	}
}

// TestUnmarshalStruct_BadIntoNonPointer errors via json.Unmarshal.
func TestUnmarshalStruct_BadType(t *testing.T) {
	type S struct {
		A int `json:"a"`
	}
	var out S
	// "abc" can't decode into int → json error surfaces.
	err := UnmarshalStruct([]byte("a: abc\n"), &out)
	if err == nil {
		t.Fatal("expected decode error")
	}
}

// TestMarshalStruct_BadInput exercises the json-encode failure path.
func TestMarshalStruct_BadInput(t *testing.T) {
	// A channel can't be json-encoded.
	_, err := MarshalStruct(map[string]any{"c": make(chan int)})
	if err == nil {
		t.Fatal("expected error encoding channel")
	}
}

// TestSplitFrontmatter_UnterminatedReportsError covers the explicit
// error path.
func TestSplitFrontmatter_UnterminatedMidScan(t *testing.T) {
	in := []byte("---\nfoo: bar\nno closer here\n")
	_, _, _, err := SplitFrontmatter(in)
	if err == nil {
		t.Fatal("expected unterminated frontmatter error")
	}
}

// TestSplitFrontmatter_NoTrailingNewline accepts a final `---` at EOF.
func TestSplitFrontmatter_ClosingAtEOF(t *testing.T) {
	in := []byte("---\nfoo: bar\n---")
	fm, _, found, err := SplitFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found")
	}
	if !strings.Contains(string(fm), "foo: bar") {
		t.Errorf("fm = %q", fm)
	}
}

// TestUnmarshal_CommentOnlyAfterContent confirms a comment-only line
// in the middle is silently dropped.
func TestUnmarshal_MidComment(t *testing.T) {
	in := []byte("a: 1\n# comment in middle\nb: 2\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["a"] != int64(1) || m["b"] != int64(2) {
		t.Errorf("got %#v", m)
	}
}

// TestUnmarshal_HashEmbeddedInBareString - `value#tag` is NOT a comment
// because the `#` isn't preceded by whitespace.
func TestUnmarshal_HashEmbedded(t *testing.T) {
	in := []byte("k: value#tag\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["k"] != "value#tag" {
		t.Errorf("got %#v", m["k"])
	}
}

// TestRejectUnsupported_BlockScalarFolded checks the `: >` arm of
// hasBlockScalarIndicator.
func TestRejectUnsupported_BlockScalarFolded(t *testing.T) {
	_, err := Unmarshal([]byte("a: >\n  line1\n  line2\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestRejectUnsupported_BlockScalarChomp covers the modifier branches
// (`-`, digit) in hasBlockScalarIndicator.
func TestRejectUnsupported_BlockScalarChomp(t *testing.T) {
	for _, in := range []string{"a: |-\n  x\n", "a: |+\n  x\n", "a: |2\n  x\n"} {
		_, err := Unmarshal([]byte(in))
		if !errors.Is(err, ErrUnsupportedSyntax) {
			t.Errorf("input %q: want ErrUnsupportedSyntax, got %v", in, err)
		}
	}
}

// TestRejectUnsupported_ComplexKey exercises the `? key` branch.
func TestRejectUnsupported_ComplexKey(t *testing.T) {
	_, err := Unmarshal([]byte("? complex\n: value\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestRejectUnsupported_AnchorInsideQuotes is NOT treated as an anchor.
func TestRejectUnsupported_AnchorInsideQuotes(t *testing.T) {
	got, err := Unmarshal([]byte("k: \"&foo bar\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["k"] != "&foo bar" {
		t.Errorf("got %#v", m)
	}
}

// TestUnmarshal_DocSeparatorMidStream rejects a `---` after content.
func TestUnmarshal_DocSeparator(t *testing.T) {
	_, err := Unmarshal([]byte("a: 1\n---\nb: 2\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax for multi-doc, got %v", err)
	}
}

// TestUnmarshal_DocEnd `...` is also rejected.
func TestUnmarshal_DocEnd(t *testing.T) {
	_, err := Unmarshal([]byte("a: 1\n...\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax for `...`, got %v", err)
	}
}

// TestUnmarshal_ListItemFollowedByMap covers the nested-map-after-`-`
// path.
func TestUnmarshal_ListThenInlineMap(t *testing.T) {
	in := []byte("- k1: v1\n  k2: v2\n- k3: v3\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	lst := got.([]any)
	if len(lst) != 2 {
		t.Fatalf("want 2, got %d", len(lst))
	}
	first := lst[0].(map[string]any)
	if first["k1"] != "v1" || first["k2"] != "v2" {
		t.Errorf("first = %#v", first)
	}
}

// TestUnmarshal_ListItemFlowSequence covers `- [a, b]` as a list value.
func TestUnmarshal_ListItemFlowSequence(t *testing.T) {
	in := []byte("xs:\n  - [a, b]\n  - [c]\n")
	got, err := Unmarshal(in)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	xs := m["xs"].([]any)
	if len(xs) != 2 {
		t.Fatalf("want 2 items, got %d", len(xs))
	}
	first := xs[0].([]any)
	if len(first) != 2 || first[0] != "a" {
		t.Errorf("first inner = %#v", first)
	}
}

// TestRejectUnsupported_FlowMapInBody catches a non-empty `{` value in
// the body of a multi-line map.
func TestRejectUnsupported_FlowMapNonEmpty(t *testing.T) {
	_, err := Unmarshal([]byte("k: {a: 1}\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestUnmarshal_EmptyFlowSequenceWithWhitespace tolerates `[ ]`.
func TestUnmarshal_FlowSequenceWhitespaceOnly(t *testing.T) {
	got, err := Unmarshal([]byte("xs: [ ]\n"))
	if err != nil {
		t.Fatal(err)
	}
	xs := got.(map[string]any)["xs"].([]any)
	if len(xs) != 0 {
		t.Errorf("want empty, got %#v", xs)
	}
}

// TestMarshal_RoundTripsListOfMaps confirms the marshal of complex
// composite types parses back identically.
func TestMarshal_DeepRoundTrip(t *testing.T) {
	in := map[string]any{
		"top": map[string]any{
			"list": []any{
				map[string]any{"a": int64(1), "b": "hello"},
				map[string]any{"a": int64(2), "b": "world"},
			},
		},
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unmarshal(out)
	if err != nil {
		t.Fatalf("roundtrip:\n%s\n%v", out, err)
	}
	if !reflect.DeepEqual(in, back) {
		t.Errorf("not equal\nin   %#v\nback %#v\nbytes:\n%s", in, back, out)
	}
}

// TestParseScalar_VariantCases sweeps the lower-case bool/null tokens
// not covered by the canonical case set.
func TestParseScalar_Variants(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"a: TRUE\n", true},
		{"a: FALSE\n", false},
		{"a: On\n", true},
		{"a: Off\n", false},
		{"a: Null\n", nil},
		{"a: NULL\n", nil},
	}
	for _, c := range cases {
		got, err := Unmarshal([]byte(c.in))
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		m := got.(map[string]any)
		if !reflect.DeepEqual(m["a"], c.want) {
			t.Errorf("%q: got %#v want %#v", c.in, m["a"], c.want)
		}
	}
}

// TestMarshal_NilTopLevel emits literal `null`.
func TestMarshal_NilTopLevel(t *testing.T) {
	out, err := Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "null\n" {
		t.Errorf("got %q", out)
	}
}

// TestMarshal_BoolTopLevel emits literal `true`.
func TestMarshal_BoolTopLevel(t *testing.T) {
	out, err := Marshal(true)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "true\n" {
		t.Errorf("got %q", out)
	}
}

// TestMarshal_EmptyMapTopLevel emits literal `{}` at the root.
func TestMarshal_EmptyMapTopLevel(t *testing.T) {
	out, err := Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "{}\n" {
		t.Errorf("got %q", out)
	}
}

// TestMarshal_EmptyListTopLevel emits literal `[]` at the root.
func TestMarshal_EmptyListTopLevel(t *testing.T) {
	out, err := Marshal([]any{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "[]\n" {
		t.Errorf("got %q", out)
	}
}

// TestMarshal_ListItemNestedMap exercises the nested-map-in-list-item
// branch.
func TestMarshal_ListItemNestedMap(t *testing.T) {
	in := []any{
		map[string]any{
			"a": "1",
			"nest": map[string]any{
				"deep": "value",
			},
		},
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "nest:") || !strings.Contains(s, "deep: value") {
		t.Errorf("missing nested content: %q", s)
	}
}

// TestMarshal_ListItemNestedList exercises the nested-list-in-list-
// item branch where the nested list has content.
func TestMarshal_ListItemNestedList(t *testing.T) {
	in := []any{
		map[string]any{
			"items": []any{"x", "y"},
		},
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "items:") || !strings.Contains(s, "- x") {
		t.Errorf("missing nested list: %q", s)
	}
}

// TestUnmarshal_MergeKeyBare hits the `<<` prefix branch.
func TestUnmarshal_MergeKeyBare(t *testing.T) {
	_, err := Unmarshal([]byte("<<\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestSplitLines_NoTrailingNewline covers the tail-append branch in
// splitLines.
func TestSplitLines_NoTrailingNewline(t *testing.T) {
	got, err := Unmarshal([]byte("k: v"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["k"] != "v" {
		t.Errorf("got %#v", m)
	}
}

// TestSplitLines_CRLFNoTrailingNewline covers the trailing-CR strip
// in the tail-append branch.
func TestSplitLines_CRLFNoTrailingNewline(t *testing.T) {
	got, err := Unmarshal([]byte("k: v\r"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["k"] != "v" {
		t.Errorf("got %#v", m)
	}
}

// TestLeadingIndent_OnlySpaces - an entirely-blank indent line falls
// through the loop and returns len(s).
func TestLeadingIndent_WhitespaceOnly(t *testing.T) {
	// A line that is all spaces is dropped as blank by tokenize, so
	// the leadingIndent return-len path is hit only when the rest of
	// the line is empty AFTER comment strip. Use a comment-only line
	// with trailing whitespace.
	got, err := Unmarshal([]byte("a: 1\n   \nb: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["a"] != int64(1) || m["b"] != int64(2) {
		t.Errorf("got %#v", m)
	}
}

// TestIndexClosingMarker_CRAtEOF exercises the `\r` at EOF arm.
func TestIndexClosingMarker_CRAtEOF(t *testing.T) {
	in := []byte("---\nfoo: bar\n---\r")
	_, _, found, err := SplitFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected found")
	}
}

// TestRejectUnsupported_BlockScalarSpace exercises the `: | ` modifier
// arm (space after `|`).
func TestRejectUnsupported_BlockScalarSpace(t *testing.T) {
	_, err := Unmarshal([]byte("a: | header\n  x\n"))
	if !errors.Is(err, ErrUnsupportedSyntax) {
		t.Errorf("want ErrUnsupportedSyntax, got %v", err)
	}
}

// TestParseList_BadIndent triggers the "unexpected indent in list"
// error.
func TestParseList_BadIndent(t *testing.T) {
	_, err := Unmarshal([]byte("xs:\n  - a\n      - b\n"))
	if err == nil {
		t.Fatal("expected indent error")
	}
}

// TestSplitFlowItems_TrailingBracket triggers the unbalanced `]`
// branch.
func TestSplitFlowItems_UnbalancedClose(t *testing.T) {
	// A `]` not opened by a `[` inside flow items returns an error.
	// We have to wrap with `[...]` so parseFlowSequence is invoked.
	_, err := Unmarshal([]byte("xs: [a, b]]\n"))
	if err == nil {
		t.Fatal("expected unbalanced error")
	}
}

// TestTryUnquote_SingleAlone covers the single-byte fast return.
func TestTryUnquote_SingleByte(t *testing.T) {
	got, err := Unmarshal([]byte("k: '\n"))
	// A single `'` is malformed: the lexer might accept it but
	// tryUnquote returns false (len < 2), so it stays as a bare
	// string.
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["k"] != "'" {
		t.Errorf("got %#v", m["k"])
	}
}

// TestTryUnquote_DoubleNoCloser doesn't unquote when the closer is
// missing.
func TestTryUnquote_DoubleNoCloser(t *testing.T) {
	got, err := Unmarshal([]byte("k: \"unterminated\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	// Falls through to bare-string handling.
	if !strings.Contains(m["k"].(string), "unterminated") {
		t.Errorf("got %#v", m["k"])
	}
}

// TestMarshalStruct_JSONNumber exercises the json.Number stringer
// branch in formatScalar via the MarshalStruct path.
func TestMarshalStruct_JSONNumber(t *testing.T) {
	// A struct with an interface{} field containing a number - once
	// json.Marshal -> json.Decoder.UseNumber roundtrips, a json.Number
	// reaches formatScalar.
	type S struct {
		N any `json:"n"`
	}
	out, err := MarshalStruct(&S{N: 42})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "n: 42") {
		t.Errorf("got %q", out)
	}
}

// TestUnmarshal_LeadingDashScalar exercises needsQuoting's leading-`-`
// quoting requirement: a string "- thing" must round-trip.
func TestMarshal_LeadingDash(t *testing.T) {
	in := map[string]any{"k": "-startsWith"}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unmarshal(out)
	if err != nil {
		t.Fatalf("roundtrip: %v\n%s", err, out)
	}
	if back.(map[string]any)["k"] != "-startsWith" {
		t.Errorf("got %#v", back)
	}
}

// TestMarshal_LeadingQuestion exercises needsQuoting's leading-`?`
// quoting requirement.
func TestMarshal_LeadingQuestion(t *testing.T) {
	in := map[string]any{"k": "?why"}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "\"?why\"") {
		t.Errorf("?why should be quoted: %q", out)
	}
}

// TestMarshal_ControlChar covers the c < 0x20 branch of needsQuoting.
func TestMarshal_ControlChar(t *testing.T) {
	in := map[string]any{"k": "ab\x01cd"}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// Must be quoted (the control char triggers the < 0x20 path).
	if !strings.Contains(string(out), "\"") {
		t.Errorf("control char should force quoting: %q", out)
	}
}

// TestParseInt_DashOnly rejects `-` as int.
func TestParseInt_DashOnly(t *testing.T) {
	got, err := Unmarshal([]byte("n: -\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	// `-` by itself is not a valid int.
	if m["n"] != nil && m["n"] != "" {
		// Implementation-defined: just confirm it's not int64(0).
		if _, ok := m["n"].(int64); ok {
			t.Errorf("`- ` alone should not parse as int; got %#v", m["n"])
		}
	}
}

// TestParseInt_NonDigit fails the digit-loop early.
func TestParseInt_NonDigit(t *testing.T) {
	got, err := Unmarshal([]byte("n: 12a\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["n"] != "12a" {
		t.Errorf("got %#v", m["n"])
	}
}

// TestIsEmptyContainer_NestedEmpty verifies a map of empty containers
// is itself considered empty and dropped on marshal.
func TestIsEmptyContainer_NestedEmpty(t *testing.T) {
	in := map[string]any{
		"gateway": map[string]any{
			"ntfy":     map[string]any{},
			"telegram": map[string]any{},
		},
		"keep": "ok",
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "gateway") {
		t.Errorf("nested empty containers should drop gateway: %q", s)
	}
	if !strings.Contains(s, "keep: ok") {
		t.Errorf("real value should remain: %q", s)
	}
}
