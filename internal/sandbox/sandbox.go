// Package sandbox provides execution backends for carlos sub-agents.
//
// Two backends ship today:
//
//   - [Local]: runs commands in the user's actual working directory. Suited
//     to read-only sub-agents (search, inspect, summarize) where filesystem
//     isolation would just add friction.
//   - [Worktree]: runs commands inside a fresh `git worktree` checked out
//     on a private branch under `<home>/.carlos/worktrees/<id>/`. Suited to
//     any sub-agent that may write to shared state — file edits, git
//     operations, bash commands that mutate the workspace.
//
// # Why worktree per side-effecting agent
//
// Carlos manage-mode spawns many sub-agents in parallel. If two of them
// both edited the same checkout, their changes would race; if one of them
// made a destructive change, the user would have no clean way to reject it
// without affecting the others. By giving each side-effecting sub-agent
// its own [Worktree], we get:
//
//   - Parallel isolation. Sub-agents can edit, build, and run tests
//     concurrently without stepping on each other.
//   - A safe "apply gate". The sub-agent's work lives on a private branch
//     until the user (or a future policy) explicitly merges it back into
//     the parent repo via [Worktree.Apply]. A reject is just
//     [Worktree.Discard] — the work disappears, the parent is untouched.
//
// # When NOT to use a worktree
//
// Read-only sub-agents don't need the isolation, and a worktree adds
// startup cost + disk footprint. Use [Local] for those.
//
// # Apply discipline: fast-forward only
//
// [Worktree.Apply] uses `git merge --ff-only`. If the parent repo's HEAD
// has advanced past the worktree's base while the sub-agent was working,
// the merge refuses — Apply returns an error the UI surfaces, and the user
// resolves manually (typically by re-running the sub-agent on the new
// base). No silent merge commits, no surprise conflict markers in the
// working tree.
//
// # Phase 4 hookup
//
// The manage-mode focus pane will call [Worktree.Diff] and
// [Worktree.ChangedFiles] to render what the sub-agent did. The approval
// queue gates [Worktree.Apply] vs [Worktree.Discard]. [Worktree.Close]
// always runs after either, idempotently.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
)

// Backend is the abstraction sub-agents call when they want to execute a
// command. Implementations decide where the command runs (host cwd vs a
// private worktree) and own teardown via Close.
type Backend interface {
	// Name identifies the backend kind: "local" or "worktree".
	Name() string
	// Exec runs cmd[0] cmd[1:]... in the backend's working directory.
	// Combined stdout/stderr are each capped at maxOutputBytes. Context
	// cancel kills the entire process tree, not just the direct child.
	Exec(ctx context.Context, cmd []string, stdin io.Reader) (stdout, stderr []byte, exit int, err error)
	// Close releases backend resources. Must be idempotent — Phase 4's
	// approval queue calls Close after Apply or Discard regardless of
	// what happened in between.
	Close() error
}

// New constructs a Backend by kind:
//
//   - kind == "local": args ignored; returns a [Local].
//   - kind == "worktree": args must be exactly {repoRoot, baseBranch};
//     returns a fresh [Worktree] via [NewWorktree].
//
// Anything else is a programming error and returns a wrapped error so
// callers can surface it without panicking.
func New(kind string, args ...string) (Backend, error) {
	switch kind {
	case "local":
		return &Local{}, nil
	case "worktree":
		if len(args) != 2 {
			return nil, fmt.Errorf("sandbox.New(%q): expected 2 args (repoRoot, baseBranch), got %d", kind, len(args))
		}
		return NewWorktree(args[0], args[1])
	default:
		return nil, fmt.Errorf("sandbox.New: unknown kind %q (expected \"local\" or \"worktree\")", kind)
	}
}

// maxOutputBytes is the per-stream cap on captured output. Mirrors the
// bash tool's 8 KiB ceiling: a sub-agent should not be able to flood the
// supervisor's context window by `cat`-ing a huge log.
const maxOutputBytes = 8 * 1024

// capWriter is an io.Writer that drops bytes once max have been written.
// It exposes how many bytes were dropped so the caller can mark output as
// truncated if it cares (the Backend interface currently does not — the
// supervisor is expected to render output as-is and the cap is a hard
// safety belt, not a UX feature).
type capWriter struct {
	buf      bytes.Buffer
	max      int
	dropped  int
}

func newCapWriter(max int) *capWriter { return &capWriter{max: max} }

func (w *capWriter) Write(p []byte) (int, error) {
	remaining := w.max - w.buf.Len()
	if remaining <= 0 {
		w.dropped += len(p)
		return len(p), nil // pretend we wrote it; never block the producer
	}
	if len(p) <= remaining {
		return w.buf.Write(p)
	}
	w.buf.Write(p[:remaining])
	w.dropped += len(p) - remaining
	return len(p), nil
}

func (w *capWriter) Bytes() []byte { return w.buf.Bytes() }

// runCommand is the shared exec primitive used by both [Local] and
// [Worktree]. It:
//
//   - sets the child's working directory to dir (empty → default Go cwd)
//   - wires stdin if provided
//   - captures stdout/stderr separately into per-stream capWriters
//   - places the child in its own process group via Setpgid so that
//     ctx-cancel can SIGKILL the whole tree, not just the immediate child
//     (bash + descendants would otherwise survive)
//   - returns exit code 0 on success, the process's exit code on a
//     non-zero exit (not surfaced as err — that's caller policy), and -1
//     for infrastructure-level failures
func runCommand(ctx context.Context, dir string, cmd []string, stdin io.Reader) (stdout, stderr []byte, exit int, err error) {
	if len(cmd) == 0 {
		return nil, nil, -1, errors.New("sandbox: empty command")
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	if dir != "" {
		c.Dir = dir
	}
	if stdin != nil {
		c.Stdin = stdin
	}
	outW := newCapWriter(maxOutputBytes)
	errW := newCapWriter(maxOutputBytes)
	c.Stdout = outW
	c.Stderr = errW
	// New process group so we can kill the whole tree.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := c.Start(); err != nil {
		return nil, nil, -1, fmt.Errorf("sandbox: start %s: %w", cmd[0], err)
	}

	// Goroutine to react to context cancel: SIGKILL the negative pgid so
	// the entire process group dies. Sending to -pid hits every member of
	// the group (POSIX semantics). The Wait below will then return.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Best-effort. Process may already be gone; the syscall just
			// returns ESRCH in that case, which we ignore.
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		case <-done:
		}
	}()

	waitErr := c.Wait()
	close(done)

	exit = 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exit = ee.ExitCode()
			waitErr = nil
		}
	}
	// If ctx was cancelled, surface that distinctly. Exit will be -1 or
	// 137 (SIGKILL) depending on how the kill landed; the err is the
	// authoritative signal.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return outW.Bytes(), errW.Bytes(), exit, ctxErr
	}
	return outW.Bytes(), errW.Bytes(), exit, waitErr
}

