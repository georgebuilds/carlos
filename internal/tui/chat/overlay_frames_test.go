package chat

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// openSwitcher wires a model with the given available frames + active
// pointer and snaps the takeover open at the active tile.
func openSwitcher(t *testing.T, active string, available []string, switchFn func(string) error) *Model {
	t.Helper()
	m := newFramedModel(t, FrameUI{
		Active:       active,
		Glyph:        "▣",
		Accent:       "rust",
		Available:    available,
		SwitchActive: switchFn,
	})
	m.openFrameSwitcher()
	return m
}

func key(s string) tea.KeyMsg {
	// String-based KeyMsg construction: a runes-typed KeyMsg's String()
	// returns the rune sequence, which is what handleFrameSwitcherKey
	// routes on. For the named keys (esc/enter/ctrl+f/etc.) we need
	// the typed form.
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "ctrl+f":
		return tea.KeyMsg{Type: tea.KeyCtrlF}
	case "ctrl+left":
		return tea.KeyMsg{Type: tea.KeyCtrlLeft}
	case "ctrl+right":
		return tea.KeyMsg{Type: tea.KeyCtrlRight}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestSwitcherColumnsResponsive(t *testing.T) {
	cases := []struct {
		w    int
		want int
	}{
		{40, 1}, {60, 1}, {70, 2}, {80, 2}, {99, 2}, {100, 3}, {200, 3},
	}
	for _, c := range cases {
		if got := switcherColumns(c.w); got != c.want {
			t.Errorf("switcherColumns(%d) = %d, want %d", c.w, got, c.want)
		}
	}
}

func TestRenderFrameSwitcher_RendersFrameGlyphsAndNames(t *testing.T) {
	ui := FrameUI{
		Active:    "personal",
		Glyph:     "◉",
		Accent:    "cream",
		Available: []string{"personal", "work", "research"},
	}
	out := renderFrameSwitcher(ui, 0, 0, 120, 30, false)
	for _, want := range []string{"personal", "work", "research"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered switcher missing %q\n%s", want, out)
		}
	}
	// Default glyphs for non-active frames should land.
	for _, glyph := range []string{"◉", "▣", "◈"} {
		if !strings.Contains(out, glyph) {
			t.Errorf("rendered switcher missing glyph %q\n%s", glyph, out)
		}
	}
	if !strings.Contains(out, "active") {
		t.Errorf("rendered switcher missing 'active' summary\n%s", out)
	}
}

func TestRenderFrameSwitcher_ShowsNewFrameTile(t *testing.T) {
	ui := FrameUI{
		Active:    "personal",
		Available: []string{"personal"},
	}
	out := renderFrameSwitcher(ui, 0, 0, 120, 30, false)
	if !strings.Contains(out, "new frame") {
		t.Errorf("expected new-frame placeholder\n%s", out)
	}
}

func TestRenderFrameSwitcher_VariousDimensions(t *testing.T) {
	ui := FrameUI{
		Active:    "work",
		Available: []string{"personal", "work", "research", "writing", "client"},
	}
	for _, dims := range [][2]int{{80, 24}, {120, 40}, {200, 60}} {
		out := renderFrameSwitcher(ui, 1, 0, dims[0]-4, dims[1]-4, false)
		if !strings.Contains(out, "work") {
			t.Errorf("%dx%d: missing 'work'\n%s", dims[0], dims[1], out)
		}
		if !strings.Contains(out, "frames") {
			t.Errorf("%dx%d: missing header\n%s", dims[0], dims[1], out)
		}
	}
}

func TestRenderFrameSwitcher_PaginatedFooter(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	ui := FrameUI{Active: "a", Available: names}
	out := renderFrameSwitcher(ui, 0, 0, 120, 30, false)
	if !strings.Contains(out, "page 1/") {
		t.Errorf("expected page indicator; got\n%s", out)
	}
}

func TestSwitcher_EnterFiresSwitchAndCloses(t *testing.T) {
	captured := ""
	m := openSwitcher(t, "personal", []string{"personal", "work", "research"}, func(name string) error {
		captured = name
		return nil
	})
	// Move cursor right so we're on "work".
	if _, _, ok := m.handleFrameSwitcherKey(key("right")); !ok {
		t.Fatal("right not handled")
	}
	if _, cmd, ok := m.handleFrameSwitcherKey(key("enter")); !ok {
		t.Fatal("enter not handled")
	} else if cmd == nil {
		t.Fatal("enter returned nil cmd")
	} else {
		msg := cmd()
		if _, ok := msg.(statusMsg); !ok {
			t.Errorf("expected statusMsg, got %T", msg)
		}
	}
	if captured != "work" {
		t.Errorf("SwitchActive captured %q, want %q", captured, "work")
	}
	if m.showFrameSwitcher {
		t.Errorf("overlay should be closed after enter")
	}
	if m.frame.Active != "work" {
		t.Errorf("frame.Active = %q, want work", m.frame.Active)
	}
}

