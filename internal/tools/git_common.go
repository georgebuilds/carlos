package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// gitTimeout bounds any single git invocation. 30s matches BashTool and
// is generous for read-only commands (status, log, diff) even on a large
// repository.
const gitTimeout = 30 * time.Second

// gitMaxOutputBytes is the per-stream output cap. Individual git tools
// can request a larger cap (e.g. GitDiff at 32 KiB); the default is the
// same 8 KiB ceiling we apply elsewhere.
const gitMaxOutputBytes = 8 * 1024

// runGit executes git with `args`, capturing combined stdout+stderr,
// capping output at `maxBytes` bytes, and surfacing exit code distinctly
// from infrastructure errors (mirrors BashTool). Sets the child in its
// own process group so a ctx cancel kills the whole tree.
//
// We dispatch git directly via exec rather than through BashTool because:
//   - Logs are clearer ("tool=git_status" vs "tool=bash, cmd=git status ...").
//   - No shell-quoting hazards for paths with spaces.
//   - Easier to audit: git args are first-class.
func runGit(ctx context.Context, dir string, maxBytes int, args ...string) ([]byte, int, error) {
	if maxBytes <= 0 {
		maxBytes = gitMaxOutputBytes
	}
	execCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// Own process group so we can kill any subprocess git spawns.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	// Cancel goroutine: SIGKILL the negative pgid on ctx done.
	done := make(chan struct{})
	if err := cmd.Start(); err != nil {
		return nil, -1, fmt.Errorf("git: start: %w", err)
	}
	go func() {
		select {
		case <-execCtx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()
	waitErr := cmd.Wait()
	close(done)

	exit := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exit = ee.ExitCode()
			waitErr = nil
		}
	}

	out := buf.Bytes()
	truncated := 0
	if len(out) > maxBytes {
		truncated = len(out) - maxBytes
		out = out[:maxBytes]
	}
	var result bytes.Buffer
	result.Write(out)
	if truncated > 0 {
		fmt.Fprintf(&result, "\n[truncated, %d more bytes]\n", truncated)
	}
	if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(&result, "\n[killed after %s timeout]\n", gitTimeout)
	}
	return result.Bytes(), exit, waitErr
}

// requireGitRepo returns an error if `dir` is not inside a git working
// tree. Used at the top of every git tool's Execute so we fail fast and
// give the model a clear "not a git repo" signal rather than an opaque
// git error.
func requireGitRepo(ctx context.Context, dir string) error {
	out, exit, err := runGit(ctx, dir, 1024, "rev-parse", "--git-dir")
	if err != nil {
		return fmt.Errorf("git: probe repo: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("git: not a git repository (rev-parse exit=%d): %s", exit, bytes.TrimSpace(out))
	}
	return nil
}
