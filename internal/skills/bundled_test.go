package skills

import (
	"testing"
)

// TestLoadBundled_ShipsAtLeastOneSkill is the load-bearing assertion:
// the binary embed must hand back at least one skill so a fresh brew
// install lights up the starter pack without manual file copying.
// We don't pin the exact skill list — that'd flap whenever the
// bundled directory grows — but every release should ship something.
func TestLoadBundled_ShipsAtLeastOneSkill(t *testing.T) {
	got := LoadBundled()
	if len(got) == 0 {
		t.Fatal("expected at least one bundled skill; got none — embed FS broken?")
	}
	for _, s := range got {
		if s == nil {
			t.Error("nil skill in bundled output")
			continue
		}
		if s.Name == "" || s.Description == "" {
			t.Errorf("bundled skill %q missing required fields: name=%q desc=%q", s.Path, s.Name, s.Description)
		}
		if s.Path == "" {
			t.Errorf("bundled skill %q missing Path tag", s.Name)
		}
	}
}

// TestLoadBundled_ContainsCalendar pins the calendar pack
// specifically — it's the one the user explicitly wanted available
// and the one v0.7.8's "skill_use" wiring depends on. If we ever
// reshape the bundle layout, this test will catch it before users
// complain.
func TestLoadBundled_ContainsCalendar(t *testing.T) {
	got := LoadBundled()
	for _, s := range got {
		if s.Name == "calendar" {
			return
		}
	}
	t.Errorf("bundled set should contain a skill named %q; got %d skills", "calendar", len(got))
}

// TestOverlayBundled_UserWins seeds a library with a user-installed
// skill that shadows the bundled name, then verifies OverlayBundled
// keeps the user's version untouched.
func TestOverlayBundled_UserWins(t *testing.T) {
	user := &Skill{Name: "calendar", Description: "user-installed override"}
	lib := &Library{Active: []*Skill{user}}
	OverlayBundled(lib)
	for _, s := range lib.Active {
		if s.Name == "calendar" && s.Description != "user-installed override" {
			t.Errorf("user-installed skill should win over bundled; got desc=%q", s.Description)
		}
	}
}

// TestOverlayBundled_AddsMissing seeds an empty library and verifies
// the embed contents land in Active.
func TestOverlayBundled_AddsMissing(t *testing.T) {
	lib := &Library{}
	OverlayBundled(lib)
	if len(lib.Active) == 0 {
		t.Fatal("expected overlay to add bundled skills to empty library")
	}
}

// TestOverlayBundled_NilLibraryIsNoOp guards the defensive nil
// branch — callers shouldn't have to nil-check before merging.
func TestOverlayBundled_NilLibraryIsNoOp(t *testing.T) {
	OverlayBundled(nil) // should not panic
}

// TestDecodeBundleSkillBytes_NoFrontmatterErrors pins the error
// branch for the embedded loader: a file with no frontmatter is
// rejected because bundle skills can't fall back to a basename
// (the file lives under a bundle namespace).
func TestDecodeBundleSkillBytes_NoFrontmatterErrors(t *testing.T) {
	_, err := decodeBundleSkillBytes([]byte("just a body, no frontmatter"))
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

// TestDecodeBundleSkillBytes_HappyPath ensures the happy path
// produces a populated *Skill with Name + Description + Body.
func TestDecodeBundleSkillBytes_HappyPath(t *testing.T) {
	raw := []byte(`---
name: test-skill
description: a test skill for the decoder
---
# test-skill

This is the body.
`)
	got, err := decodeBundleSkillBytes(raw)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if got.Name != "test-skill" || got.Description == "" {
		t.Errorf("unexpected decoded skill: %+v", got)
	}
	if got.Body == "" {
		t.Error("expected non-empty body")
	}
}
