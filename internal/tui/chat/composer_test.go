package chat

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// newTestComposer builds a Composer around a fresh focused textarea -
// the unit-level harness for chip mechanics that don't need a full
// chat Model.
func newTestComposer() (*Composer, *textarea.Model) {
	ta := textarea.New()
	ta.Prompt = "│ "
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.Focus()
	c := NewComposer(&ta)
	return c, &ta
}

// taCol returns the textarea cursor's rune column on its current line
// - the same derivation the composer uses internally.
func taCol(ta *textarea.Model) int {
	li := ta.LineInfo()
	return li.StartColumn + li.ColumnOffset
}

// ansiRe strips SGR sequences and OSC sequences (notably the OSC 8
// hyperlinks slice 9l injects around paths) so assertions about
// rendered VISIBLE content hold regardless of the test environment's
// color profile. Tests that pin the hyperlinks themselves assert on
// the raw string instead.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func pasteAtt(nick, content string) agent.Attachment {
	return agent.Attachment{Kind: agent.AttachmentPaste, Nickname: nick, Content: content}
}

// TestComposer_InsertChipPlacesMarkerAtCursor: inserting mid-text
// lands the marker exactly at the cursor and registers one ref with
// rune-accurate offsets.
func TestComposer_InsertChipPlacesMarkerAtCursor(t *testing.T) {
	c, ta := newTestComposer()
	ta.SetValue("abcd")
	ta.SetCursor(2)

	id := c.InsertChip(pasteAtt("paste#1", "big"))
	if id == "" {
		t.Fatal("InsertChip returned empty id")
	}
	want := "ab" + agent.Marker(agent.AttachmentPaste, id) + "cd"
	if got := ta.Value(); got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
	chips := c.Chips()
	if len(chips) != 1 {
		t.Fatalf("chips = %v, want 1", chips)
	}
	if chips[0].Offset != 2 || chips[0].Len != len([]rune(agent.Marker(agent.AttachmentPaste, id))) {
		t.Errorf("ref offsets wrong: %+v", chips[0])
	}
	if chips[0].Kind != agent.AttachmentPaste || chips[0].Nickname != "paste#1" {
		t.Errorf("ref metadata wrong: %+v", chips[0])
	}
	if !c.HasChips() {
		t.Error("HasChips should be true after insert")
	}
	// Cursor sits just after the marker, like after typing a grapheme.
	if got, wantCol := taCol(ta), 2+chips[0].Len; got != wantCol {
		t.Errorf("cursor col = %d, want %d", got, wantCol)
	}
}

// TestComposer_InsertChipKeepsCallerID and rejects unknown kinds.
func TestComposer_InsertChipKeepsCallerID(t *testing.T) {
	c, ta := newTestComposer()
	if got := c.InsertChip(agent.Attachment{ID: "zz", Kind: agent.AttachmentImage, Nickname: "shot"}); got != "zz" {
		t.Errorf("caller-supplied ID dropped: got %q", got)
	}
	if !strings.Contains(ta.Value(), "‹i:zz›") {
		t.Errorf("marker missing: %q", ta.Value())
	}
	if got := c.InsertChip(agent.Attachment{Kind: agent.AttachmentKind("video")}); got != "" {
		t.Errorf("unknown kind should refuse to insert; got id %q", got)
	}
}

// TestComposer_NewIDSkipsCollisions: auto IDs dodge both stored
// attachments and stale markers already present in the text.
func TestComposer_NewIDSkipsCollisions(t *testing.T) {
	c, ta := newTestComposer()
	// "1" is busy in the text (stale recalled marker), "2" in the map.
	ta.SetValue("old ‹p:1› ")
	ta.CursorEnd()
	c.atts["2"] = pasteAtt("held", "x")
	id := c.InsertChip(pasteAtt("new", "y"))
	if id == "1" || id == "2" {
		t.Errorf("newID collided: %q", id)
	}
	if id != "3" {
		t.Errorf("expected the next free base36 id (3); got %q", id)
	}
}