func TestSwitcher_EscClosesWithoutFiring(t *testing.T) {
	called := false
	m := openSwitcher(t, "personal", []string{"personal", "work"}, func(string) error {
		called = true
		return nil
	})
	if _, _, ok := m.handleFrameSwitcherKey(key("right")); !ok {
		t.Fatal("right not handled")
	}
	if _, _, ok := m.handleFrameSwitcherKey(key("esc")); !ok {
		t.Fatal("esc not handled")
	}
	if called {
		t.Errorf("esc should not fire SwitchActive")
	}
	if m.showFrameSwitcher {
		t.Errorf("esc should close overlay")
	}
	if m.frame.Active != "personal" {
		t.Errorf("active should not change on esc; got %q", m.frame.Active)
	}
}

func TestSwitcher_CtrlFTogglesClosed(t *testing.T) {
	m := openSwitcher(t, "personal", []string{"personal", "work"}, nil)
	if _, _, ok := m.handleFrameSwitcherKey(key("ctrl+f")); !ok {
		t.Fatal("ctrl+f not handled")
	}
	if m.showFrameSwitcher {
		t.Errorf("ctrl+f should close overlay")
	}
}

func TestSwitcher_NumberJumpSelectsNth(t *testing.T) {
	m := openSwitcher(t, "personal", []string{"personal", "work", "research"}, nil)
	if _, _, ok := m.handleFrameSwitcherKey(key("3")); !ok {
		t.Fatal("3 not handled")
	}
	if m.switcherCursor != 2 {
		t.Errorf("cursor = %d, want 2", m.switcherCursor)
	}
}

func TestSwitcher_NumberJumpClampsBeyondAvailable(t *testing.T) {
	m := openSwitcher(t, "personal", []string{"personal", "work"}, nil)
	prev := m.switcherCursor
	if _, _, ok := m.handleFrameSwitcherKey(key("5")); !ok {
		t.Fatal("5 not handled")
	}
	if m.switcherCursor != prev {
		t.Errorf("cursor should not move when target is out of range; got %d", m.switcherCursor)
	}
}

func TestSwitcher_NavigationClampsAtBounds(t *testing.T) {
	// 4 frames @ width 120 -> 3 cols => cursor in {0..3}.
	m := openSwitcher(t, "personal", []string{"personal", "work", "research", "writing"}, nil)
	m.switcherCursor = 0
	// up at top row should not move.
	if _, _, ok := m.handleFrameSwitcherKey(key("up")); !ok {
		t.Fatal("up not handled")
	}
	if m.switcherCursor != 0 {
		t.Errorf("up at top should clamp; got %d", m.switcherCursor)
	}
	// left at col 0 should not move.
	if _, _, ok := m.handleFrameSwitcherKey(key("left")); !ok {
		t.Fatal("left not handled")
	}
	if m.switcherCursor != 0 {
		t.Errorf("left at col 0 should clamp; got %d", m.switcherCursor)
	}
	// right twice should land at 2 (col 2, row 0).
	m.handleFrameSwitcherKey(key("right"))
	m.handleFrameSwitcherKey(key("right"))
	if m.switcherCursor != 2 {
		t.Errorf("expected cursor 2; got %d", m.switcherCursor)
	}
	// down should move to row 1 -> index 5 if available, else clamp.
	m.handleFrameSwitcherKey(key("down"))
	// frame at index 5 doesn't exist (only 4 frames), so cursor stays.
	if m.switcherCursor != 2 {
		t.Errorf("down past available should clamp; got %d", m.switcherCursor)
	}
}

func TestSwitcher_HelpToggle(t *testing.T) {
	m := openSwitcher(t, "personal", []string{"personal", "work"}, nil)
	if m.switcherHelp {
		t.Errorf("help should start hidden")
	}
	m.handleFrameSwitcherKey(key("?"))
	if !m.switcherHelp {
		t.Errorf("? should toggle help on")
	}
	m.handleFrameSwitcherKey(key("?"))
	if m.switcherHelp {
		t.Errorf("? should toggle help off")
	}
}

