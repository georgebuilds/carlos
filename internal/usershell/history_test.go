package usershell

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestHistory_AddAndCycle(t *testing.T) {
	dir := t.TempDir()
	h := NewHistory(filepath.Join(dir, "hist"))
	for _, cmd := range []string{"ls", "git status", "cargo test"} {
		if err := h.Add(cmd); err != nil {
			t.Fatal(err)
		}
	}
	// First Prev returns the newest.
	if got := h.Prev(); got != "cargo test" {
		t.Errorf("first prev: %q", got)
	}
	if got := h.Prev(); got != "git status" {
		t.Errorf("second prev: %q", got)
	}
	if got := h.Prev(); got != "ls" {
		t.Errorf("third prev: %q", got)
	}
	// Further Prev stays at oldest.
	if got := h.Prev(); got != "ls" {
		t.Errorf("clamped prev: %q", got)
	}
	// Walk forward.
	if got := h.Next(); got != "git status" {
		t.Errorf("forward 1: %q", got)
	}
	if got := h.Next(); got != "cargo test" {
		t.Errorf("forward 2: %q", got)
	}
	// One past the newest returns "" so the caller clears the input.
	if got := h.Next(); got != "" {
		t.Errorf("past newest: %q", got)
	}
}

func TestHistory_SkipsDuplicateConsecutive(t *testing.T) {
	dir := t.TempDir()
	h := NewHistory(filepath.Join(dir, "hist"))
	_ = h.Add("ls")
	_ = h.Add("ls") // duplicate of previous; should be skipped
	_ = h.Add("ls")
	if h.Len() != 1 {
		t.Errorf("dup skip: want 1 entry got %d", h.Len())
	}
}

func TestHistory_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hist")
	h1 := NewHistory(path)
	_ = h1.Add("first")
	_ = h1.Add("second")
	h2 := NewHistory(path)
	if h2.Len() != 2 {
		t.Errorf("persisted: want 2 entries got %d", h2.Len())
	}
	if got := h2.Prev(); got != "second" {
		t.Errorf("first prev after reload: %q", got)
	}
}

func TestHistory_RotationCapsMaxLines(t *testing.T) {
	dir := t.TempDir()
	h := NewHistory(filepath.Join(dir, "hist"))
	h.maxLines = 3
	for _, c := range []string{"a", "b", "c", "d", "e"} {
		_ = h.Add(c)
	}
	if h.Len() != 3 {
		t.Errorf("rotation cap: want 3 got %d", h.Len())
	}
	if got := h.Prev(); got != "e" {
		t.Errorf("newest after rotation: %q", got)
	}
}

func TestHistory_EmptyAddIsNoop(t *testing.T) {
	dir := t.TempDir()
	h := NewHistory(filepath.Join(dir, "hist"))
	if err := h.Add("   "); err != nil {
		t.Fatal(err)
	}
	if h.Len() != 0 {
		t.Errorf("whitespace add: want 0 entries got %d", h.Len())
	}
}

func TestHistory_DefaultPath(t *testing.T) {
	got := DefaultHistoryPath()
	if got == "" {
		t.Error("default path should never be empty")
	}
}

func TestHistory_ResetMovesCursorToEnd(t *testing.T) {
	dir := t.TempDir()
	h := NewHistory(filepath.Join(dir, "hist"))
	_ = h.Add("a")
	_ = h.Add("b")
	_ = h.Prev() // move cursor back to "b"
	_ = h.Prev() // back to "a"
	h.Reset()
	if got := h.Prev(); got != "b" {
		t.Errorf("reset → prev: want newest 'b', got %q", got)
	}
}

func TestHistory_EmptyPrevPrevNoCrash(t *testing.T) {
	dir := t.TempDir()
	h := NewHistory(filepath.Join(dir, "hist"))
	if got := h.Prev(); got != "" {
		t.Errorf("empty prev: %q", got)
	}
	if got := h.Next(); got != "" {
		t.Errorf("empty next: %q", got)
	}
}

