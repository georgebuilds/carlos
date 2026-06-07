package miniyaml

import (
	"bytes"
	"errors"
)

// SplitFrontmatter scans for a YAML frontmatter block bounded by
//
//	---\n
//	...yaml...\n
//	---\n
//
// at the start of `data`.  Returns (frontmatter, body, found, err).
//
//   - found=true  → frontmatter contains the bytes between the two
//     `---` lines (NOT including the markers); body is everything
//     after the closing `---\n`.  Either may be empty.
//   - found=false → data didn't start with a `---` line.  frontmatter
//     is nil; body is data verbatim.
//   - err set     → the opener was present but no matching closer was
//     found (an unterminated frontmatter - surfaces as a parse error
//     to the caller).
//
// A leading UTF-8 BOM is tolerated.  CRLF line endings are tolerated:
// both `---\n` and `---\r\n` open / close a block.
func SplitFrontmatter(data []byte) (frontmatter, body []byte, found bool, err error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	// The opener must be a `---` line at byte 0.  Accept `---\n` and
	// `---\r\n`.
	if !bytes.HasPrefix(data, []byte("---\n")) && !bytes.HasPrefix(data, []byte("---\r\n")) {
		return nil, data, false, nil
	}
	// Skip past the opener line.
	rest := data[len("---"):]
	rest = bytes.TrimPrefix(rest, []byte("\r"))
	rest = bytes.TrimPrefix(rest, []byte("\n"))

	// Find the first `---` line in rest.  A `---` on its own line means
	// either `---\n`, `---\r\n`, or `---` at EOF.
	idx := indexClosingMarker(rest)
	if idx < 0 {
		return nil, nil, true, errors.New("miniyaml: unterminated frontmatter")
	}
	fm := rest[:idx]
	tail := rest[idx+len("---"):]
	tail = bytes.TrimPrefix(tail, []byte("\r"))
	tail = bytes.TrimPrefix(tail, []byte("\n"))
	return fm, tail, true, nil
}

// indexClosingMarker finds the byte offset of a `---` line in s.  A
// matching line must begin at offset 0 or just after a `\n`, span
// exactly 3 dashes, and be followed by `\n`, `\r\n`, or EOF.  Returns
// -1 if no closer is found.
func indexClosingMarker(s []byte) int {
	off := 0
	for off <= len(s) {
		// Find `---` starting at `off`.
		i := bytes.Index(s[off:], []byte("---"))
		if i < 0 {
			return -1
		}
		abs := off + i
		// Check the byte before is start-of-string or `\n`.
		atLineStart := abs == 0 || s[abs-1] == '\n'
		// Check the byte after is EOF, `\n`, or `\r\n`.
		end := abs + 3
		atLineEnd := false
		switch {
		case end == len(s):
			atLineEnd = true
		case s[end] == '\n':
			atLineEnd = true
		case s[end] == '\r' && end+1 < len(s) && s[end+1] == '\n':
			atLineEnd = true
		case s[end] == '\r' && end+1 == len(s):
			atLineEnd = true
		}
		if atLineStart && atLineEnd {
			return abs
		}
		off = abs + 3
	}
	return -1
}
