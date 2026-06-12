// Composer scaffold + chip infrastructure (roadmap slice I-1).
//
// The Composer is a thin wrapper around the chat Model's textarea that
// adds "clippings": large pastes / images / @file mentions render as
// single-grapheme inline chips. In the UNDERLYING textarea value a chip
// is a marker (‹p:ID› / ‹i:ID› / ‹m:ID› - see internal/agent's
// attachment.go); the Composer keeps a parallel attachment store and
// rebuilds rune-offset ChipRefs from a text scan after every edit, so
// the marker text is always the single source of truth and the refs
// can never drift from it.
//
// Editing contract ("a chip is one grapheme"):
//
//   - backspace with the cursor just after (or inside) a chip removes
//     the WHOLE chip in one keypress;
//   - delete with the cursor just before (or inside) a chip does the
//     same forward;
//   - ←/→ hop over a chip in one keypress;
//   - any other edit that lands the cursor inside a marker (e.g. ↑/↓
//     between lines) is snapped out by Sync.
//
// This slice builds the machinery only; the chip PRODUCERS (paste
// clipping I-2, image paste I-3, @mentions I-4) hook in through
// InsertChip.

package chat

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// ChipRef is the composer-side handle for one inline chip: where its
// marker sits in the textarea value (rune offsets, since every cursor
// interaction is rune-based) and which attachment it references.
// Rebuilt from a full text scan by Sync after every edit; sorted by
// Offset because the scan walks the text left to right.
type ChipRef struct {
	ID       string
	Kind     agent.AttachmentKind
	Offset   int // rune offset of the marker's opening ‹ in ta.Value()
	Len      int // marker length in runes
	Nickname string
}

// Composer wraps the chat textarea with chip bookkeeping. It holds a
// POINTER to the Model's textarea (not a copy) so the dozens of
// existing m.ta call sites in chat.go keep working untouched - the
// Composer sees every mutation for free.
//
// All methods are nil-receiver safe: tests that build a bare &Model{}
// without New() skip the chip machinery entirely.
type Composer struct {
	ta   *textarea.Model
	atts map[string]agent.Attachment
	refs []ChipRef
	seq  int // monotonic base36 ID source, unique within one message

	// imeta holds compose-time stats for image chips (slice I-3),
	// keyed by attachment ID. Pruned in lockstep with atts so a
	// deleted chip can't leak its stats into a later message.
	imeta map[string]imageMeta
}

// NewComposer wires a Composer around ta. Called once from chat.New
// with &m.ta; the pointer stays valid because the chat Model is heap-
// allocated and used by pointer everywhere.
func NewComposer(ta *textarea.Model) *Composer {
	return &Composer{ta: ta, atts: make(map[string]agent.Attachment)}
}

// HasChips reports whether the current textarea value holds at least
// one live chip (marker + stored attachment). Render paths use it to
// fall back to the stock ta.View() on the chip-less common path.
func (c *Composer) HasChips() bool {
	return c != nil && len(c.refs) > 0
}

// Chips returns a copy of the current refs, sorted by offset. Copy so
// callers can't desync the internal slice from the text.
func (c *Composer) Chips() []ChipRef {
	if c == nil || len(c.refs) == 0 {
		return nil
	}
	out := make([]ChipRef, len(c.refs))
	copy(out, c.refs)
	return out
}

// InsertChip stores att and places its marker at the cursor, exactly
// like typing one grapheme. An empty att.ID gets a fresh short base36
// ID (unique within the message); a caller-supplied ID is kept so
// later slices can pre-bind chips to artifact IDs. Returns the ID, or
// "" when the attachment kind has no marker tag (unknown kind - the
// text is left untouched).
func (c *Composer) InsertChip(att agent.Attachment) string {
	if c == nil {
		return ""
	}
	if att.ID == "" {
		att.ID = c.newID()
	}
	marker := agent.Marker(att.Kind, att.ID)
	if marker == "" {
		return ""
	}
	c.atts[att.ID] = att
	c.ta.InsertString(marker)
	c.Sync()
	return att.ID
}

