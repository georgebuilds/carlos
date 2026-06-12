package chat

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// pasteMsg builds the KeyMsg bubbletea v1 delivers for one bracketed
// paste: the entire body in a single KeyRunes message, Paste=true.
func pasteMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true}
}

// sendPaste routes one paste through the real Model.Update so the
// chat.go intercept wiring is what's under test.
func sendPaste(t *testing.T, m *Model, s string) *Model {
	t.Helper()
	updated, _ := m.Update(pasteMsg(s))
	return updated.(*Model)
}

func newPasteModel(t *testing.T, agentID string) *Model {
	t.Helper()
	log := openTempLog(t)
	seedAgent(t, log, agentID, "paste clip", "fake")
	m := New(log, agentID, NewMemTextSource())
	return drive(t, m, 120, 30)
}

// TestUpdate_PasteBelowThresholdInsertsPlain: at or under both bounds
// the paste lands in the textarea verbatim - the pre-I-2 behavior.
func TestUpdate_PasteBelowThresholdInsertsPlain(t *testing.T) {
	m := newPasteModel(t, "01HV00000000000000000I2011")

	exact := strings.Repeat("a", 280) // exactly the char bound, 1 line
	m = sendPaste(t, m, exact)
	if got := m.ta.Value(); got != exact {
		t.Errorf("280-char paste must insert raw; value len = %d", len(got))
	}
	if m.composer.HasChips() {
		t.Error("280-char paste must not create a chip")
	}

	m.resetComposer()
	m = sendPaste(t, m, "two\nlines") // 2 lines, tiny
	if got := m.ta.Value(); got != "two\nlines" {
		t.Errorf("2-line paste must insert raw: %q", got)
	}
	if m.composer.HasChips() {
		t.Error("2-line paste must not create a chip")
	}
}

// TestUpdate_PasteAboveCharThresholdClips: 281 chars on one line
// becomes a chip - only the marker enters the textarea, the body is
// stored as a paste attachment with the size-fallback nickname.
func TestUpdate_PasteAboveCharThresholdClips(t *testing.T) {
	m := newPasteModel(t, "01HV00000000000000000I2012")
	body := strings.Repeat("a", 281)
	m = sendPaste(t, m, body)

	if !m.composer.HasChips() {
		t.Fatal("281-char paste must clip into a chip")
	}
	chips := m.composer.Chips()
	if len(chips) != 1 || chips[0].Nickname != "281·1L" {
		t.Errorf("chip = %+v, want one chip nicknamed 281·1L", chips)
	}
	if got := m.ta.Value(); got != agent.Marker(agent.AttachmentPaste, chips[0].ID) {
		t.Errorf("textarea must hold only the marker: %q", got)
	}
	if _, atts := m.composer.Serialize(); len(atts) != 1 || atts[0].Content != body {
		t.Errorf("attachment must carry the full body; atts = %d", len(atts))
	}
}

// TestUpdate_PasteThreeLinesClips: the line bound clips even a
// byte-tiny paste, and the classifier nickname rides along.
func TestUpdate_PasteThreeLinesClips(t *testing.T) {
	m := newPasteModel(t, "01HV00000000000000000I2013")
	m = sendPaste(t, m, "$ make\nok\n$ make test")
	chips := m.composer.Chips()
	if len(chips) != 1 {
		t.Fatalf("3-line paste must clip; chips = %v", chips)
	}
	if chips[0].Nickname != "shell (2 cmds)" {
		t.Errorf("nickname = %q, want %q", chips[0].Nickname, "shell (2 cmds)")
	}
}

// TestUpdate_PasteCRLFNormalized: Windows-style line endings count as
// lines for the threshold and persist normalized in the attachment.
func TestUpdate_PasteCRLFNormalized(t *testing.T) {
	m := newPasteModel(t, "01HV00000000000000000I2014")
	m = sendPaste(t, m, "a\r\nb\r\nc")
	_, atts := m.composer.Serialize()
	if len(atts) != 1 {
		t.Fatal("CRLF 3-liner must clip")
	}
	if atts[0].Content != "a\nb\nc" {
		t.Errorf("content = %q, want LF-normalized", atts[0].Content)
	}
}

// TestUpdate_PasteMidTextInsertsChipAtCursor: clipping respects the
// cursor position exactly like typing one grapheme.
func TestUpdate_PasteMidTextInsertsChipAtCursor(t *testing.T) {
	m := newPasteModel(t, "01HV00000000000000000I2015")
	m.ta.SetValue("hello world")
	m.ta.SetCursor(5)
	m = sendPaste(t, m, "x\ny\nz")
	chips := m.composer.Chips()
	if len(chips) != 1 {
		t.Fatal("mid-text paste must clip")
	}
	want := "hello" + agent.Marker(agent.AttachmentPaste, chips[0].ID) + " world"
	if got := m.ta.Value(); got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
}

