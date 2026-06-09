package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// BashTool runs a shell command via `bash -c`. The Anthropic Messages API
// expects a typed JSON input matching the tool's schema; carlos's bash
// schema is `{cmd: string}`. Anything more structured (env, cwd, etc.)
// can be added later - keeping the surface minimal for v0 means the model
// doesn't have to learn many parameters.
//
// Output discipline:
//   - Combined stdout + stderr captured (model can't usefully tell them
//     apart and we don't want it asking for them separately).
//   - Truncated at maxOutputBytes (current value: 8 KiB) with a clear
//     "[truncated, N more bytes]" marker. A model that runs `cat
//     /var/log/system.log` doesn't get to flood the context window.
//   - Exit code included in the result so the model knows whether the
//     command succeeded.
//
// Cancellation: ctx kills the process tree on cancel (exec.CommandContext
// + bash's default behavior). A SIGINT from `carlos please` flows here.
//
// Timeout: hardcoded 30s default. Long-running interactive commands need
// a different tool (Slice 7b: PTY-backed bash). For carlos please's
// one-shot use case, 30s is generous.
type BashTool struct {
	Timeout      time.Duration
	WorkingDir   string // empty → caller's cwd (default Go exec behavior)
	MaxOutputLen int    // 0 → defaultMaxOutputBytes
	// BaseDir is the worktree-per-coding-task seam. When non-empty and
	// WorkingDir is empty, the child process runs with cwd = BaseDir
	// instead of the parent's cwd. Set explicitly by `carlos please
	// --worktree` so shell commands the model invokes (test runners,
	// formatters, git, etc.) see the sandboxed checkout. WorkingDir,
	// when set, still wins - explicit cwd from a future test or caller
	// trumps the implicit sandbox. Zero-value preserves existing
	// behaviour bit-for-bit.
	BaseDir string
	// PTY runs the command under a pseudoterminal. Interactive commands
	// (vim, less, gum, anything that calls isatty()) only behave
	// correctly with a real PTY on stdout. The output discipline
	// (8 KiB cap, exit-code surfacing, timeout, ctx kill) is unchanged
	// regardless of PTY mode - PTY only changes how the child is spawned.
	//
	// Note that PTY mode collapses stdout and stderr into one stream
	// (that's how a terminal works), which matches the bash tool's
	// existing "combined output" semantics anyway.
	PTY bool
}

// NewBashTool constructs a BashTool with sane defaults.
func NewBashTool() *BashTool {
	return &BashTool{Timeout: 30 * time.Second}
}

func (*BashTool) Name() string { return "bash" }

func (*BashTool) Description() string {
	return "Execute a shell command via bash -c. Combined stdout+stderr is returned, truncated at ~8 KiB. The command runs with a 30-second timeout and the agent's working directory. Use for filesystem inspection, running tests, invoking CLIs, and other ephemeral shell work."
}

