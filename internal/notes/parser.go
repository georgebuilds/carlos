package notes

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/miniyaml"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// wikilinkRe matches Obsidian-flavored wikilinks: [[target]],
// [[target#section]], [[target|display]], [[target#section|display]].
//
// Group 1: target (anything that is not `]`, `|`, or `#`).
// Group 2: optional section (after `#`, before `|` or `]]`).
// Group 3: optional display (after `|`, before `]]`).
var wikilinkRe = regexp.MustCompile(`\[\[([^\]|#]+)(?:#([^\]|]+))?(?:\|([^\]]+))?\]\]`)

// tagRe matches inline `#tag` outside code blocks + markdown URLs. The
// per-line scanner in parseInline strips fenced blocks + markdown link
// anchors first so this regex only sees plain prose.
//
// Pattern: # followed by an ASCII letter, then word chars / `/` / `-`.
// The leading-letter requirement keeps `#123` numeric fragments out.
var tagRe = regexp.MustCompile(`#([a-zA-Z][\w/-]*)`)

// gm is a singleton goldmark instance.  Frontmatter is no longer
// extracted via goldmark-meta (we dropped that dep to shrink the
// binary); SplitFrontmatter + miniyaml.Unmarshal handle the
// frontmatter explicitly.  Parser state is stateless across documents
// so reusing is safe.
var gm = goldmark.New()

// parseFile reads + parses one markdown file at absolute path abs and
// returns the populated Note (Path-relative to vault root rel,
// slash-separated).
//
// Outgoing-link resolution is NOT done here - parseFile only fills the
// raw Link.Display/Section/Line + Target=raw-target. The indexer's
// second pass walks every note and rewrites Target to the resolved
// relpath (or "" if unresolved). Splitting parse vs resolve keeps
// per-file parsing independent of the full vault snapshot, which is
// what MaybeRefresh exploits.
func parseFile(abs, rel string, info os.FileInfo) (*Note, error) {
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("notes: read %s: %w", abs, err)
	}

	n := &Note{
		Path:        rel,
		Title:       defaultTitle(rel),
		Aliases:     []string{},
		Tags:        []string{},
		Frontmatter: map[string]any{},
		Headings:    []Heading{},
		Links:       []Link{},
		Backlinks:   []Link{},
		ModTime:     info.ModTime(),
		Size:        info.Size(),
	}

	// Frontmatter handling: peel a `---\n...---\n` block from the head
	// of the file via miniyaml.SplitFrontmatter, then decode the inner
	// YAML into a map[string]any.  Returns (nil, raw, false, nil) when
	// no frontmatter is present, which matches goldmark-meta's silent-
	// skip behavior.  Unterminated frontmatter is non-fatal here too -
	// we treat a malformed block as "no frontmatter" rather than
	// failing the whole parse, again matching the prior contract.
	fmBytes, _, found, fmErr := miniyaml.SplitFrontmatter(raw)
	if found && fmErr == nil && len(fmBytes) > 0 {
		v, err := miniyaml.Unmarshal(fmBytes)
		if err == nil {
			if fm, ok := v.(map[string]any); ok {
				n.Frontmatter = fm
				if t, ok := stringField(fm, "title"); ok {
					n.Title = t
				}
				n.Aliases = stringSliceField(fm, "aliases")
				fmTags := stringSliceField(fm, "tags")
				n.Tags = append(n.Tags, fmTags...)
			}
		}
	}

	// goldmark drives the heading walk (frontmatter already peeled).
	doc := gm.Parser().Parse(text.NewReader(raw))

	// Walk for headings only. Wikilinks + inline tags come from a
	// regex pass below because goldmark doesn't natively recognize
	// either, and writing custom inline parsers for both is more code
	// than the regex pass for the v0 surface.
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if h, ok := node.(*ast.Heading); ok {
			seg := headingText(h, raw)
			line := lineOf(raw, h.Lines().At(0).Start)
			n.Headings = append(n.Headings, Heading{Level: h.Level, Text: seg, Line: line})
		}
		return ast.WalkContinue, nil
	})

	// Compute bodyOffset so future incremental re-parse passes know
	// where the frontmatter ends.
	n.bodyOffset = bodyStart(raw)
	body := raw[n.bodyOffset:]
	n.body = string(body)

	inlineLinks, inlineTags := parseInline(body, n.bodyOffset, raw)
	n.Links = inlineLinks
	for _, t := range inlineTags {
		if !containsString(n.Tags, t) {
			n.Tags = append(n.Tags, t)
		}
	}

	return n, nil
}

// defaultTitle picks the filename (without .md) as a fallback when
// frontmatter doesn't supply one. The path is slash-separated; we keep
// only the basename.
func defaultTitle(rel string) string {
	base := filepath.Base(rel)
	return strings.TrimSuffix(base, ".md")
}

