package chat

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// peekTestAtt is a representative clipped paste for render tests:
// JSON class, two lines, tab in the first line.
func peekTestAtt() agent.Attachment {
	return agent.Attachment{
		ID:       "1",
		Kind:     agent.AttachmentPaste,
		Nickname: "json (2 keys)",
		Content:  "{\"name\":\t\"carlos\",\n \"ok\": true}",
	}
}

// TestRenderPeekCard_Structure pins the four-row card shape: dashed
// top rule ending in the corner tag, first-line preview (tabs
// flattened, later lines excluded), stats row with chars / lines /
// ~token estimate, dashed bottom rule.
func TestRenderPeekCard_Structure(t *testing.T) {
	out := stripANSI(renderPeekCard(peekTestAtt(), 80))
	rows := strings.Split(out, "\n")
	if len(rows) != 4 {
		t.Fatalf("card rows = %d, want 4:\n%s", len(rows), out)
	}
	if !strings.Contains(rows[0], "┄") || !strings.HasSuffix(rows[0], "json (2 keys)") {
		t.Errorf("top rule + corner tag wrong: %q", rows[0])
	}
	if !strings.Contains(rows[1], `{"name":  "carlos",`) {
		t.Errorf("preview missing / tab not flattened: %q", rows[1])
	}
	if strings.Contains(rows[1], "ok") {
		t.Errorf("preview leaked past the first line: %q", rows[1])
	}
	// Content is 31 runes / 2 lines / ~7 tokens.
	if !strings.Contains(rows[2], "31 chars · 2L · ~7 tok") {
		t.Errorf("stats row wrong: %q", rows[2])
	}
	if strings.Trim(strings.TrimSpace(rows[3]), "┄") != "" {
		t.Errorf("bottom rule must be all dashes: %q", rows[3])
	}
}

// TestRenderPeekCard_NoLeftStripe is the hard design rule: no row may
// open with a vertical-bar / block stripe glyph. Category is encoded
// by the corner tag + dashed accents only.
func TestRenderPeekCard_NoLeftStripe(t *testing.T) {
	for _, w := range []int{80, 40, 10} {
		for i, row := range strings.Split(stripANSI(renderPeekCard(peekTestAtt(), w)), "\n") {
			trimmed := strings.TrimLeft(row, " ")
			for _, banned := range []string{"│", "┃", "▌", "█", "▎"} {
				if strings.HasPrefix(trimmed, banned) {
					t.Errorf("w=%d row %d opens with banned stripe glyph %q: %q", w, i, banned, row)
				}
			}
			if !strings.HasPrefix(row, "  ") {
				t.Errorf("w=%d row %d missing 2-cell indent: %q", w, i, row)
			}
		}
	}
}

// TestRenderPeekCard_NoColor: with a NO_COLOR palette the card must
// carry its full structure as plain text - tag, preview, stats, rules.
func TestRenderPeekCard_NoColor(t *testing.T) {
	t.Cleanup(func() { ApplyPalette(theme.Load(theme.Options{})) })
	ApplyPalette(theme.Load(theme.Options{
		Env: func(k string) string {
			if k == "NO_COLOR" {
				return "1"
			}
			return ""
		},
	}))
	out := renderPeekCard(peekTestAtt(), 80)
	plain := stripANSI(out)
	for _, want := range []string{"json (2 keys)", `{"name":`, "~7 tok", "┄"} {
		if !strings.Contains(plain, want) {
			t.Errorf("NO_COLOR card missing %q:\n%s", want, plain)
		}
	}
}

// TestRenderPeekCard_NarrowWidths: tiny widths clamp instead of
// panicking, long tags and previews truncate with an ellipsis.
func TestRenderPeekCard_NarrowWidths(t *testing.T) {
	att := agent.Attachment{
		Kind:     agent.AttachmentPaste,
		Nickname: "an unreasonably long classifier nickname",
		Content:  strings.Repeat("wide preview line ", 20),
	}
	for _, w := range []int{0, 5, 10, 25} {
		out := stripANSI(renderPeekCard(att, w))
		rows := strings.Split(out, "\n")
		if len(rows) != 4 {
			t.Fatalf("w=%d rows = %d, want 4", w, len(rows))
		}
		if !strings.Contains(rows[0], "…") {
			t.Errorf("w=%d long tag should truncate with ellipsis: %q", w, rows[0])
		}
		if !strings.HasSuffix(rows[1], "…") {
			t.Errorf("w=%d long preview should truncate with ellipsis: %q", w, rows[1])
		}
		if !strings.Contains(rows[0], "┄┄┄┄") {
			t.Errorf("w=%d top rule lost its minimum dash run: %q", w, rows[0])
		}
	}
}

// TestEstTokens pins the chars/4 estimate.
func TestEstTokens(t *testing.T) {
	if got := estTokens(1000); got != 250 {
		t.Errorf("estTokens(1000) = %d, want 250", got)
	}
	if got := estTokens(3); got != 0 {
		t.Errorf("estTokens(3) = %d, want 0", got)
	}
}

// TestPeekPreviewLine covers the extraction helper directly.
func TestPeekPreviewLine(t *testing.T) {
	if got := peekPreviewLine("a\tb\nrest"); got != "a  b" {
		t.Errorf("preview = %q, want %q", got, "a  b")
	}
	if got := peekPreviewLine("single"); got != "single" {
		t.Errorf("single-line preview = %q", got)
	}
}