func TestSwitcher_Pagination(t *testing.T) {
	// 8 frames @ width 120 -> 6 per page -> 2 pages.
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	m := openSwitcher(t, "a", names, nil)
	if m.switcherPage != 0 {
		t.Errorf("initial page = %d, want 0", m.switcherPage)
	}
	m.handleFrameSwitcherKey(key("ctrl+right"))
	if m.switcherPage != 1 {
		t.Errorf("ctrl+right page = %d, want 1", m.switcherPage)
	}
	m.handleFrameSwitcherKey(key("ctrl+right"))
	if m.switcherPage != 1 {
		t.Errorf("ctrl+right past last should clamp; got %d", m.switcherPage)
	}
	m.handleFrameSwitcherKey(key("ctrl+left"))
	if m.switcherPage != 0 {
		t.Errorf("ctrl+left page = %d, want 0", m.switcherPage)
	}
}

func TestSwitcher_EnterOnSameFrameIsAlreadyEcho(t *testing.T) {
	called := false
	m := openSwitcher(t, "personal", []string{"personal", "work"}, func(string) error {
		called = true
		return nil
	})
	// Cursor lands on "personal" by openFrameSwitcher.
	_, cmd, _ := m.handleFrameSwitcherKey(key("enter"))
	if called {
		t.Errorf("enter on same frame should not fire SwitchActive")
	}
	if cmd == nil {
		t.Fatalf("expected status cmd for already-in-frame echo")
	}
	msg := cmd()
	s, ok := msg.(statusMsg)
	if !ok || !strings.Contains(s.text, "already") {
		t.Errorf("expected 'already' echo; got %+v", msg)
	}
}

func TestSwitcher_EnterWithoutHookEchoesWarn(t *testing.T) {
	m := openSwitcher(t, "personal", []string{"personal", "work"}, nil)
	m.handleFrameSwitcherKey(key("right"))
	_, cmd, _ := m.handleFrameSwitcherKey(key("enter"))
	if cmd == nil {
		t.Fatal("expected cmd echoing not-wired")
	}
	s, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(s.text, "not wired") {
		t.Errorf("expected not-wired warn; got %+v", s)
	}
}

func TestSwitcher_OpensAtActiveCursor(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:    "research",
		Available: []string{"personal", "work", "research"},
	})
	m.openFrameSwitcher()
	if m.switcherCursor != 2 {
		t.Errorf("cursor should snap to active frame; got %d", m.switcherCursor)
	}
}

func TestRenderFrameSwitcher_OneColumnAt60w(t *testing.T) {
	// innerW < 70 -> 1 column. Verify the helper returns 1 + the
	// rendered grid contains two stacked tiles (active uses thick
	// border ┏, non-active uses rounded ╭ — either qualifies as a
	// tile top-left corner).
	ui := FrameUI{
		Active:    "personal",
		Available: []string{"personal", "work"},
	}
	out := renderFrameSwitcher(ui, 0, 0, 60, 30, false)
	if switcherColumns(60) != 1 {
		t.Fatalf("expected 1 col at innerW=60; got %d", switcherColumns(60))
	}
	corners := strings.Count(out, "╭") + strings.Count(out, "┏")
	if corners < 2 {
		t.Errorf("expected at least 2 tile top-left corners (frames stack vertically); got %d\n%s", corners, out)
	}
}

func TestRenderFrameSwitcher_TwoColumnsAt80w(t *testing.T) {
	if switcherColumns(80) != 2 {
		t.Fatalf("expected 2 cols at innerW=80; got %d", switcherColumns(80))
	}
}

func TestRenderFrameSwitcher_FooterChangesWithHelp(t *testing.T) {
	ui := FrameUI{Active: "personal", Available: []string{"personal", "work"}}
	plain := renderFrameSwitcher(ui, 0, 0, 120, 30, false)
	helpy := renderFrameSwitcher(ui, 0, 0, 120, 30, true)
	if !strings.Contains(plain, "help") {
		t.Errorf("plain footer should include `help` keybind label")
	}
	if !strings.Contains(helpy, "move") {
		t.Errorf("help footer should include verbose move/jump labels; got\n%s", helpy)
	}
}

func TestSwitcher_SwitchActiveErrorEchoesWarn(t *testing.T) {
	m := openSwitcher(t, "personal", []string{"personal", "work"}, func(string) error {
		return fakeErr("disk full")
	})
	m.handleFrameSwitcherKey(key("right"))
	_, cmd, _ := m.handleFrameSwitcherKey(key("enter"))
	if cmd == nil {
		t.Fatal("expected error status echo")
	}
	s := cmd().(statusMsg)
	if !strings.Contains(s.text, "disk full") {
		t.Errorf("expected error in echo; got %q", s.text)
	}
	if m.frame.Active != "personal" {
		t.Errorf("active should not change on hook err; got %q", m.frame.Active)
	}
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
