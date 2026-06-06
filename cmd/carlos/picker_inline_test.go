package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/theme"
)

func threeFrames() *frame.Config {
	return &frame.Config{
		Default: "personal",
		Active:  "personal",
		List: []frame.Frame{
			{Name: "personal", Glyph: "◉", Accent: "cream"},
			{Name: "work", Glyph: "▣", Accent: "rust"},
			{Name: "ludus", Glyph: "⛰", Accent: "navy"},
		},
	}
}

func eightFrames(active string) *frame.Config {
	return &frame.Config{
		Default: "personal",
		Active:  active,
		List: []frame.Frame{
			{Name: "personal", Glyph: "◉", Accent: "cream"},
			{Name: "work", Glyph: "▣", Accent: "rust"},
			{Name: "ludus", Glyph: "⛰", Accent: "navy"},
			{Name: "research", Glyph: "◈", Accent: "teal"},
			{Name: "writing", Glyph: "✦", Accent: "plum"},
			{Name: "side", Glyph: "⛰", Accent: "olive"},
			{Name: "client", Glyph: "⛰", Accent: "sand"},
			{Name: "scratch", Glyph: "+", Accent: "slate"},
		},
	}
}

func TestInlinePicker_RenderThreeFrames_SingleLineRow(t *testing.T) {
	m := newInlinePickerModel("carlos research", "what's on my calendar tomorrow?", threeFrames(), theme.Palette{})
	m.width = 80
	out := m.View()
	if !strings.Contains(out, "carlos research") {
		t.Errorf("missing command name in view:\n%s", out)
	}
	for _, want := range []string{"personal", "work", "ludus"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing frame %q in view:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "what's on my calendar tomorrow?") {
		t.Errorf("missing prompt in view:\n%s", out)
	}
	// Footer mentions the 1-N hint.
	if !strings.Contains(out, "1-3") {
		t.Errorf("missing footer hint in view:\n%s", out)
	}
	// One frame per line would mean N+ lines of frames; the inline
	// layout puts all three on one row. Total render shouldn't exceed
	// a small number of lines (header + row + footer + edge newlines).
	if got := strings.Count(out, "\n"); got > 6 {
		t.Errorf("inline layout exceeded expected line count: got %d lines:\n%s", got, out)
	}
}

func TestInlinePicker_EightFrames_ActiveSortsLeft(t *testing.T) {
	cfg := eightFrames("research")
	m := newInlinePickerModel("carlos please", "draft a status update", cfg, theme.Palette{})
	if m.frames[0].Name != "research" {
		t.Errorf("active frame should sort left when N>=6; got %q at index 0", m.frames[0].Name)
	}
	if m.cursor != 0 {
		t.Errorf("cursor should land on the active (now leftmost) frame; got %d", m.cursor)
	}
	m.width = 80
	out := m.View()
	// Spot-check that the floated frame is in the rendered row.
	if !strings.Contains(out, "research") {
		t.Errorf("missing research frame in view:\n%s", out)
	}
}

func TestInlinePicker_WidthFallbackVertical(t *testing.T) {
	m := newInlinePickerModel("carlos please", "ok", threeFrames(), theme.Palette{})
	m.width = 50
	out := m.View()
	for _, want := range []string{"personal", "work", "ludus"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing frame %q in vertical view:\n%s", want, out)
		}
	}
	// Vertical layout puts the marker glyph on a separate line per
	// frame; we should see one marker per frame.
	if got := strings.Count(out, "›"); got < 1 {
		t.Errorf("expected cursor marker in vertical layout:\n%s", out)
	}
}

func TestInlinePicker_OneKeyPicksFirstFrame(t *testing.T) {
	m := newInlinePickerModel("carlos research", "q", threeFrames(), theme.Palette{})
	upd, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	mm := upd.(inlinePickerModel)
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd on digit press")
	}
	if mm.cursor != 0 {
		t.Errorf("cursor after pressing '1': got %d want 0", mm.cursor)
	}
	if mm.cancelled {
		t.Error("cancelled flag should be false on digit pick")
	}
	if mm.frames[mm.cursor].Name != "personal" {
		t.Errorf("first frame name: got %q want personal", mm.frames[mm.cursor].Name)
	}
}

func TestInlinePicker_EnterAfterDownPicksSecond(t *testing.T) {
	m := newInlinePickerModel("carlos research", "q", threeFrames(), theme.Palette{})
	upd, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = upd.(inlinePickerModel)
	if m.cursor != 1 {
		t.Fatalf("cursor after down: got %d want 1", m.cursor)
	}
	upd, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = upd.(inlinePickerModel)
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd on enter")
	}
	if m.cancelled {
		t.Error("cancelled flag should be false on enter")
	}
	if m.frames[m.cursor].Name != "work" {
		t.Errorf("picked frame: got %q want work", m.frames[m.cursor].Name)
	}
}

func TestInlinePicker_EscCancels(t *testing.T) {
	m := newInlinePickerModel("carlos please", "q", threeFrames(), theme.Palette{})
	upd, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = upd.(inlinePickerModel)
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd on esc")
	}
	if !m.cancelled {
		t.Error("cancelled flag should be true on esc")
	}
}