// stringField returns the string value of m[key] when present + a
// string. Numeric-or-other types fall through to (_, false) - the
// frontmatter map is preserved verbatim, but typed accessors only see
// the type they expect.
func stringField(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// stringSliceField extracts a []string from m[key]. Accepts both YAML
// sequence form (parsed as []interface{}) and a single-string scalar
// (parsed as string). Non-string elements in a sequence are skipped.
func stringSliceField(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return []string{}
	}
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return append([]string(nil), x...)
	case string:
		if x == "" {
			return []string{}
		}
		return []string{x}
	}
	return []string{}
}

// headingText pulls the heading's rendered text out of the source. The
// AST node Heading itself has no string accessor on goldmark; the cheap
// way is to walk its child text nodes and concat their segments.
func headingText(h *ast.Heading, src []byte) string {
	var b strings.Builder
	for child := h.FirstChild(); child != nil; child = child.NextSibling() {
		if t, ok := child.(*ast.Text); ok {
			b.Write(t.Segment.Value(src))
		} else {
			// Other inline nodes (emphasis, links, code spans) -
			// flatten their text children.
			for c := child.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					b.Write(t.Segment.Value(src))
				}
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// lineOf returns the 1-indexed line number for byte offset off within
// src. Linear scan; for vaults with thousands of files this runs ~O(N
// chars) per heading which is negligible (parsing dominates).
func lineOf(src []byte, off int) int {
	if off < 0 {
		return 1
	}
	if off > len(src) {
		off = len(src)
	}
	return bytes.Count(src[:off], []byte{'\n'}) + 1
}

// bodyStart returns the byte offset where the markdown body begins -
// just past the closing `---` of YAML frontmatter, or 0 if no
// frontmatter was present.
//
// Frontmatter detection follows goldmark-meta: the file must start with
// `---` (no BOM tolerance - that's a goldmark quirk we inherit) and the
// next line equal to `---` ends the block.
func bodyStart(src []byte) int {
	if !bytes.HasPrefix(src, []byte("---\n")) && !bytes.HasPrefix(src, []byte("---\r\n")) {
		return 0
	}
	// Find the closing `---` on its own line, after the opening.
	first := bytes.Index(src, []byte("\n")) // end of opening `---` line
	if first < 0 {
		return 0
	}
	rest := src[first+1:]
	// Match either `---\n` or `---\r\n` or a final `---` at EOF.
	idx := indexLine(rest, []byte("---"))
	if idx < 0 {
		return 0
	}
	end := first + 1 + idx + 3 // past the closing dashes
	if end < len(src) && src[end] == '\r' {
		end++
	}
	if end < len(src) && src[end] == '\n' {
		end++
	}
	return end
}

// indexLine finds line in s, where line must occupy a full line by
// itself (no leading text). Returns the byte offset where line starts,
// or -1 if not found.
func indexLine(s, line []byte) int {
	off := 0
	for off < len(s) {
		nl := bytes.IndexByte(s[off:], '\n')
		var lineEnd int
		if nl < 0 {
			lineEnd = len(s)
		} else {
			lineEnd = off + nl
		}
		cur := s[off:lineEnd]
		// Trim a trailing CR for CRLF-encoded files.
		if len(cur) > 0 && cur[len(cur)-1] == '\r' {
			cur = cur[:len(cur)-1]
		}
		if bytes.Equal(cur, line) {
			return off
		}
		if nl < 0 {
			return -1
		}
		off = lineEnd + 1
	}
	return -1
}

// parseInline scans the body for wikilinks + inline tags, line by line.
// Fenced code blocks are skipped (``` and ~~~ both supported) so a
// snippet showing `[[example]]` syntax doesn't pollute the graph.
//
// bodyOff is the byte offset where body starts within the original
// file; we use it to convert per-body line numbers into source line
// numbers (so Heading.Line and Link.Line stay in the same coordinate
// system).
func parseInline(body []byte, bodyOff int, source []byte) ([]Link, []string) {
	headerLines := 0
	if bodyOff > 0 {
		headerLines = bytes.Count(source[:bodyOff], []byte{'\n'})
	}

	var links []Link
	tagSet := map[string]struct{}{}

	inFence := false
	fenceMarker := ""
	scanner := lineScanner(body)
	lineNum := headerLines // body line N is source line (headerLines + N + 1)
	for scanner.next() {
		lineNum++
		line := scanner.text()
		trimmed := strings.TrimSpace(line)
		// Fence toggling: a line starting with ``` or ~~~ flips the
		// fence state. We don't validate the closing marker matches
		// the opener's info-string (Obsidian doesn't enforce it
		// either for rendering, and the v0 cost of stricter parsing
		// outweighs the benefit).
		if isFence(trimmed) {
			marker := fenceMarkerOf(trimmed)
			switch {
			case !inFence:
				inFence = true
				fenceMarker = marker
			case inFence && marker == fenceMarker:
				inFence = false
				fenceMarker = ""
			}
			continue
		}
		if inFence {
			continue
		}

		// Wikilinks - pure regex.
		for _, m := range wikilinkRe.FindAllStringSubmatchIndex(line, -1) {
			full := line[m[0]:m[1]]
			tgt := line[m[2]:m[3]]
			var section, display string
			if m[4] >= 0 {
				section = strings.TrimSpace(line[m[4]:m[5]])
			}
			if m[6] >= 0 {
				display = strings.TrimSpace(line[m[6]:m[7]])
			}
			if display == "" {
				display = strings.TrimSpace(tgt)
			}
			links = append(links, Link{
				Target:  strings.TrimSpace(tgt), // raw; resolver rewrites
				Display: display,
				Section: section,
				Line:    lineNum,
				Context: strings.TrimSpace(line),
			})
			_ = full // reserved for future column-level accuracy
		}

		// Inline `#tag`. Strip markdown `](url#anchor)` AND inline
		// `code spans` first so neither URL anchors nor code samples
		// pollute the tag set. Pragmatic regex pass; Obsidian's own
		// tag parser does similar.
		clean := stripInlineCode(stripMarkdownAnchors(line))
		for _, m := range tagRe.FindAllStringSubmatch(clean, -1) {
			if len(m) >= 2 {
				tagSet[m[1]] = struct{}{}
			}
		}
	}

	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}
	return links, tags
}

// isFence reports whether the trimmed line opens or closes a fenced
// code block. CommonMark requires 3+ backticks or 3+ tildes; we accept
// 3+ of either.
func isFence(trimmed string) bool {
	if strings.HasPrefix(trimmed, "```") {
		return true
	}
	if strings.HasPrefix(trimmed, "~~~") {
		return true
	}
	return false
}

// fenceMarkerOf returns the fence character (``` or ~~~) used by the
// trimmed line. Caller must guard with isFence first.
func fenceMarkerOf(trimmed string) string {
	if strings.HasPrefix(trimmed, "```") {
		return "```"
	}
	return "~~~"
}

// stripMarkdownAnchors replaces `](url#frag)` with `](url)` so the tag
// regex doesn't see the fragment as a `#tag`. Cheap state machine; we
// only care about avoiding false-positives in inline link URLs.
func stripMarkdownAnchors(line string) string {
	// Fast path: no markdown link syntax → no work.
	if !strings.Contains(line, "](") {
		return line
	}
	var b strings.Builder
	b.Grow(len(line))
	i := 0
	for i < len(line) {
		// Look for `](`.
		j := strings.Index(line[i:], "](")
		if j < 0 {
			b.WriteString(line[i:])
			break
		}
		b.WriteString(line[i : i+j+2])
		// Walk until the matching `)`. Inside, drop any `#fragment`.
		i += j + 2
		hashSeen := false
		for i < len(line) && line[i] != ')' {
			if line[i] == '#' {
				hashSeen = true
			}
			if !hashSeen {
				b.WriteByte(line[i])
			}
			i++
		}
		if i < len(line) {
			b.WriteByte(')')
			i++
		}
	}
	return b.String()
}

// stripInlineCode replaces `code spans` (single or multi backticks)
// with an empty placeholder so the tag-regex pass doesn't see
// `#example` style code samples. CommonMark inline-code rules require
// matching opener/closer backtick runs - we honor that by counting.
func stripInlineCode(line string) string {
	if !strings.ContainsRune(line, '`') {
		return line
	}
	var b strings.Builder
	b.Grow(len(line))
	i := 0
	for i < len(line) {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		// Count opener run length.
		runStart := i
		for i < len(line) && line[i] == '`' {
			i++
		}
		runLen := i - runStart
		// Look for a closer run of the SAME length.
		closerIdx := -1
		j := i
		for j < len(line) {
			if line[j] != '`' {
				j++
				continue
			}
			cs := j
			for j < len(line) && line[j] == '`' {
				j++
			}
			if j-cs == runLen {
				closerIdx = j
				break
			}
		}
		if closerIdx < 0 {
			// Unmatched backtick: write the opener verbatim and
			// move on. (Following text is normal prose.)
			b.WriteString(line[runStart:i])
			continue
		}
		// Drop the whole `code span`, opener through closer.
		i = closerIdx
	}
	return b.String()
}

// lineScanner is a minimal `\n`-delimited iterator that hands out each
// line WITHOUT the trailing newline. Using a hand-rolled iterator
// rather than bufio.Scanner because we already have the body in memory
// and want to avoid the scanner's allocation overhead per line.
type linesIter struct {
	src []byte
	off int
	cur string
}

func lineScanner(src []byte) *linesIter { return &linesIter{src: src} }

func (s *linesIter) next() bool {
	if s.off >= len(s.src) {
		return false
	}
	nl := bytes.IndexByte(s.src[s.off:], '\n')
	if nl < 0 {
		s.cur = string(s.src[s.off:])
		s.off = len(s.src)
		return true
	}
	end := s.off + nl
	line := s.src[s.off:end]
	// Strip CR if present (CRLF source).
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	s.cur = string(line)
	s.off = end + 1
	return true
}

func (s *linesIter) text() string { return s.cur }

// containsString reports whether haystack contains needle. Used for
// tag dedup so we don't drag a third-party slice helper in for one
// short loop.
func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// fileMTime is a tiny helper used by MaybeRefresh; kept here so the
// parser package owns the os.Stat conversion convention.
func fileMTime(path string) (time.Time, int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, 0, err
	}
	return info.ModTime(), info.Size(), nil
}
