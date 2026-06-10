// activity_strip.go renders a run of consecutive tool-call entries as
// a single indented line ("activity strip") instead of the legacy
// bordered tool-card group. The strip is the chat surface's answer to
// the v0.7.7-era complaint that a 6-call context-load preamble ate 15+
// vertical lines of transcript - the same information now sits on one
// line, with category-specific leading glyphs so users see at a glance
// whether the run was tool work, an errored run, or a skill invocation.
//
// Visual rules (Concept A from the design mockup):
//
//	▸  read ×2 · git_status · git_log     173 lines  e expand
//	✗  bash ×3                            3 errors  e expand
//	📚 calendar                           loaded    e expand
//
// Leading glyph is one of three (▸ tool, ✗ all-errored, 📚 all-skill),
// each in its own color. Per-segment color carries the per-entry kind
// for mixed groups (errored names go warn-red, inline skill chips go
// accent-blue). Same-name consecutive entries fold into "name ×N";
// distinct names list inline separated by faint middots. Right-aligned
// metadata shows error count + line count; a dim "e expand" hint
// suggests the (future) expand-to-full-rows keybind.
//
// Width handling: the strip prefers to render LEFT ... META  HINT in
// one row. When the row would overflow contentW the hint drops first,
// then the metadata, until just the left section remains - mirroring
// the bordered card's "best-effort, don't crash on tiny widths" policy.
package chat

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// skillUseToolName is the canonical name of the tool a model calls to
// load a skill body. Centralised here so the chat package isn't
// peppered with raw "skill_use" string literals - search-and-replace
// stays clean if the upstream tool ever renames.
const skillUseToolName = "skill_use"

// Leading-glyph alphabet for the strip. One per category, chosen for
// distinctness on common monospace fonts (no Nerd Font dependency).
//
//	stripGlyphTool   - generic "the agent did work here" chevron
//	stripGlyphError  - "this run failed" cross
//	stripGlyphSkill  - "a skill was invoked" books
//
// The trio answers the design directive "tool calls, errors, and skill
// invocations need different emojis" - pre-Concept-A every group led
// with the same 🔧 wrench so a failed bash run and a skill load read
// identically at a glance.
const (
	stripGlyphTool  = "▸"
	stripGlyphError = "✗"
	stripGlyphSkill = "📚"
)

// stripIndent matches the bordered tool card's left margin (sideMargin
// in view.go) so strip-rendered groups visually align with whatever
// bordered surfaces still live above or below them (approvals, error
// cards) in the same transcript.
const stripIndent = 4

// minStripContentW is the floor below which we stop trying to scale
// down. Mirrors the tool card's contentW floor; below this, the strip
// renders with whatever it has and accepts terminal-side wrap.
const minStripContentW = 30

// parseSkillName extracts the "name" field from a skill_use tool's
// raw JSON input. Returns empty when the payload is unparseable or
// missing the field - the caller falls back to the bare tool name so
// rendering never breaks on a malformed call.
func parseSkillName(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var doc struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(doc.Name)
}

// stripSegment is one logical item in the strip's tools list. Adjacent
// transcript entries with the same display label AND the same error
// state fold into a single segment with count > 1 ("read ×2");
// distinct labels stay as separate segments listed inline. Order
// matches the entries' order so the strip reads left-to-right in call
// order.
type stripSegment struct {
	label   string // tool name, or "📚 <skillname>" for skill segments
	count   int
	isError bool // every underlying entry errored
	isSkill bool // every underlying entry is a skill_use
}

// stripRollup folds consecutive transcript entries that share a
// display label AND error state into stripSegments. The label is the
// tool name for regular calls and "📚 <skillname>" for skill_use calls
// (so two skill_use calls for different skills do NOT fold - "calendar
// then onboarding" reads as two distinct segments, not "skill_use ×2").
func stripRollup(es []transcriptEntry) []stripSegment {
	if len(es) == 0 {
		return nil
	}
	out := make([]stripSegment, 0, len(es))
	cur := stripSegment{
		label:   segmentLabel(es[0]),
		count:   1,
		isError: es[0].isError,
		isSkill: es[0].isSkill,
	}
	for i := 1; i < len(es); i++ {
		e := es[i]
		label := segmentLabel(e)
		if label == cur.label && e.isError == cur.isError {
			cur.count++
			continue
		}
		out = append(out, cur)
		cur = stripSegment{
			label:   label,
			count:   1,
			isError: e.isError,
			isSkill: e.isSkill,
		}
	}
	out = append(out, cur)
	return out
}

