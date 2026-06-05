package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitOrSkip skips the test if git is missing from PATH (no point exercising
// the worktree backend in a git-less CI). Returns the discovered git path,
// though tests use the package's own implementation (which itself relies
// on `git` on PATH) — the lookup just gates the skip.
func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// useTempHome redirects HOME to t.TempDir() so NewWorktree writes its
// worktrees under the test's sandbox, not the developer's real home.
// Restores HOME via t.Cleanup.
func useTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// mkRepo initialises a fresh git repo at t.TempDir(), commits an initial
// file so HEAD is real, and returns the repo path. Branch is forced to
// `main` so tests don't have to care about the user's init.defaultBranch.
func mkRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGit(t, dir, "add", "README.md")
	mustGit(t, dir, "commit", "-m", "init")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestWorktree_HappyPath is the headline test. End-to-end: spawn a
// worktree, commit a file inside it, Diff returns that diff,
// ChangedFiles lists the file, Apply merges back into the parent, Close
// tears down. If any link in this chain breaks, Phase 4's approval gate
// has nothing to render.
func TestWorktree_HappyPath(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)

	w, err := NewWorktree(repo, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	// Path under the temp home must exist after add.
	if _, err := os.Stat(w.Root); err != nil {
		t.Fatalf("worktree root missing: %v", err)
	}
	if !strings.HasPrefix(w.Branch, "carlos/") {
		t.Fatalf("Branch=%q, want carlos/<id>", w.Branch)
	}

	// Sub-agent does its thing: write a new file and commit it.
	newFile := filepath.Join(w.Root, "file2")
	if err := os.WriteFile(newFile, []byte("new content\n"), 0o644); err != nil {
		t.Fatalf("write file2: %v", err)
	}
	mustGit(t, w.Root, "config", "user.email", "agent@example.com")
	mustGit(t, w.Root, "config", "user.name", "Agent")
	mustGit(t, w.Root, "add", "file2")
	mustGit(t, w.Root, "commit", "-m", "add file2")

	// Diff must include the new file.
	diff, err := w.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(string(diff), "file2") {
		t.Fatalf("diff missing file2:\n%s", diff)
	}
	if !strings.Contains(string(diff), "new content") {
		t.Fatalf("diff missing content:\n%s", diff)
	}

	// ChangedFiles must list exactly file2.
	files, err := w.ChangedFiles()
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "file2" {
		t.Fatalf("ChangedFiles=%v, want [file2]", files)
	}

	// Apply: parent HEAD should now contain file2.
	if err := w.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "file2")); err != nil {
		t.Fatalf("after Apply, file2 missing from parent: %v", err)
	}

	// Close removes the worktree dir and the branch.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(w.Root); !os.IsNotExist(err) {
		t.Fatalf("after Close, worktree dir still exists: err=%v", err)
	}
	branches := gitOut(t, repo, "branch", "--list", w.Branch)
	if branches != "" {
		t.Fatalf("after Close, branch %s still present: %q", w.Branch, branches)
	}
}

// TestWorktree_ApplyRefusesNonFF locks in the apply discipline: if the
// parent advanced past the worktree's base, Apply must refuse rather
// than dragging the user into a surprise merge commit. This is the
// safety-belt for the manage-mode approval queue.
func TestWorktree_ApplyRefusesNonFF(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)

	w, err := NewWorktree(repo, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Sub-agent commits its thing.
	if err := os.WriteFile(filepath.Join(w.Root, "agent.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write agent.txt: %v", err)
	}
	mustGit(t, w.Root, "config", "user.email", "agent@example.com")
	mustGit(t, w.Root, "config", "user.name", "Agent")
	mustGit(t, w.Root, "add", "agent.txt")
	mustGit(t, w.Root, "commit", "-m", "agent change")

	// Parent advances HEAD with a separate commit.
	if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("p\n"), 0o644); err != nil {
		t.Fatalf("write parent.txt: %v", err)
	}
	mustGit(t, repo, "add", "parent.txt")
	mustGit(t, repo, "commit", "-m", "parent advance")

	// ff-only merge must refuse.
	if err := w.Apply(); err == nil {
		t.Fatalf("Apply: expected non-ff error, got nil")
	}
}

