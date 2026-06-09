package chat

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/tui/slash"
)

// TestSlashSuggest_RefreshTrackingValue covers the four refresh
// regimes: closed (non-slash), opened by "/" with all builtins,
// narrowed by a prefix, and pivoted into args-hint mode by a
// trailing space.
func TestSlashSuggest_RefreshTrackingValue(t *testing.T) {
	var s slashSuggest

	s.refresh("hello", nil)
	if s.open {
		t.Errorf("non-slash input should leave the suggest closed; got open=%v", s.open)
	}

	s.refresh("/", nil)
	if !s.open {
		t.Fatal("'/' should open the suggest")
	}
	if len(s.matches) != len(slash.Builtins) {
		t.Errorf("'/' should match every builtin (%d), got %d", len(slash.Builtins), len(s.matches))
	}
	if s.inArgs {
		t.Error("inArgs should be false at '/'")
	}

	s.refresh("/fr", nil)
	if !s.open || s.verb != "fr" || s.inArgs {
		t.Errorf("'/fr' bad state: %+v", s)
	}
	if len(s.matches) == 0 {
		t.Fatal("'/fr' should have at least one match")
	}

	s.refresh("/frame ", nil)
	if !s.inArgs {
		t.Error("'/frame ' should set inArgs")
	}
	if len(s.matches) != 1 || s.matches[0].Name != "frame" {
		t.Errorf("'/frame ' should lock to /frame, got %+v", s.matches)
	}

	s.refresh("howdy", nil)
	if s.open {
		t.Error("returning to non-slash input should close the suggest")
	}
}

// TestSlashSuggest_CursorPreservedAcrossNarrowing keeps a selection
// stable when the user types another character that narrows the list
// but doesn't drop the selected spec.
func TestSlashSuggest_CursorPreservedAcrossNarrowing(t *testing.T) {
	var s slashSuggest
	s.refresh("/fr", nil)
	// Find the index of "frame" in the matches.
	wantIdx := -1
	for i, m := range s.matches {
		if m.Name == "frame" {
			wantIdx = i
			break
		}
	}
	if wantIdx < 0 {
		t.Fatal("test premise broken: '/fr' should match /frame")
	}
	s.cursor = wantIdx
	s.refresh("/fra", nil)
	spec, ok := s.selected()
	if !ok || spec.Name != "frame" {
		t.Errorf("narrowing dropped the /frame selection: %+v ok=%v", spec, ok)
	}
}

// TestSlashSuggest_NavWraps confirms ↑↓ wrap at the ends.
func TestSlashSuggest_NavWraps(t *testing.T) {
	var s slashSuggest
	s.refresh("/", nil)
	last := len(s.matches) - 1

	s.cursorUp()
	if s.cursor != last {
		t.Errorf("cursorUp from 0 should wrap to %d, got %d", last, s.cursor)
	}
	s.cursorDown()
	if s.cursor != 0 {
		t.Errorf("cursorDown from last should wrap to 0, got %d", s.cursor)
	}
}

// TestSlashSuggest_CompletionAddsSpaceWhenSpecHasArgs is the
// keystroke economy claim: Tab on /frame should land the cursor
// in arg-entry mode without the user pressing space.
func TestSlashSuggest_CompletionAddsSpaceWhenSpecHasArgs(t *testing.T) {
	var s slashSuggest
	s.refresh("/fra", nil)
	// Force selection to /frame.
	for i, m := range s.matches {
		if m.Name == "frame" {
			s.cursor = i
			break
		}
	}
	got := s.completion()
	if got != "/frame " {
		t.Errorf("completion = %q, want %q", got, "/frame ")
	}
}

func TestSlashSuggest_CompletionNoSpaceWhenArglessSpec(t *testing.T) {
	var s slashSuggest
	s.refresh("/cl", nil)
	// Force selection to /clear (no args).
	for i, m := range s.matches {
		if m.Name == "clear" {
			s.cursor = i
			break
		}
	}
	got := s.completion()
	if got != "/clear" {
		t.Errorf("completion = %q, want %q", got, "/clear")
	}
}