// newID mints the next base36 ID that collides with neither a stored
// attachment nor a marker already present in the text (e.g. a stale
// marker recalled from chat history).
func (c *Composer) newID() string {
	inText := make(map[string]bool)
	for _, ms := range agent.FindMarkers(c.ta.Value()) {
		inText[ms.ID] = true
	}
	for {
		c.seq++
		id := strconv.FormatInt(int64(c.seq), 36)
		if _, taken := c.atts[id]; !taken && !inText[id] {
			return id
		}
	}
}

// HandleKey intercepts the four cursor-adjacent chip operations BEFORE
// the textarea sees the keystroke. Returns true when the key was fully
// handled (the caller must then skip ta.Update for it). Everything
// else returns false and flows to the textarea unchanged.
//
// The alias keys mirror the textarea's DefaultKeyMap so emacs-style
// bindings get the same atomicity as the arrow/edit keys.
func (c *Composer) HandleKey(msg tea.KeyMsg) bool {
	if c == nil || len(c.atts) == 0 {
		return false
	}
	switch msg.String() {
	case "backspace", "ctrl+h":
		return c.deleteChipBackward()
	case "delete", "ctrl+d":
		return c.deleteChipForward()
	case "left", "ctrl+b":
		return c.skipChipLeft()
	case "right", "ctrl+f":
		return c.skipChipRight()
	}
	return false
}

// runeSpan is a marker occurrence on ONE logical line, in rune
// offsets relative to that line. Markers never contain newlines, so
// every chip operation is line-local by construction.
type runeSpan struct {
	start, end int // rune offsets, end exclusive
	id         string
}

// lineSpans returns the marker spans of the cursor's logical line plus
// the cursor's rune column within it. The column derivation leans on
// textarea.LineInfo: StartColumn is the rune index where the current
// soft-wrapped row begins, ColumnOffset the rune offset within it.
func (c *Composer) lineSpans() (spans []runeSpan, col int) {
	row := c.ta.Line()
	li := c.ta.LineInfo()
	col = li.StartColumn + li.ColumnOffset
	lines := strings.Split(c.ta.Value(), "\n")
	if row < 0 || row >= len(lines) {
		return nil, col
	}
	ln := lines[row]
	for _, ms := range agent.FindMarkers(ln) {
		spans = append(spans, runeSpan{
			start: utf8.RuneCountInString(ln[:ms.Start]),
			end:   utf8.RuneCountInString(ln[:ms.End]),
			id:    ms.ID,
		})
	}
	return spans, col
}

// deleteChipBackward implements one-keypress chip removal: when the
// cursor sits just after a marker (or, defensively, inside one), the
// whole marker is deleted by replaying N backspaces through the
// textarea itself - which keeps every internal invariant (soft wrap,
// cursor, memoized width) exactly as if the user had pressed
// backspace N times. The attachment is dropped with the marker.
func (c *Composer) deleteChipBackward() bool {
	spans, col := c.lineSpans()
	for _, sp := range spans {
		if col > sp.start && col <= sp.end {
			c.ta.SetCursor(sp.end)
			c.replayKey(tea.KeyBackspace, sp.end-sp.start)
			delete(c.atts, sp.id)
			c.Sync()
			return true
		}
	}
	return false
}

// deleteChipForward mirrors deleteChipBackward for the delete key:
// cursor just before (or inside) a marker removes the whole chip.
func (c *Composer) deleteChipForward() bool {
	spans, col := c.lineSpans()
	for _, sp := range spans {
		if col >= sp.start && col < sp.end {
			c.ta.SetCursor(sp.start)
			c.replayKey(tea.KeyDelete, sp.end-sp.start)
			delete(c.atts, sp.id)
			c.Sync()
			return true
		}
	}
	return false
}