// TestWorktree_Discard removes the path and the branch — same behaviour
// as Close, exposed under a name that maps to the user's "reject" choice
// in the approval queue.
func TestWorktree_Discard(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)

	w, err := NewWorktree(repo, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	branch := w.Branch
	root := w.Root

	// Sub-agent scribbles untracked + committed work; both should vanish.
	if err := os.WriteFile(filepath.Join(root, "scratch"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write scratch: %v", err)
	}

	if err := w.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("after Discard, root still exists: err=%v", err)
	}
	if br := gitOut(t, repo, "branch", "--list", branch); br != "" {
		t.Fatalf("after Discard, branch %s still present: %q", branch, br)
	}
}

// TestWorktree_CloseIdempotent — supervisor defer chains call Close
// after Apply (or after a no-op error path). Second Close must be a
// no-op.
func TestWorktree_CloseIdempotent(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)

	w, err := NewWorktree(repo, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestWorktree_ExecCtxCancelKillsChild mirrors the Local test for the
// worktree backend — the process-group kill path is shared, but this
// test pins the contract at the Backend interface boundary.
func TestWorktree_ExecCtxCancelKillsChild(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not on PATH")
	}

	w, err := NewWorktree(repo, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, _, _, err = w.Exec(ctx, []string{"sleep", "10"}, nil)
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("Exec returned after %s; ctx-kill not effective", elapsed)
	}
	if err == nil {
		t.Fatalf("Exec returned nil err after cancel")
	}
}

// TestWorktree_ExecRunsInWorktree confirms commands run with cwd =
// w.Root. If the dir wiring is broken, `pwd` would return the test's
// working dir, not the worktree's.
func TestWorktree_ExecRunsInWorktree(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	if _, err := exec.LookPath("pwd"); err != nil {
		t.Skip("pwd not on PATH")
	}

	w, err := NewWorktree(repo, "main")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	stdout, _, exit, err := w.Exec(context.Background(), []string{"pwd"}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	// macOS resolves /var → /private/var via realpath, so compare via a
	// stat-based equality rather than a string compare.
	want, _ := filepath.EvalSymlinks(w.Root)
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(stdout)))
	if got != want {
		t.Fatalf("Exec cwd=%q, want %q", got, want)
	}
}

// TestNewWorktree_NotARepo confirms NewWorktree wraps the "not a git
// repo" failure cleanly instead of panicking. Important: the supervisor
// must be able to display this error to the user without a stack trace.
func TestNewWorktree_NotARepo(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	dir := t.TempDir() // empty, not a repo
	_, err := NewWorktree(dir, "HEAD")
	if err == nil {
		t.Fatalf("NewWorktree on non-repo: expected error")
	}
	if !strings.Contains(err.Error(), "not a git repo") {
		t.Fatalf("error %q does not mention 'not a git repo'", err.Error())
	}
}

// TestNewWorktree_EmptyRepoRoot confirms the empty-string guard fires
// before we shell out to git.
func TestNewWorktree_EmptyRepoRoot(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	_, err := NewWorktree("", "HEAD")
	if err == nil {
		t.Fatalf("NewWorktree(\"\"): expected error")
	}
}

// TestNewWorktreeFactory exercises the New("worktree", repoRoot, base)
// path so changes to the factory signature surface in tests.
func TestNewWorktreeFactory(t *testing.T) {
	gitOrSkip(t)
	useTempHome(t)
	repo := mkRepo(t)
	b, err := New("worktree", repo, "main")
	if err != nil {
		t.Fatalf("New(worktree): %v", err)
	}
	defer b.Close()
	if b.Name() != "worktree" {
		t.Fatalf("Name=%q, want worktree", b.Name())
	}
}

// TestNewWorktreeFactory_WrongArity makes sure the factory rejects a bad
// arg count rather than panicking on index-out-of-range.
func TestNewWorktreeFactory_WrongArity(t *testing.T) {
	_, err := New("worktree", "only-one-arg")
	if err == nil {
		t.Fatalf("New(worktree, 1 arg): expected error")
	}
}
