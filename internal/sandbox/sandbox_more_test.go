package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktree_Exec_NilReceiver(t *testing.T) {
	var w *Worktree
	_, _, code, err := w.Exec(context.Background(), []string{"true"}, nil)
	if err == nil {
		t.Fatal("expected error on nil receiver")
	}
	if code != -1 {
		t.Errorf("code=%d want -1", code)
	}
}

func TestWorktree_Name(t *testing.T) {
	var w *Worktree
	if got := w.Name(); got != "worktree" {
		t.Errorf("Name=%q want worktree", got)
	}
}

func TestWorktree_Close_NilReceiver(t *testing.T) {
	var w *Worktree
	if err := w.Close(); err != nil {
		t.Errorf("Close on nil should be no-op, got %v", err)
	}
}

func TestWorktree_Close_Idempotent(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	wt, err := NewWorktreeIn(repo, "HEAD", "")
	if err != nil {
		t.Fatalf("NewWorktreeIn: %v", err)
	}
	if err := wt.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := wt.Close(); err != nil {
		t.Errorf("second Close should be no-op, got %v", err)
	}
}

func TestWorktree_Diff_NoChangesEmpty(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	wt, err := NewWorktreeIn(repo, "HEAD", "")
	if err != nil {
		t.Fatalf("NewWorktreeIn: %v", err)
	}
	defer wt.Close()
	out, err := wt.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("Diff with no changes returned %q", string(out))
	}
}

func TestWorktree_ChangedFiles_None(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	wt, err := NewWorktreeIn(repo, "HEAD", "")
	if err != nil {
		t.Fatalf("NewWorktreeIn: %v", err)
	}
	defer wt.Close()
	files, err := wt.ChangedFiles()
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("ChangedFiles with no changes: %v", files)
	}
}

func TestWorktree_ChangedFiles_AfterEdit(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	wt, err := NewWorktreeIn(repo, "main", "")
	if err != nil {
		t.Fatalf("NewWorktreeIn: %v", err)
	}
	defer wt.Close()
	newFile := filepath.Join(wt.Root, "added.txt")
	if err := os.WriteFile(newFile, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wt.Root, "add", "added.txt")
	mustGit(t, wt.Root, "commit", "-m", "add file")
	files, err := wt.ChangedFiles()
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "added.txt" {
		t.Errorf("ChangedFiles=%v want [added.txt]", files)
	}
	out, err := wt.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(string(out), "added.txt") {
		t.Errorf("Diff output missing changed file: %q", string(out))
	}
}

func TestWorktree_Exec_RunsInsideRoot(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	wt, err := NewWorktreeIn(repo, "HEAD", "")
	if err != nil {
		t.Fatalf("NewWorktreeIn: %v", err)
	}
	defer wt.Close()
	stdout, _, code, err := wt.Exec(context.Background(), []string{"pwd"}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != 0 {
		t.Errorf("code=%d", code)
	}
	if !strings.Contains(string(stdout), wt.Root) {
		t.Errorf("pwd=%q should be under %q", string(stdout), wt.Root)
	}
}

func TestNewWorktreeIn_CustomBaseDir(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	base := filepath.Join(t.TempDir(), "frames", "personal", "worktrees")
	wt, err := NewWorktreeIn(repo, "HEAD", base)
	if err != nil {
		t.Fatalf("NewWorktreeIn: %v", err)
	}
	defer wt.Close()
	if !strings.HasPrefix(wt.Root, base) {
		t.Errorf("Root=%q should be under base=%q", wt.Root, base)
	}
}

func TestRandID_NonEmptyAndDistinct(t *testing.T) {
	a, errA := randID(8)
	b, errB := randID(8)
	if errA != nil || errB != nil {
		t.Fatalf("randID errs: %v %v", errA, errB)
	}
	if a == "" || b == "" {
		t.Error("randID returned empty")
	}
	if a == b {
		t.Errorf("randID collision: %q", a)
	}
}
