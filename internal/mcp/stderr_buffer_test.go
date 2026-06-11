package mcp

import (
	"strings"
	"sync"
	"testing"
)

func TestBoundedBuffer_RetainsWritesUnderCap(t *testing.T) {
	b := newBoundedBuffer(16)
	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if got := b.String(); got != "hello" {
		t.Fatalf("String() = %q, want %q", got, "hello")
	}
}

func TestBoundedBuffer_WriteReportsFullLength(t *testing.T) {
	b := newBoundedBuffer(4)
	// Even though only the last 4 bytes are retained, Write must report the
	// full input length so the child's pipe is considered fully drained.
	n, err := b.Write([]byte("abcdefgh"))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != 8 {
		t.Fatalf("Write n = %d, want 8", n)
	}
	if got := b.String(); got != "efgh" {
		t.Fatalf("String() = %q, want %q", got, "efgh")
	}
}

func TestBoundedBuffer_OversizedSingleWriteKeepsTail(t *testing.T) {
	b := newBoundedBuffer(5)
	if _, err := b.Write([]byte("0123456789")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if got := b.String(); got != "56789" {
		t.Fatalf("String() = %q, want %q", got, "56789")
	}
}

func TestBoundedBuffer_AccumulatesSmallWrites(t *testing.T) {
	b := newBoundedBuffer(8)
	for _, s := range []string{"ab", "cd", "ef"} {
		if _, err := b.Write([]byte(s)); err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
	}
	if got := b.String(); got != "abcdef" {
		t.Fatalf("String() = %q, want %q", got, "abcdef")
	}
	// One more small write tips the running total past cap; the oldest
	// bytes slide off the front.
	if _, err := b.Write([]byte("ghij")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if got := b.String(); got != "cdefghij" {
		t.Fatalf("String() = %q, want %q", got, "cdefghij")
	}
}

func TestBoundedBuffer_EmptyReadsToEmptyString(t *testing.T) {
	b := newBoundedBuffer(16)
	if got := b.String(); got != "" {
		t.Fatalf("String() of empty buffer = %q, want %q", got, "")
	}
}

func TestBoundedBuffer_NonPositiveCapFallsBackToDefault(t *testing.T) {
	for _, cap := range []int{0, -1} {
		b := newBoundedBuffer(cap)
		if b.cap != defaultStderrCap {
			t.Fatalf("newBoundedBuffer(%d).cap = %d, want %d", cap, b.cap, defaultStderrCap)
		}
	}
}

func TestBoundedBuffer_ConcurrentWritesDontRace(t *testing.T) {
	b := newBoundedBuffer(64)
	const goroutines = 16
	const perGoroutine = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if _, err := b.Write([]byte("xy")); err != nil {
					t.Errorf("Write returned error: %v", err)
					return
				}
				_ = b.String()
			}
		}()
	}
	wg.Wait()
	// The exact contents are nondeterministic, but the tail must never
	// exceed cap and must be a run of the only bytes we ever wrote.
	got := b.String()
	if len(got) > 64 {
		t.Fatalf("retained %d bytes, want <= cap 64", len(got))
	}
	if strings.Trim(got, "xy") != "" {
		t.Fatalf("retained unexpected bytes: %q", got)
	}
}
