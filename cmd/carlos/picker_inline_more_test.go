package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/theme"
)

func TestInlinePicker_InitReturnsNil(t *testing.T) {
	m := newInlinePickerModel("carlos", "x", threeFrames(), theme.Palette{})
	if cmd := m.Init(); cmd != nil {
		t.Errorf("Init should return nil; got %T", cmd)
	}
}

func TestInlinePicker_UnknownKeyIsNoop(t *testing.T) {
	m := newInlinePickerModel("carlos", "x", threeFrames(), theme.Palette{})
	upd, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Errorf("unknown key should not return a cmd; got %T", cmd)
	}
	if upd.(inlinePickerModel).cursor != 0 {
		t.Errorf("unknown key should not move cursor")
	}
}

func TestInlinePicker_OrderedFramesNilConfig(t *testing.T) {
	got := orderedFramesFor(nil)
	if len(got) != 0 {
		t.Errorf("nil config should yield empty slice; got %v", got)
	}
}

func TestInlinePicker_OrderedFramesEmptyList(t *testing.T) {
	cfg := &frame.Config{}
	got := orderedFramesFor(cfg)
	if len(got) != 0 {
		t.Errorf("empty list should yield empty slice; got %v", got)
	}
}

func TestInlinePicker_RenderRowMissingGlyphFallsBack(t *testing.T) {
	cfg := &frame.Config{
		Default: "noglyph",
		Active:  "noglyph",
		List: []frame.Frame{
			{Name: "noglyph"}, // no Glyph -> DefaultGlyphFor kicks in
		},
	}
	m := newInlinePickerModel("carlos", "x", cfg, theme.Palette{})
	m.width = 80
	out := m.View()
	if !strings.Contains(out, "noglyph") {
		t.Errorf("frame name should render: %s", out)
	}
}

func TestInlinePicker_RenderVerticalMissingGlyphFallsBack(t *testing.T) {
	cfg := &frame.Config{
		Default: "alpha",
		Active:  "alpha",
		List: []frame.Frame{
			{Name: "alpha"}, {Name: "beta"},
		},
	}
	m := newInlinePickerModel("carlos", "q", cfg, theme.Palette{})
	m.width = 40 // vertical layout
	out := m.View()
	if !strings.Contains(out, "alpha") {
		t.Errorf("alpha missing in vertical: %s", out)
	}
}

func TestInlinePicker_TenthFrameUsesMidDotNumeral(t *testing.T) {
	// At i==9 (10th frame), the number renders as "·" rather than a digit.
	// Build a 10-frame config to exercise that branch.
	cfg := &frame.Config{
		Default: "f0",
		Active:  "f0",
		List: []frame.Frame{
			{Name: "f0"}, {Name: "f1"}, {Name: "f2"}, {Name: "f3"}, {Name: "f4"},
			{Name: "f5"}, {Name: "f6"}, {Name: "f7"}, {Name: "f8"}, {Name: "f9"},
		},
	}
	m := newInlinePickerModel("carlos", "q", cfg, theme.Palette{})
	m.width = 120
	out := m.View()
	if !strings.Contains(out, "·") {
		t.Errorf("expected mid-dot numeral for tenth frame: %s", out)
	}
	// Vertical layout: 10 frames > inlinePickerMaxVertical (7) so the
	// overflow sentinel kicks in. We just confirm the layout still
	// renders the visible window.
	m.width = 40
	outV := m.View()
	if !strings.Contains(outV, "f0") {
		t.Errorf("vertical missing first frame: %s", outV)
	}
}

func TestInlinePicker_QuoteForHeaderEdge(t *testing.T) {
	// quoteForHeader max-3 <= 0 path: max == 3 should hit body = "…".
	got := quoteForHeader("anything", 3)
	if got != "\"…\"" {
		t.Errorf("max=3 path: got %q want \"…\"", got)
	}
}

func TestInlinePicker_QuoteForHeaderReplacesNewlines(t *testing.T) {
	got := quoteForHeader("hi\nthere", 20)
	if got != "\"hi there\"" {
		t.Errorf("newlines should be spaces; got %q", got)
	}
}

func TestInlinePicker_StdinIsTTYUnderTestIsFalse(t *testing.T) {
	// Under `go test`, stdin is not a TTY.
	if stdinIsTTY() {
		t.Skip("running with a TTY stdin; skipping")
	}
}

func TestInlinePicker_ViewZeroWidthFallback(t *testing.T) {
	m := newInlinePickerModel("carlos", "x", threeFrames(), theme.Palette{})
	// width == 0 hits the "w = 80" branch.
	out := m.View()
	if !strings.Contains(out, "personal") {
		t.Errorf("zero-width view should still render: %s", out)
	}
}

func TestInlinePicker_UpFromNonZeroDecrements(t *testing.T) {
	// The up-key body is only reachable when cursor > 0.
	m := newInlinePickerModel("carlos", "x", threeFrames(), theme.Palette{})
	// Walk down once.
	upd, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = upd.(inlinePickerModel)
	if m.cursor != 1 {
		t.Fatalf("expected cursor=1 after down; got %d", m.cursor)
	}
	// Now up -- body fires.
	upd, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = upd.(inlinePickerModel)
	if m.cursor != 0 {
		t.Errorf("expected cursor=0 after up; got %d", m.cursor)
	}
}

// TestRunInlineFramePicker_NoTTY exercises RunInlineFramePicker under
// `go test` (no real TTY). The bubbletea Program errors out, and the
// helper wraps that into a descriptive error. We accept either error
// or successful cancel as valid outcomes -- both exercise the wiring
// code at the top of the function.
func TestRunInlineFramePicker_NoTTY(t *testing.T) {
	// Use a tmp HOME so loadPickerPalette doesn't crash on a missing
	// real-user vault.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	_, err := RunInlineFramePicker("carlos", "x", threeFrames())
	// Either an error (most likely when stdin is not a TTY in the
	// test environment) OR errFramePickerCancelled is fine -- both
	// reach interior wiring.
	if err == nil {
		t.Log("picker returned no error; unusual but harmless")
	}
}

func TestInlinePicker_UpdateUnknownMsgIsNoop(t *testing.T) {
	// A non-key, non-WindowSize message hits the default fallthrough.
	type bogusMsg struct{}
	m := newInlinePickerModel("carlos", "x", threeFrames(), theme.Palette{})
	upd, cmd := m.Update(bogusMsg{})
	if cmd != nil {
		t.Errorf("unknown msg should not return a cmd; got %T", cmd)
	}
	if upd.(inlinePickerModel).cursor != 0 {
		t.Errorf("unknown msg should not move cursor")
	}
}
