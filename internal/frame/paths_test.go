package frame

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathsFor_BuildsLayout(t *testing.T) {
	p := PathsFor("/home/u", "work")
	want := map[string]string{
		"Root":         "/home/u/.carlos/frames/work",
		"ResearchDir":  "/home/u/.carlos/frames/work/research",
		"JobsDir":      "/home/u/.carlos/frames/work/usershell",
		"WorktreesDir": "/home/u/.carlos/frames/work/worktrees",
		"DigestDir":    "/home/u/.carlos/frames/work/digest",
	}
	got := map[string]string{
		"Root":         p.Root,
		"ResearchDir":  p.ResearchDir,
		"JobsDir":      p.JobsDir,
		"WorktreesDir": p.WorktreesDir,
		"DigestDir":    p.DigestDir,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestPathsFor_ZeroValueOnEmptyArgs(t *testing.T) {
	if (PathsFor("", "x") != Paths{}) {
		t.Error("empty home should return zero Paths")
	}
	if (PathsFor("/home/u", "") != Paths{}) {
		t.Error("empty name should return zero Paths")
	}
}

func TestMigrate_MovesLegacyResearch(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".carlos", "research")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "alpha.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "beta.md"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := Migrate(home, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if report.ResearchMoved != 2 {
		t.Errorf("ResearchMoved = %d, want 2", report.ResearchMoved)
	}
	if !report.HasMovement() {
		t.Error("HasMovement() returned false despite moves")
	}

	dest := filepath.Join(home, ".carlos", "frames", "personal", "research")
	for _, name := range []string{"alpha.md", "beta.md"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("expected %s at destination: %v", name, err)
		}
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy dir should be removed after drain; got err=%v", err)
	}
}

func TestMigrate_IdempotentReRun(t *testing.T) {
	home := t.TempDir()
	if _, err := Migrate(home, "personal"); err != nil {
		t.Fatal(err)
	}
	report, err := Migrate(home, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if report.HasMovement() {
		t.Errorf("second Migrate moved %d files; expected no-op", report.ResearchMoved+report.JobsMoved+report.WorktreesMoved)
	}
}

func TestMigrate_SkipsExistingDestinationFiles(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".carlos", "research")
	dest := filepath.Join(home, ".carlos", "frames", "personal", "research")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	// Same name exists at both; legacy should be skipped (frame-scoped wins).
	if err := os.WriteFile(filepath.Join(legacy, "same.md"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "same.md"), []byte("frame"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Migrate(home, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if report.ResearchMoved != 0 {
		t.Errorf("ResearchMoved = %d, want 0", report.ResearchMoved)
	}
	if report.ResearchSkipped != 1 {
		t.Errorf("ResearchSkipped = %d, want 1", report.ResearchSkipped)
	}
	got, _ := os.ReadFile(filepath.Join(dest, "same.md"))
	if string(got) != "frame" {
		t.Errorf("dest content overwritten: %q", got)
	}
}

func TestMigrate_DefaultsToPersonalWhenNameEmpty(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".carlos", "research")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "x.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(home, ""); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, ".carlos", "frames", DefaultPersonalName, "research", "x.md")
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("file did not land at default personal frame: %v", err)
	}
}

func TestMigrate_EmptyHomeIsError(t *testing.T) {
	if _, err := Migrate("", "personal"); err == nil {
		t.Error("expected error for empty home")
	}
}

func TestMigrate_NoLegacyDirsIsNoOp(t *testing.T) {
	home := t.TempDir()
	report, err := Migrate(home, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if report.HasMovement() || len(report.Errors) > 0 {
		t.Errorf("expected clean no-op; got %+v", report)
	}
}
