package miniyaml

import (
	"fmt"
	"strings"
)

// parser holds the line cursor state during a parse.  The recursion is
// driven by indent levels: parseValue at column C consumes lines until
// it sees a line at column ≤ C.
type parser struct {
	lines []line
	pos   int
}

func (p *parser) eof() bool       { return p.pos >= len(p.lines) }
func (p *parser) peek() line      { return p.lines[p.pos] }
func (p *parser) advance()        { p.pos++ }
func (p *parser) hasMore() bool   { return p.pos < len(p.lines) }
func (p *parser) lookback() *line { // for error messages
	if p.pos == 0 {
		return nil
	}
	l := p.lines[p.pos-1]
	return &l
}

// parseValue parses a YAML value rooted at indent strictly greater than
// parentIndent.  Returns either a map[string]any, a []any, or a scalar
// (string, int64, float64, bool, nil).  Caller is responsible for the
// indent invariant; we panic-free check on the first line.
func (p *parser) parseValue(parentIndent int) (any, error) {
	if p.eof() {
		return nil, nil
	}
	first := p.peek()
	if first.indent <= parentIndent {
		// Nothing for us at this level - value is null.
		return nil, nil
	}
	indent := first.indent

	if isListItem(first.body) {
		return p.parseList(indent)
	}
	if hasMapKey(first.body) {
		return p.parseMap(indent)
	}
	// Single bare scalar line.  Consume it and return.
	p.advance()
	return parseScalar(first.body, first.num)
}

// parseMap consumes a block-style mapping at the given indent column.
// Stops as soon as a line drops below indent OR the line is a list item
// at exactly this indent (a list-of-maps' next sibling starts with `-`).
func (p *parser) parseMap(indent int) (map[string]any, error) {
	out := make(map[string]any)
	for !p.eof() {
		cur := p.peek()
		if cur.indent < indent {
			break
		}
		if cur.indent > indent {
			return nil, fmt.Errorf("miniyaml: line %d: unexpected indent (expected %d, got %d)", cur.num, indent, cur.indent)
		}
		// At exactly this indent - must be a mapping line.  A bare list
		// item at this indent terminates the map and belongs to the
		// caller (e.g. a list-of-maps where the items live at the same
		// indent as the parent key - common in carlos's `schedules:`
		// block).
		if isListItem(cur.body) {
			break
		}
		if !hasMapKey(cur.body) {
			return nil, fmt.Errorf("miniyaml: line %d: expected `key:` mapping, got %q", cur.num, cur.body)
		}
		key, rest, err := splitMapKey(cur.body, cur.num)
		if err != nil {
			return nil, err
		}
		p.advance()
		val, err := p.parseMapValue(rest, indent, cur.num)
		if err != nil {
			return nil, err
		}
		out[key] = val
	}
	return out, nil
}

// parseMapValue resolves the right-hand side of `key: <rest>`.  If rest
// is non-empty we have an inline scalar (or flow sequence).  Otherwise
// we recurse into the next deeper block.
//
// A common YAML idiom puts a list value at the SAME indent as its
// parent key:
//
//	schedules:
//	- name: a
//	  spec: "0 9 * * *"
//
// We detect that here by peeking at the next line: if it's a list item
// at exactly parentIndent, parse a list at that indent.  Otherwise we
// recurse into a deeper block.
func (p *parser) parseMapValue(rest string, parentIndent, lineNum int) (any, error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		// Same-indent list value?
		if !p.eof() {
			n := p.peek()
			if n.indent == parentIndent && isListItem(n.body) {
				return p.parseList(parentIndent)
			}
		}
		// Block-style nested value at deeper indent.
		return p.parseValue(parentIndent)
	}
	// Inline value on the same line.
	if strings.HasPrefix(rest, "[") {
		return parseFlowSequence(rest, lineNum)
	}
	// Tolerate empty flow map `{}` (e.g. `theme: {}`) - emit nil.
	if strings.TrimSpace(rest) == "{}" {
		return map[string]any{}, nil
	}
	return parseScalar(rest, lineNum)
}

