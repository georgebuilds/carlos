package miniyaml

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseScalar decodes a YAML scalar token into the canonical Go type:
//   - "" / "null" / "~"        → nil
//   - "true"/"false"/etc       → bool
//   - integer literal          → int64
//   - float literal (has '.')  → float64
//   - quoted string            → string (with escapes processed)
//   - bare anything else       → string
//
// lineNum is used only for error messages.
func parseScalar(raw string, lineNum int) (any, error) {
	s := strings.TrimSpace(raw)
	// Quoted strings short-circuit type inference.
	if u, ok := tryUnquote(s); ok {
		return u, nil
	}
	if s == "" || s == "null" || s == "Null" || s == "NULL" || s == "~" {
		return nil, nil
	}
	switch strings.ToLower(s) {
	case "true", "yes", "on":
		return true, nil
	case "false", "no", "off":
		return false, nil
	}
	// Integer? Negative sign tolerated; underscores are NOT supported.
	if i, ok := parseInt(s); ok {
		return i, nil
	}
	// Float?  Require a `.` to avoid swallowing ints that just happen
	// to look numeric — yaml.v3 distinguishes the two and our struct
	// roundtrip relies on it.
	if strings.ContainsAny(s, ".eE") {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, nil
		}
	}
	// Bare string — return verbatim.  We do NOT validate further: YAML
	// allows almost anything as a bare scalar so long as it doesn't
	// start with a reserved indicator (`!`, `&`, `*`, `?`, `:`, `-`
	// followed by space, etc.).  Those reserved cases were filtered by
	// the lexer's rejectUnsupported pass.
	_ = lineNum
	return s, nil
}

// parseInt is a thin wrapper over strconv.ParseInt that rejects values
// with leading `+` (YAML doesn't allow them on bare scalars without
// shenanigans we don't need to support).
func parseInt(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	// Allow a leading `-`.  Reject `+`.
	if s[0] == '+' {
		return 0, false
	}
	// Reject leading zero on multi-digit integers — yaml.v3 treats
	// `0777` as octal but our consumers never feed us octals; keeping
	// the bare leading-zero case as a string preserves IDs like
	// `0123`.
	body := s
	if body[0] == '-' {
		body = body[1:]
	}
	if len(body) > 1 && body[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// tryUnquote reports whether s is a quoted string and, if so, returns
// the unquoted value.  We support both `"..."` (with backslash escapes)
// and `'...'` (with `''` as the escape for a literal apostrophe).
func tryUnquote(s string) (string, bool) {
	if len(s) < 2 {
		return "", false
	}
	switch s[0] {
	case '"':
		if s[len(s)-1] != '"' {
			return "", false
		}
		return unquoteDouble(s[1 : len(s)-1]), true
	case '\'':
		if s[len(s)-1] != '\'' {
			return "", false
		}
		return unquoteSingle(s[1 : len(s)-1]), true
	}
	return "", false
}

// unquoteDouble handles the YAML double-quote escape set we care about:
// \" \\ \n \r \t \0 \/ — covers everything carlos's files (or the JSON
// roundtrip path) emits.  Anything else stays verbatim.
func unquoteDouble(inner string) string {
	var b strings.Builder
	b.Grow(len(inner))
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) {
			switch inner[i+1] {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '/':
				b.WriteByte('/')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '0':
				b.WriteByte(0)
			default:
				// Unknown escape — preserve verbatim.
				b.WriteByte(inner[i])
				b.WriteByte(inner[i+1])
			}
			i++
			continue
		}
		b.WriteByte(inner[i])
	}
	return b.String()
}

// unquoteSingle handles YAML single-quoted strings: `''` is the only
// escape and represents one literal `'`.
func unquoteSingle(inner string) string {
	if !strings.Contains(inner, "''") {
		return inner
	}
	return strings.ReplaceAll(inner, "''", "'")
}

// parseFlowSequence parses an inline `[a, b, "c, d", 5]` and returns the
// element list.  Only scalar elements are supported (no nested flow
// sequences, no inline maps).
func parseFlowSequence(s string, lineNum int) ([]any, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") {
		return nil, fmt.Errorf("miniyaml: line %d: flow sequence must start with `[`", lineNum)
	}
	if !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("miniyaml: line %d: unterminated flow sequence", lineNum)
	}
	inner := s[1 : len(s)-1]
	if strings.TrimSpace(inner) == "" {
		return []any{}, nil
	}
	parts, err := splitFlowItems(inner, lineNum)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		v, err := parseScalar(p, lineNum)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// splitFlowItems splits `a, b, "c, d", 5` on commas that aren't inside
