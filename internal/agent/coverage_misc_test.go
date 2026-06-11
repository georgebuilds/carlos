package agent

// Small whitebox unit tests for pure helpers and simple guards that the
// integration tests exercise only partially: the live-text control-byte
// scrubber's DEL + C1 branches, and OrphanSweeper.SetLogger's nil
// fallback.

import (
	"log/slog"
	"testing"
)

func TestIsControlByte_AllBranches(t *testing.T) {
	// Whitelisted whitespace is NOT a control byte.
	for _, c := range []byte{'\t', '\n', '\r'} {
		if isControlByte(c) {
			t.Errorf("0x%02x (whitespace) should be allowed", c)
		}
	}
	// DEL and the C0 range are control bytes.
	for _, c := range []byte{0x00, 0x07, 0x1b, 0x1f, 0x7f} {
		if !isControlByte(c) {
			t.Errorf("0x%02x should be a control byte", c)
		}
	}
	// The C1 range [0x80, 0x9f] is scrubbed.
	for _, c := range []byte{0x80, 0x90, 0x9f} {
		if !isControlByte(c) {
			t.Errorf("0x%02x (C1) should be a control byte", c)
		}
	}
	// Printable ASCII and UTF-8 continuation/lead bytes are kept.
	for _, c := range []byte{'a', ' ', '~', 0xc0, 0xff} {
		if isControlByte(c) {
			t.Errorf("0x%02x should NOT be a control byte", c)
		}
	}
}

// TestOrphanSweeper_SetLoggerNilFallsBack confirms SetLogger(nil) swaps in
// slog.Default rather than nil-ing the field.
func TestOrphanSweeper_SetLoggerNilFallsBack(t *testing.T) {
	sw := NewOrphanSweeper(nil, nil, 0, 0)
	sw.SetLogger(nil) // nil → slog.Default()
	sw.mu.Lock()
	got := sw.logger
	sw.mu.Unlock()
	if got == nil {
		t.Fatal("SetLogger(nil) left logger nil; should fall back to slog.Default")
	}
	// A real logger is honored too.
	custom := slog.Default()
	sw.SetLogger(custom)
	sw.mu.Lock()
	got = sw.logger
	sw.mu.Unlock()
	if got != custom {
		t.Error("SetLogger did not store the provided logger")
	}
}