// skipChipLeft hops the cursor over an adjacent chip in one ←. The
// strictly-inside case can't normally occur (Sync snaps the cursor
// out) but is handled the same way for robustness.
func (c *Composer) skipChipLeft() bool {
	spans, col := c.lineSpans()
	for _, sp := range spans {
		if col > sp.start && col <= sp.end {
			c.ta.SetCursor(sp.start)
			return true
		}
	}
	return false
}

// skipChipRight hops the cursor over an adjacent chip in one →.
func (c *Composer) skipChipRight() bool {
	spans, col := c.lineSpans()
	for _, sp := range spans {
		if col >= sp.start && col < sp.end {
			c.ta.SetCursor(sp.end)
			return true
		}
	}
	return false
}

// replayKey feeds n synthetic keypresses of type t through the
// textarea's own Update. Slightly slower than direct value surgery but
// immune to desync: the textarea applies its own editing rules, so we
// never have to reimplement them. Returned blink commands are dropped;
// the chat's own textarea.Blink keeps the cursor pulsing.
func (c *Composer) replayKey(t tea.KeyType, n int) {
	for i := 0; i < n; i++ {
		*c.ta, _ = c.ta.Update(tea.KeyMsg{Type: t})
	}
}

// Sync re-derives the chip state from the current textarea value:
//
//  1. refs are rebuilt by scanning the text for markers whose ID has a
//     stored attachment (markers without one - e.g. recalled from chat
//     input history after the attachments were submitted - degrade to
//     literal text);
//  2. attachments whose marker vanished (kill-line, word-delete, any
//     edit the four intercepted keys don't cover) are pruned;
//  3. the cursor is snapped out of any marker interior so the next
//     keystroke can't split a marker.
//
// Called after every textarea update on the chat's default key route,
// after InsertChip, and defensively at the top of Serialize.
func (c *Composer) Sync() {
	if c == nil {
		return
	}
	c.refs = c.refs[:0]
	if len(c.atts) == 0 {
		// No live attachments means no live image chips either; drop
		// any stats stranded by a kill-line / multi-chip delete.
		for id := range c.imeta {
			delete(c.imeta, id)
		}
		return
	}
	val := c.ta.Value()
	seen := make(map[string]bool, len(c.atts))
	runeOff := 0
	prevByte := 0
	for _, ms := range agent.FindMarkers(val) {
		runeOff += utf8.RuneCountInString(val[prevByte:ms.Start])
		mlen := utf8.RuneCountInString(val[ms.Start:ms.End])
		start := runeOff
		runeOff += mlen
		prevByte = ms.End
		att, ok := c.atts[ms.ID]
		if !ok {
			continue
		}
		seen[ms.ID] = true
		c.refs = append(c.refs, ChipRef{
			ID:       ms.ID,
			Kind:     att.Kind,
			Offset:   start,
			Len:      mlen,
			Nickname: att.Nickname,
		})
	}
	for id := range c.atts {
		if !seen[id] {
			delete(c.atts, id)
		}
	}
	for id := range c.imeta {
		if !seen[id] {
			delete(c.imeta, id)
		}
	}
	c.snapCursor()
}

// setImageMeta stores the compose-time stats for one image chip. The
// stats feed the peek card's size / dimensions / format row; they are
// deliberately NOT persisted on the Attachment (the peek only exists
// while composing).
func (c *Composer) setImageMeta(id string, meta imageMeta) {
	if c == nil || id == "" {
		return
	}
	if c.imeta == nil {
		c.imeta = make(map[string]imageMeta)
	}
	c.imeta[id] = meta
}

// imageMetaFor returns the stored stats for an image chip; the zero
// meta (size 0, no format) for chips inserted without one.
func (c *Composer) imageMetaFor(id string) imageMeta {
	if c == nil {
		return imageMeta{}
	}
	return c.imeta[id]
}

// snapCursor moves the cursor to the end of a marker when an edit
// (↑/↓ across lines, mouse repositioning, value rewrite) left it
// strictly inside one. End rather than start so the very next
// backspace still removes the chip the user is "on".
func (c *Composer) snapCursor() {
	spans, col := c.lineSpans()
	for _, sp := range spans {
		if col > sp.start && col < sp.end {
			c.ta.SetCursor(sp.end)
			return
		}
	}
}

