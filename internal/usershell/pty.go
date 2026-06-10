package usershell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// killGrace is how long we wait between SIGTERM and SIGKILL when the
// caller cancels a job. Matches opencode's recipe; 200ms is the
// sweet spot between "well-behaved processes get to clean up" and
// "the user doesn't notice the delay".
const killGrace = 200 * time.Millisecond

// shellPicker selects the user's shell. Default: $SHELL → platform
// fallback. Exposed as a package-level variable so tests can
// substitute a deterministic shell without env-var manipulation.
var shellPicker = pickShell

// pickShell reads $SHELL and falls back to a platform-appropriate
// default. We don't try to find the "best" shell - whatever the user
// has set, we use, because their aliases / functions / completions
// live there.
func pickShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	return "/bin/sh"
}

// runner is the subprocess driver an Execer hands to the Manager.
// Stub exists so tests can substitute a deterministic in-process
// runner instead of forking a real shell.
type runner interface {
	// Start begins execution. Returns a reader that streams the
	// process's combined stdout+stderr (PTYs merge the two; that's
	// part of the point), a wait func that blocks until the
	// process exits and returns its exit code or a fail error, and
	// a kill func that requests termination of the whole process
	// group.
	Start(ctx context.Context, command, cwd string) (io.Reader, func() (int, error), func(), error)
}

// ptyRunner is the production runner: spawns $SHELL -c <command> in
// its own process group via a pseudo-tty so interactive programs
// (git, less-paged commands, anything that probes isatty) behave as
// the user expects.
type ptyRunner struct{}

// Start implements runner via creack/pty. Cancellation is two-
// staged: ctx.Done OR the returned kill func send SIGTERM to the
// negative pgid, wait killGrace, then SIGKILL if the process
// is still alive. The PTY file descriptor is closed last so the
// io.Reader returned to the caller sees EOF on a clean exit.
func (ptyRunner) Start(ctx context.Context, command, cwd string) (io.Reader, func() (int, error), func(), error) {
	shell := shellPicker()
	cmd := exec.Command(shell, "-c", command)
	if cwd != "" {
		cmd.Dir = cwd
	}
	// Process group leader so we can SIGTERM/SIGKILL the whole
	// tree. Setsid implies Setpgid on Linux + macOS and is what
	// `creack/pty` recommends for clean job-control behavior.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	tty, err := pty.Start(cmd)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("usershell: pty start: %w", err)
	}

	// Goroutine: react to ctx cancellation by killing the group.
	// Separate from the wait func because cancellation can happen
	// before the wait func is ever called (TUI dispatches a kill
	// after deciding the user pressed esc).
	killOnce := sync.Once{}
	doKill := func() {
		killOnce.Do(func() {
			pid := cmd.Process.Pid
			// SIGTERM the group; give it killGrace to clean up.
			_ = syscall.Kill(-pid, syscall.SIGTERM)
			time.Sleep(killGrace)
			// Best-effort SIGKILL. ESRCH (process already gone)
			// is the expected case after a polite SIGTERM.
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		})
	}

	// doWait reaps the subprocess exactly once. The Manager's runJob
	// always calls wait(); but an orphaned caller (one that grabs the
	// kill func and drops wait) used to leak the proc + the pty fd +
	// the ctx-watcher goroutine. Now the ctx-watcher invokes doWait
	// itself when ctx fires, so the cleanup converges either way.
	var (
		waitOnce sync.Once
		waitExit int
		waitErr  error
		waitDone = make(chan struct{})
	)
	doWait := func() {
		waitOnce.Do(func() {
			defer close(waitDone)
			err := cmd.Wait()
			if err == nil {
				waitExit = cmd.ProcessState.ExitCode()
				return
			}
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				waitExit = ee.ExitCode()
				return
			}
			waitExit = -1
			waitErr = err
		})
	}

	// closeTTY guards tty.Close behind a sync.Once so the wait-path
	// and the ctx-watcher path can both attempt it without double-
	// closing the fd (which would be reported as EBADF on macOS /
	// Linux). The first caller wins; the second is a no-op.
	var ttyCloseOnce sync.Once
	closeTTY := func() {
		ttyCloseOnce.Do(func() {
			if err := tty.Close(); err != nil {
				warnf("pty close (pid %d): %v", cmd.Process.Pid, err)
			}
		})
	}

	go func() {
		select {
		case <-ctx.Done():
			// Defensive cleanup for the orphaned-caller path:
			// kill the group, reap the proc, close the fd.
			// If the caller does later invoke wait(), doWait's
			// sync.Once makes it a cheap pass-through and the
			// recorded exit/err are still returned.
			doKill()
			doWait()
			closeTTY()
		case <-waitDone:
			// wait() path is handling cleanup; nothing more to do.
		}
	}()

	wait := func() (int, error) {
		doWait()
		// Always close the tty after wait returns so the reader
		// goroutine sees EOF. Without this it parks on a pty fd
		// no one will ever write to again.
		closeTTY()
		return waitExit, waitErr
	}

	return tty, wait, doKill, nil
}

