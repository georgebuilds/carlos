package mcp

import "sync"

// defaultStderrCap is how many bytes of a server's stderr we retain. 8 KiB
// is enough to capture a typical startup banner plus a stack-traceish error
// without letting a chatty (or runaway) child balloon carlos's RSS. The
// buffer keeps the *tail*, so the most recent - and usually most diagnostic
// - output wins when a server exceeds the cap.
const defaultStderrCap = 8192

// boundedBuffer is a thread-safe io.Writer that retains only the last cap
// bytes written to it. It exists so MCP server subprocesses can scribble to
// stderr at any time without that output painting over the live TUI frame:
// we point each child's cmd.Stderr at one of these instead of os.Stderr, and
// surface the captured tail only when we actually need it (a connect failure,
// or an operator-facing StderrTail read).
//
// Write never errors and never short-writes - it always reports len(p) - so
// a child blocked on a full pipe is impossible. When a single Write (or the
// running total) exceeds cap, the oldest bytes are dropped and only the most
// recent cap bytes are kept (truncate-from-the-front / ring semantics).
type boundedBuffer struct {
	mu  sync.Mutex
	cap int
	buf []byte
}

// newBoundedBuffer returns a boundedBuffer that retains at most cap bytes. A
// non-positive cap falls back to defaultStderrCap so a misconfigured caller
// can't create an always-empty (cap 0) or panicking (negative) buffer.
func newBoundedBuffer(cap int) *boundedBuffer {
	if cap <= 0 {
		cap = defaultStderrCap
	}
	return &boundedBuffer{cap: cap}
}

// Write appends p to the buffer, dropping the oldest bytes so the retained
// contents never exceed cap. It always returns (len(p), nil): we deliberately
// account every byte as "written" even when we immediately discard the
// overflow, because the child only cares that its pipe drained, not that we
// kept the bytes.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	// A single write larger than cap can only ever leave its own last cap
	// bytes behind, so short-circuit and skip growing buf past cap.
	if n >= b.cap {
		b.buf = append(b.buf[:0], p[n-b.cap:]...)
		return n, nil
	}
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.cap; over > 0 {
		// Drop the oldest `over` bytes, sliding the tail to the front so we
		// don't keep growing the backing array unboundedly.
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	return n, nil
}

// String returns the retained stderr tail as a string. An empty (or never
// written) buffer yields "". Safe to call concurrently with Write.
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
