package tools

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestBash_SimpleEcho(t *testing.T) {
	tool := NewBashTool()
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"echo hello world"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !bytes.Contains(out, []byte("hello world")) {
		t.Errorf("output missing payload: %q", out)
	}
	if !bytes.Contains(out, []byte("[exit 0]")) {
		t.Errorf("output missing [exit 0] tag: %q", out)
	}
}

func TestBash_ExitCodeNonZero(t *testing.T) {
	tool := NewBashTool()
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"exit 7"}`))
	if err != nil {
		t.Fatalf("Execute: %v (non-zero exit must NOT be an infra error)", err)
	}
	if !bytes.Contains(out, []byte("[exit 7]")) {
		t.Errorf("output missing [exit 7] tag: %q", out)
	}
}

func TestBash_StderrCaptured(t *testing.T) {
	tool := NewBashTool()
	out, _ := tool.Execute(context.Background(), []byte(`{"cmd":"echo oops 1>&2"}`))
	if !bytes.Contains(out, []byte("oops")) {
		t.Errorf("stderr not captured in combined output: %q", out)
	}
}

func TestBash_OutputTruncation(t *testing.T) {
	tool := &BashTool{MaxOutputLen: 256, Timeout: 5 * time.Second}
	// Produce more than 256 bytes.
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"yes x | head -c 4096"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !bytes.Contains(out, []byte("[truncated,")) {
		t.Errorf("expected truncation marker, got: %q", out)
	}
	// Exactly one truncation marker. Previously the PTY path could
	// double-emit (cw's internal marker + Execute's external marker).
	if c := bytes.Count(out, []byte("[truncated,")); c != 1 {
		t.Errorf("expected exactly one truncation marker, got %d: %q", c, out)
	}
	// Discarded byte count must match real output - cap. `yes x | head -c 4096`
	// produces 4096 bytes; cap is 256 → discard count = 4096 - 256 = 3840.
	wantDiscard := 4096 - 256
	wantMarker := []byte("[truncated, " + itoaTest(wantDiscard) + " more bytes]")
	if !bytes.Contains(out, wantMarker) {
		t.Errorf("truncation marker should report %d discarded bytes; got: %q", wantDiscard, out)
	}
}

// itoaTest is a tiny helper to keep the truncation assertion sting-free
// without pulling strconv into the test imports.
func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestBash_Timeout(t *testing.T) {
	tool := &BashTool{Timeout: 100 * time.Millisecond, MaxOutputLen: 1024}
	start := time.Now()
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"sleep 5"}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute should not surface timeout as infra error: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Execute did not honor timeout: took %s", elapsed)
	}
	if !bytes.Contains(out, []byte("[killed after")) {
		t.Errorf("output missing [killed after ...] tag: %q", out)
	}
}

func TestBash_BadInputReturnsError(t *testing.T) {
	tool := NewBashTool()
	if _, err := tool.Execute(context.Background(), []byte(`not json`)); err == nil {
		t.Error("expected parse error on bad JSON")
	}
	if _, err := tool.Execute(context.Background(), []byte(`{}`)); err == nil {
		t.Error("expected empty-cmd error")
	}
}

func TestBash_ContextCancel(t *testing.T) {
	tool := &BashTool{Timeout: 5 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel quickly while a long sleep is in flight.
	done := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
		close(done)
	}()
	start := time.Now()
	_, _ = tool.Execute(ctx, []byte(`{"cmd":"sleep 5"}`))
	elapsed := time.Since(start)
	<-done
	if elapsed > 2*time.Second {
		t.Errorf("ctx cancellation didn't kill the process: took %s", elapsed)
	}
}

func TestBash_SchemaIsValidJSON(t *testing.T) {
	tool := NewBashTool()
	s := string(tool.Schema())
	if !strings.Contains(s, `"cmd"`) {
		t.Error("schema missing cmd field")
	}
	if !strings.Contains(s, `"required"`) {
		t.Error("schema missing required array")
	}
}

func TestBash_Description(t *testing.T) {
	tool := NewBashTool()
	if tool.Description() == "" {
		t.Error("Description must not be empty — Anthropic surfaces this to the model")
	}
}

// TestBash_PTYEcho verifies the PTY codepath produces real output. We
// skip if PTY isn't available on the test machine (rare on Darwin/Linux
// but possible in heavily-restricted CI containers).
func TestBash_PTYEcho(t *testing.T) {
	tool := &BashTool{PTY: true, Timeout: 5 * time.Second}
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"echo hello"}`))
	if err != nil {
		t.Skipf("PTY not available in this environment: %v", err)
	}
	if !bytes.Contains(out, []byte("hello")) {
		t.Errorf("PTY output missing payload: %q", out)
	}
	if !bytes.Contains(out, []byte("[exit 0]")) {
		t.Errorf("PTY output missing [exit 0] tag: %q", out)
	}
}