// Serialize returns the submit-ready pair: the raw textarea value
// (markers intact - they are the persisted form) and the attachments
// referenced by live chips, in text order. A defensive Sync first so
// the result is correct even if the value was rewritten outside the
// composer's key route.
func (c *Composer) Serialize() (string, []agent.Attachment) {
	if c == nil {
		return "", nil
	}
	c.Sync()
	text := c.ta.Value()
	if len(c.refs) == 0 {
		return text, nil
	}
	atts := make([]agent.Attachment, 0, len(c.refs))
	for _, r := range c.refs {
		atts = append(atts, c.atts[r.ID])
	}
	return text, atts
}

// Reset clears the textarea AND the chip state. Replaces the bare
// ta.Reset() on every submit path so attachments can't leak into the
// next message.
func (c *Composer) Reset() {
	if c == nil {
		return
	}
	c.ta.Reset()
	c.refs = c.refs[:0]
	for id := range c.atts {
		delete(c.atts, id)
	}
	for id := range c.imeta {
		delete(c.imeta, id)
	}
}

// ----- rendering -------------------------------------------------------

// chipSigil maps a chip kind to its theme sigil + accent color. The
// sigil alone encodes the kind (NO_COLOR-safe); color is reinforcement.
func chipSigil(kind agent.AttachmentKind) (string, lipgloss.Color) {
	switch kind {
	case agent.AttachmentImage:
		return theme.ChipSigilImage, colorOK
	case agent.AttachmentMention:
		return theme.ChipSigilMention, colorAccent
	default: // paste, and any future kind until it gets its own sigil
		return theme.ChipSigilPaste, colorTool
	}
}

// chipLabel is the text half of a rendered chip: nickname, falling
// back to the kind name so a nickname-less chip still reads as
// something ("⌇ paste") rather than a bare sigil.
func chipLabel(kind agent.AttachmentKind, nickname string) string {
	if nickname != "" {
		return nickname
	}
	return string(kind)
}

// renderChip paints one styled inline chip for the composer input row:
// sigil + label in the kind color, bold so the chip pops against
// typed text. Used ONLY where the surrounding layout is ANSI-aware;
// the transcript path uses displayChips (plain) instead.
//
// visionWarn applies the slice-I-3 capability-gate treatment to IMAGE
// chips only: colorWarn with an underline, signalling "this frame's
// model can't read this" without blocking submit. lipgloss v1 has no
// dashed-underline (SGR 4:5) support, so a plain underline stands in
// for the sketchbook's dashed stroke; the NO_COLOR-safe half of the
// signal is the warning line on the chip's peek card.
func renderChip(kind agent.AttachmentKind, nickname string, visionWarn bool) string {
	sigil, color := chipSigil(kind)
	st := lipgloss.NewStyle().Foreground(color).Bold(true)
	if visionWarn && kind == agent.AttachmentImage {
		st = lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Underline(true)
	}
	return st.Render(sigil + " " + chipLabel(kind, nickname))
}

// displayChips substitutes chip markers with their PLAIN sigil+label
// form for transcript rendering. Deliberately unstyled: the result
// flows through wordWrap + renderAvatarBlock's per-line styling, and
// embedded ANSI would corrupt both the wrap math and the line styles.
// The sigil alone still encodes the kind (NO_COLOR contract). Markers
// without a matching attachment pass through literally - the reader
// sees exactly what the payload holds.
func displayChips(text string, atts []agent.Attachment) string {
	if len(atts) == 0 || !strings.Contains(text, "‹") {
		return text
	}
	byID := make(map[string]agent.Attachment, len(atts))
	for _, a := range atts {
		byID[a.ID] = a
	}
	var b strings.Builder
	prev := 0
	for _, ms := range agent.FindMarkers(text) {
		att, ok := byID[ms.ID]
		if !ok {
			continue
		}
		sigil, _ := chipSigil(att.Kind)
		b.WriteString(text[prev:ms.Start])
		b.WriteString(sigil + " " + chipLabel(att.Kind, att.Nickname))
		prev = ms.End
	}
	b.WriteString(text[prev:])
	return b.String()
}

