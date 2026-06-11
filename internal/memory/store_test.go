package memory_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/georgebuilds/carlos/internal/memory"
)

// newStore opens an OpenStore-backed Store in a fresh temp dir and
// registers Close on cleanup. Returned dbPath lets tests reopen to
// assert persistence.
func newStore(t *testing.T) (*memory.Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	s, err := memory.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dbPath
}

// TestOpenStore_CreatesParentDir verifies that OpenStore mkdirs the
// parent directory at 0700 if it doesn't exist (matches ~/.carlos
// security posture).
func TestOpenStore_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "deep", "carlos", "state.db")
	s, err := memory.OpenStore(nested)
	if err != nil {
		t.Fatalf("OpenStore nested: %v", err)
	}
	defer s.Close()
	// Parent must exist now.
	if _, err := filepath.Abs(filepath.Dir(nested)); err != nil {
		t.Fatalf("abs parent: %v", err)
	}
}

// TestOpenStore_SchemaIdempotent verifies that opening twice on the
// same DB does not error. CREATE TABLE IF NOT EXISTS is the
// underlying guarantee; this is the regression test against an
// accidental DROP/CREATE.
func TestOpenStore_SchemaIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	s1, err := memory.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = s1.Close()
	s2, err := memory.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s2.Close()
}

// TestOpenStore_EmptyPathRejected verifies the input-validation
// guard.
func TestOpenStore_EmptyPathRejected(t *testing.T) {
	if _, err := memory.OpenStore(""); err == nil {
		t.Error("expected error on empty path")
	}
}

// TestClose_NilStoreSafe verifies that Close on a nil Store doesn't
// panic — callers may defer it before checking the open error.
func TestClose_NilStoreSafe(t *testing.T) {
	var s *memory.Store
	if err := s.Close(); err != nil {
		t.Errorf("nil Close: %v", err)
	}
}

// TestPersistenceAcrossReopen verifies that data inserted into one
// Store survives a Close + reopen on the same path. Covers both
// summaries and user_model.
func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	ctx := context.Background()

	s1, err := memory.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := s1.AppendSummary(ctx, memory.Summary{
		AgentID: "a", Text: "first chat",
	}); err != nil {
		t.Fatalf("AppendSummary: %v", err)
	}
	if err := s1.ApplyFact(ctx, "name", "George", memory.FactSourceUser); err != nil {
		t.Fatalf("ApplyFact: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := memory.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	hits, err := s2.RecentInFrame(ctx, memory.AnyFrames(), 10)
	if err != nil {
		t.Fatalf("RecentInFrame: %v", err)
	}
	if len(hits) != 1 || hits[0].Text != "first chat" {
		t.Errorf("after reopen: want 1 summary 'first chat', got %+v", hits)
	}
	v, ok, err := s2.GetFact(ctx, "name")
	if err != nil {
		t.Fatalf("GetFact: %v", err)
	}
	if !ok || v != "George" {
		t.Errorf("after reopen: want fact name=George, got %q ok=%v", v, ok)
	}
}
