package usershell

import (
	"path/filepath"
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
	// One past the newest returns "" — caller clears the input.
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