// segmentLabel returns the display label for one entry. Skill calls
// surface their skill name ("📚 calendar") so the user sees what the
// model loaded; regular calls surface their tool name. A skill_use
// call whose JSON input failed to parse falls back to the bare tool
// name so the row still renders.
func segmentLabel(e transcriptEntry) string {
	if e.isSkill {
		name := strings.TrimSpace(e.skillName)
		if name == "" {
			name = e.tool
			if name == "" {
				name = skillUseToolName
			}
		}
		return stripGlyphSkill + " " + name
	}
	if e.tool == "" {
		return "?"
	}
	return e.tool
}

// stripGlyph picks the leading glyph and color for the strip based on
// the dominant category in the run:
//
//	every entry errored  → ✗ in warn (red)
//	every entry is skill → 📚 in accent (brand blue)
//	everything else      → ▸ in tool (amber)
//
// Mixed groups don't get a dedicated leading glyph; per-segment color
// and inline 📚 chips carry per-entry meaning. The "all-or-nothing"
// rule mirrors the legacy toolCardGroupBorderColor logic - a single
// failure inside an otherwise-successful run does NOT flip the whole
// strip to ✗, because the failed segment already paints itself in
// warn color.
func stripGlyph(es []transcriptEntry) (string, lipgloss.Color) {
	if len(es) == 0 {
		return stripGlyphTool, colorTool
	}
	allError := true
	allSkill := true
	for _, e := range es {
		if !e.isError {
			allError = false
		}
		if !e.isSkill {
			allSkill = false
		}
		if !allError && !allSkill {
			break
		}
	}
	switch {
	case allError:
		return stripGlyphError, colorWarn
	case allSkill:
		// colorAccent (brand blue) doubles as the skill color until a
		// dedicated theme.Palette.Skill slot lands. Distinct from the
		// amber tool color AND the warn-red error color, which is the
		// load-bearing property here.
		return stripGlyphSkill, colorAccent
	default:
		return stripGlyphTool, colorTool
	}
}

// renderStripSegment paints one segment in the strip's tools list.
// Errored segments go warn-red, skill segments accent-blue, regular
// tool segments amber. Rolled-up segments (count > 1) carry a faint
// " ×N" multiplier suffix in the muted color so the user can scan
// for "what ran twice" without the multiplier overpowering the name.
func renderStripSegment(seg stripSegment) string {
	var styled string
	switch {
	case seg.isError:
		styled = lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render(seg.label)
	case seg.isSkill:
		styled = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(seg.label)
	default:
		styled = lipgloss.NewStyle().Foreground(colorTool).Bold(true).Render(seg.label)
	}
	if seg.count > 1 {
		mult := lipgloss.NewStyle().Foreground(colorMuted).Render(" ×" + strconv.Itoa(seg.count))
		styled += mult
	}
	return styled
}

// stripSegmentsList composes the inline middot-separated list of
// rendered segments. Separator is a faint " · " so it visually recedes
// against the bold tool names.
func stripSegmentsList(es []transcriptEntry) string {
	segs := stripRollup(es)
	if len(segs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		parts = append(parts, renderStripSegment(s))
	}
	sep := lipgloss.NewStyle().Foreground(colorMuted).Render(" · ")
	return strings.Join(parts, sep)
}

// stripMetadata returns the right-aligned status text for the strip.
// Format depends on the aggregated state of the run:
//
//	all entries still running       → "running…"
//	any entries errored             → "N error(s) · M lines" (chip + lines)
//	all entries returned no output  → "no output"
//	otherwise                       → "M lines"
//
// Returns empty when there is nothing meaningful to surface (an
// impossible case in practice - len(es) > 0 always yields something).
func stripMetadata(es []transcriptEntry) string {
	if len(es) == 0 {
		return ""
	}
	var errCount, runningCount, doneCount, lineSum int
	for _, e := range es {
		if !e.hasResult {
			runningCount++
			continue
		}
		doneCount++
		if e.isError {
			errCount++
			continue
		}
		trimmed := strings.TrimRight(e.toolResult, "\n")
		if trimmed != "" {
			lineSum += strings.Count(trimmed, "\n") + 1
		}
	}

	muted := lipgloss.NewStyle().Foreground(colorMuted)
	warn := lipgloss.NewStyle().Foreground(colorWarn).Bold(true)

	if runningCount == len(es) {
		return muted.Render("running…")
	}

	var parts []string
	if errCount > 0 {
		word := "errors"
		if errCount == 1 {
			word = "error"
		}
		parts = append(parts, warn.Render(fmt.Sprintf("%d %s", errCount, word)))
	}
	switch {
	case lineSum > 0:
		word := "lines"
		if lineSum == 1 {
			word = "line"
		}
		parts = append(parts, muted.Render(fmt.Sprintf("%d %s", lineSum, word)))
	case doneCount > 0 && errCount == 0:
		parts = append(parts, muted.Render("no output"))
	}

	return strings.Join(parts, muted.Render(" · "))
}

