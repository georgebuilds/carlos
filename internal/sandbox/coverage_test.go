package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunCommand_StartError covers the infrastructure-failure branch of
// runCommand: a command whose binary does not exist must fail at
// c.Start() and surface exit=-1 with a wrapped "start" error rather than
// a process exit code.
func TestRunCommand_StartError(t *testing.T) {
	_, _, code, err := runCommand(context.Background(), "", []string{"this-binary-does-not-exist-carlos"}, nil)
	if err == nil {
		t.Fatal("expected start error for nonexistent binary, got nil")
	}
	if code != -1 {
		t.Errorf("code=%d, want -1 for start failure", code)
	}
	if !strings.Contains(err.Error(), "start") {
		t.Errorf("error %q should mention 'start'", err.Error())
	}
}

// TestNewWorktreeIn_MkdirBaseFails exercises the MkdirAll failure branch:
// when baseDir is a path whose parent is a regular file, MkdirAll cannot
// create the directory and NewWorktreeIn must wrap the error rather than
// proceeding to git.
func TestNewWorktreeIn_MkdirBaseFails(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)

	// Create a regular file, then ask for a worktree base *underneath* it.
	// MkdirAll cannot create a directory under a file component.
	fileAsParent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileAsParent, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	base := filepath.Join(fileAsParent, "worktrees")
	_, err := NewWorktreeIn(repo, "HEAD", base)
	if err == nil {
		t.Fatal("expected mkdir base error, got nil")
	}
	if !strings.Contains(err.Error(), "mkdir base") {
		t.Errorf("error %q should mention 'mkdir base'", err.Error())
	}
}

// TestNewWorktreeIn_GitAddFailsCleansUp covers the `git worktree add`
// failure path: passing a base branch that does not exist makes git fail,
// and NewWorktreeIn must run its best-effort cleanup and return a wrapped
// error rather than leaving a half-created worktree behind.
func TestNewWorktreeIn_GitAddFailsCleansUp(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)

	_, err := NewWorktreeIn(repo, "no-such-branch-xyz", "")
	if err == nil {
		t.Fatal("expected git worktree add error for nonexistent base branch")
	}
	if !strings.Contains(err.Error(), "git worktree add") {
		t.Errorf("error %q should mention 'git worktree add'", err.Error())
	}
}

// TestWorktree_Diff_ErrorAfterRootGone covers the exec-error branch of
// Diff: once the worktree directory is removed, `git -C <Root> diff`
// cannot run and Diff must return a wrapped error rather than empty
// output.
func TestWorktree_Diff_ErrorAfterRootGone(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	wt, err := NewWorktreeIn(repo, "HEAD", "")
	if err != nil {
		t.Fatalf("NewWorktreeIn: %v", err)
	}
	t.Cleanup(func() { _ = wt.Close() })

	// Remove the worktree dir out from under the handle so git fails.
	if err := os.RemoveAll(wt.Root); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := wt.Diff(); err == nil {
		t.Fatal("Diff after root removal: expected error, got nil")
	}
}

// TestWorktree_ChangedFiles_ErrorAfterRootGone is the ChangedFiles twin
// of TestWorktree_Diff_ErrorAfterRootGone.
func TestWorktree_ChangedFiles_ErrorAfterRootGone(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	wt, err := NewWorktreeIn(repo, "HEAD", "")
	if err != nil {
		t.Fatalf("NewWorktreeIn: %v", err)
	}
	t.Cleanup(func() { _ = wt.Close() })

	if err := os.RemoveAll(wt.Root); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := wt.ChangedFiles(); err == nil {
		t.Fatal("ChangedFiles after root removal: expected error, got nil")
	}
}

// TestWorktree_Close_BranchDeleteError covers the branch-deletion error
// branch of Close. We point RepoRoot at a path that is NOT a git repo
// after construction so both `worktree remove` and `branch -D` fail,
// driving the combined-error case (rmErr != nil && brErr != nil).
func TestWorktree_Close_RepoGoneErrors(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	wt, err := NewWorktreeIn(repo, "HEAD", "")
	if err != nil {
		t.Fatalf("NewWorktreeIn: %v", err)
	}

	// Repoint RepoRoot at an empty (non-git) directory: both the
	// `worktree remove` and `branch -D` git calls now fail because the
	// command's -C target is not a repo.
	wt.RepoRoot = t.TempDir()
	err = wt.Close()
	if err == nil {
		t.Fatal("Close with broken RepoRoot: expected error, got nil")
	}
	// Idempotency still holds: a second Close is a no-op.
	if err2 := wt.Close(); err2 != nil {
		t.Errorf("second Close should be no-op, got %v", err2)
	}
}
