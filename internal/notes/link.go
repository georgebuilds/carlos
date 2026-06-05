package notes

import "strings"

// SectionBody returns the body content under the heading whose text
// (case-insensitive) matches sectionName. The section runs from the
// matching heading line down to (but not including) the next heading
// at the same level or higher.
//
// Returns "" when no heading matches.
//
// Lives in link.go because it's the "follow a wikilink with #anchor"
// helper, not the Obsidian resolver itself — but both concerns are
// link-shaped, so grouping them in one file keeps the package surface
// scannable.
func SectionBody(n *Note, sectionName string) string {
	if n == nil || sectionName == "" {
		return ""
	}
	target := strings.ToLower(strings.TrimSpace(sectionName))
	bodyLines := strings.Split(n.body, "\n")

	// Frontmatter lines aren't part of body; bodyLine N corresponds to
	// source line (headerLines + N + 1). For section extraction we
	// only care about body coordinates anyway.
	startBody := -1
	startLevel := 0
	for i, l := range bodyLines {
		if level, text, ok := parseHeading(l); ok {
			if strings.EqualFold(text, sectionName) || strings.EqualFold(text, "# "+sectionName) {
				startBody = i + 1
				startLevel = level
				break
			}
			// Loose prefix match — "Phase 11" should match
			// "Phase 11 — Research mode" without forcing the
			// model to quote the trailing em-dash phrase.
			if strings.HasPrefix(strings.ToLower(text), target) {
				startBody = i + 1
				startLevel = level
				break
			}
		}
	}
	if startBody < 0 {
		return ""
	}
	var b strings.Builder
	for i := startBody; i < len(bodyLines); i++ {
		if level, _, ok := parseHeading(bodyLines[i]); ok && level <= startLevel {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(bodyLines[i])
	}
	return strings.TrimSpace(b.String())
}

// parseHeading reports whether l is an ATX-style heading line + returns
// its level + trimmed text. Setext headings (=== / --- underline) are
// out of scope for v0; Obsidian users overwhelmingly use ATX.
func parseHeading(l string) (level int, text string, ok bool) {
	trimmed := strings.TrimLeft(l, " ")
	if !strings.HasPrefix(trimmed, "#") {
		return 0, "", false
	}
	i := 0
	for i < len(trimmed) && trimmed[i] == '#' {
		i++
	}
	if i == 0 || i > 6 {
		return 0, "", false
	}
	// ATX requires a space after the # run unless the rest is empty.
	if i < len(trimmed) && trimmed[i] != ' ' {
		return 0, "", false
	}
	return i, strings.TrimSpace(trimmed[i:]), true
}

// BodyRaw returns the post-frontmatter body of the note as it was at
// parse time. Used by notes_get when `body: true` is set. Kept as a
// helper rather than exporting Note.body so we can re-shape internal
// storage (e.g. drop the in-memory copy in favour of re-reading from
// disk) without churning callers.
func BodyRaw(n *Note) string {
	if n == nil {
		return ""
	}
	return n.body
}

// Description returns the frontmatter `description:` value if present,
// otherwise the first non-heading paragraph of the body. Used by
// notes_tagged to give the model a one-line summary per tagged note.
func Description(n *Note) string {
	if n == nil {
		return ""
	}
	if d, ok := stringField(n.Frontmatter, "description"); ok && d != "" {
		return d
	}
	// First non-heading, non-empty line, joined paragraph-style.
	var b strings.Builder
	in := false
	for _, l := range strings.Split(n.body, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			if in {
				break
			}
			continue
		}
		if _, _, ok := parseHeading(t); ok {
			if in {
				break
			}
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(t)
		in = true
		if b.Len() > 200 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
