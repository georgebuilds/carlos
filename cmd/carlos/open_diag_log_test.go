package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openDiagLog must open a real append-mode file under <home>/.carlos so
// best-effort diagnostics land off-terminal. A second open should append,
// not truncate, and the cleanup must close the underlying handle.
func TestOpenDiagLog_WritesToAppendFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".carlos"), 0o755); err != nil {
		t.Fatalf("mkdir .carlos: %v", err)
	}

	w, cleanup := openDiagLog(home)
	if w == io.Discard {
		t.Fatal("expected a real file writer, got io.Discard")
	}
	if _, err := io.WriteString(w, "first line\n"); err != nil {
		t.Fatalf("write through diag writer: %v", err)
	}
	cleanup()

	// Re-open: append mode means the second line lands after the first.
	w2, cleanup2 := openDiagLog(home)
	if _, err := io.WriteString(w2, "second line\n"); err != nil {
		t.Fatalf("write through second diag writer: %v", err)
	}
	cleanup2()

	got, err := os.ReadFile(filepath.Join(home, ".carlos", "carlos.log"))
	if err != nil {
		t.Fatalf("read back log: %v", err)
	}
	if s := string(got); !strings.Contains(s, "first line") || !strings.Contains(s, "second line") {
		t.Errorf("append semantics broken; got %q", s)
	}
}

// openDiagLog does not create the parent .carlos directory; OpenFile
// errors when it is absent. In that case we must get the io.Discard
// fallback (never os.Stderr) and writing through it must not panic.
func TestOpenDiagLog_MissingCarlosDirFallsBackToDiscard(t *testing.T) {
	home := t.TempDir() // no .carlos subdir created
	w, cleanup := openDiagLog(home)
	if w != io.Discard {
		t.Fatalf("expected io.Discard fallback when .carlos dir missing, got %T", w)
	}
	if w == os.Stderr {
		t.Fatal("openDiagLog must NEVER return os.Stderr")
	}
	// Writing through the discard sink and calling cleanup must be safe.
	if _, err := io.WriteString(w, "noop\n"); err != nil {
		t.Errorf("write through discard fallback errored: %v", err)
	}
	cleanup() // no-op, must not panic
}

// An empty home short-circuits to io.Discard with a no-op cleanup, and
// must never hand back os.Stderr (which would corrupt the TUI frame).
func TestOpenDiagLog_EmptyHomeReturnsDiscard(t *testing.T) {
	w, cleanup := openDiagLog("")
	if w != io.Discard {
		t.Fatalf("expected io.Discard for empty home, got %T", w)
	}
	if w == os.Stderr {
		t.Fatal("openDiagLog must NEVER return os.Stderr")
	}
	if _, err := io.WriteString(w, "noop\n"); err != nil {
		t.Errorf("write through discard fallback errored: %v", err)
	}
	cleanup() // must be safe to call
}