// parseList consumes a block-style sequence at the given indent column.
// Each item starts with `-` (followed by space or end-of-line).
func (p *parser) parseList(indent int) ([]any, error) {
	var out []any
	for !p.eof() {
		cur := p.peek()
		if cur.indent < indent {
			break
		}
		if cur.indent > indent {
			return nil, fmt.Errorf("miniyaml: line %d: unexpected indent in list", cur.num)
		}
		if !isListItem(cur.body) {
			break
		}
		// Consume the `- ` prefix and parse what follows.
		item, err := p.parseListItem(cur, indent)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// parseListItem handles one `- ...` entry.  Three shapes:
//
//   - `-` alone → empty value; next non-blank deeper line is the body
//     (probably a nested map).
//   - `- scalar`        → inline scalar.
//   - `- key: value`    → inline first map entry; subsequent map entries
//     live at indent = (original indent + 2 chars for `- `).
func (p *parser) parseListItem(cur line, indent int) (any, error) {
	body := cur.body
	if body == "-" || body == "- " {
		// Empty inline; consume and recurse for the body at any deeper
		// indent.
		p.advance()
		return p.parseValue(indent)
	}
	if !strings.HasPrefix(body, "- ") {
		return nil, fmt.Errorf("miniyaml: line %d: malformed list item %q", cur.num, body)
	}
	rest := body[2:]
	if hasMapKey(rest) {
		// Inline first key.  Synthesize a line at indent+2 for it and
		// recurse into parseMap so subsequent same-level keys land in
		// the same map.
		key, kRest, err := splitMapKey(rest, cur.num)
		if err != nil {
			return nil, err
		}
		// Construct a synthetic line representing the inline `key:
		// rest` at indent+2.  Then advance past the original line and
		// patch it in.
		synth := line{num: cur.num, indent: indent + 2, body: rest}
		// Replace current line with synth (still at p.pos).
		p.lines[p.pos] = synth
		// Now parseMap at the new indent will pick up this line and any
		// subsequent keys at the same indent+2.
		m := map[string]any{}
		// Parse the first key inline.
		p.advance()
		v, err := p.parseMapValue(kRest, indent+2, cur.num)
		if err != nil {
			return nil, err
		}
		m[key] = v
		// Pick up subsequent same-indent map entries.
		for !p.eof() {
			n := p.peek()
			if n.indent != indent+2 {
				break
			}
			if isListItem(n.body) || !hasMapKey(n.body) {
				break
			}
			k2, r2, err := splitMapKey(n.body, n.num)
			if err != nil {
				return nil, err
			}
			p.advance()
			v2, err := p.parseMapValue(r2, indent+2, n.num)
			if err != nil {
				return nil, err
			}
			m[k2] = v2
		}
		return m, nil
	}
	// Plain `- scalar`.
	p.advance()
	if strings.HasPrefix(rest, "[") {
		return parseFlowSequence(rest, cur.num)
	}
	return parseScalar(rest, cur.num)
}

// isListItem reports whether body opens with `- ` or is exactly `-`.
// The trimmed body never has leading whitespace by construction, so a
// `- ` prefix unambiguously means a block list item.
func isListItem(body string) bool {
	if body == "-" {
		return true
	}
	if strings.HasPrefix(body, "- ") {
		return true
	}
	return false
}

// hasMapKey scans for an unquoted colon followed by whitespace or
// end-of-line - the syntactic signature of `key:`.
func hasMapKey(body string) bool {
	_, _, err := splitMapKey(body, 0)
	return err == nil
}

// splitMapKey peels `key:` off the front of a line body.  Returns
// (key, rest, err).  rest is the trimmed string after the colon (may
// be empty).  Honors quoting on the key.
func splitMapKey(body string, lineNum int) (string, string, error) {
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
			// Must be followed by whitespace or end-of-line to count.
			if i+1 == len(body) || body[i+1] == ' ' || body[i+1] == '\t' {
				key := strings.TrimSpace(body[:i])
				if key == "" {
					return "", "", fmt.Errorf("miniyaml: line %d: empty map key", lineNum)
				}
				// Unquote the key if it was quoted.
				if k, ok := tryUnquote(key); ok {
					key = k
				}
				rest := ""
				if i+1 < len(body) {
					rest = strings.TrimSpace(body[i+1:])
				}
				return key, rest, nil
			}
		}
	}
	return "", "", fmt.Errorf("miniyaml: line %d: not a mapping line", lineNum)
}