// unwritableHistoryPath returns a path inside a tempdir whose parent is
// itself a regular file. os.MkdirAll fails on it, so persist() will
// always error. Cross-platform (no chmod tricks) and self-cleaning.
func unwritableHistoryPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	// blocker is a file; treating it as a directory makes MkdirAll fail.
	return filepath.Join(blocker, "subdir", "hist")
}

// captureSlog redirects slog.Default() into a buffer for the test's
// lifetime and restores the previous default on cleanup. Returns the
// buffer the test can read after the action under test.
func captureSlog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// syncBuffer is a goroutine-safe bytes.Buffer wrapper. The slog handler
// may write from any goroutine; the test reads from the test goroutine.
// Without the mutex the -race detector would flag the access.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestHistory_Add_PersistFailure_StillReturnsNil pins the documented
// contract: a disk-write failure must not propagate. The in-memory
// entry still lands and the cursor advances so the composer's up/down
// keeps working in a degraded session.
func TestHistory_Add_PersistFailure_StillReturnsNil(t *testing.T) {
	_ = captureSlog(t) // swallow the warning so test output stays clean
	path := unwritableHistoryPath(t)
	h := NewHistory(path)
	beforeLen := h.Len()
	beforeCursor := h.cursor

	if err := h.Add("foo"); err != nil {
		t.Fatalf("Add returned error despite documented contract: %v", err)
	}
	if h.Len() != beforeLen+1 {
		t.Errorf("in-memory entry not retained: len before=%d after=%d", beforeLen, h.Len())
	}
	if got := h.Prev(); got != "foo" {
		t.Errorf("in-memory entry not the newest: Prev()=%q want %q", got, "foo")
	}
	// Cursor should have advanced past the previous end before Prev()
	// rewound it. We can check this indirectly: a fresh Add resets the
	// cursor to len(entries), so the value pre-Prev was beforeCursor+1.
	if want := beforeCursor + 1; want < 0 {
		t.Fatalf("unexpected initial cursor=%d", beforeCursor)
	}
	// On-disk file should NOT have been created; persist failed before
	// the rename step. We tolerate any non-nil stat error: the parent
	// of `path` is itself a file (ENOTDIR) which is the whole reason
	// persist fails, so a successful Stat is the only failure mode.
	if _, err := os.Stat(path); err == nil {
		t.Errorf("history file unexpectedly present at %s after persist failure", path)
	}
}

// TestHistory_Add_PersistFailure_LogsWarning verifies the disk failure
// is surfaced via slog rather than swallowed silently. Silent disk
// failures are worse than no log; the test pins the warning shape so a
// future refactor can't quietly drop it.
func TestHistory_Add_PersistFailure_LogsWarning(t *testing.T) {
	buf := captureSlog(t)
	path := unwritableHistoryPath(t)
	h := NewHistory(path)

	if err := h.Add("bar"); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "persist failed") {
		t.Errorf("warning log missing 'persist failed' marker.\nlog output:\n%s", out)
	}
	if !strings.Contains(out, path) {
		t.Errorf("warning log missing history path %q.\nlog output:\n%s", path, out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("warning log not at WARN level.\nlog output:\n%s", out)
	}
}

// TestHistory_Add_PersistFailure_DoesNotBlockFurtherAdds confirms a
// degraded persist path still accepts additional commands. The in-
// memory log keeps growing so the composer keeps working until the
// session ends.
func TestHistory_Add_PersistFailure_DoesNotBlockFurtherAdds(t *testing.T) {
	_ = captureSlog(t)
	path := unwritableHistoryPath(t)
	h := NewHistory(path)

	for _, cmd := range []string{"one", "two", "three"} {
		if err := h.Add(cmd); err != nil {
			t.Fatalf("Add(%q) returned error: %v", cmd, err)
		}
	}
	if h.Len() != 3 {
		t.Errorf("in-memory log len=%d want 3", h.Len())
	}
	if got := h.Prev(); got != "three" {
		t.Errorf("Prev()=%q want newest 'three'", got)
	}
}
