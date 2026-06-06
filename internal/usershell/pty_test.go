package usershell

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRingBuffer_AppendsBelowCap(t *testing.T) {
	rb := NewRingBuffer(16)
	n, err := rb.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}
	if string(rb.Snapshot()) != "hello" {
		t.Errorf("snapshot: %q", string(rb.Snapshot()))
	}
	if rb.Len() != 5 {
		t.Errorf("Len: %d", rb.Len())
	}
}

func TestRingBuffer_OverflowDropsOldest(t *testing.T) {
	rb := NewRingBuffer(8)
	_, _ = rb.Write([]byte("12345"))
	_, _ = rb.Write([]byte("67890"))
	got := rb.Snapshot()
	if len(got) != 8 {
		t.Errorf("len after overflow: want 8, got %d", len(got))
	}
	if string(got) != "34567890" {
		t.Errorf("oldest should drop: got %q", string(got))
	}
}

func TestRingBuffer_HugeSingleWriteKeepsTail(t *testing.T) {
	rb := NewRingBuffer(8)
	huge := []byte(strings.Repeat("a", 100))
	huge = append(huge, []byte("TAIL1234")...)
	_, _ = rb.Write(huge)
	if got := string(rb.Snapshot()); got != "TAIL1234" {
		t.Errorf("huge write tail: got %q", got)
	}
}

func TestRingBuffer_DefaultCap(t *testing.T) {
	rb := NewRingBuffer(0)
	if rb.Cap() != 64*1024 {
		t.Errorf("default cap: got %d", rb.Cap())
	}
	rb = NewRingBuffer(-5)
	if rb.Cap() != 64*1024 {
		t.Errorf("negative cap should normalize to default: got %d", rb.Cap())
	}
}

func TestRingBuffer_EmptyWrite(t *testing.T) {
	rb := NewRingBuffer(8)
	n, err := rb.Write(nil)
	if n != 0 || err != nil {
		t.Errorf("empty write: n=%d err=%v", n, err)
	}
}

func TestRingBuffer_ConcurrentWritesSafe(t *testing.T) {
	rb := NewRingBuffer(1024)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			_, _ = rb.Write([]byte("a"))
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = rb.Snapshot()
	}
	<-done
	// Buffer should be full of 'a' bytes; final length capped at 1024.
	got := rb.Snapshot()
	if len(got) > 1024 {
		t.Errorf("len after concurrent: %d (> 1024)", len(got))
	}
	if !bytes.Equal(got, bytes.Repeat([]byte("a"), len(got))) {
		t.Errorf("concurrent writes left non-'a' bytes in buffer")
	}
}

// TestPTYRunner_Smoke is a real-shell smoke test. Skipped when $SHELL
// isn't available or when running in a CI environment without a tty.
// Verifies the production runner can actually spawn, capture output,
// and exit cleanly.
func TestPTYRunner_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke: -short")
	}
	if _, err := os.Stat(shellPicker()); err != nil {
		t.Skipf("smoke: shell %s unavailable: %v", shellPicker(), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader, wait, _, err := ptyRunner{}.Start(ctx, "echo hello-from-pty", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	buf := make([]byte, 4096)
	out := []byte{}
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	exit, werr := wait()
	if werr != nil {
		t.Fatalf("wait: %v", werr)
	}
	if exit != 0 {
		t.Errorf("exit: want 0 got %d", exit)
	}
	if !strings.Contains(string(out), "hello-from-pty") {
		t.Errorf("output missing expected marker: %q", string(out))
	}
}

// TestPTYRunner_Cancel verifies SIGTERM→SIGKILL on cancel actually
// reaps a long-running process. Uses sleep — should die within the
// killGrace window plus tolerance.
func TestPTYRunner_Cancel(t *testing.T) {
	if testing.Short() {
		t.Skip("cancel: -short")
	}
	if _, err := os.Stat(shellPicker()); err != nil {
		t.Skipf("cancel: shell %s unavailable: %v", shellPicker(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, wait, kill, err := ptyRunner{}.Start(ctx, "sleep 30", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	done := make(chan int, 1)
	go func() {
		ec, _ := wait()
		done <- ec
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		kill()
		t.Fatal("process did not die after cancel within 2s")
	}
}

func TestPickShell_RespectsEnv(t *testing.T) {
	t.Setenv("SHELL", "/usr/local/bin/whatever")
	if got := pickShell(); got != "/usr/local/bin/whatever" {
		t.Errorf("pickShell: got %q", got)
	}
}

func TestPickShell_FallbackWhenUnset(t *testing.T) {
	t.Setenv("SHELL", "")
	got := pickShell()
	if got == "" {
		t.Error("pickShell with empty SHELL should fall back")
	}
}
