package skills_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/skills"
)

// TestCurator_FreshSkillUnchanged: a skill created today, swept today,
// stays active.
func TestCurator_FreshSkillUnchanged(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	s := mkLibSkill(t, root, "fresh", now)
	lib := &skills.Library{Active: []*skills.Skill{s}}

	rep, err := skills.NewCurator().SweepOnce(context.Background(), lib, now)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.Active != 1 || rep.Stale != 0 || rep.Archived != 0 {
		t.Errorf("want 1 active, got %+v", rep)
	}
	if len(rep.Transitions) != 0 {
		t.Errorf("want 0 transitions, got %v", rep.Transitions)
	}
}

// TestCurator_ActiveToStaleTransition: a skill 31 days idle goes
// stale; its SKILL.md frontmatter is rewritten with status: stale.
func TestCurator_ActiveToStaleTransition(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := mkLibSkill(t, root, "going-stale", base)
	lib := &skills.Library{Active: []*skills.Skill{s}}

	// Sweep 31 days later.
	rep, err := skills.NewCurator().SweepOnce(context.Background(), lib, base.Add(31*24*time.Hour))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.Stale != 1 {
		t.Errorf("want 1 stale, got %+v", rep)
	}
	if s.Status != skills.StatusStale {
		t.Errorf("want StatusStale on skill, got %q", s.Status)
	}
	// Confirm the on-disk SKILL.md was rewritten.
	loaded, err := skills.LoadSkill(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != skills.StatusStale {
		t.Errorf("on-disk status: want stale, got %q", loaded.Status)
	}
}

// TestCurator_StaleToArchivedTransition: a stale skill 91 days idle is
// archived (moved to _archive/<name>/).
func TestCurator_StaleToArchivedTransition(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := mkLibSkill(t, root, "going-archived", base)
	s.Status = skills.StatusStale
	lib := &skills.Library{Active: []*skills.Skill{s}}

	rep, err := skills.NewCurator().SweepOnce(context.Background(), lib, base.Add(91*24*time.Hour))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.Archived != 1 {
		t.Errorf("want 1 archived, got %+v", rep)
	}
	if len(lib.Active) != 0 {
		t.Errorf("archived skill should drop from Active, got %d", len(lib.Active))
	}
	// The directory should now live under root/_archive/<name>/.
	want := filepath.Join(root, "_archive", "going-archived")
	if _, err := os.Stat(filepath.Join(want, "SKILL.md")); err != nil {
		t.Errorf("expected archived dir at %s: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(root, "going-archived")); !os.IsNotExist(err) {
		t.Errorf("original dir should be gone: %v", err)
	}
}

// TestCurator_FullPipeline: walking through active → stale → archived
// over two sweeps preserves the lifecycle.
func TestCurator_FullPipeline(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := mkLibSkill(t, root, "pipeline", base)
	lib := &skills.Library{Active: []*skills.Skill{s}}
	cur := skills.NewCurator()

	// Sweep at 31d → stale.
	if _, err := cur.SweepOnce(context.Background(), lib, base.Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.Status != skills.StatusStale {
		t.Fatalf("want stale at 31d, got %q", s.Status)
	}
	// Sweep again at 91d → archived.
	if _, err := cur.SweepOnce(context.Background(), lib, base.Add(91*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.Status != skills.StatusArchived {
		t.Errorf("want archived at 91d, got %q", s.Status)
	}
}

// TestCurator_LastUsedDrivesIdleAge: a recently-used skill stays active
// even if created long ago.
func TestCurator_LastUsedDrivesIdleAge(t *testing.T) {
	root := t.TempDir()
	long := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s := mkLibSkill(t, root, "recent-use", long)
	recent := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s.LastUsed = &recent
	// Rewrite to disk so loaded copies share the LastUsed.
	if err := skills.WriteSkill(s.Path, s); err != nil {
		t.Fatal(err)
	}

	lib := &skills.Library{Active: []*skills.Skill{s}}
	rep, err := skills.NewCurator().SweepOnce(context.Background(), lib, time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Active != 1 || rep.Stale != 0 {
		t.Errorf("recently-used skill should stay active, got %+v", rep)
	}
}

// TestCurator_NeverHardDelete: archived directories survive multiple
// sweeps (no rm).
func TestCurator_NeverHardDelete(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := mkLibSkill(t, root, "preserved", base)
	lib := &skills.Library{Active: []*skills.Skill{s}}
	cur := skills.NewCurator()
	if _, err := cur.SweepOnce(context.Background(), lib, base.Add(91*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Sweep again at 365d; the archive dir must still exist.
	if _, err := cur.SweepOnce(context.Background(), lib, base.Add(365*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "_archive", "preserved", "SKILL.md")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("archived skill must survive second sweep: %v", err)
	}
}

// TestCurator_NilLibrary: defensive nil check.
func TestCurator_NilLibrary(t *testing.T) {
	_, err := skills.NewCurator().SweepOnce(context.Background(), nil, time.Now())
	if err == nil {
		t.Error("want error for nil library")
	}
}

// mkLibSkill builds a fresh skill on disk under root/<name>/ with the
// given Created timestamp; returns the loaded *Skill (Path populated).
func mkLibSkill(t *testing.T, root, name string, created time.Time) *skills.Skill {
	t.Helper()
	dir := filepath.Join(root, name)
	s := &skills.Skill{
		Name:        name,
		Description: "Use when running curator tests against " + name,
		Provenance:  skills.ProvInduced,
		Created:     created,
		Updated:     created,
		Body:        "body\n",
	}
	if err := skills.WriteSkill(dir, s); err != nil {
		t.Fatalf("WriteSkill(%s): %v", name, err)
	}
	loaded, err := skills.LoadSkill(dir)
	if err != nil {
		t.Fatalf("LoadSkill(%s): %v", name, err)
	}
	return loaded
}
