// Composer-chip marker syntax + model-bound expansion (slice I-1).
//
// The chat composer represents large pastes / images / @file mentions
// as single-grapheme inline chips. In the PERSISTED text (the
// MessagePayload.Text of an EvtUserMessage) each chip is a marker:
//
//	‹p:ID›   paste
//	‹i:ID›   image
//	‹m:ID›   mention
//
// where ID is short (base36) and unique within the message, and the
// payload's Attachments slice carries the referenced content. The
// angle quotes U+2039/U+203A were picked because they are (a) a single
// terminal cell each, (b) wildly unlikely to appear around a ":id"
// core in organic typing, and (c) not meaningful to markdown, shells,
// or slash parsing.
//
// Two consumers split the work:
//
//   - the chat TUI substitutes markers with styled chips at render
//     time (internal/tui/chat/composer.go);
//   - chatglue expands markers into model-facing text via
//     [ExpandMarkers] before history reaches the provider - raw
//     markers must NEVER leak into the model context.
//
// The helpers live in package agent (next to Attachment) so both
// consumers share one definition without a chat↔chatglue import edge.

package agent

import (
	"regexp"
	"strings"
)

// markerRe matches one well-formed chip marker. The tag set [pim] and
// the lowercase-base36 ID charset are deliberately tight: a partially
// edited marker (e.g. the user deleted the closing ›) simply stops
// matching and degrades to literal text instead of half-expanding.
var markerRe = regexp.MustCompile("‹([pim]):([a-z0-9]+)›")

// markerTag returns the single-letter marker tag for a kind, or ""
// for an unknown kind (callers treat "" as "don't emit a marker").
func markerTag(kind AttachmentKind) string {
	switch kind {
	case AttachmentPaste:
		return "p"
	case AttachmentImage:
		return "i"
	case AttachmentMention:
		return "m"
	}
	return ""
}

// markerKind is markerTag's inverse. The regexp guarantees tag ∈
// {p,i,m} for every match, so the empty fallback is defensive only.
func markerKind(tag string) AttachmentKind {
	switch tag {
	case "p":
		return AttachmentPaste
	case "i":
		return AttachmentImage
	case "m":
		return AttachmentMention
	}
	return ""
}

// Marker renders the persisted marker text for a chip. Returns "" when
// the kind is unknown so a future kind added without a tag mapping
// fails loudly in tests (no marker inserted) rather than corrupting
// the composer text.
func Marker(kind AttachmentKind, id string) string {
	tag := markerTag(kind)
	if tag == "" || id == "" {
		return ""
	}
	return "‹" + tag + ":" + id + "›"
}

// MarkerSpan locates one chip marker inside a scanned string. Start
// and End are BYTE offsets (regexp-native); rune-offset consumers
// convert at the call site where they already hold the string.
type MarkerSpan struct {
	Start int // byte offset of the opening ‹
	End   int // byte offset just past the closing ›
	Kind  AttachmentKind
	ID    string
}

// FindMarkers returns every well-formed chip marker in s, in text
// order. Nil when s has none (the common chip-less path costs one
// quick scan for the opening quote).
func FindMarkers(s string) []MarkerSpan {
	if !strings.Contains(s, "‹") {
		return nil
	}
	idxs := markerRe.FindAllStringSubmatchIndex(s, -1)
	if len(idxs) == 0 {
		return nil
	}
	spans := make([]MarkerSpan, 0, len(idxs))
	for _, ix := range idxs {
		spans = append(spans, MarkerSpan{
			Start: ix[0],
			End:   ix[1],
			Kind:  markerKind(s[ix[2]:ix[3]]),
			ID:    s[ix[4]:ix[5]],
		})
	}
	return spans
}

// ContainsMarker reports whether s holds at least one well-formed chip
// marker. chatglue's tests use it to assert the "no raw marker ever
// reaches the model" invariant.
func ContainsMarker(s string) bool {
	return strings.Contains(s, "‹") && markerRe.MatchString(s)
}

// ExpandMarkers rewrites chip markers into their model-facing text.
// This is the ONLY transformation between persisted user text and the
// provider context - applied exactly once, in chatglue's buildHistory.
//
// Expansion by kind:
//
//   - paste: the full attachment content in a fenced block labeled
//     with the chip nickname. The fence stretches past any backtick
//     run inside the content so the block can't be broken out of.
//   - image: a "[image: nickname]" placeholder. A later slice replaces
//     this with a real image block; the placeholder keeps the marker
//     from leaking until then.
//   - mention: a compact "@path" reference plus a note that the file
//     can be read with tools (slice I-4). File contents are
//     deliberately NOT inlined - the model has read tools, and a
//     mention is a pointer, not a paste.
//   - unknown ID (attachment missing): a "[attachment X unavailable]"
//     placeholder - deterministic, and still no raw marker.
//
// Markers are replaced in a single regexp pass, so content that itself
// contains marker-shaped text is NOT re-expanded.
func ExpandMarkers(text string, atts []Attachment) string {
	if !strings.Contains(text, "‹") {
		return text
	}
	byID := make(map[string]Attachment, len(atts))
	for _, a := range atts {
		byID[a.ID] = a
	}
	return markerRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := markerRe.FindStringSubmatch(m)
		att, ok := byID[sub[2]]
		return expandMarkerText(markerKind(sub[1]), sub[2], att, ok)
	})
}

