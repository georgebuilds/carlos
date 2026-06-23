package todo

import (
	"crypto/rand"
	"encoding/base32"
	"regexp"
	"strings"
)

// This file is the pure markdown layer: turning a single `- [ ] task` line
// into a structured taskLine and back, with no filesystem or vault knowledge.
// Keeping it standalone makes the fiddly regex work exhaustively unit-testable.

// checkboxRe matches a GitHub/Obsidian task line: optional indent, a list
// marker (-, *, or +), a `[ ]`/`[x]` checkbox, then the description.
//
//	group 1: leading indentation
//	group 2: list marker rune
//	group 3: checkbox state char (space, x, or X)
//	group 4: the remaining description text
var checkboxRe = regexp.MustCompile(`^(\s*)([-*+]) \[([ xX])\] (.*)$`)

// blockRefRe matches a trailing Obsidian block reference `^slug` at end of
// line. Obsidian restricts block ids to letters, digits, and hyphens, and the
// caret must be at the start of the trailing token (preceded by whitespace or
// the line start). Requiring that boundary prevents a mid-word caret in prose
// (e.g. "a^b") from being mistaken for a block ref and silently stripped on
// the next write.
var blockRefRe = regexp.MustCompile(`(?:^|\s)\^([A-Za-z0-9-]+)\s*$`)

// dueRe matches the Obsidian Tasks-plugin due marker `📅 YYYY-MM-DD`.
var dueRe = regexp.MustCompile(`📅\s*(\d{4}-\d{2}-\d{2})`)

// tagRe matches a standalone `#tag` token (letter-led, then word chars, /, -).
// Mirrors the notes package's inline tag rule so a todo line tags the same way
// the rest of the vault does.
var tagRe = regexp.MustCompile(`(^|\s)#([a-zA-Z][\w/-]*)`)

// idPrefix is prepended to every minted block-ref so todos are visually
// distinct from hand-authored Obsidian block ids.
const idPrefix = "todo-"

// taskLine is the structured form of one parsed checkbox line. It retains the
// original Indent and Marker so a re-render preserves the author's list style.
type taskLine struct {
	Indent string
	Marker string // "-", "*", or "+"
	Done   bool
	Text   string // description, with due/tags/block-ref stripped out
	Due    string // YYYY-MM-DD or ""
	Tags   []string
	ID     string // block-ref slug without the caret, "" if the line had none
}

// parseTaskLine parses a single line. ok is false when the line is not a task
// checkbox (blank lines, prose, headings, plain list items all return false),
// so the scanner can skip non-task content cheaply.
func parseTaskLine(line string) (taskLine, bool) {
	m := checkboxRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
	if m == nil {
		return taskLine{}, false
	}
	t := taskLine{
		Indent: m[1],
		Marker: m[2],
		Done:   m[3] == "x" || m[3] == "X",
		Tags:   []string{},
	}
	rest := m[4]

	// Block-ref id (trailing) first, so it can't be mistaken for prose.
	if bm := blockRefRe.FindStringSubmatch(rest); bm != nil {
		t.ID = bm[1]
		rest = rest[:len(rest)-len(bm[0])]
	}
	// Due marker.
	if dm := dueRe.FindStringSubmatch(rest); dm != nil {
		t.Due = dm[1]
		rest = strings.Replace(rest, dm[0], "", 1)
	}
	// Tags (collect, then strip from the description text).
	for _, tm := range tagRe.FindAllStringSubmatch(rest, -1) {
		t.Tags = append(t.Tags, tm[2])
	}
	rest = tagRe.ReplaceAllString(rest, "$1")

	// Collapse the whitespace left behind by the strips.
	t.Text = strings.Join(strings.Fields(rest), " ")
	return t, true
}

// render reconstructs a canonical task line from the structured fields. Order
// is description, then due marker, then tags, then block-ref id, matching the
// Obsidian Tasks convention so the result stays plugin-friendly.
func (t taskLine) render() string {
	var b strings.Builder
	b.WriteString(t.Indent)
	b.WriteString(t.Marker)
	if t.Done {
		b.WriteString(" [x] ")
	} else {
		b.WriteString(" [ ] ")
	}
	b.WriteString(strings.TrimSpace(t.Text))
	if t.Due != "" {
		b.WriteString(" 📅 ")
		b.WriteString(t.Due)
	}
	for _, tag := range t.Tags {
		b.WriteString(" #")
		b.WriteString(tag)
	}
	if t.ID != "" {
		b.WriteString(" ^")
		b.WriteString(t.ID)
	}
	return b.String()
}

// item converts a parsed line into an Item, stamped with the given frame,
// backend, and source locator.
func (t taskLine) item(frame, backend, source string) Item {
	tags := t.Tags
	if tags == nil {
		tags = []string{}
	}
	return Item{
		ID:      t.ID,
		Text:    t.Text,
		Done:    t.Done,
		Frame:   frame,
		Backend: backend,
		Source:  source,
		Due:     t.Due,
		Tags:    tags,
	}
}

// newTaskLine builds a fresh, not-done task line from a Draft and a freshly
// minted id, using the default `- ` list marker and no indentation.
func newTaskLine(d Draft, id string) taskLine {
	return taskLine{
		Marker: "-",
		Done:   false,
		Text:   strings.TrimSpace(d.Text),
		Due:    strings.TrimSpace(d.Due),
		Tags:   normalizeTags(d.Tags),
		ID:     id,
	}
}

// applyPatch mutates the line in place from a Patch, leaving nil fields alone.
func (t *taskLine) applyPatch(p Patch) {
	if p.Text != nil {
		t.Text = strings.TrimSpace(*p.Text)
	}
	if p.Done != nil {
		t.Done = *p.Done
	}
	if p.Due != nil {
		t.Due = strings.TrimSpace(*p.Due)
	}
	if p.Tags != nil {
		t.Tags = normalizeTags(*p.Tags)
	}
}

// normalizeTags strips a leading '#' from each tag, drops blanks, and never
// returns nil so render/round-trip stay stable.
func normalizeTags(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#"))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// b32 is a lowercase, no-padding base32 encoder for compact block-ref slugs.
var b32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewID mints a fresh Obsidian block-ref slug (without the caret), e.g.
// "todo-ab12cd". It draws 5 random bytes → 8 base32 chars. A crypto/rand
// failure (vanishingly rare) falls back to a fixed marker so Add never panics;
// the caller's per-file collision check still guards uniqueness.
func NewID() string {
	var buf [5]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return idPrefix + "x"
	}
	return idPrefix + b32.EncodeToString(buf[:])
}