// Schema returns the JSON schema the model sees. Minimal on purpose:
// every additional knob is an additional thing the model can get wrong.
func (*BashTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"cmd": {
				"type": "string",
				"description": "The shell command to run, as a single string. e.g. \"ls -la /tmp\" or \"grep -r TODO src/\"."
			}
		},
		"required": ["cmd"]
	}`)
}

type bashInput struct {
	Cmd string `json:"cmd"`
}

const defaultMaxOutputBytes = 8 * 1024

// Execute parses the input JSON, runs the command via bash -c, and
// returns the combined output + exit code as a plain text body the
// model can read directly. Errors returned are infrastructure errors
// (input parse fail, exec setup fail); a command exiting non-zero is
// NOT an error - it's part of the output the model needs to see.
func (t *BashTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("bash: parse input: %w", err)
	}
	if in.Cmd == "" {
		return nil, errors.New("bash: empty cmd")
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxLen := t.MaxOutputLen
	if maxLen <= 0 {
		maxLen = defaultMaxOutputBytes
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "bash", "-c", in.Cmd)
	switch {
	case t.WorkingDir != "":
		cmd.Dir = t.WorkingDir
	case t.BaseDir != "":
		cmd.Dir = t.BaseDir
	}
	// Place the child in its own process group so a ctx-cancel can
	// SIGKILL the whole tree, not just `bash` (which would otherwise
	// leave grandchild processes alive). Mirrors sandbox/sandbox.go's
	// runCommand pattern. With PTY mode we also set Setsid so the PTY
	// becomes the session's controlling terminal - required for
	// terminal-aware programs like vim/less to interact correctly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if t.PTY {
		cmd.SysProcAttr.Setsid = true
	}

	var buf bytes.Buffer
	var runErr error
	if t.PTY {
		runErr = t.runPTY(execCtx, cmd, &buf, maxLen)
	} else {
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		runErr = cmd.Start()
		if runErr == nil {
			// Reap the process; concurrent goroutine handles ctx cancel.
			done := make(chan struct{})
			go func() {
				select {
				case <-execCtx.Done():
					if cmd.Process != nil {
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					}
				case <-done:
				}
			}()
			runErr = cmd.Wait()
			close(done)
		}
	}

	exit := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exit = ee.ExitCode()
			runErr = nil // not an infrastructure error; surfaced via exit
		}
	}

	out := buf.Bytes()
	truncated := 0
	if len(out) > maxLen {
		truncated = len(out) - maxLen
		out = out[:maxLen]
	}

	var result bytes.Buffer
	result.Write(out)
	if truncated > 0 {
		fmt.Fprintf(&result, "\n[truncated, %d more bytes]\n", truncated)
	}
	// Include exit code so the model knows success/failure unambiguously.
	if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(&result, "\n[killed after %s timeout]\n", timeout)
	}
	fmt.Fprintf(&result, "\n[exit %d]\n", exit)

	return result.Bytes(), runErr
}

// runPTY starts cmd attached to a pseudoterminal and copies its output
// into buf, capped at maxLen + a small overflow buffer so the truncation
// math in Execute stays consistent with non-PTY mode. ctx cancel SIGKILLs
// the whole process group via the same -pid trick used elsewhere.
//
// We rely on github.com/creack/pty for the cross-platform ptmx/grantpt
// dance. The library has zero transitive dependencies (only stdlib +
// build-tagged per-OS syscalls), so the supply-chain cost is negligible.
func (t *BashTool) runPTY(ctx context.Context, cmd *exec.Cmd, buf *bytes.Buffer, maxLen int) error {
	f, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("bash: pty start: %w", err)
	}
	defer f.Close()

	// Kill the process group on ctx cancel. The PTY master close in the
	// deferred Close above will then unblock the io.Copy below as the
	// child exits.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()

	// Copy with a cap: we don't need to read past maxLen + a small
	// overflow because Execute truncates anyway, but we *do* need to
	// keep draining so the child doesn't block on a full PTY buffer.
	// Use a discarding tail after the cap is hit.
	cw := &cappedWriter{buf: buf, max: maxLen + 1024}
	_, _ = io.Copy(cw, f)
	waitErr := cmd.Wait()
	close(done)
	return waitErr
}

// cappedWriter writes up to `max` bytes into buf, then silently
// discards the rest. Necessary in PTY mode so we never block the child
// on a stalled io.Copy.
//
// When the cap is first reached, cappedWriter appends a one-shot
// truncation marker mirroring the non-PTY path's convention so the model
// can tell "command finished" apart from "output was cut". The marker is
// emitted exactly once and its bytes do NOT count toward `max` (they sit
// after the cap as a sentinel).
type cappedWriter struct {
	buf         *bytes.Buffer
	max         int
	markerWrote bool
}

// truncationMarker is the sentinel cappedWriter writes once on overflow.
// Chosen to match the non-PTY path's style (`\n[truncated, ...]\n`) so
// downstream log readers and the model see the same shape regardless of
// PTY mode. The cap is interpolated rather than the discarded byte count
// because cappedWriter can't faithfully report the true discarded total
// across subsequent writes without recording it for a marker we've
// promised to emit only once.
func truncationMarker(cap int) string {
	return fmt.Sprintf("\n[truncated, output capped at %d bytes]\n", cap)
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	remaining := w.max - w.buf.Len()
	if remaining <= 0 {
		w.writeMarkerOnce()
		return len(p), nil
	}
	if len(p) <= remaining {
		return w.buf.Write(p)
	}
	w.buf.Write(p[:remaining])
	w.writeMarkerOnce()
	return len(p), nil
}

// writeMarkerOnce appends the truncation sentinel to buf the first time
// the cap is exceeded. Subsequent calls are no-ops so repeated overflows
// don't duplicate the marker. The marker bytes sit beyond `max` and are
// not counted against the cap, so a follow-on Write that finds
// `buf.Len() > max` still routes to the discard path rather than
// re-triggering the marker (markerWrote stays true).
func (w *cappedWriter) writeMarkerOnce() {
	if w.markerWrote {
		return
	}
	w.markerWrote = true
	w.buf.WriteString(truncationMarker(w.max))
}

// Compile-time check: BashTool implements Tool.
var _ Tool = (*BashTool)(nil)
