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

// TestCurator_ArchiveCollisionSurfacesError: when an _archive/<name>/
// directory already exists, the sweep refuses to overwrite it, keeps
// the skill in Active, and surfaces the error. Regression for the
// archiveSkill "dest already exists" guard + the SweepOnce firstErr
// archive-failure branch.
func TestCurator_ArchiveCollisionSurfacesError(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := mkLibSkill(t, root, "collide", base)
	s.Status = skills.StatusStale
	// Pre-create the destination so the rename collides.
	collisionDir := filepath.Join(root, "_archive", "collide")
	if err := os.MkdirAll(collisionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lib := &skills.Library{Active: []*skills.Skill{s}}

	rep, err := skills.NewCurator().SweepOnce(context.Background(), lib, base.Add(91*24*time.Hour))
	if err == nil {
		t.Fatal("want archive-collision error")
	}
	// Skill stays in Active (counted active) and is NOT archived.
	if rep.Archived != 0 {
		t.Errorf("want 0 archived on collision, got %d", rep.Archived)
	}
	if len(lib.Active) != 1 {
		t.Errorf("collided skill should stay in Active, got %d", len(lib.Active))
	}
	if s.Status == skills.StatusArchived {
		t.Error("skill should NOT be marked archived after a failed move")
	}
}

// TestCurator_AlreadyArchivedTallied: a skill already in StatusArchived
// is tallied in the archived bucket without re-moving it (exercises the
// default-case archived tally).
func TestCurator_AlreadyArchivedTallied(t *testing.T) {
	now := time.Now().UTC()
	s := &skills.Skill{Name: "done", Description: "Use when done", Status: skills.StatusArchived, Created: now}
	lib := &skills.Library{Active: []*skills.Skill{s}}
	rep, err := skills.NewCurator().SweepOnce(context.Background(), lib, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Archived != 1 || rep.Active != 0 {
		t.Errorf("want 1 archived tally, got %+v", rep)
	}
	if len(rep.Transitions) != 0 {
		t.Errorf("no transition expected for already-archived, got %v", rep.Transitions)
	}
}

// TestCurator_AlreadyStaleNotYetArchivedTallied: a stale skill that is
// idle past the stale threshold but NOT yet past archive is tallied in
// the stale bucket with no transition.
func TestCurator_AlreadyStaleTallied(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := mkLibSkill(t, root, "still-stale", base)
	s.Status = skills.StatusStale
	lib := &skills.Library{Active: []*skills.Skill{s}}
	// 40 days: past stale (30) but well short of archive (90).
	rep, err := skills.NewCurator().SweepOnce(context.Background(), lib, base.Add(40*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stale != 1 {
		t.Errorf("want 1 stale tally, got %+v", rep)
	}
	if len(rep.Transitions) != 0 {
		t.Errorf("no transition expected for already-stale skill, got %v", rep.Transitions)
	}
}

// TestCurator_ContextCancelled: a cancelled context aborts the sweep
// before processing entries.
func TestCurator_ContextCancelled(t *testing.T) {
	now := time.Now().UTC()
	s := &skills.Skill{Name: "x", Description: "Use when x", Created: now}
	lib := &skills.Library{Active: []*skills.Skill{s}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := skills.NewCurator().SweepOnce(ctx, lib, now); err == nil {
		t.Error("want context-cancelled error")
	}
}

// TestCurator_NilSkillSkipped: a nil entry in Active is skipped without
// panicking and is dropped from the survivor set.
func TestCurator_NilSkillSkipped(t *testing.T) {
	now := time.Now().UTC()
	lib := &skills.Library{Active: []*skills.Skill{
		nil,
		{Name: "real", Description: "Use when real", Created: now},
	}}
	rep, err := skills.NewCurator().SweepOnce(context.Background(), lib, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Active != 1 {
		t.Errorf("want 1 active, got %+v", rep)
	}
	if len(lib.Active) != 1 {
		t.Errorf("nil entry should be dropped, got %d survivors", len(lib.Active))
	}
}

// TestCurator_NoTimestampsTreatedAsFresh: a skill with neither Created
// nor LastUsed is treated as zero-age (never auto-archived). Exercises
// the skillIdleAge zero-ref defensive branch.
func TestCurator_NoTimestampsTreatedAsFresh(t *testing.T) {
	s := &skills.Skill{Name: "no-ts", Description: "Use when no timestamps"}
	lib := &skills.Library{Active: []*skills.Skill{s}}
	rep, err := skills.NewCurator().SweepOnce(context.Background(), lib, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Active != 1 || rep.Stale != 0 || rep.Archived != 0 {
		t.Errorf("timestamp-less skill should stay active, got %+v", rep)
	}
}

// TestCurator_FutureCreatedClampedToZero: a Created in the future yields
// zero idle age (now.Before(ref) branch in skillIdleAge).
func TestCurator_FutureCreatedClampedToZero(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)
	s := &skills.Skill{Name: "future", Description: "Use when future", Created: future}
	lib := &skills.Library{Active: []*skills.Skill{s}}
	rep, err := skills.NewCurator().SweepOnce(context.Background(), lib, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Active != 1 {
		t.Errorf("future-created skill should be active (zero age), got %+v", rep)
	}
}

// TestCurator_ZeroThresholdsFallBackToDefaults: a Curator with zeroed
// thresholds uses the package defaults rather than treating everything
// as instantly stale.
func TestCurator_ZeroThresholdsFallBackToDefaults(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	s := mkLibSkill(t, root, "zero-thresh", now)
	lib := &skills.Library{Active: []*skills.Skill{s}}
	cur := &skills.Curator{} // StaleAfter=0, ArchiveAfter=0
	rep, err := cur.SweepOnce(context.Background(), lib, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Active != 1 {
		t.Errorf("zeroed thresholds should fall back to defaults; fresh skill stays active, got %+v", rep)
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
