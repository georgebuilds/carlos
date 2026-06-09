package chat

import (
	"strings"
	"testing"
)

// TestSkillsSlash_NotWiredEcho falls back to a status warning when
// the runtime hasn't wired SkillsCatalog (dev-aid / tests).
func TestSkillsSlash_NotWiredEcho(t *testing.T) {
	m := &Model{}
	cmd := m.skillsSlash("list")
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	msg := cmd().(statusMsg)
	if msg.kind != statusWarn {
		t.Errorf("expected warn kind; got %v", msg.kind)
	}
	if !strings.Contains(msg.text, "not wired") {
		t.Errorf("expected 'not wired'; got %q", msg.text)
	}
}

// TestSkillsSlash_EmptyCatalog gives the user a hint on where to
// drop new skills so the empty case isn't a dead-end.
func TestSkillsSlash_EmptyCatalog(t *testing.T) {
	m := &Model{
		frame: FrameUI{
			SkillsCatalog: func() []SkillCatalogEntry { return nil },
		},
	}
	cmd := m.skillsSlash("")
	msg := cmd().(statusMsg)
	if !strings.Contains(msg.text, "no skills available") {
		t.Errorf("expected 'no skills available' hint; got %q", msg.text)
	}
}

// TestSkillsSlash_PopulatedCatalogJoinsEntries renders the catalog
// as a status row joining each entry with the brand separator.
func TestSkillsSlash_PopulatedCatalogJoinsEntries(t *testing.T) {
	m := &Model{
		frame: FrameUI{
			SkillsCatalog: func() []SkillCatalogEntry {
				return []SkillCatalogEntry{
					{Name: "calendar", Description: "calendar entry point"},
					{Name: "calendar-caldav", Description: "talk to CalDAV"},
				}
			},
		},
	}
	cmd := m.skillsSlash("list")
	msg := cmd().(statusMsg)
	if !strings.Contains(msg.text, "calendar") || !strings.Contains(msg.text, "calendar-caldav") {
		t.Errorf("expected both skills surfaced; got %q", msg.text)
	}
	if !strings.Contains(msg.text, "·") {
		t.Errorf("expected entries joined with brand separator; got %q", msg.text)
	}
}

// TestSkillsSlash_UnknownVerbHint echoes a friendly hint instead of
// silently dropping the input.
func TestSkillsSlash_UnknownVerbHint(t *testing.T) {
	m := &Model{}
	cmd := m.skillsSlash("nuke")
	msg := cmd().(statusMsg)
	if !strings.Contains(msg.text, "not yet wired") {
		t.Errorf("expected 'not yet wired'; got %q", msg.text)
	}
}

// TestSkillsSlash_TruncatesLongDescription trims overly verbose
// descriptions so the status row doesn't blow out — the user can
// always call skill_use for the full body.
func TestSkillsSlash_TruncatesLongDescription(t *testing.T) {
	long := strings.Repeat("x", 200)
	m := &Model{
		frame: FrameUI{
			SkillsCatalog: func() []SkillCatalogEntry {
				return []SkillCatalogEntry{{Name: "verbose", Description: long}}
			},
		},
	}
	cmd := m.skillsSlash("list")
	msg := cmd().(statusMsg)
	if !strings.Contains(msg.text, "…") {
		t.Errorf("expected ellipsis for capped description; got %q", msg.text)
	}
}

// TestSkillsSlash_SkipsEmptyNameRows guards against malformed
// entries (no name): they shouldn't render as a stray " — desc"
// row.
func TestSkillsSlash_SkipsEmptyNameRows(t *testing.T) {
	m := &Model{
		frame: FrameUI{
			SkillsCatalog: func() []SkillCatalogEntry {
				return []SkillCatalogEntry{
					{Name: "", Description: "should be skipped"},
					{Name: "good", Description: "should render"},
				}
			},
		},
	}
	cmd := m.skillsSlash("list")
	msg := cmd().(statusMsg)
	if strings.Contains(msg.text, "should be skipped") {
		t.Errorf("empty-name row leaked into output: %q", msg.text)
	}
	if !strings.Contains(msg.text, "good") {
		t.Errorf("expected 'good' row; got %q", msg.text)
	}
}