// renderComposerInput is the chip-aware replacement for ta.View() in
// renderInput. Chip-less input (the overwhelmingly common path) gets
// the stock textarea render, pixel-identical to pre-I-1. With live
// chips we substitute markers for styled chips, mirroring the
// renderSlashInputRow approach: the textarea stays the source of
// truth for value + cursor, we only override the RENDER.
//
// Simplifications shared with the slash-row renderer: logical lines
// only (no soft wrap - long rows clip at the frame), padded to
// taHeight rows, with the window slid so the cursor row stays visible.
func (m *Model) renderComposerInput(w int) string {
	c := m.composer
	if c == nil {
		return m.ta.View()
	}
	c.Sync()
	if !c.HasChips() {
		return m.ta.View()
	}

	prompt := m.ta.Prompt
	lines := strings.Split(m.ta.Value(), "\n")
	row := m.ta.Line()
	li := m.ta.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	// Capability gate, resolved once per render: when the live
	// provider can't read images, every image chip on every row gets
	// the warn treatment in the same frame.
	visionWarn := m.imageVisionWarn()

	rendered := make([]string, len(lines))
	for i, ln := range lines {
		body := c.renderLine(ln, visionWarn)
		if i == row {
			body = c.renderLineWithCursor(m, ln, col, visionWarn)
		}
		rendered[i] = padRight(prompt+body, w)
	}

	// Slide a taHeight-row window so the cursor row is always visible;
	// pad with empty prompt rows so the band height never jitters.
	h := taHeight(m)
	start := 0
	if row >= h {
		start = row - h + 1
	}
	rows := make([]string, 0, h)
	for i := start; i < len(rendered) && len(rows) < h; i++ {
		rows = append(rows, rendered[i])
	}
	for len(rows) < h {
		rows = append(rows, padRight(prompt, w))
	}
	return strings.Join(rows, "\n")
}

// renderLine substitutes every live marker in one logical line with
// its styled chip. Markers without a stored attachment render
// literally (they are plain text as far as the composer is concerned).
// visionWarn flows through to renderChip's image-chip gate treatment.
func (c *Composer) renderLine(ln string, visionWarn bool) string {
	if !strings.Contains(ln, "‹") {
		return ln
	}
	var b strings.Builder
	prev := 0
	for _, ms := range agent.FindMarkers(ln) {
		att, ok := c.atts[ms.ID]
		if !ok {
			continue
		}
		b.WriteString(ln[prev:ms.Start])
		b.WriteString(renderChip(att.Kind, att.Nickname, visionWarn))
		prev = ms.End
	}
	b.WriteString(ln[prev:])
	return b.String()
}

// renderLineWithCursor renders the cursor's line with the textarea's
// own cursor model drawn at the cursor column, so the blink stays in
// sync with the existing textarea.Blink command. When the rune under
// the cursor opens a marker the cursor renders as a block LEFT of the
// chip instead of splitting it - a chip is one grapheme on screen too.
func (c *Composer) renderLineWithCursor(m *Model, ln string, col int, visionWarn bool) string {
	runes := []rune(ln)
	if col < 0 {
		col = 0
	}
	if col > len(runes) {
		col = len(runes)
	}
	before := string(runes[:col])
	after := string(runes[col:])

	curChar := " "
	rest := after
	if after != "" && !strings.HasPrefix(after, "‹") {
		r, size := utf8.DecodeRuneInString(after)
		curChar = string(r)
		rest = after[size:]
	}
	cur := m.ta.Cursor
	cur.SetChar(curChar)
	return c.renderLine(before, visionWarn) + cur.View() + c.renderLine(rest, visionWarn)
}
