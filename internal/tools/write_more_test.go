package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWrite_MkdirFailsWhenParentIsFile — when an ancestor of the target
// path is a regular file, MkdirAll fails and Execute surfaces the error.
func TestWrite_MkdirFailsWhenParentIsFile(t *testing.T) {
	dir := t.TempDir()
	// Create a FILE named "blocker", then try to write under it as if it
	// were a directory.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blocker, "child.txt")
	in, _ := json.Marshal(map[string]any{"path": target, "content": "y"})
	if _, err := NewWriteTool().Execute(context.Background(), in); err == nil {
		t.Fatal("expected mkdir failure when ancestor is a file")
	}
}

// TestWrite_StatErrorNonNotExist — when stat on the target returns an
// error other than NotExist (e.g. a non-directory in the path), create
// mode reports it rather than proceeding.
func TestWrite_StatErrorNonNotExist(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stat of <file>/child yields ENOTDIR, not ErrNotExist.
	target := filepath.Join(blocker, "child.txt")
	in, _ := json.Marshal(map[string]any{"path": target, "content": "y", "mode": "create"})
	if _, err := NewWriteTool().Execute(context.Background(), in); err == nil {
		t.Fatal("expected a stat error for a non-directory ancestor")
	}
}

// TestWrite_BaseDirRelativePath — a relative path resolves against BaseDir.
func TestWrite_BaseDirRelativePath(t *testing.T) {
	base := t.TempDir()
	tool := &WriteTool{BaseDir: base}
	in, _ := json.Marshal(map[string]any{"path": "notes/x.txt", "content": "hi"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), filepath.Join(base, "notes", "x.txt")) {
		t.Errorf("receipt should reference BaseDir-resolved path: %q", out)
	}
	if _, err := os.Stat(filepath.Join(base, "notes", "x.txt")); err != nil {
		t.Errorf("file should exist under BaseDir: %v", err)
	}
}

// TestAtomicWrite_OpenTmpFails — atomicWrite returns a wrapped error when
// the temp file cannot be created (parent is not a directory).
func TestAtomicWrite_OpenTmpFails(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "f")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// path under a non-directory -> OpenFile(tmp) fails with ENOTDIR.
	err := atomicWrite(filepath.Join(notADir, "child"), []byte("y"), 0o644)
	if err == nil || !strings.Contains(err.Error(), "open tmp") {
		t.Errorf("want open-tmp error, got %v", err)
	}
}

// TestAtomicWrite_Roundtrip — happy path writes the bytes and leaves no
// stale temp file.
func TestAtomicWrite_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	if err := atomicWrite(p, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "payload" {
		t.Errorf("read back = %q (err %v), want payload", got, err)
	}
	if _, err := os.Stat(p + ".tmp"); err == nil {
		t.Error("stale tmp lingered")
	}
}
