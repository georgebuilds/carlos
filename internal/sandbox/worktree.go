package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree runs commands inside a fresh `git worktree` checked out on a
// private branch under `<home>/.carlos/worktrees/<id>/`. See the package
// doc for the "why" and the apply discipline.
//
// Lifecycle:
//
//  1. [NewWorktree] creates the worktree and the private branch.
//  2. Sub-agent calls [Worktree.Exec] N times.
//  3. The supervisor (Phase 4 approval gate) calls [Worktree.Diff] and
//     [Worktree.ChangedFiles] to render what happened.
//  4. The user picks one of:
//     a. [Worktree.Apply] — merge ff-only into the parent. May fail if
//     the parent advanced; the UI surfaces the error.
//     b. [Worktree.Discard] — throw the work away.
//  5. [Worktree.Close] runs regardless. It is idempotent so step 4a/4b
//     and step 5 can both be deferred safely.
type Worktree struct {
	// Root is the absolute path of the worktree on disk. Commands run
	// here.
	Root string
	// Branch is the private branch checked out in the worktree
	// (`carlos/<id>`). Apply merges this branch into RepoRoot's HEAD;
	// Close deletes it.
	Branch string
	// RepoRoot is the originating repo — the parent. `git worktree add`,
	// `git worktree remove`, the apply-step `git merge --ff-only`, and
	// branch deletion all run with `-C RepoRoot`.
	RepoRoot string
	// baseBranch is the ref Worktree was checked out from. Diff is
	// computed as baseBranch...HEAD so it shows only the sub-agent's
	// commits, not unrelated drift.
	baseBranch string
	// closed tracks Close idempotency.
	closed bool
}

// NewWorktree creates a fresh worktree under `<home>/.carlos/worktrees/<id>/`
// checked out at baseBranch. The private branch is named `carlos/<id>`
// where <id> is 12 hex chars from crypto/rand — collision probability is
// negligible and the name is short enough to read in a `git branch` list.
//
// repoRoot must be a path inside a git repository (we let `git worktree
// add` complain if it isn't, but pre-check with `git rev-parse` so the
// error message is ours, not git's). baseBranch is typically "HEAD" or a
// concrete branch name; the value is passed verbatim to `git worktree
// add`.
//
// If anything fails, NewWorktree cleans up partial state before returning
// — the caller does not have to worry about orphaned directories or
// branches.
func NewWorktree(repoRoot, baseBranch string) (*Worktree, error) {
	return NewWorktreeIn(repoRoot, baseBranch, "")
}

// NewWorktreeIn is the Phase F-17 variant that lets the caller specify
// the parent directory the worktree is created under. Empty baseDir
// falls back to `<home>/.carlos/worktrees/` (legacy behavior).
// Frame-aware callers pass the active frame's WorktreesDir so each
// frame's sub-agent sandboxes live alongside its other artifacts.
func NewWorktreeIn(repoRoot, baseBranch, baseDir string) (*Worktree, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("sandbox/worktree: git not found on PATH: %w", err)
	}
	if repoRoot == "" {
		return nil, errors.New("sandbox/worktree: repoRoot is empty")
	}
	if baseBranch == "" {
		baseBranch = "HEAD"
	}
	// Pre-flight: confirm repoRoot is actually a git repo so we can fail
	// with a clean wrapped error instead of git's stderr leaking into the
	// caller.
	check := exec.Command("git", "-C", repoRoot, "rev-parse", "--git-dir")
	if out, err := check.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sandbox/worktree: %s is not a git repo: %w (%s)", repoRoot, err, strings.TrimSpace(string(out)))
	}

	id, err := randID(6) // 12 hex chars
	if err != nil {
		return nil, fmt.Errorf("sandbox/worktree: mint id: %w", err)
	}
	branch := "carlos/" + id

	base := baseDir
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("sandbox/worktree: home dir: %w", err)
		}
		base = filepath.Join(home, ".carlos", "worktrees")
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox/worktree: mkdir base: %w", err)
	}
	wtPath := filepath.Join(base, id)

	// `git worktree add -b <branch> <path> <baseBranch>` creates the
	// branch from baseBranch and checks it out into a new worktree at
	// <path>. The branch is shared with the parent repo — that's how the
	// later `git merge --ff-only <branch>` from the parent picks up the
	// sub-agent's commits.
	add := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", branch, wtPath, baseBranch)
	if out, err := add.CombinedOutput(); err != nil {
		// Best-effort cleanup of anything git did create.
		_ = exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", wtPath).Run()
		_ = exec.Command("git", "-C", repoRoot, "branch", "-D", branch).Run()
		_ = os.RemoveAll(wtPath)
		return nil, fmt.Errorf("sandbox/worktree: git worktree add: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return &Worktree{
		Root:       wtPath,
		Branch:     branch,
		RepoRoot:   repoRoot,
		baseBranch: baseBranch,
	}, nil
}

