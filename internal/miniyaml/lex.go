package miniyaml

import (
	"bytes"
	"fmt"
	"strings"
)

// line is one logical YAML line after comment stripping.  Blank lines
// (and lines that were entirely a comment) are dropped during
// tokenization so the parser never has to think about them.
type line struct {
	num    int    // 1-indexed source line number, for error messages
	indent int    // count of leading ASCII spaces (tabs are rejected)
	body   string // content with comment + trailing whitespace stripped
}

// tokenize splits raw YAML into [line] records.  It strips comments,
// blank lines, and document separators (a bare `---` or `...`).  The
// resulting slice feeds straight into the structural parser.
//
// Tab indentation is rejected.  YAML forbids tabs for indentation and
// allowing them silently would create round-trip drift the moment a
// user paste-edited a frontmatter block.
func tokenize(data []byte) ([]line, error) {
	// Tolerate a leading UTF-8 BOM.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	var out []line
	srcLines := splitLines(data)
	for i, raw := range srcLines {
		num := i + 1
		stripped, err := stripComment(raw)
		if err != nil {
			return nil, fmt.Errorf("miniyaml: line %d: %w", num, err)
		}
		// Drop trailing whitespace; preserve leading.
		stripped = strings.TrimRight(stripped, " \t\r")

		// Blank → skip entirely.
		if strings.TrimSpace(stripped) == "" {
			continue
		}

		// Document separator inside a stream.  We tolerate it but treat
		// it as a hard error for now because we only support
		// single-document streams.  (The frontmatter helper consumes
		// `---` markers BEFORE handing the body to [Unmarshal], so a
		// `---` here means a multi-doc stream we can't handle.)
		trimmed := strings.TrimSpace(stripped)
		if trimmed == "---" || trimmed == "..." {
			return nil, fmt.Errorf("miniyaml: line %d: multi-document streams: %w", num, ErrUnsupportedSyntax)
		}

		indent, err := leadingIndent(stripped, num)
		if err != nil {
			return nil, err
		}

		body := stripped[indent:]
		if err := rejectUnsupported(body, num); err != nil {
			return nil, err
		}

		out = append(out, line{num: num, indent: indent, body: body})
	}
	return out, nil
}

// splitLines splits on `\n` and trims a trailing `\r` per line.  Unlike
// strings.Split, the final empty record (when data ends in `\n`) is
// dropped because tokenize would skip it anyway and we save an
// allocation.
func splitLines(data []byte) []string {
	out := make([]string, 0, bytes.Count(data, []byte{'\n'})+1)
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			end := i
			if end > start && data[end-1] == '\r' {
				end--
			}
			out = append(out, string(data[start:end]))
			start = i + 1
		}
	}
	if start < len(data) {
		tail := data[start:]
		if len(tail) > 0 && tail[len(tail)-1] == '\r' {
			tail = tail[:len(tail)-1]
		}
		out = append(out, string(tail))
	}
	return out
}

// leadingIndent returns the count of leading ASCII spaces.  Tabs in the
// indent are rejected because YAML forbids them and silently accepting
// them would create round-trip drift.
func leadingIndent(s string, num int) (int, error) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ':
			continue
		case '\t':
			return 0, fmt.Errorf("miniyaml: line %d: tab in indentation: %w", num, ErrUnsupportedSyntax)
		default:
			return i, nil
		}
	}
	return len(s), nil
}

// stripComment removes a `#` comment from a line, honoring quoted
// strings (a `#` inside `"..."` or `'...'` is content, not a comment).
// YAML also requires a space before `#` to start a comment unless the
// `#` is at the very start of (the unindented portion of) the line; we
// honor that to keep `value#tag` style identifiers intact.
func stripComment(raw string) (string, error) {
	var b strings.Builder
	b.Grow(len(raw))
	inDouble := false
	inSingle := false
	prev := byte(' ') // pretend prior char is space so leading `#` is a comment
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"' && !inSingle:
			// Toggle, honoring `\"` inside double-quoted strings.
			if inDouble && prev == '\\' {
				// escaped quote inside double-quoted string — content
			} else {
				inDouble = !inDouble
			}
		case c == '\'' && !inDouble:
			// In single-quoted strings, `''` is the escape for a single
			// quote.  We toggle but the next iteration will toggle back
			// if the very next char is also `'`.
			inSingle = !inSingle
		case c == '#' && !inDouble && !inSingle:
			// Comment start requires preceding whitespace OR start of
			// the (unindented) line.
			if prev == ' ' || prev == '\t' || i == 0 {
				return b.String(), nil
			}
		}
		b.WriteByte(c)
		prev = c
	}
	return b.String(), nil
}

