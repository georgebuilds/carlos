package mcp

import (
	"reflect"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseArgs_AcceptsValidObject(t *testing.T) {
	cases := map[string]struct {
		in   string
		want map[string]any
	}{
		"empty input":            {"", map[string]any{}},
		"whitespace only":        {"   \n\t", map[string]any{}},
		"explicit null":          {"null", map[string]any{}},
		"empty object":           {"{}", map[string]any{}},
		"object with whitespace": {"  { \"k\": 1 } ", map[string]any{"k": float64(1)}},
		"object with strings":    {`{"a":"b","c":"d"}`, map[string]any{"a": "b", "c": "d"}},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			got, err := parseArgs([]byte(tc.in))
			if err != nil {
				t.Fatalf("parseArgs(%q) returned error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseArgs(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseArgs_RejectsNonObject(t *testing.T) {
	cases := map[string]struct {
		in       string
		wantKind string
	}{
		"array":         {`["a","b"]`, "array"},
		"empty array":   {`[]`, "array"},
		"number":        {`42`, "number"},
		"negative":      {`-1.5`, "number"},
		"string":        {`"hello"`, "string"},
		"bool true":     {`true`, "bool"},
		"bool false":    {`false`, "bool"},
		"padded array":  {"  [1,2]  ", "array"},
		"padded string": {"  \"x\"  ", "string"},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			got, err := parseArgs([]byte(tc.in))
			if err == nil {
				t.Fatalf("parseArgs(%q) = %#v, want error", tc.in, got)
			}
			if got != nil {
				t.Fatalf("parseArgs(%q) returned map %#v on error; want nil", tc.in, got)
			}
			msg := err.Error()
			if !strings.Contains(msg, "expected JSON object") {
				t.Fatalf("parseArgs(%q) error %q missing 'expected JSON object'", tc.in, msg)
			}
			if !strings.Contains(msg, tc.wantKind) {
				t.Fatalf("parseArgs(%q) error %q missing kind label %q", tc.in, msg, tc.wantKind)
			}
		})
	}
}

func TestParseArgs_PropagatesMalformedJSON(t *testing.T) {
	// Shape-valid (starts with '{') but otherwise malformed input should
	// still surface a JSON parse error rather than be silently dropped.
	_, err := parseArgs([]byte(`{not json`))
	if err == nil {
		t.Fatal("parseArgs of malformed object returned nil error")
	}
}

func TestJoinContent_SkipsNilBlock(t *testing.T) {
	valid := func(s string) *sdk.TextContent { return &sdk.TextContent{Text: s} }

	t.Run("interleaved nils match clean slice", func(t *testing.T) {
		withNils := []sdk.Content{nil, valid("hello"), nil, valid("world"), nil}
		clean := []sdk.Content{valid("hello"), valid("world")}
		gotNils := joinContent(withNils)
		gotClean := joinContent(clean)
		if gotNils != gotClean {
			t.Fatalf("nil-interleaved=%q, clean=%q; want equal", gotNils, gotClean)
		}
		if gotNils != "hello\nworld" {
			t.Fatalf("joinContent = %q, want %q", gotNils, "hello\nworld")
		}
	})

	t.Run("leading nil does not emit leading newline", func(t *testing.T) {
		got := joinContent([]sdk.Content{nil, valid("a")})
		if got != "a" {
			t.Fatalf("joinContent leading-nil = %q, want %q", got, "a")
		}
	})

	t.Run("trailing nil does not emit trailing newline", func(t *testing.T) {
		got := joinContent([]sdk.Content{valid("a"), nil})
		if got != "a" {
			t.Fatalf("joinContent trailing-nil = %q, want %q", got, "a")
		}
	})

	t.Run("all nils returns empty string without panic", func(t *testing.T) {
		// Run inside a closure so a panic here fails the subtest rather
		// than aborting the whole binary.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("joinContent panicked on all-nil slice: %v", r)
			}
		}()
		got := joinContent([]sdk.Content{nil, nil, nil})
		if got != "" {
			t.Fatalf("joinContent all-nil = %q, want empty", got)
		}
	})

	t.Run("nil-only does not panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("joinContent panicked on single nil: %v", r)
			}
		}()
		_ = joinContent([]sdk.Content{nil})
	})
}

func TestJoinContent_HappyPathUnchanged(t *testing.T) {
	// Guard against accidental regressions in the non-nil path while we
	// were patching the nil guard in.
	in := []sdk.Content{
		&sdk.TextContent{Text: "first"},
		&sdk.TextContent{Text: "second"},
	}
	got := joinContent(in)
	if got != "first\nsecond" {
		t.Fatalf("joinContent = %q, want %q", got, "first\nsecond")
	}
}