// RingBuffer is a fixed-capacity byte buffer that drops the OLDEST
// bytes on overflow. Used to keep the live PTY output in memory for
// the transcript view without unbounded growth - `tail -f /dev/null`
// running for an hour stays at `cap` bytes regardless.
//
// Concurrent-safe: the read goroutine writes, the TUI reads. Lock
// granularity is the whole buffer because the operations are tiny
// (memcpy + bookkeeping) and contention is one writer + occasional
// reader.
type RingBuffer struct {
	mu    sync.Mutex
	buf   []byte
	cap   int
	full  bool
	start int // index of the oldest valid byte when full
}

// NewRingBuffer constructs a buffer with the given capacity. cap <= 0
// is normalized to a sensible default (64 KiB) so callers don't have
// to remember.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 64 * 1024
	}
	return &RingBuffer{cap: capacity}
}

// Write appends p to the buffer, dropping the oldest bytes if the
// total would exceed cap. Always returns len(p), nil - the buffer
// never errors, just rolls over. Satisfies io.Writer so an io.Copy
// from the PTY reader writes straight in.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	// Common case: input fits without overflow.
	if !r.full && len(r.buf)+n <= r.cap {
		r.buf = append(r.buf, p...)
		return n, nil
	}
	// Overflow path. If p alone is bigger than cap, keep only the
	// tail of p and discard everything else.
	if n >= r.cap {
		r.buf = append(r.buf[:0], p[n-r.cap:]...)
		r.full = true
		r.start = 0
		return n, nil
	}
	// Else: extend the buffer to cap if not yet full, then advance
	// the start pointer for any additional input.
	if !r.full {
		// Fill up to cap first.
		room := r.cap - len(r.buf)
		if room > 0 {
			take := room
			if take > n {
				take = n
			}
			r.buf = append(r.buf, p[:take]...)
			p = p[take:]
			n2 := len(p)
			if n2 == 0 {
				return n, nil
			}
			// Remaining bytes wrap the buffer.
			r.full = true
		}
	}
	// We're full now; copy remaining p into the start of the
	// buffer, advancing start by the same amount so reads emit the
	// correct order.
	if len(p) > 0 {
		// Flatten: rewrite buf as [tail+p] sliced to cap.
		// Easier than maintaining a true ring with two slices.
		tail := r.snapshotLocked()
		combined := append(tail, p...)
		if len(combined) > r.cap {
			combined = combined[len(combined)-r.cap:]
		}
		r.buf = append(r.buf[:0], combined...)
		r.start = 0
	}
	return n, nil
}

// Snapshot returns a copy of the buffer's logical contents in order
// (oldest byte first, newest last). The caller may modify the
// returned slice freely.
func (r *RingBuffer) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

// snapshotLocked is the lock-held variant Write uses internally.
func (r *RingBuffer) snapshotLocked() []byte {
	if !r.full {
		out := make([]byte, len(r.buf))
		copy(out, r.buf)
		return out
	}
	// When full + start>0, we'd need to splice; flattening on
	// every overflow keeps start=0 always, so this branch is just
	// a straight copy.
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

// Len returns how many bytes the buffer currently holds.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf)
}

// Cap returns the buffer's capacity.
func (r *RingBuffer) Cap() int { return r.cap }