// TestBash_PTYTimeout confirms the PTY path also honours the timeout
// and kills the child process tree.
func TestBash_PTYTimeout(t *testing.T) {
	tool := &BashTool{PTY: true, Timeout: 200 * time.Millisecond}
	start := time.Now()
	out, err := tool.Execute(context.Background(), []byte(`{"cmd":"sleep 5"}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Skipf("PTY not available in this environment: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("PTY timeout not honored: took %s", elapsed)
	}
	if !bytes.Contains(out, []byte("[killed after")) {
		t.Errorf("PTY output missing kill marker: %q", out)
	}
}

// TestCappedWriter_OverflowCountsDiscarded drives the PTY-mode capped
// sink directly: write past the cap in chunks, confirm the buffer holds
// exactly `cap` bytes of payload (no internal marker — Execute owns the
// single marker), and that Discarded() reports the actual overflow.
// Regression test for the double-counted + double-marker bug at
// bash.go:174-184, 256-286.
func TestCappedWriter_OverflowCountsDiscarded(t *testing.T) {
	const cap = 32
	var buf bytes.Buffer
	cw := &cappedWriter{buf: &buf, max: cap}

	// Write 100 bytes in chunks to exercise the multi-Write path. Chunks
	// chosen so the cap is crossed mid-chunk (the second write) and
	// subsequent chunks land in the discard branch.
	chunks := [][]byte{
		bytes.Repeat([]byte("A"), 20),
		bytes.Repeat([]byte("B"), 20), // crosses cap inside this chunk
		bytes.Repeat([]byte("C"), 20),
		bytes.Repeat([]byte("D"), 20),
		bytes.Repeat([]byte("E"), 20),
	}
	totalWritten := 0
	for _, c := range chunks {
		n, err := cw.Write(c)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != len(c) {
			t.Errorf("Write returned %d, want %d (cappedWriter must report full consumption to keep io.Copy draining)", n, len(c))
		}
		totalWritten += len(c)
	}
	if totalWritten != 100 {
		t.Fatalf("sanity: expected to write 100 bytes, wrote %d", totalWritten)
	}

	wantPayload := bytes.Repeat([]byte("A"), 20)
	wantPayload = append(wantPayload, bytes.Repeat([]byte("B"), 12)...)

	if !bytes.Equal(buf.Bytes(), wantPayload) {
		t.Errorf("buffer mismatch\n got: %q\nwant: %q", buf.Bytes(), wantPayload)
	}
	if got := len(buf.Bytes()); got != cap {
		t.Errorf("buffer length = %d, want %d (cap)", got, cap)
	}
	if bytes.Contains(buf.Bytes(), []byte("truncated")) {
		t.Errorf("cappedWriter should not write its own marker; Execute owns the single marker: %q", buf.Bytes())
	}
	// Discarded must equal "everything past the cap" = 100 - 32 = 68.
	if got := cw.Discarded(); got != 100-cap {
		t.Errorf("Discarded = %d, want %d", got, 100-cap)
	}
}

// TestCappedWriter_NoDiscardedWhenWithinCap confirms cappedWriter stays
// silent and reports zero discards when the cap is never reached.
func TestCappedWriter_NoDiscardedWhenWithinCap(t *testing.T) {
	const cap = 64
	var buf bytes.Buffer
	cw := &cappedWriter{buf: &buf, max: cap}

	payload := []byte("hello world")
	if _, err := cw.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Errorf("buffer = %q, want %q", buf.Bytes(), payload)
	}
	if bytes.Contains(buf.Bytes(), []byte("truncated")) {
		t.Errorf("unexpected truncation marker on under-cap write: %q", buf.Bytes())
	}
	if got := cw.Discarded(); got != 0 {
		t.Errorf("Discarded = %d, want 0 under cap", got)
	}
}

// TestCappedWriter_ExactFillNoDiscarded confirms a write that fills the
// buffer to exactly `cap` (no overflow) does not register as discarded.
func TestCappedWriter_ExactFillNoDiscarded(t *testing.T) {
	const cap = 16
	var buf bytes.Buffer
	cw := &cappedWriter{buf: &buf, max: cap}

	payload := bytes.Repeat([]byte("x"), cap)
	if _, err := cw.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Errorf("buffer = %q, want %q", buf.Bytes(), payload)
	}
	if got := cw.Discarded(); got != 0 {
		t.Errorf("Discarded = %d after exact-fill, want 0", got)
	}

	// Now overflow by one byte across a second Write — discard counter
	// should increment by exactly 1 and buf stays at cap bytes.
	if _, err := cw.Write([]byte("y")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := buf.Len(); got != cap {
		t.Errorf("buffer length after overflow = %d, want %d", got, cap)
	}
	if got := cw.Discarded(); got != 1 {
		t.Errorf("Discarded after 1-byte overflow = %d, want 1", got)
	}
}