// expandMarkerText renders ONE marker's model-facing text. Shared by
// ExpandMarkers (every marker) and ExpandMarkerSegments (every marker
// that doesn't become an image segment) so the two paths can never
// drift apart.
func expandMarkerText(kind AttachmentKind, id string, att Attachment, ok bool) string {
	if !ok {
		return "[attachment " + id + " unavailable]"
	}
	switch kind {
	case AttachmentPaste:
		return expandPaste(att)
	case AttachmentImage:
		return ImagePlaceholder(att)
	default: // AttachmentMention - the regexp admits no other tag.
		return expandMention(att)
	}
}

// expandMention renders the model-facing text for one @file mention
// chip (slice I-4): the path the user attached, prefixed "@" so it
// reads naturally inside the sentence, plus a parenthetical noting the
// file is referenced - not inlined - and readable with tools. Token
// discipline: carlos has read tools, so shipping file CONTENTS here
// would burn context the model can fetch on demand (and would go stale
// the moment the file changes). Falls back to the nickname/ID label
// chain for a path-less attachment.
func expandMention(a Attachment) string {
	target := a.Path
	if target == "" {
		target = attachmentLabel(a)
	}
	return "@" + target + " (mentioned file, not inlined; read it with file tools if needed)"
}

// ImagePlaceholder is the model-facing text stand-in for an image chip
// whose pixels are NOT being sent (provider without vision, or image
// bytes unavailable). Exported because chatglue needs the same wording
// when an artifact read fails mid-bridge.
func ImagePlaceholder(a Attachment) string {
	return "[image: " + attachmentLabel(a) + "]"
}

// Segment is one ordered piece of a marker-expanded user message:
// either a run of model-facing text (Image == nil) or one image chip
// (Image != nil, Text == ""). Produced by [ExpandMarkerSegments];
// consumed by chatglue, which turns image segments into provider image
// blocks by loading the referenced bytes from the artifact store.
type Segment struct {
	Text  string
	Image *Attachment
}

// ExpandMarkerSegments is the multi-block sibling of [ExpandMarkers]:
// it splits text at image markers (tag "i" with a known attachment)
// and expands every other marker inline exactly as ExpandMarkers does
// (shared per-marker renderer, so the text halves are byte-identical).
// Segments come back in text order; adjacent text accumulates into one
// segment; empty text runs are dropped. A chip-less message returns a
// single text segment carrying the input verbatim.
//
// The "no raw marker ever reaches the model" invariant holds here too:
// image markers leave the text entirely (they become Image segments),
// everything else is rewritten by the same single pass.
func ExpandMarkerSegments(text string, atts []Attachment) []Segment {
	spans := FindMarkers(text)
	if len(spans) == 0 {
		return []Segment{{Text: text}}
	}
	byID := make(map[string]Attachment, len(atts))
	for _, a := range atts {
		byID[a.ID] = a
	}
	var segs []Segment
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			segs = append(segs, Segment{Text: b.String()})
			b.Reset()
		}
	}
	last := 0
	for _, sp := range spans {
		b.WriteString(text[last:sp.Start])
		last = sp.End
		att, ok := byID[sp.ID]
		if ok && sp.Kind == AttachmentImage {
			flush()
			a := att // dedicated copy so the pointer escapes cleanly
			segs = append(segs, Segment{Image: &a})
			continue
		}
		b.WriteString(expandMarkerText(sp.Kind, sp.ID, att, ok))
	}
	b.WriteString(text[last:])
	flush()
	return segs
}

// expandPaste renders a paste attachment as a labeled fenced block.
// The fence is lengthened until it can't collide with a backtick run
// inside the content, mirroring how markdown nesting is escaped.
func expandPaste(a Attachment) string {
	fence := "```"
	for strings.Contains(a.Content, fence) {
		fence += "`"
	}
	var b strings.Builder
	b.WriteString("[pasted: " + attachmentLabel(a) + "]\n")
	b.WriteString(fence + "\n")
	b.WriteString(a.Content)
	if a.Content != "" && !strings.HasSuffix(a.Content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(fence)
	return b.String()
}

// attachmentLabel picks the human-facing name for an attachment:
// nickname, then path, then ID - never empty for a stored attachment.
func attachmentLabel(a Attachment) string {
	switch {
	case a.Nickname != "":
		return a.Nickname
	case a.Path != "":
		return a.Path
	}
	return a.ID
}