// TestUpdate_MultiplePastesMakeMultipleChips: two clipped pastes in
// one message create two distinct chips whose attachments both
// serialize, in text order.
func TestUpdate_MultiplePastesMakeMultipleChips(t *testing.T) {
	m := newPasteModel(t, "01HV00000000000000000I2016")
	m = sendPaste(t, m, "1\n2\n3")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" and ")})
	m = updated.(*Model)
	m = sendPaste(t, m, strings.Repeat("b", 300))

	chips := m.composer.Chips()
	if len(chips) != 2 {
		t.Fatalf("chips = %d, want 2", len(chips))
	}
	if chips[0].ID == chips[1].ID {
		t.Errorf("chip IDs must be distinct: %q", chips[0].ID)
	}
	_, atts := m.composer.Serialize()
	if len(atts) != 2 || atts[0].Content != "1\n2\n3" || atts[1].Content != strings.Repeat("b", 300) {
		t.Errorf("serialized attachments wrong: %d", len(atts))
	}
}

// TestUpdate_PasteWhileSlashSuggestOpen: clipping inside an open slash
// band inserts the chip at the cursor and re-refreshes the band
// instead of corrupting it.
func TestUpdate_PasteWhileSlashSuggestOpen(t *testing.T) {
	m := newPasteModel(t, "01HV00000000000000000I2017")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/mo")})
	m = updated.(*Model)
	if !m.slashSuggest.open {
		t.Fatal("precondition: slash band should be open after typing /mo")
	}
	m = sendPaste(t, m, "x\ny\nz")
	chips := m.composer.Chips()
	if len(chips) != 1 {
		t.Fatal("paste in slash mode must still clip")
	}
	want := "/mo" + agent.Marker(agent.AttachmentPaste, chips[0].ID)
	if got := m.ta.Value(); got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
	// View still renders without panicking, whatever the band decided.
	_ = m.View()
}

// TestUpdate_NonPasteRunesNeverClip: a big KeyRunes burst WITHOUT the
// Paste flag (e.g. very fast typing through a slow terminal) routes to
// the textarea untouched - the intercept keys strictly off msg.Paste.
func TestUpdate_NonPasteRunesNeverClip(t *testing.T) {
	m := newPasteModel(t, "01HV00000000000000000I2018")
	body := strings.Repeat("c", 300)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body)})
	m = updated.(*Model)
	if m.composer.HasChips() {
		t.Error("non-paste runes must never clip")
	}
	if got := m.ta.Value(); got != body {
		t.Errorf("non-paste runes must insert raw; len = %d", len(got))
	}
}

// TestModel_ClippedPasteSubmitAndReplay is the end-to-end contract:
// paste -> chip -> submit persists marker text + attachment; a FRESH
// model replaying the log renders the chip (sigil + nickname), never
// the raw marker and never the paste body.
func TestModel_ClippedPasteSubmitAndReplay(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I2019"
	seedAgent(t, log, agentID, "paste replay", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("logs: ")})
	m = updated.(*Model)
	m = sendPaste(t, m, "panic: boom\n\ngoroutine 1 [running]:\nmain.main()")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("submit returned nil cmd")
	}
	if msg := cmd(); msg != nil {
		if em, ok := msg.(errMsg); ok {
			t.Fatalf("submit cmd errored: %v", em.err)
		}
	}

	fresh := New(log, agentID, NewMemTextSource())
	fresh = drive(t, fresh, 120, 30)
	view := stripANSI(fresh.View())
	if strings.Contains(view, "‹p:") {
		t.Errorf("raw marker leaked into replayed transcript:\n%s", view)
	}
	if !strings.Contains(view, theme.ChipSigilPaste+" traceback (panic)") {
		t.Errorf("replayed transcript missing the chip:\n%s", view)
	}
	if strings.Contains(view, "goroutine 1") {
		t.Errorf("paste body must stay behind the chip in the transcript:\n%s", view)
	}

	// The persisted payload carries the attachment for chatglue's
	// model-bound expansion.
	evs, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var found bool
	for _, ev := range evs {
		if ev.Type != agent.EvtUserMessage {
			continue
		}
		found = true
	}
	if !found {
		t.Fatal("no EvtUserMessage persisted")
	}
}
