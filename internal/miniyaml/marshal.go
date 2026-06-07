package miniyaml

import (
	"bytes"
	"fmt"
	"sort"
)

// appendValue renders v into buf at the given indent column.
// `topLevel` distinguishes the root call (where leading newlines and
// `key:` framing don't apply) from recursive calls.  The convention
// across the marshal path:
//
//   - Maps emit `key: value\n` per entry, scalars inline, nested
//     containers prefixed with `\n` and indented two more spaces.
//   - Lists emit `- item\n` per entry, with nested containers either
//     inline-after-the-dash (for the first key of a list-of-maps) or
//     on subsequent lines indented by 2.
//   - Empty maps render as `{}`, empty lists as `[]` - only used when
//     a container is the *value* of a key; at top level an empty
//     container renders as `{}\n` / `[]\n`.
//
// Errors propagate up from unsupported scalar types.
func appendValue(buf []byte, v any, indent int, _inList bool) ([]byte, error) {
	switch x := v.(type) {
	case map[string]any:
		return appendMap(buf, x, indent)
	case []any:
		return appendList(buf, x, indent)
	case []string:
		// Promote []string to []any so we share one path.
		conv := make([]any, len(x))
		for i, s := range x {
			conv[i] = s
		}
		return appendList(buf, conv, indent)
	case map[string]string:
		conv := make(map[string]any, len(x))
		for k, val := range x {
			conv[k] = val
		}
		return appendMap(buf, conv, indent)
	default:
		s, err := formatScalar(v)
		if err != nil {
			return nil, err
		}
		buf = append(buf, s...)
		buf = append(buf, '\n')
		return buf, nil
	}
}

// appendMap emits a block-style mapping.  Keys are sorted alphabetically
// for deterministic output.  Empty top-level maps render as `{}\n`;
// empty sub-maps are dropped entirely (treated as effectively-omitempty
// at the YAML layer).  This dovetails with json's omitempty quirk on
// non-pointer struct fields: a zero-valued struct can't be tagged
// omitempty in encoding/json, but we still don't want a stray
// `theme: {}` line in the user-facing config.
func appendMap(buf []byte, m map[string]any, indent int) ([]byte, error) {
	if len(m) == 0 {
		buf = append(buf, "{}\n"...)
		return buf, nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		val := m[k]
		// Drop empty sub-containers: see appendMap doc above.
		if isEmptyContainer(val) {
			continue
		}
		buf = appendIndent(buf, indent)
		buf = append(buf, formatKey(k)...)
		buf = append(buf, ':')
		switch x := val.(type) {
		case map[string]any:
			buf = append(buf, '\n')
			nested, err := appendMap(nil, x, indent+2)
			if err != nil {
				return nil, err
			}
			buf = append(buf, nested...)
		case map[string]string:
			conv := make(map[string]any, len(x))
			for kk, vv := range x {
				conv[kk] = vv
			}
			buf = append(buf, '\n')
			nested, err := appendMap(nil, conv, indent+2)
			if err != nil {
				return nil, err
			}
			buf = append(buf, nested...)
		case []any:
			buf = append(buf, '\n')
			nested, err := appendList(nil, x, indent+2)
			if err != nil {
				return nil, err
			}
			buf = append(buf, nested...)
		case []string:
			buf = append(buf, '\n')
			conv := make([]any, len(x))
			for i, s := range x {
				conv[i] = s
			}
			nested, err := appendList(nil, conv, indent+2)
			if err != nil {
				return nil, err
			}
			buf = append(buf, nested...)
		default:
			s, err := formatScalar(val)
			if err != nil {
				return nil, fmt.Errorf("miniyaml: key %q: %w", k, err)
			}
			buf = append(buf, ' ')
			buf = append(buf, s...)
			buf = append(buf, '\n')
		}
	}
	return buf, nil
}