// TestModel_PeekAttachmentSticky drives the cursor around a chip and
// checks the peek's sticky window: adjacent-left, inside, and
// adjacent-right all peek; anywhere else doesn't.
func TestModel_PeekAttachmentSticky(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I2001"
	seedAgent(t, log, agentID, "peek sticky", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("see ")
	m.ta.CursorEnd()
	id := m.composer.InsertChip(agent.Attachment{
		Kind: agent.AttachmentPaste, Nickname: "test paste", Content: "line one\nline two",
	})
	mlen := len([]rune(agent.Marker(agent.AttachmentPaste, id)))

	// InsertChip leaves the cursor at the marker end: peek is up.
	if att, ok := m.peekAttachment(); !ok || att.Nickname != "test paste" {
		t.Fatalf("peek after insert = %v/%v, want test paste", att, ok)
	}
	// Left edge (col == marker start) still peeks.
	m.ta.SetCursor(4)
	if _, ok := m.peekAttachment(); !ok {
		t.Error("peek should hold at the chip's left edge")
	}
	// Strictly inside the marker peeks too.
	m.ta.SetCursor(4 + mlen/2)
	if _, ok := m.peekAttachment(); !ok {
		t.Error("peek should hold inside the marker")
	}
	// Cursor away from the chip: card gone.
	m.ta.SetCursor(0)
	if _, ok := m.peekAttachment(); ok {
		t.Error("peek must vanish when the cursor leaves the chip")
	}
}

// TestModel_PeekAllChipKinds: every chip kind raises the card (paste
// and image since I-2/I-3, mention since I-4), and the bare-model /
// nil-composer path stays safe.
func TestModel_PeekAllChipKinds(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I2002"
	seedAgent(t, log, agentID, "peek kinds", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.composer.InsertChip(agent.Attachment{Kind: agent.AttachmentMention, Nickname: "loop.go", Path: "internal/agent/loop.go"})
	if att, ok := m.peekAttachment(); !ok || att.Kind != agent.AttachmentMention {
		t.Errorf("mention chip should peek (I-4): %v/%v", att, ok)
	}
	// Nil composer / bare model is safe.
	bare := &Model{}
	if _, ok := bare.peekAttachment(); ok {
		t.Error("bare model must not peek")
	}
}

// TestModel_PeekFirstChipWinsAtBoundary: with two chips back to back,
// the cursor on the shared boundary peeks the LEFT chip - one card at
// a time, deterministically.
func TestModel_PeekFirstChipWinsAtBoundary(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I2003"
	seedAgent(t, log, agentID, "peek boundary", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	idA := m.composer.InsertChip(agent.Attachment{Kind: agent.AttachmentPaste, Nickname: "first", Content: "a"})
	m.composer.InsertChip(agent.Attachment{Kind: agent.AttachmentPaste, Nickname: "second", Content: "b"})
	mlenA := len([]rune(agent.Marker(agent.AttachmentPaste, idA)))
	m.ta.SetCursor(mlenA) // exactly between the two markers
	att, ok := m.peekAttachment()
	if !ok || att.Nickname != "first" {
		t.Errorf("boundary peek = %v/%v, want the left chip (first)", att, ok)
	}
}

// TestRenderInput_PeekBandPlacement: the card occupies the hint-band
// slot above the separator while the cursor touches a paste chip, and
// leaves when the cursor does.
func TestRenderInput_PeekBandPlacement(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I2004"
	seedAgent(t, log, agentID, "peek band", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("ab")
	m.ta.CursorEnd()
	m.composer.InsertChip(agent.Attachment{
		Kind: agent.AttachmentPaste, Nickname: "diff (1 file)", Content: "--- a\n+++ b",
	})
	out := stripANSI(m.renderInput(100))
	if !strings.Contains(out, "diff (1 file)") || !strings.Contains(out, "┄") {
		t.Errorf("peek card missing from input block:\n%s", out)
	}
	// Card stacks ABOVE the separator row.
	sepIdx := strings.Index(out, "─")
	tagIdx := strings.Index(out, "diff (1 file)")
	if tagIdx > sepIdx {
		t.Errorf("peek card must render above the separator:\n%s", out)
	}

	m.ta.SetCursor(0)
	out = stripANSI(m.renderInput(100))
	if strings.Contains(out, "┄") {
		t.Errorf("peek card should vanish when the cursor leaves the chip:\n%s", out)
	}
}

// TestRenderInput_SlashModeSuppressesPeek: when the slash band owns
// the slot, the peek card yields (explicit precedence).
func TestRenderInput_SlashModeSuppressesPeek(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I2005"
	seedAgent(t, log, agentID, "peek vs slash", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.composer.InsertChip(agent.Attachment{
		Kind: agent.AttachmentPaste, Nickname: "shadowed", Content: "x",
	})
	m.slashSuggest.open = true
	out := stripANSI(m.renderInput(100))
	if strings.Contains(out, "shadowed") || strings.Contains(out, "┄") {
		t.Errorf("peek card must yield the slot to the slash band:\n%s", out)
	}
}
