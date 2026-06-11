package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRunGit_TruncatesOutput — output exceeding maxBytes is cut and a
// truncation marker appended.
func TestRunGit_TruncatesOutput(t *testing.T) {
	dir := mkGitRepo(t)
	// Add a file with many lines so `git show HEAD` is comfortably long.
	big := bytes.Repeat([]byte("line of content\n"), 200)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "add big")

	out, exit, err := runGit(context.Background(), dir, 64, "show", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if exit != 0 {
		t.Fatalf("git show exit = %d", exit)
	}
	if !bytes.Contains(out, []byte("[truncated,")) {
		t.Errorf("expected truncation marker; got %q", out)
	}
}

// TestRunGit_MaxBytesDefault — passing maxBytes<=0 falls back to the
// 8 KiB default rather than returning empty output.
func TestRunGit_MaxBytesDefault(t *testing.T) {
	dir := mkGitRepo(t)
	out, exit, err := runGit(context.Background(), dir, 0, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	// Clean repo -> empty porcelain, but the call must succeed (no panic,
	// no spurious truncation marker).
	if bytes.Contains(out, []byte("[truncated")) {
		t.Errorf("clean status should not be truncated: %q", out)
	}
}

// TestRunGit_NonZeroExitNoError — a git command that exits non-zero (here
// log on an unknown ref) returns the exit code distinctly from an infra
// error.
func TestRunGit_NonZeroExitNoError(t *testing.T) {
	dir := mkGitRepo(t)
	_, exit, err := runGit(context.Background(), dir, 1024, "rev-parse", "--verify", "no-such-ref")
	if err != nil {
		t.Fatalf("non-zero exit should not be an infra error: %v", err)
	}
	if exit == 0 {
		t.Error("expected a non-zero exit for an unknown ref")
	}
}

// commit stages all changes and commits with the inline identity.
func commit(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}