// TestComposer_BackspaceDeletesWholeChip is the headline atomicity
// contract: ONE backspace with the cursor just after a chip removes
// the entire marker and its attachment.
func TestComposer_BackspaceDeletesWholeChip(t *testing.T) {
	c, ta := newTestComposer()
	ta.SetValue("ab")
	ta.CursorEnd()
	c.InsertChip(pasteAtt("paste#1", "big"))
	// Cursor is just after the marker.
	if handled := c.HandleKey(tea.KeyMsg{Type: tea.KeyBackspace}); !handled {
		t.Fatal("backspace adjacent to chip should be handled by the composer")
	}
	if got := ta.Value(); got != "ab" {
		t.Errorf("value after one backspace = %q, want %q", got, "ab")
	}
	if c.HasChips() || len(c.atts) != 0 {
		t.Errorf("chip state should be empty; refs=%v atts=%v", c.refs, c.atts)
	}
	if got := taCol(ta); got != 2 {
		t.Errorf("cursor col = %d, want 2", got)
	}
}

// TestComposer_BackspaceAwayFromChipFallsThrough: a backspace NOT
// adjacent to a chip returns false so the textarea handles it.
func TestComposer_BackspaceAwayFromChipFallsThrough(t *testing.T) {
	c, ta := newTestComposer()
	c.InsertChip(pasteAtt("p", "x"))
	ta.InsertString("tail")
	if c.HandleKey(tea.KeyMsg{Type: tea.KeyBackspace}) {
		t.Error("backspace over plain text must fall through to the textarea")
	}
	// Same for the other three ops away from any chip edge.
	for _, k := range []tea.KeyType{tea.KeyDelete, tea.KeyLeft, tea.KeyRight} {
		if c.HandleKey(tea.KeyMsg{Type: k}) {
			t.Errorf("key %v away from chips must fall through", k)
		}
	}
}

// TestComposer_DeleteForwardRemovesWholeChip: one forward-delete with
// the cursor at the chip's left edge removes the whole chip.
func TestComposer_DeleteForwardRemovesWholeChip(t *testing.T) {
	c, ta := newTestComposer()
	ta.SetValue("ab")
	ta.CursorEnd()
	c.InsertChip(pasteAtt("p", "x"))
	ta.InsertString("cd")
	ta.SetCursor(2) // at the opening ‹
	if !c.HandleKey(tea.KeyMsg{Type: tea.KeyDelete}) {
		t.Fatal("delete at chip start should be handled")
	}
	if got := ta.Value(); got != "abcd" {
		t.Errorf("value = %q, want abcd", got)
	}
	if len(c.atts) != 0 {
		t.Errorf("attachment should be dropped with the chip; atts=%v", c.atts)
	}
	if got := taCol(ta); got != 2 {
		t.Errorf("cursor col = %d, want 2", got)
	}
}

// TestComposer_ArrowsHopOverChip: ← from the right edge lands on the
// left edge in one keypress; → mirrors it. The chip is one grapheme.
func TestComposer_ArrowsHopOverChip(t *testing.T) {
	c, ta := newTestComposer()
	ta.SetValue("ab")
	ta.CursorEnd()
	id := c.InsertChip(pasteAtt("p", "x"))
	mlen := len([]rune(agent.Marker(agent.AttachmentPaste, id)))

	if !c.HandleKey(tea.KeyMsg{Type: tea.KeyLeft}) {
		t.Fatal("← at chip right edge should be handled")
	}
	if got := taCol(ta); got != 2 {
		t.Errorf("after ←: col = %d, want 2", got)
	}
	if !c.HandleKey(tea.KeyMsg{Type: tea.KeyRight}) {
		t.Fatal("→ at chip left edge should be handled")
	}
	if got := taCol(ta); got != 2+mlen {
		t.Errorf("after →: col = %d, want %d", got, 2+mlen)
	}
}

// TestComposer_SyncSnapsCursorOutOfMarker: any edit that strands the
// cursor inside a marker gets snapped to the marker end so the next
// keystroke can't split the chip.
func TestComposer_SyncSnapsCursorOutOfMarker(t *testing.T) {
	c, ta := newTestComposer()
	ta.SetValue("ab")
	ta.CursorEnd()
	id := c.InsertChip(pasteAtt("p", "x"))
	mlen := len([]rune(agent.Marker(agent.AttachmentPaste, id)))
	ta.SetCursor(4) // strictly inside the marker
	c.Sync()
	if got := taCol(ta); got != 2+mlen {
		t.Errorf("cursor not snapped out: col = %d, want %d", got, 2+mlen)
	}
}