func TestInlinePicker_CtrlCCancels(t *testing.T) {
	m := newInlinePickerModel("carlos please", "q", threeFrames(), theme.Palette{})
	upd, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = upd.(inlinePickerModel)
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd on ctrl+c")
	}
	if !m.cancelled {
		t.Error("cancelled flag should be true on ctrl+c")
	}
}

func TestInlinePicker_UpDownClampsToBounds(t *testing.T) {
	m := newInlinePickerModel("carlos research", "q", threeFrames(), theme.Palette{})
	// At cursor 0, pressing up should stay at 0.
	upd, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = upd.(inlinePickerModel)
	if m.cursor != 0 {
		t.Errorf("cursor clamped at top: got %d want 0", m.cursor)
	}
	// Walk to the end and try to overshoot.
	for i := 0; i < len(m.frames)+2; i++ {
		upd, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = upd.(inlinePickerModel)
	}
	if m.cursor != len(m.frames)-1 {
		t.Errorf("cursor clamped at bottom: got %d want %d", m.cursor, len(m.frames)-1)
	}
}

func TestInlinePicker_SingleFrameEnterReturnsIt(t *testing.T) {
	cfg := &frame.Config{
		Default: "personal",
		Active:  "personal",
		List:    []frame.Frame{{Name: "personal", Glyph: "◉", Accent: "cream"}},
	}
	m := newInlinePickerModel("carlos please", "q", cfg, theme.Palette{})
	if m.cursor != 0 || len(m.frames) != 1 {
		t.Fatalf("unexpected init state: cursor=%d frames=%d", m.cursor, len(m.frames))
	}
	upd, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := upd.(inlinePickerModel)
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd")
	}
	if mm.cancelled {
		t.Error("single-frame enter should not cancel")
	}
	if mm.frames[mm.cursor].Name != "personal" {
		t.Errorf("picked frame: got %q want personal", mm.frames[mm.cursor].Name)
	}
}

func TestInlinePicker_DigitBeyondFramesIgnored(t *testing.T) {
	m := newInlinePickerModel("carlos please", "q", threeFrames(), theme.Palette{})
	upd, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
	m = upd.(inlinePickerModel)
	if cmd != nil {
		t.Error("digit beyond frame count should not quit")
	}
	if m.cursor != 0 {
		t.Errorf("cursor should not move on out-of-range digit: got %d", m.cursor)
	}
}

func TestInlinePicker_WindowSizeSetsWidth(t *testing.T) {
	m := newInlinePickerModel("carlos please", "q", threeFrames(), theme.Palette{})
	upd, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = upd.(inlinePickerModel)
	if m.width != 120 {
		t.Errorf("width after WindowSizeMsg: got %d want 120", m.width)
	}
}

func TestInlinePicker_QuoteForHeaderTruncates(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hi", 10, `"hi"`},
		{"hello world", 7, `"hell…"`},
		{"abc", 1, `"…"`},
		{"abc", 2, `"…"`},
	}
	for _, tc := range cases {
		if got := quoteForHeader(tc.in, tc.max); got != tc.want {
			t.Errorf("quoteForHeader(%q, %d) = %q want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestInlinePicker_OrderedFramesNoSortBelowThreshold(t *testing.T) {
	cfg := threeFrames()
	cfg.Active = "ludus"
	ordered := orderedFramesFor(cfg)
	if ordered[0].Name != "personal" {
		t.Errorf("below threshold should keep config order: got %q", ordered[0].Name)
	}
}

func TestInlinePicker_ActiveNameForFallbacks(t *testing.T) {
	if got := activeNameFor(nil); got != "" {
		t.Errorf("nil config: got %q want empty", got)
	}
	cfg := &frame.Config{
		Default: "personal",
		List:    []frame.Frame{{Name: "first"}, {Name: "second"}},
	}
	if got := activeNameFor(cfg); got != "personal" {
		t.Errorf("default fallback: got %q want personal", got)
	}
	cfg = &frame.Config{List: []frame.Frame{{Name: "first"}}}
	if got := activeNameFor(cfg); got != "first" {
		t.Errorf("first-list fallback: got %q want first", got)
	}
	cfg = &frame.Config{}
	if got := activeNameFor(cfg); got != "" {
		t.Errorf("empty config: got %q want empty", got)
	}
}

func TestInlinePicker_VerticalCapsWithOverflowSentinel(t *testing.T) {
	cfg := &frame.Config{
		Default: "personal",
		Active:  "personal",
		List: []frame.Frame{
			{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"},
			{Name: "e"}, {Name: "f"}, {Name: "g"}, {Name: "h"},
			{Name: "i"}, {Name: "j"},
		},
	}
	m := newInlinePickerModel("carlos please", "q", cfg, theme.Palette{})
	m.width = 40
	out := m.View()
	if !strings.Contains(out, "… 3 more") {
		t.Errorf("expected overflow sentinel for 10 frames at vertical width:\n%s", out)
	}
}