// quoted strings.  Nested `[` or `{` is rejected.
func splitFlowItems(s string, lineNum int) ([]string, error) {
	var out []string
	inDouble := false
	inSingle := false
	depth := 0
	last := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case (c == '[' || c == '{') && !inDouble && !inSingle:
			if c == '{' {
				return nil, fmt.Errorf("miniyaml: line %d: flow-style maps unsupported: %w", lineNum, ErrUnsupportedSyntax)
			}
			depth++
			return nil, fmt.Errorf("miniyaml: line %d: nested flow sequences unsupported: %w", lineNum, ErrUnsupportedSyntax)
		case c == ']' && !inDouble && !inSingle:
			if depth == 0 {
				return nil, fmt.Errorf("miniyaml: line %d: unbalanced `]` in flow sequence", lineNum)
			}
			depth--
		case c == ',' && !inDouble && !inSingle && depth == 0:
			out = append(out, strings.TrimSpace(s[last:i]))
			last = i + 1
		}
	}
	out = append(out, strings.TrimSpace(s[last:]))
	return out, nil
}

// formatScalar renders a Go scalar as its YAML representation for use
// in [Marshal].  Quoting is applied conservatively: any value that
// would round-trip ambiguously (looks numeric / boolean / contains
// special chars) is double-quoted with the standard escapes.
func formatScalar(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "null", nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case string:
		return quoteIfNeeded(x), nil
	case int:
		return strconv.FormatInt(int64(x), 10), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float32:
		return formatFloat(float64(x)), nil
	case float64:
		return formatFloat(x), nil
	case time.Time:
		// RFC3339 with nanoseconds where present; matches yaml.v3 +
		// json behavior closely enough for our round-trip needs.
		return quoteIfNeeded(x.Format(time.RFC3339Nano)), nil
	}
	// json.Number is a string under the hood; the encoding/json
	// roundtrip in MarshalStruct yields these for numbers.
	type stringer interface{ String() string }
	if s, ok := v.(stringer); ok {
		// json.Number: print verbatim if it parses as a number.
		raw := s.String()
		if _, err := strconv.ParseFloat(raw, 64); err == nil {
			return raw, nil
		}
		return quoteIfNeeded(raw), nil
	}
	return "", fmt.Errorf("miniyaml: cannot marshal scalar of type %T", v)
}

// formatFloat renders a float with the shortest representation that
// still round-trips, preferring the integer form when the value is a
// whole number.
func formatFloat(f float64) string {
	// Use 'g' for compact output; ensure a `.` is present so the
	// scalar parser knows it's a float and not an int.
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// quoteIfNeeded wraps a string in double quotes when leaving it bare
// would change its meaning on parse-back (looks like a bool / number /
// null, has leading-or-trailing space, contains special characters).
func quoteIfNeeded(s string) string {
	if needsQuoting(s) {
		return quoteDouble(s)
	}
	return s
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	// Trim test: leading/trailing space → quote.
	if s != strings.TrimSpace(s) {
		return true
	}
	// Reserved bare scalar values.
	switch strings.ToLower(s) {
	case "null", "~", "true", "false", "yes", "no", "on", "off":
		return true
	}
	// Looks like a number?
	if _, ok := parseInt(s); ok {
		return true
	}
	if strings.ContainsAny(s, ".eE") {
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			return true
		}
	}
	// Special chars that the parser would interpret.
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ':', '#', '"', '\'', '\n', '\r', '\t', '\\':
			return true
		case '[', ']', '{', '}', ',', '&', '*', '!', '|', '>', '%', '@', '`':
			return true
		}
		// Control chars (< 0x20 other than tab — tab already handled).
		if c < 0x20 {
			return true
		}
	}
	// Leading dash, question, ampersand, etc.
	switch s[0] {
	case '-', '?':
		return true
	}
	return false
}

// quoteDouble wraps s in `"..."` with backslash escapes for the
// characters that would otherwise break the line.  Keeps the output
// readable: only " \ \n \r \t \0 are escaped.
func quoteDouble(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case 0:
			b.WriteString(`\0`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