// TestComposer_SyncPrunesVanishedChips: an edit the intercepted keys
// don't cover (here: a wholesale SetValue) drops the orphaned
// attachment instead of leaking it into the next Serialize.
func TestComposer_SyncPrunesVanishedChips(t *testing.T) {
	c, ta := newTestComposer()
	c.InsertChip(pasteAtt("keep", "k"))
	c.InsertChip(pasteAtt("kill", "x"))
	val := ta.Value()
	// Wipe the second marker the way a kill-line would.
	spans := agent.FindMarkers(val)
	ta.SetValue(val[:spans[1].Start])
	c.Sync()
	if len(c.atts) != 1 || len(c.refs) != 1 {
		t.Fatalf("prune failed: atts=%v refs=%v", c.atts, c.refs)
	}
	if c.refs[0].Nickname != "keep" {
		t.Errorf("wrong chip survived: %+v", c.refs[0])
	}
}

// TestComposer_SerializeReturnsTextAndAttachmentsInOrder: markers stay
// in the text (persisted form); attachments come back in text order;
// stale markers without attachments are excluded.
func TestComposer_SerializeReturnsTextAndAttachmentsInOrder(t *testing.T) {
	c, ta := newTestComposer()
	c.InsertChip(agent.Attachment{Kind: agent.AttachmentMention, Nickname: "loop.go", Path: "agent/loop.go"})
	ta.InsertString(" then ")
	c.InsertChip(pasteAtt("paste#2", "body"))
	ta.InsertString(" plus stale ‹p:zz›")

	text, atts := c.Serialize()
	if !strings.Contains(text, "‹m:1›") || !strings.Contains(text, "‹p:2›") || !strings.Contains(text, "‹p:zz›") {
		t.Errorf("markers must survive in serialized text: %q", text)
	}
	if len(atts) != 2 {
		t.Fatalf("atts = %v, want 2 (stale marker excluded)", atts)
	}
	if atts[0].Kind != agent.AttachmentMention || atts[1].Nickname != "paste#2" {
		t.Errorf("attachment order should follow text order: %v", atts)
	}
}

// TestComposer_ResetClearsChipState alongside the textarea.
func TestComposer_ResetClearsChipState(t *testing.T) {
	c, ta := newTestComposer()
	c.InsertChip(pasteAtt("p", "x"))
	c.Reset()
	if ta.Value() != "" || c.HasChips() || len(c.atts) != 0 {
		t.Errorf("reset incomplete: value=%q refs=%v atts=%v", ta.Value(), c.refs, c.atts)
	}
	// IDs keep advancing after a reset (no reuse within the session).
	if id := c.InsertChip(pasteAtt("q", "y")); id != "2" {
		t.Errorf("post-reset id = %q, want 2 (seq continues)", id)
	}
}

// TestComposer_NilReceiverSafety: every method must no-op on nil so
// bare test Models (no New()) skip the chip machinery.
func TestComposer_NilReceiverSafety(t *testing.T) {
	var c *Composer
	if c.HasChips() || c.Chips() != nil || c.HandleKey(tea.KeyMsg{Type: tea.KeyBackspace}) {
		t.Error("nil composer should report no chips and handle no keys")
	}
	if id := c.InsertChip(pasteAtt("p", "x")); id != "" {
		t.Errorf("nil InsertChip = %q, want empty", id)
	}
	c.Sync()  // must not panic
	c.Reset() // must not panic
	if text, atts := c.Serialize(); text != "" || atts != nil {
		t.Errorf("nil Serialize = %q/%v, want empty", text, atts)
	}
}

// TestModel_ResetComposerNilFallback: a bare Model (no New(), so no
// composer) still clears its textarea through resetComposer.
func TestModel_ResetComposerNilFallback(t *testing.T) {
	m := &Model{ta: textarea.New()}
	m.ta.SetValue("leftover")
	m.resetComposer()
	if got := m.ta.Value(); got != "" {
		t.Errorf("nil-composer reset left %q in the textarea", got)
	}
}

