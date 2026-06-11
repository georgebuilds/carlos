package chat

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChatHistory_DownWalksForwardThenRestoresDraft(t *testing.T) {
	m := newHistoryModel(t, "oldest", "middle", "newest")
	// Type a draft, then walk up twice (→ "middle"), then down twice.
	m.ta.SetValue("draft-text")

	up := func() { u, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp}); m = u.(*Model) }
	down := func() { u, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown}); m = u.(*Model) }

	up() // newest (cursor 0)
	up() // middle (cursor 1)
	if m.ta.Value() != "middle" {
		t.Fatalf("after two ↑: want 'middle'; got %q", m.ta.Value())
	}
	down() // back to newest (cursor 0)
	if m.ta.Value() != "newest" {
		t.Errorf("after ↓: want 'newest'; got %q", m.ta.Value())
	}
	down() // cursor was 0 → restore the draft and end the walk
	if m.ta.Value() != "draft-text" {
		t.Errorf("↓ at cursor 0 should restore the draft; got %q", m.ta.Value())
	}
	if m.chatHistoryCursor != -1 {
		t.Errorf("walk should have ended (cursor -1); got %d", m.chatHistoryCursor)
	}
}

func TestChatHistory_DownWithoutActiveWalkIsNoOp(t *testing.T) {
	m := newHistoryModel(t, "a", "b")
	m.ta.SetValue("typing")
	if got := m.chatHistoryDown(); got {
		t.Error("chatHistoryDown with no active walk should return false")
	}
	if m.ta.Value() != "typing" {
		t.Errorf("no-op down should leave the composer untouched; got %q", m.ta.Value())
	}
}
