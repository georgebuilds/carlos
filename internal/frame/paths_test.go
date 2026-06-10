package frame

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// TestMigrate_NonEXDEVRenameFailureIsSurfaced regression-covers the
// previous bug where any os.Rename failure (EACCES, ENOSPC, ...) silently
// fell back to copy+remove and inflated the moved counter, masking the
// legitimate failure. Only EXDEV ("invalid cross-device link") should
// trigger the copy fallback now; everything else must be reported in
// report.Errors and leave the moved counter at zero.
//
// Mechanism: make the legacy directory read+execute only (0o500). The
// initial ReadDir succeeds (it needs r+x), but os.Rename of the entries
// out requires write on the parent directory and fails with EACCES.
// Without the fix, the copy path also succeeds (copyFile reads the file
// + writes the destination), but os.Remove(srcPath) silently fails on
// the read-only parent and the entry is still counted as moved - the
// exact masking the fix prevents.
func TestMigrate_NonEXDEVRenameFailureIsSurfaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission semantics not available on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permission checks")
	}
	home := t.TempDir()
	legacy := filepath.Join(home, ".carlos", "research")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "alpha.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Read+execute only: ReadDir works, Rename out of the dir fails.
	if err := os.Chmod(legacy, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Restore write so t.TempDir's cleanup can drain the tree.
		_ = os.Chmod(legacy, 0o700)
	})

	report, err := Migrate(home, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if report.ResearchMoved != 0 {
		t.Errorf("ResearchMoved = %d, want 0 (rename failed, no fallback should mask it)", report.ResearchMoved)
	}
	if len(report.Errors) == 0 {
		t.Fatal("expected at least one error in report.Errors; got none")
	}
	// The surfaced error must describe the rename failure, not a copy
	// failure - the copy path must not be reached for non-EXDEV errors.
	found := false
	for _, e := range report.Errors {
		if e != nil && strings.Contains(e.Error(), "rename") && strings.Contains(e.Error(), "alpha.md") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a rename error mentioning alpha.md; got %v", report.Errors)
	}
}