// TestSlashSuggest_TabExpandsTextarea is the integration glue: the
// Tab keystroke routed through Update should fully rewrite the
// textarea AND re-refresh the suggest state so the band immediately
// pivots into args-hint mode for verbs that take args.
func TestSlashSuggest_TabExpandsTextarea(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FA0"
	seedAgent(t, log, agentID, "tab complete", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("/fra")
	m.slashSuggest.refresh("/fra", nil)
	// Pin selection on /frame so the assertion isn't sensitive to
	// the order of matches in slash.Builtins.
	for i, mm := range m.slashSuggest.matches {
		if mm.Name == "frame" {
			m.slashSuggest.cursor = i
			break
		}
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm := next.(*Model)
	if mm.ta.Value() != "/frame " {
		t.Errorf("textarea after Tab = %q, want %q", mm.ta.Value(), "/frame ")
	}
	if !mm.slashSuggest.inArgs {
		t.Error("expected suggest to pivot into args mode after Tab completion")
	}
}

// TestSlashSuggest_EnterCompletesThenSubmits is the
// complete-then-submit Enter behavior the user picked over plain
// Tab-required completion. Typing "/fr" + Enter (without Tab)
// should dispatch /frame, not echo "unknown command /fr".
func TestSlashSuggest_EnterCompletesThenSubmits(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FA1"
	seedAgent(t, log, agentID, "complete then submit", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("/fra")
	m.slashSuggest.refresh("/fra", nil)
	for i, mm := range m.slashSuggest.matches {
		if mm.Name == "frame" {
			m.slashSuggest.cursor = i
			break
		}
	}

	cmd := m.submit()
	if cmd == nil {
		t.Fatal("submit returned nil cmd")
	}
	msg := cmd()
	st, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("submit produced %T, want statusMsg", msg)
	}
	// /frame with no args echoes a status line about the active frame.
	// We can't predict the exact wording in the test fake (no frame
	// wired) but we can prove the verb landed: a successful dispatch
	// never returns "unknown command".
	if strings.Contains(st.text, "unknown command") {
		t.Errorf("complete-then-submit failed: status = %q", st.text)
	}
}

// TestSlashSuggest_EscDismisses keeps the input but closes the band.
func TestSlashSuggest_EscDismisses(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FA2"
	seedAgent(t, log, agentID, "esc dismiss", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("/fr")
	m.slashSuggest.refresh("/fr", nil)
	if !m.slashSuggest.open {
		t.Fatal("test premise broken: '/fr' should open the suggest")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm := next.(*Model)
	if mm.slashSuggest.open {
		t.Error("Esc should close the suggest band")
	}
	if mm.ta.Value() != "/fr" {
		t.Errorf("Esc clobbered the textarea: %q", mm.ta.Value())
	}
	// Returning to non-slash input should re-arm the dismissed flag.
	mm.ta.SetValue("")
	mm.slashSuggest.refresh("", nil)
	if mm.slashSuggest.dismissed {
		t.Error("leaving slash mode should clear the dismissed flag")
	}
}

// TestSlashSuggest_HintRendersIntoView asserts the slash hint band
// shows up in the View output when the user types "/" - this is
// the discoverability claim that motivated the whole feature.
func TestSlashSuggest_HintRendersIntoView(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FA3"
	seedAgent(t, log, agentID, "hint visible", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("/")
	m.slashSuggest.refresh("/", nil)

	v := m.View()
	// The description row prints the selected /clear (first builtin)
	// plus its description. Verify both anchor strings appear.
	if !strings.Contains(v, "/clear") {
		t.Error("View missing /clear chip when input is '/'")
	}
	if !strings.Contains(v, "tab") || !strings.Contains(v, "complete") {
		t.Error("View missing keybind hint row")
	}
}

// TestSlashSuggest_NavWhenClosedIsNoOp pins the early-return paths
// in cursorUp/cursorDown/selected for the closed state. Without
// these the methods read as accidentally entered from the closed
// branch, which masks future bugs.
func TestSlashSuggest_NavWhenClosedIsNoOp(t *testing.T) {
	var s slashSuggest
	if _, ok := s.selected(); ok {
		t.Error("selected on closed suggest should report ok=false")
	}
	s.cursorUp()
	s.cursorDown()
	if s.cursor != 0 {
		t.Errorf("nav on closed suggest changed cursor: %d", s.cursor)
	}
}

// TestSlashSuggest_SelectedOutOfBoundsReturnsFalse covers the
// defensive index guard.
func TestSlashSuggest_SelectedOutOfBoundsReturnsFalse(t *testing.T) {
	var s slashSuggest
	s.refresh("/", nil)
	s.cursor = 9999
	if _, ok := s.selected(); ok {
		t.Error("selected with OOB cursor should report ok=false")
	}
}

// TestStyleSlashValue_NonSlashPassThrough hits the early-return
// branch for inputs that don't actually start with "/".
func TestStyleSlashValue_NonSlashPassThrough(t *testing.T) {
	if got := styleSlashValue("hello world"); got != "hello world" {
		t.Errorf("non-slash input should pass through unchanged; got %q", got)
	}
}

// TestSlashSuggest_RefreshAfterDismissShowsClosed walks the
// dismissed-path branch in refresh: a dismissed suggest stays
// closed even while the input is still a slash command.
func TestSlashSuggest_RefreshAfterDismissShowsClosed(t *testing.T) {
	var s slashSuggest
	s.refresh("/fr", nil)
	s.dismiss()
	s.refresh("/fra", nil)
	if s.open {
		t.Errorf("dismissed suggest should stay closed across refresh; %+v", s)
	}
}

// TestHandleSlashSuggestKey_ReadOnlyShortCircuit pins the read-only
// path so a snapshot/viewer surface never has its arrow keys
// silently swallowed by the suggest layer.
func TestHandleSlashSuggestKey_ReadOnlyShortCircuit(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FB4"
	seedAgent(t, log, agentID, "ro", "fake")
	m := New(log, agentID, NewMemTextSource(), WithReadOnly())
	m = drive(t, m, 120, 30)
	m.slashSuggest.open = true
	m.slashSuggest.matches = []slash.Spec{{Name: "x"}, {Name: "y"}}
	for _, k := range []string{"tab", "up", "down", "esc"} {
		if _, handled := m.handleSlashSuggestKey(k); handled {
			t.Errorf("read-only mode should not consume %q", k)
		}
	}
}

// TestHandleSlashSuggestKey_MultiMatchArrowKeys exercises the
// >1-match branches of ↑↓ so the navigation path is fully covered.
func TestHandleSlashSuggestKey_MultiMatchArrowKeys(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FB5"
	seedAgent(t, log, agentID, "arrows", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	m.ta.SetValue("/")
	m.slashSuggest.refresh("/", nil)
	start := m.slashSuggest.cursor
	if _, handled := m.handleSlashSuggestKey("down"); !handled {
		t.Error("down should be consumed with multiple matches")
	}
	if m.slashSuggest.cursor == start {
		t.Errorf("down did not advance the cursor: %d", m.slashSuggest.cursor)
	}
	if _, handled := m.handleSlashSuggestKey("up"); !handled {
		t.Error("up should be consumed with multiple matches")
	}
	if m.slashSuggest.cursor != start {
		t.Errorf("up did not move cursor back to start: %d", m.slashSuggest.cursor)
	}
}

// TestHandleSlashSuggestKey_UnknownKeyPassThrough proves the default
// branch returns handled=false so the textarea sees ordinary
// characters.
func TestHandleSlashSuggestKey_UnknownKeyPassThrough(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FB6"
	seedAgent(t, log, agentID, "pass", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	m.ta.SetValue("/")
	m.slashSuggest.refresh("/", nil)
	if _, handled := m.handleSlashSuggestKey("a"); handled {
		t.Error("letter key should pass through to the textarea")
	}
}

// TestHandleSlashSuggestKey_TabWithNoCompletion is the no-op branch:
// Tab with no matches (dismissed or unknown verb) consumes the key
// without changing anything. Without the test the truncate path on
// completion="" reads as untested.
func TestHandleSlashSuggestKey_TabWithNoCompletion(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FB1"
	seedAgent(t, log, agentID, "tab nop", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	// Force the suggest open with no matches via dismissal trick.
	m.slashSuggest.open = true
	m.slashSuggest.matches = nil
	_, handled := m.handleSlashSuggestKey("tab")
	if !handled {
		t.Errorf("Tab should be consumed even with no matches")
	}
}

// TestHandleSlashSuggestKey_NoOpWhenClosed proves the early-return
// path: a key arriving while the suggest is closed should not be
// claimed (handled=false), so the textarea sees it.
func TestHandleSlashSuggestKey_NoOpWhenClosed(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FB2"
	seedAgent(t, log, agentID, "closed", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	// Suggest closed by default.
	if _, handled := m.handleSlashSuggestKey("tab"); handled {
		t.Errorf("Tab should NOT be handled when suggest is closed")
	}
}

// TestHandleSlashSuggestKey_UpDownSingleMatchPassthrough proves we
// only intercept arrow keys when there's actually a list to navigate.
// Single-match → up/down fall through to the textarea so the user's
// natural cursor motion keeps working.
func TestHandleSlashSuggestKey_UpDownSingleMatchPassthrough(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FB3"
	seedAgent(t, log, agentID, "single match", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	// Force single-match state (in args mode locks to one spec).
	m.ta.SetValue("/frame ")
	m.slashSuggest.refresh("/frame ", nil)
	if _, handled := m.handleSlashSuggestKey("up"); handled {
		t.Errorf("up should pass through with single match")
	}
	if _, handled := m.handleSlashSuggestKey("down"); handled {
		t.Errorf("down should pass through with single match")
	}
}

// TestSuggestRender_NarrowViewportClamps stresses the width-floor
// path in renderSlashHint + chips so a tiny terminal still produces
// something sensible.
func TestSuggestRender_NarrowViewportClamps(t *testing.T) {
	var s slashSuggest
	s.refresh("/", nil)
	out := renderSlashHint(s, 10) // below the contentW floor
	if out == "" {
		t.Error("hint should still render on narrow viewport")
	}
}

// TestSuggestRender_UnknownVerbWarning verifies the "no matches"
// chip-row branch fires when the user has typed a verb that does not
// exist with a trailing space.
func TestSuggestRender_UnknownVerbWarning(t *testing.T) {
	var s slashSuggest
	s.refresh("/nopecmd ", nil)
	// In args mode the chip row collapses; force back into non-args
	// mode by clearing inArgs so the renderer takes the warning path.
	s.inArgs = false
	row := renderSlashChips(s, 60)
	if !strings.Contains(row, "no matches for /nopecmd") {
		t.Errorf("expected 'no matches' warning, got %q", row)
	}
}

// TestStyleSlashGhost_EmptyReturnsEmpty pins the early-return path
// so the ghost renderer doesn't emit lone style escapes.
func TestStyleSlashGhost_EmptyReturnsEmpty(t *testing.T) {
	if got := styleSlashGhost(""); got != "" {
		t.Errorf("empty ghost should render to empty; got %q", got)
	}
}

// TestPadRight_WiderInputPassesThrough covers the no-pad branch.
func TestPadRight_WiderInputPassesThrough(t *testing.T) {
	in := "longer than target"
	if got := padRight(in, 5); got != in {
		t.Errorf("padRight should pass through wide input; got %q", got)
	}
}

// TestTruncateRight_ShortInputPassThrough covers the no-truncate
// branch + the tiny-width ellipsis-only edge case.
func TestTruncateRight_AllBranches(t *testing.T) {
	if got := truncateRight("short", 10); got != "short" {
		t.Errorf("no-truncate: got %q", got)
	}
	if got := truncateRight("abcdefghij", 5); got != "abcd…" {
		t.Errorf("normal-truncate: got %q", got)
	}
	if got := truncateRight("abc", 1); got != "…" {
		t.Errorf("tiny-width: got %q", got)
	}
	if got := truncateRight("abc", 0); got != "" {
		t.Errorf("zero-width: got %q", got)
	}
}

// TestSlashSuggest_NoBandWhenInputIsPlainText guards against the
// hint band rendering when slash mode is not active - it would be
// distracting noise on every model-bound message.
func TestSlashSuggest_NoBandWhenInputIsPlainText(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000000FA4"
	seedAgent(t, log, agentID, "no band", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)

	m.ta.SetValue("hello carlos")
	m.slashSuggest.refresh("hello carlos", nil)

	v := m.View()
	if strings.Contains(v, "tab complete") {
		t.Error("hint band leaked into a plain-text composer")
	}
}
