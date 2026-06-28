package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A relative write whose path resolves outside the sandbox through a
// symlink is rejected, and the out-of-tree victim is left untouched.
// resolveBaseDir's lexical `..` check alone would not catch this.
func TestWrite_RejectsSymlinkEscapeFromSandbox(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "worktree")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{base, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlink inside the sandbox that points outside it.
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Fatal(err)
	}

	in, _ := json.Marshal(map[string]any{
		"path": "escape/secret.txt", "content": "pwned", "mode": "overwrite",
	})
	_, err := (&WriteTool{BaseDir: base}).Execute(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "containment") {
		t.Fatalf("want symlink-containment error, got %v", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "original" {
		t.Errorf("victim modified through symlink: %q", got)
	}
}

// An absolute path is an intentional, user-visible policy escape (see
// resolveBaseDir) and is NOT subject to sandbox symlink containment.
func TestWrite_AbsolutePathBypassesContainment(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "worktree")
	if err := os.Mkdir(base, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "explicit.txt")
	in, _ := json.Marshal(map[string]any{"path": target, "content": "ok"})
	if _, err := (&WriteTool{BaseDir: base}).Execute(context.Background(), in); err != nil {
		t.Fatalf("absolute write should be honoured: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "ok" {
		t.Errorf("absolute write content = %q", b)
	}
}

// atomicWrite no longer writes through a predictable `path + ".tmp"` name,
// so a symlink pre-planted there cannot redirect the write (the old
// O_TRUNC open followed it) to clobber an out-of-tree file.
func TestAtomicWrite_IgnoresPlantedTmpSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "out.txt")
	if err := os.Symlink(victim, target+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(target, []byte("new"), 0o644); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "safe" {
		t.Errorf("victim clobbered through planted tmp symlink: %q", b)
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Errorf("target content = %q", b)
	}
}

// atomicCreate fails (without clobbering) if the target already exists; the
// existence check and creation are one atomic link, so there is no TOCTOU
// window. The error message matches the old create-mode wording.
func TestAtomicCreate_NoClobber(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := atomicCreate(p, []byte("new"), 0o644)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want already-exists error, got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "orig" {
		t.Errorf("create clobbered existing file: %q", b)
	}
}

func TestAtomicCreate_WritesNewLeavingNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := atomicCreate(p, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "hi" {
		t.Errorf("content = %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected only the created file, got %d dir entries", len(entries))
	}
}
