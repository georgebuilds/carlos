// boottrace.go - slice 9f boot-performance instrumentation.
//
// CARLOS_BOOT_TRACE (any non-empty value) turns on a wall-clock
// checkpoint trace of the TUI boot path: process start -> config loaded
// -> state.db open -> dispatch ready -> agent seeded -> first bubbletea
// frame. On the final checkpoint (Finish) the trace prints ONE compact
// line to stderr, e.g.
//
//	carlos boot trace: config_loaded=2.1ms db_open=6.4ms dispatch_ready=7.0ms agent_ready=11.3ms first_frame=48.9ms
//
// Each value is cumulative milliseconds since process start, so the last
// figure IS the launch-to-first-frame number the slice-9f budget
// (<100ms) is measured against.
//
// Note on "process start": main.go carries uncommitted work and is off
// limits for this slice, so the zero point is bootTraceProcessStart,
// captured by a package-level var initializer. Package init runs after
// the Go runtime boots but before main(), so the delta from true exec()
// is the runtime+init cost (~1-2ms on an M3) - small, constant, and
// included in any external PTY measurement for comparison.
//
// Off-path cost: bootTraceFromEnv returns a nil *bootTrace when the env
// var is unset, and every method is nil-receiver safe, so the disabled
// path is a single pointer nil-check per checkpoint.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// bootTraceProcessStart is the trace zero point, captured at package
// init - the earliest instant reachable without editing main.go.
var bootTraceProcessStart = time.Now()

// bootTraceEnv is the opt-in switch. Non-empty value = trace on.
const bootTraceEnv = "CARLOS_BOOT_TRACE"

// bootMark is one named checkpoint: elapsed is the cumulative duration
// since the trace zero point.
type bootMark struct {
	name    string
	elapsed time.Duration
}

// bootTrace accumulates checkpoints and prints them once on Finish.
// A nil *bootTrace is the disabled state; all methods no-op on nil.
type bootTrace struct {
	start time.Time
	out   io.Writer

	mu    sync.Mutex
	marks []bootMark
	done  bool
}

// bootTraceFromEnv builds the trace for the real boot path: enabled by
// CARLOS_BOOT_TRACE, anchored at process (package-init) start, printing
// to stderr. Returns nil (disabled) when the env var is empty.
func bootTraceFromEnv() *bootTrace {
	return newBootTrace(os.Getenv(bootTraceEnv), os.Stderr, bootTraceProcessStart)
}

// newBootTrace returns a trace anchored at start writing to out, or nil
// when enabled is empty (the disabled, zero-cost state). A nil out
// falls back to io.Discard; a zero start falls back to time.Now().
func newBootTrace(enabled string, out io.Writer, start time.Time) *bootTrace {
	if enabled == "" {
		return nil
	}
	if out == nil {
		out = io.Discard
	}
	if start.IsZero() {
		start = time.Now()
	}
	return &bootTrace{start: start, out: out}
}

// Mark records a named checkpoint at the current wall clock. No-op on a
// nil or already-finished trace.
func (t *bootTrace) Mark(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.marks = append(t.marks, bootMark{name: name, elapsed: time.Since(t.start)})
}

// Finish records the final checkpoint and prints the one-line summary.
// Idempotent: only the first Finish prints; later Finish/Mark calls
// no-op (the chat TUI re-enters its View loop on every chat<->manage
// swap, and only the FIRST first-frame is a boot measurement).
func (t *bootTrace) Finish(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	t.marks = append(t.marks, bootMark{name: name, elapsed: time.Since(t.start)})
	fmt.Fprintln(t.out, t.lineLocked())
}

// lineLocked renders the compact summary. Caller must hold t.mu.
func (t *bootTrace) lineLocked() string {
	var b strings.Builder
	b.WriteString("carlos boot trace:")
	for _, m := range t.marks {
		fmt.Fprintf(&b, " %s=%s", m.name, formatBootDur(m.elapsed))
	}
	return b.String()
}

// formatBootDur renders a duration as fixed-point milliseconds with one
// decimal ("48.9ms"). Millisecond resolution with a single decimal is
// the sweet spot: enough to see a 0.4ms pragma, no fake nanosecond
// precision in a wall-clock trace.
func formatBootDur(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000.0)
}
