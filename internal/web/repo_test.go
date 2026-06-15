package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// mkGitDir makes dir/.git a real directory (a plain repo).
func mkGitDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestGitRepoRoot_PlainRepo(t *testing.T) {
	root := t.TempDir()
	mkGitDir(t, root)

	got, name, ok := gitRepoRoot(root)
	if !ok {
		t.Fatal("plain repo: ok=false, want true")
	}
	if got != filepath.Clean(root) {
		t.Errorf("root = %q, want %q", got, root)
	}
	if name != filepath.Base(root) {
		t.Errorf("name = %q, want %q", name, filepath.Base(root))
	}
}

func TestGitRepoRoot_NestedSubdir(t *testing.T) {
	root := t.TempDir()
	mkGitDir(t, root)
	sub := filepath.Join(root, "internal", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, name, ok := gitRepoRoot(sub)
	if !ok {
		t.Fatal("nested subdir: ok=false, want true")
	}
	if got != filepath.Clean(root) {
		t.Errorf("root = %q, want walk-up to %q", got, root)
	}
	if name != filepath.Base(root) {
		t.Errorf("name = %q, want %q", name, filepath.Base(root))
	}
}

func TestGitRepoRoot_LinkedWorktreeFoldsToMain(t *testing.T) {
	main := t.TempDir()
	mkGitDir(t, main)
	// A linked worktree: its .git is a FILE pointing at
	// <main>/.git/worktrees/<name>. gitRepoRoot must resolve to <main>.
	wtName := "feature-x"
	worktreesDir := filepath.Join(main, ".git", "worktrees", wtName)
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir() // the worktree's working dir, anywhere on disk
	gitFile := filepath.Join(wt, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+worktreesDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, name, ok := gitRepoRoot(wt)
	if !ok {
		t.Fatal("linked worktree: ok=false, want true")
	}
	if got != filepath.Clean(main) {
		t.Errorf("root = %q, want main worktree root %q", got, main)
	}
	if name != filepath.Base(main) {
		t.Errorf("name = %q, want %q", name, filepath.Base(main))
	}
}

func TestGitRepoRoot_WorktreeFromNestedSubdir(t *testing.T) {
	main := t.TempDir()
	mkGitDir(t, main)
	worktreesDir := filepath.Join(main, ".git", "worktrees", "wt")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+worktreesDir), 0o644); err != nil {
		t.Fatal(err)
	}
	// Resolve from a directory NESTED inside the worktree: the walk-up finds
	// the worktree's .git file, then folds to main.
	sub := filepath.Join(wt, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, _, ok := gitRepoRoot(sub)
	if !ok || got != filepath.Clean(main) {
		t.Errorf("nested-in-worktree = %q/%v, want %q/true", got, ok, main)
	}
}

func TestGitRepoRoot_NonGit(t *testing.T) {
	// A temp dir with no .git anywhere up the chain. t.TempDir lives under a
	// path that is not a git repo in the test environment.
	got, name, ok := gitRepoRoot(t.TempDir())
	if ok {
		t.Errorf("non-git: ok=true (root %q), want false", got)
	}
	if name != "" || got != "" {
		t.Errorf("non-git: got %q/%q, want empty/empty", got, name)
	}
}

func TestGitRepoRoot_EmptyCwd(t *testing.T) {
	if _, _, ok := gitRepoRoot(""); ok {
		t.Error("empty cwd: ok=true, want false")
	}
}

func TestGitRepoRoot_GarbageGitFileFallsBack(t *testing.T) {
	// A .git FILE with no gitdir: line is degenerate; the resolver falls back
	// to treating the holding dir as the root rather than erroring.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a worktree pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, name, ok := gitRepoRoot(dir)
	if !ok || got != filepath.Clean(dir) || name != filepath.Base(dir) {
		t.Errorf("garbage .git file = %q/%q/%v, want holding dir/true", got, name, ok)
	}
}

func TestGitRepoRoot_WorktreePointerWithoutMarker(t *testing.T) {
	// A .git file whose gitdir: line does NOT contain "/.git/worktrees/":
	// mainWorktreeRoot returns false, and gitRepoRoot falls back to the
	// holding directory as the root.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /somewhere/odd"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, ok := gitRepoRoot(dir)
	if !ok || got != filepath.Clean(dir) {
		t.Errorf("malformed gitdir = %q/%v, want holding dir/true", got, ok)
	}
}

func TestGroupStore_RepoStoreErrorsWhenClosed(t *testing.T) {
	_, path := newTestLog(t)
	gs := newTestGroups(t, path)
	_ = gs.Close() // subsequent queries error
	ctx := context.Background()
	if err := gs.SetThreadRepo(ctx, "t1", "/x", "x"); err == nil {
		t.Error("SetThreadRepo on a closed store should error")
	}
	if _, err := gs.RepoMap(ctx); err == nil {
		t.Error("RepoMap on a closed store should error")
	}
}

func TestGroupStore_SetThreadRepoAndMap(t *testing.T) {
	_, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	if err := gs.SetThreadRepo(ctx, "t1", "/Users/george/Code/anneal", "anneal"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Upsert: a second write updates in place, not a duplicate key error.
	if err := gs.SetThreadRepo(ctx, "t1", "/Users/george/Code/carlos", "carlos"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := gs.SetThreadRepo(ctx, "cc:abc", "/Users/george/Code/kino", "kino"); err != nil {
		t.Fatalf("set cc: %v", err)
	}

	m, err := gs.RepoMap(ctx)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if m["t1"] != (RepoRef{Root: "/Users/george/Code/carlos", Name: "carlos"}) {
		t.Errorf("t1 = %+v, want carlos (upserted)", m["t1"])
	}
	if m["cc:abc"] != (RepoRef{Root: "/Users/george/Code/kino", Name: "kino"}) {
		t.Errorf("cc:abc = %+v, want kino (backend agnostic)", m["cc:abc"])
	}

	// Empty thread id is rejected.
	if err := gs.SetThreadRepo(ctx, "", "/x", "x"); err == nil {
		t.Error("empty thread id should error")
	}
}