// rejectUnsupported scans a line body for tokens that belong to YAML
// constructs we explicitly don't handle and returns an error pointing
// to the line number when one is found.  We are conservative: only
// patterns that unambiguously identify the unsupported construct
// trigger here.
func rejectUnsupported(body string, num int) error {
	// Anchors: `&name` as a value or after `key: `.
	if hasFreeStandingMarker(body, '&') {
		return fmt.Errorf("miniyaml: line %d: anchors unsupported: %w", num, ErrUnsupportedSyntax)
	}
	// Aliases: `*name` as a value or after `key: `.
	if hasFreeStandingMarker(body, '*') {
		return fmt.Errorf("miniyaml: line %d: aliases unsupported: %w", num, ErrUnsupportedSyntax)
	}
	// Merge keys: `<<:` somewhere in the line.
	if strings.Contains(body, "<<:") || strings.HasPrefix(strings.TrimSpace(body), "<<") {
		return fmt.Errorf("miniyaml: line %d: merge keys unsupported: %w", num, ErrUnsupportedSyntax)
	}
	// Type tags: `!!something`.
	if strings.Contains(body, "!!") {
		return fmt.Errorf("miniyaml: line %d: type tags unsupported: %w", num, ErrUnsupportedSyntax)
	}
	// Flow-style maps: `{...}`.  Flow-style sequences `[...]` are
	// supported (scalar-only) and validated at value-parse time.
	if hasFlowMap(body) {
		return fmt.Errorf("miniyaml: line %d: flow-style maps unsupported: %w", num, ErrUnsupportedSyntax)
	}
	// Block scalar indicators `|` and `>` immediately after a colon.
	if hasBlockScalarIndicator(body) {
		return fmt.Errorf("miniyaml: line %d: block scalars (| / >) unsupported: %w", num, ErrUnsupportedSyntax)
	}
	// Complex keys: `? key`.
	if strings.HasPrefix(strings.TrimSpace(body), "? ") {
		return fmt.Errorf("miniyaml: line %d: complex keys unsupported: %w", num, ErrUnsupportedSyntax)
	}
	return nil
}

// hasFreeStandingMarker reports whether `body` contains `marker` (e.g.
// '&' or '*') in a position that would be a YAML construct — preceded
// by start-of-string, whitespace, or `:`/`,`/`-`, followed by an
// identifier character.  Inside quoted strings the marker is content
// and we ignore it.
func hasFreeStandingMarker(body string, marker byte) bool {
	inDouble := false
	inSingle := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == marker && !inDouble && !inSingle:
			// Preceded by start, whitespace, `:`, `,`, or `-`?
			prev := byte(' ')
			if i > 0 {
				prev = body[i-1]
			}
			if i > 0 && prev != ' ' && prev != '\t' && prev != ':' && prev != ',' && prev != '-' && prev != '[' && prev != '{' {
				continue
			}
			// Followed by identifier char?
			if i+1 < len(body) {
				n := body[i+1]
				if (n >= 'a' && n <= 'z') || (n >= 'A' && n <= 'Z') || n == '_' {
					return true
				}
			}
		}
	}
	return false
}

// hasFlowMap reports whether body contains a non-empty `{` outside
// quoted strings.  Flow maps with any content are rare in practice and
// rejected; the bare empty-map literal `{}` is tolerated because it's
// the natural value for a zero-valued struct emitted by other YAML
// libraries (and we round-trip cleanly through it).
func hasFlowMap(body string) bool {
	inDouble := false
	inSingle := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '{' && !inDouble && !inSingle:
			// Allow bare `{}`: scan past whitespace looking for `}`.
			j := i + 1
			for j < len(body) && (body[j] == ' ' || body[j] == '\t') {
				j++
			}
			if j < len(body) && body[j] == '}' {
				continue
			}
			return true
		}
	}
	return false
}

// hasBlockScalarIndicator looks for `: |` or `: >` (with optional
// chomping/indent modifiers after) which is the syntax that opens a
// block scalar.  We forbid those because supporting them would
// significantly complicate the lexer.
func hasBlockScalarIndicator(body string) bool {
	// Find the first colon outside quoted strings, then look at the
	// next non-whitespace char.
	inDouble := false
	inSingle := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == ':' && !inDouble && !inSingle:
			// Look past whitespace.
			j := i + 1
			for j < len(body) && (body[j] == ' ' || body[j] == '\t') {
				j++
			}
			if j >= len(body) {
				return false
			}
			n := body[j]
			if n == '|' || n == '>' {
				// Make sure what follows is end-of-line or a modifier
				// (`+`, `-`, digit) — i.e. a true block scalar, not
				// the `>` of a quoted string.
				if j+1 >= len(body) {
					return true
				}
				m := body[j+1]
				if m == '+' || m == '-' || (m >= '0' && m <= '9') {
					return true
				}
				if m == ' ' || m == '\t' {
					return true
				}
			}
			return false
		}
	}
	return false
}