// TestModel_KeyRouteChipAtomicity drives the chip keys through the
// real Model.Update so the chat.go wiring (intercept-before-textarea)
// is what's under test, not just the composer in isolation.
func TestModel_KeyRouteChipAtomicity(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I0001"
	seedAgent(t, log, agentID, "chips", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("hi ")
	m.ta.CursorEnd()
	m.composer.InsertChip(pasteAtt("paste#1", "BODY"))

	// One backspace through the real Update removes the whole chip.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(*Model)
	if got := m.ta.Value(); got != "hi " {
		t.Errorf("after backspace through Update: value = %q, want %q", got, "hi ")
	}
	if m.composer.HasChips() {
		t.Error("chip should be gone after one backspace")
	}

	// Plain typing still routes to the textarea unharmed.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(*Model)
	if got := m.ta.Value(); got != "hi x" {
		t.Errorf("typing after chip delete: value = %q, want %q", got, "hi x")
	}
}

// TestModel_SubmitPersistsAttachments walks submit end to end: the
// payload row carries Text-with-markers AND the attachments, and the
// optimistic transcript entry holds them too.
func TestModel_SubmitPersistsAttachments(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I0002"
	seedAgent(t, log, agentID, "chips submit", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("see ")
	m.ta.CursorEnd()
	id := m.composer.InsertChip(pasteAtt("paste#1", "the big paste body"))
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("submit returned nil cmd")
	}
	if msg := cmd(); msg != nil {
		if em, ok := msg.(errMsg); ok {
			t.Fatalf("submit cmd errored: %v", em.err)
		}
	}
	// Composer must be clean for the next message.
	if m.ta.Value() != "" || m.composer.HasChips() {
		t.Errorf("composer not reset after submit: value=%q", m.ta.Value())
	}
	// Optimistic transcript row carries the attachments.
	last := m.transcript[len(m.transcript)-1]
	if last.kind != entryUserMessage || len(last.attachments) != 1 {
		t.Errorf("optimistic entry missing attachments: %+v", last)
	}

	evs, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var found bool
	for _, ev := range evs {
		if ev.Type != agent.EvtUserMessage {
			continue
		}
		var p agent.MessagePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		found = true
		if !strings.Contains(p.Text, agent.Marker(agent.AttachmentPaste, id)) {
			t.Errorf("persisted text lost the marker: %q", p.Text)
		}
		if len(p.Attachments) != 1 || p.Attachments[0].Content != "the big paste body" {
			t.Errorf("persisted attachments wrong: %+v", p.Attachments)
		}
	}
	if !found {
		t.Fatal("no EvtUserMessage persisted")
	}
}

// TestModel_QueuedChipMessageKeepsAttachments: a mid-turn submit
// parks text AND attachments; the flush dispatches both.
func TestModel_QueuedChipMessageKeepsAttachments(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I0003"
	seedAgent(t, log, agentID, "chips queue", "fake")
	src := NewMemTextSource()
	m := New(log, agentID, src)
	m = drive(t, m, 120, 30)

	src.Append(agentID, "streaming…") // assistant busy
	m.composer.InsertChip(pasteAtt("p", "payload"))
	if cmd := m.submit(); cmd != nil {
		t.Fatal("busy submit should queue, not dispatch")
	}
	if len(m.queuedUserMessages) != 1 || len(m.queuedUserMessages[0].atts) != 1 {
		t.Fatalf("queue lost the attachments: %+v", m.queuedUserMessages)
	}

	src.Reset(agentID) // idle again
	if cmd := m.flushQueuedUserMessage(); cmd == nil {
		t.Fatal("flush should dispatch the queued chip message")
	}
	last := m.transcript[len(m.transcript)-1]
	if len(last.attachments) != 1 || last.attachments[0].Content != "payload" {
		t.Errorf("flushed entry lost attachments: %+v", last)
	}
}

// TestModel_ReplayRendersChipsNotMarkers is the transcript-replay
// contract: rebuild a fresh Model from the log and the user row paints
// the sigil+nickname chip, never the raw ‹p:ID› marker.
func TestModel_ReplayRendersChipsNotMarkers(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I0004"
	seedAgent(t, log, agentID, "chips replay", "fake")

	payload, err := json.Marshal(agent.MessagePayload{
		Text: "look at ‹p:1› now",
		Attachments: []agent.Attachment{
			{ID: "1", Kind: agent.AttachmentPaste, Nickname: "paste#1", Content: "huge"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: agentID, TS: time.Now().UTC(), Type: agent.EvtUserMessage, Payload: payload,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	view := stripANSI(m.View())
	if strings.Contains(view, "‹p:1›") {
		t.Errorf("raw marker leaked into the rendered transcript:\n%s", view)
	}
	if !strings.Contains(view, theme.ChipSigilPaste+" paste#1") {
		t.Errorf("chip (sigil+nickname) missing from replayed transcript:\n%s", view)
	}
}

// TestRenderComposerInput_SubstitutesChips: with a live chip the input
// band paints sigil+nickname instead of the marker, prompt-prefixed
// and padded to the textarea height.
func TestRenderComposerInput_SubstitutesChips(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I0005"
	seedAgent(t, log, agentID, "chips input", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	// Chip-less: byte-identical to the stock textarea render.
	m.ta.SetValue("plain")
	if got, want := m.renderComposerInput(100), m.ta.View(); got != want {
		t.Errorf("chip-less composer render must match ta.View()\n got: %q\nwant: %q", got, want)
	}

	m.ta.CursorEnd()
	m.composer.InsertChip(agent.Attachment{Kind: agent.AttachmentImage, Nickname: "shot.png"})
	out := stripANSI(m.renderComposerInput(100))
	if strings.Contains(out, "‹i:") {
		t.Errorf("raw marker in composer render:\n%s", out)
	}
	if !strings.Contains(out, theme.ChipSigilImage+" shot.png") {
		t.Errorf("styled chip missing:\n%s", out)
	}
	if !strings.Contains(out, "plain") {
		t.Errorf("typed text missing:\n%s", out)
	}
	if got := len(strings.Split(out, "\n")); got != 3 {
		t.Errorf("band height = %d rows, want 3 (taHeight)", got)
	}
}

// TestRenderComposerInput_CursorWindowFollowsMultiline: with more
// logical lines than the band height, the window slides so the cursor
// row stays visible.
func TestRenderComposerInput_CursorWindowFollowsMultiline(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I0006"
	seedAgent(t, log, agentID, "chips multiline", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("one\ntwo\nthree\nfour")
	m.ta.CursorEnd() // cursor on row 3 ("four")
	m.composer.InsertChip(pasteAtt("tail", "x"))
	out := stripANSI(m.renderComposerInput(100))
	if strings.Contains(out, "one") {
		t.Errorf("window should have slid past row 0:\n%s", out)
	}
	if !strings.Contains(out, "four") {
		t.Errorf("cursor row must be visible:\n%s", out)
	}
	if !strings.Contains(out, theme.ChipSigilPaste+" tail") {
		t.Errorf("chip on cursor row missing:\n%s", out)
	}
}

// TestRenderLineWithCursor_CursorLeftOfChip: when the rune under the
// cursor opens a marker, the cursor draws as a block BEFORE the chip
// instead of splitting it.
func TestRenderLineWithCursor_CursorLeftOfChip(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I0007"
	seedAgent(t, log, agentID, "chips cursor", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.composer.InsertChip(pasteAtt("p", "x"))
	m.ta.SetCursor(0) // at the opening ‹
	out := stripANSI(m.renderComposerInput(100))
	if strings.Contains(out, "‹") {
		t.Errorf("marker split or leaked around the cursor:\n%s", out)
	}
	if !strings.Contains(out, theme.ChipSigilPaste+" p") {
		t.Errorf("chip should render whole with the cursor at its left edge:\n%s", out)
	}
}

// TestDisplayChips_PlainSubstitution covers the transcript-side
// (unstyled) substitution: every kind maps to its sigil, unknown
// markers pass through literally, ANSI never appears.
func TestDisplayChips_PlainSubstitution(t *testing.T) {
	atts := []agent.Attachment{
		{ID: "1", Kind: agent.AttachmentPaste, Nickname: "logs"},
		{ID: "2", Kind: agent.AttachmentImage, Nickname: "shot"},
		{ID: "3", Kind: agent.AttachmentMention, Nickname: "main.go"},
	}
	got := displayChips("a ‹p:1› b ‹i:2› c ‹m:3› d ‹p:gone›", atts)
	want := "a " + theme.ChipSigilPaste + " logs b " + theme.ChipSigilImage + " shot c " +
		theme.ChipSigilMention + " main.go d ‹p:gone›"
	if got != want {
		t.Errorf("displayChips:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("transcript substitution must be ANSI-free: %q", got)
	}
	// Fast paths: no atts / no markers.
	if got := displayChips("plain", atts); got != "plain" {
		t.Errorf("marker-less text must pass through: %q", got)
	}
	if got := displayChips("raw ‹p:1›", nil); got != "raw ‹p:1›" {
		t.Errorf("attachment-less text must pass through: %q", got)
	}
}

// TestRenderChip_NoColorContent pins the NO_COLOR contract: stripped
// of any styling, a chip still reads as "<sigil> <label>" with the
// kind-specific sigil, and a nickname-less chip falls back to the
// kind name.
func TestRenderChip_NoColorContent(t *testing.T) {
	cases := []struct {
		kind agent.AttachmentKind
		nick string
		want string
	}{
		{agent.AttachmentPaste, "paste#1", theme.ChipSigilPaste + " paste#1"},
		{agent.AttachmentImage, "shot", theme.ChipSigilImage + " shot"},
		{agent.AttachmentMention, "x.go", theme.ChipSigilMention + " x.go"},
		{agent.AttachmentPaste, "", theme.ChipSigilPaste + " paste"},
	}
	for _, tc := range cases {
		if got := stripANSI(renderChip(tc.kind, tc.nick, false)); got != tc.want {
			t.Errorf("renderChip(%s, %q) = %q, want %q", tc.kind, tc.nick, got, tc.want)
		}
	}
}

// TestChipSigil_KindMapping pins sigil + color pairing per kind plus
// the defensive default.
func TestChipSigil_KindMapping(t *testing.T) {
	if s, c := chipSigil(agent.AttachmentPaste); s != theme.ChipSigilPaste || c != colorTool {
		t.Errorf("paste sigil/color = %q/%v", s, c)
	}
	if s, c := chipSigil(agent.AttachmentImage); s != theme.ChipSigilImage || c != colorOK {
		t.Errorf("image sigil/color = %q/%v", s, c)
	}
	if s, c := chipSigil(agent.AttachmentMention); s != theme.ChipSigilMention || c != colorAccent {
		t.Errorf("mention sigil/color = %q/%v", s, c)
	}
	if s, _ := chipSigil(agent.AttachmentKind("future")); s != theme.ChipSigilPaste {
		t.Errorf("unknown kind should default to the paste sigil; got %q", s)
	}
}

// TestComposer_EmacsAliasesInterceptToo: ctrl+h / ctrl+d / ctrl+b /
// ctrl+f get the same chip atomicity as their arrow/edit twins.
func TestComposer_EmacsAliasesInterceptToo(t *testing.T) {
	c, ta := newTestComposer()
	c.InsertChip(pasteAtt("p", "x"))
	if !c.HandleKey(tea.KeyMsg{Type: tea.KeyCtrlB}) {
		t.Error("ctrl+b at chip right edge should hop")
	}
	if !c.HandleKey(tea.KeyMsg{Type: tea.KeyCtrlF}) {
		t.Error("ctrl+f at chip left edge should hop")
	}
	if !c.HandleKey(tea.KeyMsg{Type: tea.KeyCtrlH}) {
		t.Error("ctrl+h adjacent to chip should delete it whole")
	}
	if ta.Value() != "" || len(c.atts) != 0 {
		t.Errorf("chip survived ctrl+h: %q", ta.Value())
	}
	// ctrl+d at the start of a fresh chip.
	c.InsertChip(pasteAtt("q", "y"))
	ta.SetCursor(0)
	if !c.HandleKey(tea.KeyMsg{Type: tea.KeyCtrlD}) {
		t.Error("ctrl+d at chip left edge should delete it whole")
	}
	if ta.Value() != "" {
		t.Errorf("chip survived ctrl+d: %q", ta.Value())
	}
}

// TestComposer_MultilineChipOpsStayLineLocal: chips on a later line
// keep working (the span math is per logical line).
func TestComposer_MultilineChipOpsStayLineLocal(t *testing.T) {
	c, ta := newTestComposer()
	ta.SetValue("first line\nsecond ")
	ta.CursorEnd()
	c.InsertChip(pasteAtt("deep", "x"))
	if !c.HandleKey(tea.KeyMsg{Type: tea.KeyBackspace}) {
		t.Fatal("backspace after chip on line 2 should be handled")
	}
	if got := ta.Value(); got != "first line\nsecond " {
		t.Errorf("value = %q", got)
	}
}

// TestComposer_HandleKeyIgnoresNonChipKeys: with live chips, keys
// outside the four intercepted ops fall through untouched.
func TestComposer_HandleKeyIgnoresNonChipKeys(t *testing.T) {
	c, _ := newTestComposer()
	c.InsertChip(pasteAtt("p", "x"))
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("a")},
		{Type: tea.KeyEnter},
		{Type: tea.KeyUp},
		{Type: tea.KeyTab},
	} {
		if c.HandleKey(msg) {
			t.Errorf("key %q must fall through to the textarea", msg.String())
		}
	}
}

// TestRenderLineWithCursor_Branches covers the cursor-character
// selection directly: over a normal rune, at end of line, and the
// defensive column clamps.
func TestRenderLineWithCursor_Branches(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I0008"
	seedAgent(t, log, agentID, "chips cursor branches", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	c := m.composer
	c.atts["1"] = pasteAtt("p", "x")

	line := "ab‹p:1›cd"
	// Cursor over a normal rune: the rune renders under the cursor
	// exactly once (no duplication, no drop).
	if got := stripANSI(c.renderLineWithCursor(m, line, 0, false)); !strings.HasPrefix(got, "a") || strings.Count(got, "a") != 1 {
		t.Errorf("cursor over 'a' mangled the line: %q", got)
	}
	// Cursor at end of line: a blank cursor cell is appended.
	atEnd := stripANSI(c.renderLineWithCursor(m, line, len([]rune(line)), false))
	if !strings.HasSuffix(atEnd, "cd ") {
		t.Errorf("EOL cursor should append a blank cell: %q", atEnd)
	}
	// Defensive clamps: out-of-range columns behave as 0 / EOL.
	if got := stripANSI(c.renderLineWithCursor(m, line, -5, false)); !strings.HasPrefix(got, "a") {
		t.Errorf("negative col should clamp to 0: %q", got)
	}
	if got := stripANSI(c.renderLineWithCursor(m, line, 999, false)); !strings.HasSuffix(got, "cd ") {
		t.Errorf("oversized col should clamp to EOL: %q", got)
	}
	// All four substitute the chip, never the marker.
	for _, col := range []int{0, 2, 9, -5, 999} {
		if got := stripANSI(c.renderLineWithCursor(m, line, col, false)); strings.Contains(got, "‹p:1›") {
			t.Errorf("raw marker at col %d: %q", col, got)
		}
	}
}

// TestModel_HistoryRecallWithStaleMarkersDegrades: recalling a
// submitted chip message from input history brings markers back
// WITHOUT attachments (they left with the submit); the composer must
// treat them as plain text - no refs, no serialize leakage.
func TestModel_HistoryRecallWithStaleMarkersDegrades(t *testing.T) {
	c, ta := newTestComposer()
	ta.SetValue("recalled ‹p:1› text")
	ta.CursorEnd()
	c.Sync()
	if c.HasChips() {
		t.Error("stale markers must not produce chips")
	}
	text, atts := c.Serialize()
	if text != "recalled ‹p:1› text" || atts != nil {
		t.Errorf("stale serialize = %q/%v", text, atts)
	}
}
