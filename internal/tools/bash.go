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
	ptyDiscarded := 0
	if t.PTY {
		ptyDiscarded, runErr = t.runPTY(execCtx, cmd, &buf, maxLen)
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
		// Non-PTY path: cmd writes directly into buf via Stdout/Stderr,
		// so any overflow lives in the buffer and the discard count is
		// the buffer overshoot minus the cap.
		truncated = len(out) - maxLen
		out = out[:maxLen]
	}
	// PTY mode tracks discarded bytes inside cappedWriter so we get an
	// honest count (the buffer is capped at write-time, so len(out) can
	// never exceed maxLen for the PTY path). Either path can contribute,
	// never both — pick the non-zero one.
	if ptyDiscarded > 0 {
		truncated += ptyDiscarded
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
// into buf, capped at maxLen bytes. Bytes past the cap are counted and
// returned as `discarded` so Execute can render a single truthful
// truncation marker. ctx cancel SIGKILLs the whole process group via
// the same -pid trick used elsewhere.
//
// We rely on github.com/creack/pty for the cross-platform ptmx/grantpt
// dance. The library has zero transitive dependencies (only stdlib +
// build-tagged per-OS syscalls), so the supply-chain cost is negligible.
func (t *BashTool) runPTY(ctx context.Context, cmd *exec.Cmd, buf *bytes.Buffer, maxLen int) (discarded int, err error) {
	f, ptyErr := pty.Start(cmd)
	if ptyErr != nil {
		return 0, fmt.Errorf("bash: pty start: %w", ptyErr)
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

	// Copy with a cap: we don't need to read past maxLen because Execute
	// owns the truncation marker, but we *do* need to keep draining so
	// the child doesn't block on a full PTY buffer. cappedWriter
	// silently discards bytes past the cap and records the count so
	// Execute can report the true overflow figure.
	cw := &cappedWriter{buf: buf, max: maxLen}
	_, _ = io.Copy(cw, f)
	waitErr := cmd.Wait()
	close(done)
	return cw.Discarded(), waitErr
}

// cappedWriter writes up to `max` bytes into buf, then silently
// discards the rest while counting the discarded byte total. Necessary
// in PTY mode so we never block the child on a stalled io.Copy.
//
// The writer does NOT append its own truncation marker — Execute owns
// the single marker so PTY and non-PTY modes produce identical
// "[truncated, N more bytes]" tails, with N reflecting the actual
// dropped bytes rather than an inflated count that includes overflow-
// buffer bytes or a hand-stamped sentinel.
type cappedWriter struct {
	buf       *bytes.Buffer
	max       int
	discarded int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	remaining := w.max - w.buf.Len()
	if remaining <= 0 {
		w.discarded += len(p)
		return len(p), nil
	}
	if len(p) <= remaining {
		return w.buf.Write(p)
	}
	w.buf.Write(p[:remaining])
	w.discarded += len(p) - remaining
	return len(p), nil
}

// Discarded reports the number of bytes Write was asked to consume past
// the cap. Combined with the in-buf bytes this equals the total output
// the child produced.
func (w *cappedWriter) Discarded() int { return w.discarded }

// Compile-time check: BashTool implements Tool.
var _ Tool = (*BashTool)(nil)
