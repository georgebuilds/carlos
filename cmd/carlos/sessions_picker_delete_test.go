package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// stubDelete builds a deleteOverride that records the id it was called
// with and returns the supplied (n, err).
func stubDelete(captured *string, n int, err error) func(context.Context, *agent.SQLiteEventLog, string, bool) (int, error) {
	return func(_ context.Context, _ *agent.SQLiteEventLog, id string, _ bool) (int, error) {
		if captured != nil {
			*captured = id
		}
		return n, err
	}
}

func threeSessionPicker() sessionPickerModel {
	sessions := []agent.Session{
		{ID: "01A", Title: "alpha"},
		{ID: "01B", Title: "beta"},
		{ID: "01C", Title: ""},
	}
	return newSessionPickerModel(sessions, theme.Palette{})
}

func TestSessionPicker_DeleteArmsThenConfirms(t *testing.T) {
	var got string
	m := threeSessionPicker()
	m.deleteOverride = stubDelete(&got, 3, nil)
	m.cursor = 1 // focus "01B"

	// First 'x' arms the confirm; nothing deleted yet.
	upd, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = upd.(sessionPickerModel)
	if m.deleteArmed != 1 {
		t.Fatalf("deleteArmed = %d want 1", m.deleteArmed)
	}
	if !strings.Contains(m.status, "press x again") {
		t.Errorf("arm status: %q", m.status)
	}
	if got != "" {
		t.Errorf("should not have deleted on first press, got %q", got)
	}
	if len(m.all) != 3 {
		t.Errorf("list shrank on arm: %d", len(m.all))
	}

	// Second 'x' applies the delete.
	upd, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = upd.(sessionPickerModel)
	if got != "01B" {
		t.Errorf("deleted id = %q want 01B", got)
	}
	if len(m.all) != 2 {
		t.Errorf("list size after delete = %d want 2", len(m.all))
	}
	for _, s := range m.all {
		if s.ID == "01B" {
			t.Error("01B still present after delete")
		}
	}
	if !strings.Contains(m.status, "deleted") || !strings.Contains(m.status, "3 agent rows") {
		t.Errorf("result status: %q", m.status)
	}
	if m.deleteArmed != -1 {
		t.Error("should disarm after apply")
	}
}

func TestSessionPicker_DDKeyAlsoDeletes(t *testing.T) {
	var got string
	m := threeSessionPicker()
	m.deleteOverride = stubDelete(&got, 1, nil)
	m.cursor = 0
	for i := 0; i < 2; i++ {
		upd, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
		m = upd.(sessionPickerModel)
	}
	if got != "01A" {
		t.Errorf("d delete id = %q want 01A", got)
	}
}

func TestSessionPicker_NavigationDisarms(t *testing.T) {
	var got string
	m := threeSessionPicker()
	m.deleteOverride = stubDelete(&got, 1, nil)
	m.cursor = 0

	// Arm on row 0.
	upd, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = upd.(sessionPickerModel)
	if m.deleteArmed != 0 {
		t.Fatal("should be armed")
	}
	// Move down - disarms.
	upd, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = upd.(sessionPickerModel)
	if m.deleteArmed != -1 {
		t.Error("navigation should disarm")
	}
	// 'x' on the new row arms it (now row 1), doesn't fire the stale arm.
	upd, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = upd.(sessionPickerModel)
	if got != "" {
		t.Errorf("nothing should be deleted yet, got %q", got)
	}
	if m.deleteArmed != 1 {
		t.Errorf("re-armed row = %d want 1", m.deleteArmed)
	}
}

func TestSessionPicker_DeleteUntitledLabel(t *testing.T) {
	m := threeSessionPicker()
	m.deleteOverride = stubDelete(nil, 1, nil)
	m.cursor = 2 // the untitled "01C"
	upd, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = upd.(sessionPickerModel)
	if !strings.Contains(m.status, "(untitled)") {
		t.Errorf("untitled arm status: %q", m.status)
	}
}

func TestSessionPicker_DeleteErrorsMapToStatus(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{agent.ErrSessionLive, "live in another process"},
		{agent.ErrNotTopLevel, "sub-agent"},
		{agent.ErrSessionNotFound, "not found"},
	}
	for _, tc := range cases {
		m := threeSessionPicker()
		m.deleteOverride = stubDelete(nil, 0, tc.err)
		m.cursor = 0
		// Arm + confirm.
		for i := 0; i < 2; i++ {
			upd, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
			m = upd.(sessionPickerModel)
		}
		if !strings.Contains(m.status, tc.want) {
			t.Errorf("err %v: status %q missing %q", tc.err, m.status, tc.want)
		}
		// List must be intact on error.
		if len(m.all) != 3 {
			t.Errorf("err %v: list shrank to %d", tc.err, len(m.all))
		}
		if m.deleteArmed != -1 {
			t.Errorf("err %v: should disarm", tc.err)
		}
	}
}

func TestSessionPicker_DeleteOnEmptyListNoop(t *testing.T) {
	m := newSessionPickerModel(nil, theme.Palette{})
	upd, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = upd.(sessionPickerModel)
	if m.deleteArmed != -1 {
		t.Error("empty list should not arm")
	}
	if m.status != "" {
		t.Errorf("empty list status: %q", m.status)
	}
}

func TestSessionPicker_ApplyDeleteResultClampsCursor(t *testing.T) {
	m := threeSessionPicker()
	m.cursor = 2 // focus last
	// Delete the last row directly.
	m = m.applyDeleteResult(2, 1, nil)
	if len(m.filtered) != 2 {
		t.Fatalf("filtered = %d want 2", len(m.filtered))
	}
	if m.cursor >= len(m.filtered) {
		t.Errorf("cursor %d out of bounds (len %d)", m.cursor, len(m.filtered))
	}
}

func TestSessionPicker_ApplyDeleteResultOutOfRangeIdx(t *testing.T) {
	m := threeSessionPicker()
	// An out-of-range idx must not panic or mutate the list; it just
	// disarms and refilters (success path with no removal).
	before := len(m.all)
	m = m.applyDeleteResult(99, 1, nil)
	if len(m.all) != before {
		t.Errorf("out-of-range idx changed list: %d want %d", len(m.all), before)
	}
	if m.deleteArmed != -1 {
		t.Error("should disarm")
	}
}

func TestSessionPicker_StatusRendersInView(t *testing.T) {
	m := threeSessionPicker()
	m.width, m.height = 80, 24
	m.status = "deleted \"alpha\" (1 agent row)"
	out := m.View()
	if !strings.Contains(out, "deleted") {
		t.Errorf("status not rendered in view: %q", out)
	}
}

func TestSessionPicker_FocusedIndex(t *testing.T) {
	m := threeSessionPicker()
	m.cursor = 1
	if got := m.focusedIndex(); got != 1 {
		t.Errorf("focusedIndex = %d want 1", got)
	}
	empty := newSessionPickerModel(nil, theme.Palette{})
	if got := empty.focusedIndex(); got != -1 {
		t.Errorf("empty focusedIndex = %d want -1", got)
	}
}

func TestSessionPicker_DeleteFnDefaultsToReal(t *testing.T) {
	m := threeSessionPicker()
	if m.deleteFn() == nil {
		t.Error("default deleteFn should be non-nil")
	}
}

func TestSessionPicker_FooterShowsDeleteKey(t *testing.T) {
	m := threeSessionPicker()
	if !strings.Contains(m.renderFooter(), "delete") {
		t.Error("footer should advertise the delete key")
	}
}