// Name returns "worktree".
func (*Worktree) Name() string { return "worktree" }

// Exec runs cmd inside the worktree. Combined cap at 8 KiB per stream;
// ctx-cancel kills the process tree.
func (w *Worktree) Exec(ctx context.Context, cmd []string, stdin io.Reader) ([]byte, []byte, int, error) {
	if w == nil {
		return nil, nil, -1, errors.New("sandbox/worktree: nil receiver")
	}
	return runCommand(ctx, w.Root, cmd, stdin)
}

// Diff returns `git -C <Root> diff <baseBranch>...HEAD` — the
// three-dot form, which shows changes on HEAD relative to the merge
// base with baseBranch. That's exactly what the manage-mode approval
// pane wants to render: "what did the sub-agent do, ignoring anything
// the parent did concurrently."
func (w *Worktree) Diff() ([]byte, error) {
	spec := w.baseBranch + "...HEAD"
	c := exec.Command("git", "-C", w.Root, "diff", spec)
	out, err := c.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("sandbox/worktree: git diff: %w (%s)", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("sandbox/worktree: git diff: %w", err)
	}
	return out, nil
}

// ChangedFiles lists files changed in the worktree vs base — same
// semantics as [Diff] but `--name-only`. Used to surface "the model wants
// to edit these files" in the approval pane.
func (w *Worktree) ChangedFiles() ([]string, error) {
	spec := w.baseBranch + "...HEAD"
	c := exec.Command("git", "-C", w.Root, "diff", "--name-only", spec)
	out, err := c.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("sandbox/worktree: git diff --name-only: %w (%s)", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("sandbox/worktree: git diff --name-only: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	// strings.Split("", "\n") returns [""]; collapse that to an empty
	// list so callers can `if len(files) == 0` without surprise.
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}

// Apply merges the worktree's branch back into the parent repo's HEAD
// via `git merge --ff-only`. Refuses non-fast-forward — see the package
// doc for justification. After a successful Apply, the supervisor still
// calls Close to clean up the worktree directory and the now-merged
// branch.
//
// Apply does NOT call Close itself: keeping the two semantics separate
// makes the lifecycle easier to reason about (Apply vs Discard is "what
// happens to the work"; Close is "release the resource").
func (w *Worktree) Apply() error {
	c := exec.Command("git", "-C", w.RepoRoot, "merge", "--ff-only", w.Branch)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sandbox/worktree: git merge --ff-only %s: %w (%s)", w.Branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Discard is a semantic alias for Close. The supervisor calls Discard
// when the user rejects the work in the approval queue; the worktree
// directory and the private branch both go away.
func (w *Worktree) Discard() error {
	return w.Close()
}

// Close removes the worktree and deletes the private branch. Idempotent
// — a second call is a no-op so Apply-then-Close and Discard-then-Close
// (or any belt-and-braces defer chain) both work.
//
// We use --force on `worktree remove` because the sub-agent may have
// left untracked files behind that we don't want to surface as an
// error; the directory is private to carlos and disposable by design.
func (w *Worktree) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true

	// Remove the worktree first. If the branch deletion is attempted
	// while the worktree still references it, git refuses with "branch
	// is checked out at ..." — and the operator is then left with a
	// dangling worktree.
	rmOut, rmErr := exec.Command("git", "-C", w.RepoRoot, "worktree", "remove", "--force", w.Root).CombinedOutput()
	brOut, brErr := exec.Command("git", "-C", w.RepoRoot, "branch", "-D", w.Branch).CombinedOutput()
	// Best-effort path cleanup in case `worktree remove` half-succeeded.
	_ = os.RemoveAll(w.Root)

	switch {
	case rmErr != nil && brErr != nil:
		return fmt.Errorf("sandbox/worktree: close (worktree remove + branch -D failed): %w (%s) / %w (%s)",
			rmErr, strings.TrimSpace(string(rmOut)), brErr, strings.TrimSpace(string(brOut)))
	case rmErr != nil:
		return fmt.Errorf("sandbox/worktree: worktree remove: %w (%s)", rmErr, strings.TrimSpace(string(rmOut)))
	case brErr != nil:
		return fmt.Errorf("sandbox/worktree: branch -D %s: %w (%s)", w.Branch, brErr, strings.TrimSpace(string(brOut)))
	}
	return nil
}

// randID returns 2*n hex chars from crypto/rand. n=6 → 12 chars, enough
// to make collision within a single user's worktree directory practically
// impossible while still being short enough to read.
func randID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Compile-time check.
var _ Backend = (*Worktree)(nil)
