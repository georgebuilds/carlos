package chat

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/frame"
)

// headerClickModel mirrors newFramedModel but also forces a render
// pass so renderHeader populates the pill hitboxes the click handler
// reads. Without the render the bounds stay zero and any click would
// match nothing.
func headerClickModel(t *testing.T, mode string) *Model {
	t.Helper()
	m := newFramedModel(t, FrameUI{
		Active:    "work",
		Glyph:     "▣",
		Accent:    "rust",
		Available: []string{"personal", "work"},
		Mode:      mode,
	})
	// View() runs renderHeader → populates *PillCol* fields.
	_ = m.View()
	return m
}

func TestRenderHeader_PopulatesFramePillBounds(t *testing.T) {
	m := headerClickModel(t, frame.ModeSolo)
	if m.framePillColEnd <= m.framePillColStart {
		t.Fatalf("framePillCol bounds invalid: [%d, %d)",
			m.framePillColStart, m.framePillColEnd)
	}
	if m.framePillColStart < 2 {
		t.Errorf("framePillColStart = %d, expected >= 2 (past border+padding)",
			m.framePillColStart)
	}
}

func TestRenderHeader_PopulatesModePillBoundsEvenForSolo(t *testing.T) {
	// Solo used to be hidden; the new contract is "always render the
	// mode pill so the click target is stable across modes". The pill
	// stays subtle for solo but the hitbox is real.
	m := headerClickModel(t, frame.ModeSolo)
	if m.modePillColEnd <= m.modePillColStart {
		t.Fatalf("modePillCol bounds invalid: [%d, %d)",
			m.modePillColStart, m.modePillColEnd)
	}
	if m.modePillColStart < m.framePillColEnd {
		t.Errorf("mode pill should sit to the right of frame pill: mode=%d frameEnd=%d",
			m.modePillColStart, m.framePillColEnd)
	}
}

func TestRenderHeader_HitboxesZeroWhenFrameUnwired(t *testing.T) {
	m := newFramedModel(t, FrameUI{})
	_ = m.View()
	if m.framePillColStart != 0 || m.framePillColEnd != 0 {
		t.Errorf("frame pill bounds should be 0/0 without a wired frame; got [%d, %d)",
			m.framePillColStart, m.framePillColEnd)
	}
	if m.modePillColStart != 0 || m.modePillColEnd != 0 {
		t.Errorf("mode pill bounds should be 0/0 without a wired frame; got [%d, %d)",
			m.modePillColStart, m.modePillColEnd)
	}
}

func TestMouseClick_OnFramePill_OpensFrameSwitcher(t *testing.T) {
	m := headerClickModel(t, frame.ModeSolo)
	x := (m.framePillColStart + m.framePillColEnd) / 2
	msg := tea.MouseMsg{
		Y:      1,
		X:      x,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}
	out, _ := m.Update(msg)
	mm := out.(*Model)
	if !mm.showFrameSwitcher {
		t.Errorf("click at x=%d in frame pill [%d,%d) should open frame switcher",
			x, m.framePillColStart, m.framePillColEnd)
	}
}

func TestMouseClick_OnModePill_OpensModeSwitcher(t *testing.T) {
	m := headerClickModel(t, frame.ModeSolo)
	x := (m.modePillColStart + m.modePillColEnd) / 2
	msg := tea.MouseMsg{
		Y:      1,
		X:      x,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}
	out, _ := m.Update(msg)
	mm := out.(*Model)
	if !mm.showModeSwitcher {
		t.Errorf("click at x=%d in mode pill [%d,%d) should open mode switcher",
			x, m.modePillColStart, m.modePillColEnd)
	}
}

func TestMouseClick_OutsidePills_DoesNotOpenSwitchers(t *testing.T) {
	m := headerClickModel(t, frame.ModeSolo)
	// Click on row 1 but far past both hitboxes.
	msg := tea.MouseMsg{
		Y:      1,
		X:      m.modePillColEnd + 20,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}
	out, _ := m.Update(msg)
	mm := out.(*Model)
	if mm.showFrameSwitcher || mm.showModeSwitcher {
		t.Errorf("click outside pills opened a switcher: frame=%v mode=%v",
			mm.showFrameSwitcher, mm.showModeSwitcher)
	}
}

func TestMouseClick_WrongRow_DoesNotOpenSwitchers(t *testing.T) {
	// Pill columns only apply on the header row (Y=1). A click at the
	// same X but a different Y must NOT trigger an open.
	m := headerClickModel(t, frame.ModeSolo)
	x := (m.framePillColStart + m.framePillColEnd) / 2
	msg := tea.MouseMsg{
		Y:      10,
		X:      x,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}
	out, _ := m.Update(msg)
	mm := out.(*Model)
	if mm.showFrameSwitcher {
		t.Error("transcript-row click should not open frame switcher")
	}
}

func TestMouseClick_NonLeftButton_IsForwardedNotConsumed(t *testing.T) {
	// A right-button press in the frame pill's X should NOT open the
	// switcher - we reserve left-click for the affordance and forward
	// every other mouse event to the viewport (wheel-scroll, etc).
	m := headerClickModel(t, frame.ModeSolo)
	x := (m.framePillColStart + m.framePillColEnd) / 2
	msg := tea.MouseMsg{
		Y:      1,
		X:      x,
		Button: tea.MouseButtonRight,
		Action: tea.MouseActionPress,
	}
	out, _ := m.Update(msg)
	mm := out.(*Model)
	if mm.showFrameSwitcher {
		t.Error("right-button press should not open frame switcher")
	}
}
