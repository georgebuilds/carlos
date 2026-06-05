package sandbox

import (
	"bytes"
	"testing"
)

// TestCapWriter_StopsAtMax confirms the per-stream cap is hard. The
// producer's Write returns the full input length (we never want to block
// or error a child process), but the buffer never grows past max and the
// dropped counter tracks the overflow.
func TestCapWriter_StopsAtMax(t *testing.T) {
	w := newCapWriter(10)
	n, err := w.Write([]byte("0123456789ABCDEF"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 16 {
		t.Fatalf("Write returned n=%d, want 16", n)
	}
	if got := w.Bytes(); !bytes.Equal(got, []byte("0123456789")) {
		t.Fatalf("buf=%q, want 0123456789", got)
	}
	if w.dropped != 6 {
		t.Fatalf("dropped=%d, want 6", w.dropped)
	}
}

// TestCapWriter_MultipleWrites — across many small writes, the cap is
// still respected end-to-end. Mirrors how stdout/stderr arrive from a
// real process (lots of little chunks).
func TestCapWriter_MultipleWrites(t *testing.T) {
	w := newCapWriter(5)
	for _, s := range []string{"abc", "def", "ghi"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if got := w.Bytes(); !bytes.Equal(got, []byte("abcde")) {
		t.Fatalf("buf=%q, want abcde", got)
	}
	if w.dropped != 4 {
		t.Fatalf("dropped=%d, want 4", w.dropped)
	}
}

// TestRunCommand_EmptyCmd guards the "infrastructure error" boundary —
// an empty cmd is a programming bug, not a child failure, so we return
// err and exit=-1 rather than running anything.
func TestRunCommand_EmptyCmd(t *testing.T) {
	_, _, exit, err := runCommand(nil, "", nil, nil) //nolint:staticcheck // intentional nil ctx; we never reach exec.
	if err == nil {
		t.Fatalf("runCommand(nil): expected error")
	}
	if exit != -1 {
		t.Fatalf("exit=%d, want -1", exit)
	}
}
