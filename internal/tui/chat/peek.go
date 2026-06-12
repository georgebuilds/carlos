// Sticky paste-peek card (roadmap slice I-2).
//
// While the textarea cursor sits adjacent to (or inside) a paste chip,
// a small preview card renders in the hint-band slot above the input
// separator: first-line preview, size / line / ~token stats, and a dim
// italic corner tag naming the paste's class. The card is sticky - it
// stays up for every keystroke that keeps the cursor on the chip and
// vanishes the moment the cursor leaves.
//
// Design rules (sandlot sketchbook, per George):
//   - the kind reads from an italic corner tag + dashed ┄ rule
//     accents, NEVER from a left color stripe (banned);
//   - NO_COLOR-safe: strip every SGR code and the card still carries
//     the tag text, the preview, and the dashed rules.
//
// One peek at a time: the first chip the cursor touches (left to
// right on its line) wins.

package chat

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// peekAttachment returns the attachment whose chip the cursor is
// currently adjacent to or inside, if any. Adjacency is rune-exact:
// the cursor column anywhere in [marker start, marker end] counts, so
// the card appears right after InsertChip (cursor lands at the end)
// and survives a ← hop to the chip's left edge. All three chip kinds
// peek: paste (content preview), image (stats), mention (path + stat,
// slice I-4).
func (m *Model) peekAttachment() (agent.Attachment, bool) {
	c := m.composer
	if c == nil || !c.HasChips() {
		return agent.Attachment{}, false
	}
	spans, col := c.lineSpans()
	for _, sp := range spans {
		if col < sp.start || col > sp.end {
			continue
		}
		att, ok := c.atts[sp.id]
		if !ok {
			continue
		}
		return att, true
	}
	return agent.Attachment{}, false
}

// renderPeek dispatches the peek card by chip kind: image chips get
// the binary-safe card (stats + capability warning, no content
// preview); mention chips get the path + stat card (mention.go);
// everything else keeps the slice-I-2 paste card.
func (m *Model) renderPeek(att agent.Attachment, w int) string {
	switch att.Kind {
	case agent.AttachmentImage:
		return renderImagePeekCard(m.composer.imageMetaFor(att.ID), m.imageVisionWarn(), w)
	case agent.AttachmentMention:
		return renderMentionPeekCard(att, w)
	}
	return renderPeekCard(att, w)
}

// estTokens is the deliberately rough chars/4 token estimate shown in
// the peek stats row. Always rendered with a "~" prefix so nobody
// mistakes it for a real tokenizer count.
func estTokens(chars int) int {
	return chars / 4
}

// renderPeekCard paints the four-row peek card for one paste
// attachment at width w:
//
//	┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄ json (24 keys)
//	{"name": "carlos", "version": "0.7.…
//	1.8k chars · 42L · ~456 tok
//	┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄
//
// Row 1's dashed rule runs up to the corner tag (the chip nickname,
// dim italic, top-right). Rows are indented two cells like the slash
// hint band so the card reads as part of the composer, not a popup.
func renderPeekCard(att agent.Attachment, w int) string {
	const indent = "  "
	contentW := w - len(indent)
	if contentW < 20 {
		contentW = 20
	}

	dim := lipgloss.NewStyle().Foreground(colorMuted)
	tagStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)

	// Corner tag: the classifier's nickname, capped at contentW-8 so
	// at least a 7-dash rule survives at the narrowest clamp.
	tag := truncateRight(chipLabel(att.Kind, att.Nickname), contentW-8)
	ruleW := contentW - lipgloss.Width(tag) - 1
	top := dim.Render(strings.Repeat("┄", ruleW)) + " " + tagStyle.Render(tag)

	preview := truncateRight(peekPreviewLine(att.Content), contentW)

	chars := utf8.RuneCountInString(att.Content)
	stats := dim.Render(compactCount(chars) + " chars · " +
		itoa(pasteLineCount(att.Content)) + "L · ~" +
		compactCount(estTokens(chars)) + " tok")

	bottom := dim.Render(strings.Repeat("┄", contentW))

	return strings.Join([]string{
		indent + top,
		indent + preview,
		indent + stats,
		indent + bottom,
	}, "\n")
}

// renderImagePeekCard paints the peek card for one image chip at
// width w (slice I-3):
//
//	┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄ image
//	24 KB · 640×480 · png
//	↳ this frame's model can't read images
//	┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄
//
// No first-line preview - the content is binary. The warning row
// appears only when the active provider lacks vision; it is plain
// text (colorWarn as reinforcement), so the capability gate survives
// NO_COLOR. Same sandlot rules as the paste card: dashed rules, dim
// italic corner tag, never a left color stripe.
func renderImagePeekCard(meta imageMeta, visionWarn bool, w int) string {
	const indent = "  "
	contentW := w - len(indent)
	if contentW < 20 {
		contentW = 20
	}

	dim := lipgloss.NewStyle().Foreground(colorMuted)
	tagStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)

	tag := truncateRight(string(agent.AttachmentImage), contentW-8)
	ruleW := contentW - lipgloss.Width(tag) - 1
	top := dim.Render(strings.Repeat("┄", ruleW)) + " " + tagStyle.Render(tag)

	stats := dim.Render(truncateRight(imageStatsLine(meta), contentW))

	rows := []string{indent + top, indent + stats}
	if visionWarn {
		warn := lipgloss.NewStyle().Foreground(colorWarn).Render(
			truncateRight("↳ this frame's model can't read images", contentW))
		rows = append(rows, indent+warn)
	}
	rows = append(rows, indent+dim.Render(strings.Repeat("┄", contentW)))
	return strings.Join(rows, "\n")
}

// peekPreviewLine extracts the paste's first line for the preview row,
// flattening tabs to spaces so the row's width math stays cell-exact.
func peekPreviewLine(content string) string {
	if i := strings.IndexByte(content, '\n'); i >= 0 {
		content = content[:i]
	}
	return strings.ReplaceAll(content, "\t", "  ")
}