// stripHint returns the faint right-most "e expand" affordance.
// The "e" is brand-accent so it reads as a keybind cue, the "expand"
// is muted so it stays out of the way. The hint is the only place
// users learn about the (future) expand-to-full-rows keybind without
// pulling up /help, which matters per the awesome-TUI research
// finding: "if a power user must read your README to discover what a
// key does, you've already lost."
func stripHint() string {
	k := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("e")
	label := lipgloss.NewStyle().Foreground(colorMuted).Render(" expand")
	return k + label
}

// composeStripLine lays out the strip's three sections (left, meta,
// hint) inside a contentW-cell budget. Tries the full layout first;
// drops the hint when it overflows; drops the meta as the final
// fallback. Below that floor the left section renders alone and the
// terminal handles any further wrap.
func composeStripLine(left, meta, hint string, contentW int) string {
	lw := lipgloss.Width(left)
	mw := lipgloss.Width(meta)
	hw := lipgloss.Width(hint)

	const metaHintPad = 2

	// Plan A: LEFT _____ META  HINT  (full layout)
	if mw > 0 && hw > 0 {
		gap := contentW - lw - mw - hw - metaHintPad
		if gap >= 2 {
			return left + strings.Repeat(" ", gap) + meta + strings.Repeat(" ", metaHintPad) + hint
		}
	}
	// Plan B: LEFT _____ META  (drop hint)
	if mw > 0 {
		gap := contentW - lw - mw
		if gap >= 1 {
			return left + strings.Repeat(" ", gap) + meta
		}
	}
	// Plan C: just the left section. Accept overflow; the terminal
	// will wrap. This matches the legacy card's behavior on tiny
	// widths (it also stopped trying to be clever).
	return left
}

// renderToolStrip is Concept A's entry point: one indented line per
// run of consecutive tool calls. Replaces the bordered tool-card
// group; the two callsites in view.go (single-entry path in
// renderEntry, group path in composeTranscript) both delegate here so
// solo and multi-entry surfaces share the same code path.
//
// Width arrives as the viewport width (terminal width minus the chat
// header/footer and any sidebar). We carve out stripIndent cells on
// each side to align with the legacy card's visual envelope.
//
// Note: hiddenChatToolNames in chat.go suppresses entries BEFORE they
// reach this function. If "skill_use" is ever added to that map all
// skill strips will vanish - keep this contract in mind when adding
// new silent tools.
func renderToolStrip(es []transcriptEntry, width int) string {
	if len(es) == 0 {
		return ""
	}
	contentW := width - stripIndent*2
	if contentW < minStripContentW {
		contentW = minStripContentW
	}

	glyph, glyphColor := stripGlyph(es)
	glyphR := lipgloss.NewStyle().Foreground(glyphColor).Bold(true).Render(glyph)

	left := glyphR + "  " + stripSegmentsList(es)
	meta := stripMetadata(es)
	hint := stripHint()

	// Single solo non-skill call: append a faint args preview so the
	// row still answers "what was the call?" - matching the legacy
	// single-entry card which always showed the input. Multi-entry
	// runs drop the preview (the inputs differ across rows; a single
	// preview would be misleading).
	//
	// Budget the preview against the actual meta + hint widths plus
	// the two inter-section gaps composeStripLine inserts. Using the
	// real measurements (instead of a worst-case magic number)
	// recovers ~10-20 cells of preview room on mid-width viewports.
	if len(es) == 1 && es[0].toolInput != "" && !es[0].isSkill {
		muted := lipgloss.NewStyle().Foreground(colorMuted)
		const gapLeftMeta = 1   // composeStripLine's minimum LEFT|META separator
		const gapMetaHint = 2   // fixed pad between meta and hint
		const previewSep = 3    // " · " between segment and preview
		previewMax := contentW - lipgloss.Width(left) - lipgloss.Width(meta) - lipgloss.Width(hint) - gapLeftMeta - gapMetaHint - previewSep
		if previewMax >= 10 {
			preview := oneLine(es[0].toolInput, previewMax)
			if preview != "" {
				left = left + muted.Render(" · ") + muted.Render(preview)
			}
		}
	}

	line := composeStripLine(left, meta, hint, contentW)

	return strings.Repeat(" ", stripIndent) + line
}
