package main

import (
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

func TestMintSessionID_UniqueSortable(t *testing.T) {
	seen := map[string]bool{}
	prev := ""
	for i := 0; i < 50; i++ {
		id, err := mintSessionID(time.Now().UTC().Add(time.Duration(i) * time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate session id: %s", id)
		}
		seen[id] = true
		if prev != "" && id <= prev {
			t.Errorf("not sortable: %s ≤ %s", id, prev)
		}
		prev = id
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		past time.Time
		want string
	}{
		{now.Add(-10 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-90 * time.Minute), "1h ago"},
		{now.Add(-26 * time.Hour), "1d ago"},
		{now.Add(-45 * 24 * time.Hour), now.Add(-45 * 24 * time.Hour).Local().Format("2006-01-02")},
	}
	for _, tc := range cases {
		if got := relativeTime(now, tc.past); got != tc.want {
			t.Errorf("relativeTime(%v) = %q, want %q", tc.past, got, tc.want)
		}
	}
}

func TestPickerModel_Refilter(t *testing.T) {
	sessions := []agent.Session{
		{ID: "01", Title: "alpha", Preview: "talked about cargo", Model: "claude-sonnet-4-6"},
		{ID: "02", Title: "beta", Preview: "asked for help with vim", Model: "gpt-5"},
		{ID: "03", Title: "gamma", Preview: "let me rant", Model: "claude-opus-4-7"},
	}
	m := newSessionPickerModel(sessions, theme.Palette{})
	if len(m.filtered) != 3 {
		t.Errorf("initial filtered: want 3, got %d", len(m.filtered))
	}

	m.filter = "vim"
	m.refilter()
	if len(m.filtered) != 1 || sessions[m.filtered[0]].ID != "02" {
		t.Errorf("filter vim: %v", m.filtered)
	}

	m.filter = "CLAUDE"
	m.refilter()
	if len(m.filtered) != 2 {
		t.Errorf("case-insensitive: %d matches", len(m.filtered))
	}

	m.filter = "no-such-thing"
	m.refilter()
	if len(m.filtered) != 0 {
		t.Errorf("no matches: %v", m.filtered)
	}
}

func TestPickerModel_RefilterClampsCursor(t *testing.T) {
	sessions := []agent.Session{
		{ID: "01", Title: "alpha"},
		{ID: "02", Title: "beta"},
		{ID: "03", Title: "gamma"},
	}
	m := newSessionPickerModel(sessions, theme.Palette{})
	m.cursor = 2
	m.filter = "alpha"
	m.refilter()
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		t.Errorf("cursor out of bounds after refilter: %d, len=%d", m.cursor, len(m.filtered))
	}
}

func TestPickerModel_CommitSelection(t *testing.T) {
	sessions := []agent.Session{
		{ID: "01H", Title: "x"},
		{ID: "02H", Title: "y"},
	}
	m := newSessionPickerModel(sessions, theme.Palette{})
	m.cursor = 1
	after, _ := m.commitSelection()
	if mm := after.(sessionPickerModel); mm.chosen != "02H" {
		t.Errorf("commit chose: %q", mm.chosen)
	}
}

func TestPickerModel_CommitWithEmptyFilteredIsNoop(t *testing.T) {
	m := newSessionPickerModel(nil, theme.Palette{})
	after, cmd := m.commitSelection()
	if cmd != nil {
		t.Error("empty filtered should not commit")
	}
	if mm := after.(sessionPickerModel); mm.chosen != "" {
		t.Errorf("chosen leaked: %q", mm.chosen)
	}
}

func TestPluralS(t *testing.T) {
	if pluralS(1) != "" {
		t.Error("singular: expected empty suffix")
	}
	if pluralS(2) != "s" {
		t.Errorf("plural: got %q", pluralS(2))
	}
}

func TestTruncatePickerLine(t *testing.T) {
	if got := truncatePickerLine("hello world", 8); got != "hello w…" {
		t.Errorf("got %q", got)
	}
	if got := truncatePickerLine("hi", 10); got != "hi" {
		t.Errorf("under-cap: %q", got)
	}
	if got := truncatePickerLine("anything", 1); got != "…" {
		t.Errorf("min cap: %q", got)
	}
}

func TestPickerModel_FilterContainsMultipleFields(t *testing.T) {
	// Filter should match across title, preview, model, AND id —
	// the user can paste a ULID to find a specific session.
	sessions := []agent.Session{
		{ID: "01H123ABC", Title: "alpha", Preview: "p1", Model: "claude"},
		{ID: "01HXYZDEF", Title: "beta", Preview: "p2", Model: "gpt"},
	}
	m := newSessionPickerModel(sessions, theme.Palette{})
	m.filter = "XYZ"
	m.refilter()
	if len(m.filtered) != 1 || sessions[m.filtered[0]].ID != "01HXYZDEF" {
		t.Errorf("id filter: %v", m.filtered)
	}
}

// Smoke: the View body actually renders without panicking on an
// empty model. This catches indexing bugs in the row renderer.
func TestPickerModel_ViewEmpty(t *testing.T) {
	m := newSessionPickerModel(nil, theme.Palette{})
	m.width = 80
	m.height = 24
	out := m.View()
	if !strings.Contains(out, "Resume") {
		t.Errorf("missing header: %q", out)
	}
}
