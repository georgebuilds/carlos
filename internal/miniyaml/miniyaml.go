// Package miniyaml is a tiny YAML subset parser tailored to carlos's
// actual usage.  It exists to replace gopkg.in/yaml.v3 (direct) and
// gopkg.in/yaml.v2 (transitive via goldmark-meta) and drop ~1.2 MB
// from the stripped binary while staying pure-Go.
//
// # Supported subset
//
//   - Block-style maps: `key: value`, arbitrary nesting (2-space convention,
//     but any consistent indent works as long as children are deeper than
//     parents).
//   - Block-style lists: `- item`, including `- key: value` (lists of maps).
//   - Scalars: bare strings, double-quoted (with the common escapes), single-
//     quoted (with `''` for embedded apostrophes), ints (int64), floats
//     (float64, must contain a `.`), bools (`true`/`false`/`yes`/`no`,
//     case-insensitive), nulls (`null` / `~` / empty value).
//   - Flow-style sequences of scalars: `[a, b, c]` on a single line. Flow-
//     style maps are NOT supported.
//   - Comments: `# ...` to end of line, except inside quoted strings.
//   - Frontmatter detection via [SplitFrontmatter]: a leading `---\n` opener
//     and a `---\n` closer.
//
// # Unsupported syntax (returns [ErrUnsupportedSyntax])
//
//   - Anchors (`&anchor`) and aliases (`*anchor`)
//   - Merge keys (`<<: *base`)
//   - Type tags (`!!str`, `!!int`, ...)
//   - Multi-document streams beyond the frontmatter pattern
//   - Flow-style maps (`{a: 1, b: 2}`)
//   - Block scalars (`|` literal, `>` folded)
//   - Complex keys (`?` style)
//
// # Public API
//
//   - [Unmarshal] / [Marshal] - generic Go value tree.
//   - [UnmarshalStruct] / [MarshalStruct] - struct mapping via JSON
//     roundtrip; callers tag with `json:"..."`, not `yaml:"..."`. This
//     dodges adding reflection to the parser itself.
//   - [SplitFrontmatter] - peels a YAML frontmatter block from the start
//     of a byte slice (used by notes + skills).
//
// The parser produces deterministic output: maps are sorted alphabetically
// by key on [Marshal] so saved files diff predictably.
package miniyaml

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrUnsupportedSyntax is returned (typically wrapped with a line number)
// when the parser hits a YAML construct outside carlos's supported
// subset.  Callers can use [errors.Is] to detect this.
var ErrUnsupportedSyntax = errors.New("miniyaml: unsupported YAML syntax")

// Unmarshal parses YAML bytes into a generic Go value tree.  Maps decode
// to map[string]any, lists to []any, scalars to one of string, int64,
// float64, bool, or nil.  Empty input returns (nil, nil).
func Unmarshal(data []byte) (any, error) {
	lines, err := tokenize(data)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}
	p := &parser{lines: lines}
	v, err := p.parseValue(-1)
	if err != nil {
		return nil, err
	}
	if !p.eof() {
		return nil, fmt.Errorf("miniyaml: unexpected content at line %d", p.peek().num)
	}
	return v, nil
}

// UnmarshalStruct parses YAML and decodes into out via a JSON roundtrip.
// The intermediate tree is rendered to JSON and fed back through
// encoding/json so struct tags use `json:"..."` (NOT `yaml:"..."`).
// Callers convert their tags accordingly.
func UnmarshalStruct(data []byte, out any) error {
	v, err := Unmarshal(data)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	j, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("miniyaml: intermediate json encode: %w", err)
	}
	if err := json.Unmarshal(j, out); err != nil {
		return fmt.Errorf("miniyaml: decode into struct: %w", err)
	}
	return nil
}

// Marshal renders a value tree to YAML bytes.  Maps emit keys in
// alphabetical order so two calls with semantically-equal inputs
// produce byte-identical output.  Symmetric with [Unmarshal] modulo
// the key-sort: Marshal(Unmarshal(x)) parses back to the same tree.
//
// Accepted scalar types: string, bool, nil, int / int32 / int64, uint /
// uint32 / uint64, float32, float64, json.Number.  Maps may be
// map[string]any or map[string]string; lists may be []any or []string.
func Marshal(v any) ([]byte, error) {
	var buf []byte
	buf, err := appendValue(buf, v, 0, false)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// MarshalStruct is the symmetric counterpart of [UnmarshalStruct]:
// encode through JSON to honor `json:"..."` tags (omitempty included),
// then run the resulting tree through [Marshal].
func MarshalStruct(v any) ([]byte, error) {
	j, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("miniyaml: intermediate json encode: %w", err)
	}
	var tree any
	dec := json.NewDecoder(bytesReader(j))
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("miniyaml: intermediate json decode: %w", err)
	}
	return Marshal(tree)
}