// isEmptyContainer reports whether v is an empty map or empty slice.
// Used by appendMap to silently drop empty sub-containers so that
// json-roundtripped omitempty-equivalent behavior holds even for
// non-pointer struct fields (which encoding/json can't omit).
//
// "Empty" is recursive for maps: a map whose every entry is itself an
// empty container (e.g. an outer `gateway: {ntfy: {}, telegram: {}}`
// produced by serializing a zero-value struct with all-struct fields)
// is treated as empty so the outer key is dropped. Slices are not
// recursed because a slice with empty elements is still meaningful
// data (lists of maps record the number of items).
func isEmptyContainer(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			return true
		}
		for _, val := range x {
			if !isEmptyContainer(val) {
				return false
			}
		}
		return true
	case map[string]string:
		return len(x) == 0
	case []any:
		return len(x) == 0
	case []string:
		return len(x) == 0
	}
	return false
}

// appendList emits a block-style sequence at the given column.  Each
// item is prefixed with `- `; nested maps go inline-after-the-dash so
// the first key sits on the same line (idiomatic YAML).
func appendList(buf []byte, xs []any, indent int) ([]byte, error) {
	if len(xs) == 0 {
		buf = append(buf, "[]\n"...)
		return buf, nil
	}
	for _, x := range xs {
		buf = appendIndent(buf, indent)
		buf = append(buf, "- "...)
		switch v := x.(type) {
		case map[string]any:
			if len(v) == 0 {
				buf = append(buf, "{}\n"...)
				continue
			}
			// Render the map keys in sorted order; the first key sits
			// on the dash line, subsequent keys indented by 2 from the
			// dash column.
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			first := true
			for _, k := range keys {
				if first {
					// no extra indent - already on the dash line
					first = false
				} else {
					buf = appendIndent(buf, indent+2)
				}
				buf = append(buf, formatKey(k)...)
				buf = append(buf, ':')
				val := v[k]
				switch vv := val.(type) {
				case map[string]any:
					if len(vv) == 0 {
						buf = append(buf, " {}\n"...)
						continue
					}
					buf = append(buf, '\n')
					nested, err := appendMap(nil, vv, indent+4)
					if err != nil {
						return nil, err
					}
					buf = append(buf, nested...)
				case []any:
					if len(vv) == 0 {
						buf = append(buf, " []\n"...)
						continue
					}
					buf = append(buf, '\n')
					nested, err := appendList(nil, vv, indent+4)
					if err != nil {
						return nil, err
					}
					buf = append(buf, nested...)
				default:
					s, err := formatScalar(val)
					if err != nil {
						return nil, err
					}
					buf = append(buf, ' ')
					buf = append(buf, s...)
					buf = append(buf, '\n')
				}
			}
		case []any:
			// List of lists.  Drop to a new line and indent by 2.
			if len(v) == 0 {
				buf = append(buf, "[]\n"...)
				continue
			}
			buf = append(buf, '\n')
			nested, err := appendList(nil, v, indent+2)
			if err != nil {
				return nil, err
			}
			buf = append(buf, nested...)
		default:
			s, err := formatScalar(x)
			if err != nil {
				return nil, err
			}
			buf = append(buf, s...)
			buf = append(buf, '\n')
		}
	}
	return buf, nil
}

// appendIndent writes n spaces of indentation to buf.
func appendIndent(buf []byte, n int) []byte {
	for i := 0; i < n; i++ {
		buf = append(buf, ' ')
	}
	return buf
}

// formatKey emits a mapping key.  Quoting is applied with the same
// rules as scalar values so e.g. a key that looks like an integer
// (`"123"`) round-trips as a string, not a number.
func formatKey(k string) string {
	return quoteIfNeeded(k)
}

// bytesReader is a tiny helper so MarshalStruct can json-decode without
// pulling in bytes.NewReader in the public-facing file.
func bytesReader(b []byte) *bytes.Reader {
	return bytes.NewReader(b)
}
