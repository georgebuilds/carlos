package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestStore_LoadReadError covers the non-ENOENT read failure branch of
// Load: when the path is a directory (not a file), os.ReadFile returns an
// error that is NOT os.ErrNotExist, so Load must wrap and return it.
func TestStore_LoadReadError(t *testing.T) {
	dir := t.TempDir()
	// Point the store at a directory; ReadFile on a dir errors with EISDIR.
	s := NewStore(dir)
	if err := s.Load(); err == nil {
		t.Fatal("Load on a directory path: expected read error, got nil")
	}
}

// TestStore_LoadEmptyFileIsNoop covers the len(raw) == 0 early return: an
// existing but empty file is treated as "no trusted workspaces" rather
// than a JSON parse error.
func TestStore_LoadEmptyFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}
	s := NewStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("Load empty file: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("empty file should yield empty list, got %d", len(list))
	}
}

// TestStore_LoadSkipsBlankPathEntry covers the `if e.Path == "" continue`
// branch in Load: a persisted record with an empty Path is dropped rather
// than indexed under the empty key.
func TestStore_LoadSkipsBlankPathEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")
	// One blank-path entry (skipped) + one real entry (kept).
	content := `[{"path":"","trusted_at":"2026-01-01T00:00:00Z"},` +
		`{"path":"/real/workspace","trusted_at":"2026-01-01T00:00:00Z"}]`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	s := NewStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("blank-path entry should be skipped; want 1 entry, got %d", len(list))
	}
	if list[0].Path != "/real/workspace" {
		t.Errorf("kept entry path = %q, want /real/workspace", list[0].Path)
	}
}

// TestStore_SaveMkdirError covers the MkdirAll failure branch of save:
// when the parent directory cannot be created (its own parent is a file),
// Trust propagates the wrapped mkdir error.
func TestStore_SaveMkdirError(t *testing.T) {
	dir := t.TempDir()
	// A regular file sitting where save() wants to MkdirAll a directory.
	fileAsParent := filepath.Join(dir, "blocker")
	if err := os.WriteFile(fileAsParent, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	// store path lives *under* the file, so filepath.Dir(path) is the file.
	s := NewStore(filepath.Join(fileAsParent, "sub", "t.json"))
	ws := t.TempDir()
	if err := s.Trust(ws); err == nil {
		t.Fatal("Trust with un-creatable parent dir: expected mkdir error, got nil")
	}
}

// TestStore_SaveRenameError covers the os.Rename failure branch of save:
// when the destination path is itself an existing directory, rename of
// the temp file onto it fails and save wraps the error.
func TestStore_SaveRenameError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("rename-onto-directory semantics differ on Windows")
	}
	dir := t.TempDir()
	// Make the store path a directory so the final os.Rename fails.
	storePath := filepath.Join(dir, "t.json")
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatalf("mkdir store path as dir: %v", err)
	}
	s := NewStore(storePath)
	ws := t.TempDir()
	if err := s.Trust(ws); err == nil {
		t.Fatal("Trust with directory-shaped dest path: expected rename error, got nil")
	}
}

// TestStore_UntrustPersists exercises Untrust's save() path end-to-end:
// after untrusting, a fresh store reading the same file must not see the
// entry (the delete was persisted, not just in-memory).
func TestStore_UntrustPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.json")
	ws := t.TempDir()

	s := NewStore(path)
	if err := s.Trust(ws); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if err := s.Untrust(ws); err != nil {
		t.Fatalf("Untrust: %v", err)
	}
	// Fresh store off the same file must not see the entry.
	s2 := NewStore(path)
	ok, err := s2.IsTrusted(ws)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if ok {
		t.Error("Untrust did not persist: reload still reports trusted")
	}
}

// TestStore_UntrustEmptyPathErrors covers the normalize-error branch of
// Untrust (the empty-path guard fires before any disk touch).
func TestStore_UntrustEmptyPathErrors(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	if err := s.Untrust(""); err == nil {
		t.Error("Untrust(\"\") should error")
	}
}

// TestDefaultPath_HomeUnsetFallback covers the os.UserHomeDir error branch
// of DefaultPath. With HOME unset (and the OS-specific overrides cleared)
// UserHomeDir fails and DefaultPath falls back to the relative
// ".carlos/..." form.
func TestDefaultPath_HomeUnsetFallback(t *testing.T) {
	switch runtime.GOOS {
	case "windows", "plan9":
		t.Skip("HOME is not the home-dir source on this OS")
	}
	t.Setenv("HOME", "")
	got := DefaultPath()
	want := filepath.Join(".carlos", "trusted-workspaces.json")
	if got != want {
		t.Errorf("DefaultPath with HOME unset = %q, want %q", got, want)
	}
}

// TestStore_OpsPropagateLoadError covers the `if err := s.Load(); err
// != nil { return ... }` branch in IsTrusted, Trust, Untrust, and List:
// a corrupt on-disk file makes the lazy Load fail, and every public op
// must surface that error rather than operating on a half-loaded cache.
func TestStore_OpsPropagateLoadError(t *testing.T) {
	seedCorrupt := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "t.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("seed corrupt: %v", err)
		}
		return path
	}
	ws := t.TempDir()

	t.Run("IsTrusted", func(t *testing.T) {
		s := NewStore(seedCorrupt(t))
		if _, err := s.IsTrusted(ws); err == nil {
			t.Error("IsTrusted should propagate Load error on corrupt file")
		}
	})
	t.Run("Trust", func(t *testing.T) {
		s := NewStore(seedCorrupt(t))
		if err := s.Trust(ws); err == nil {
			t.Error("Trust should propagate Load error on corrupt file")
		}
	})
	t.Run("Untrust", func(t *testing.T) {
		s := NewStore(seedCorrupt(t))
		if err := s.Untrust(ws); err == nil {
			t.Error("Untrust should propagate Load error on corrupt file")
		}
	})
	t.Run("List", func(t *testing.T) {
		s := NewStore(seedCorrupt(t))
		if _, err := s.List(); err == nil {
			t.Error("List should propagate Load error on corrupt file")
		}
	})
}

// TestNormalize_NonexistentPathFallsBackToLexical confirms the
// EvalSymlinks-failure fallback: a path that does not exist on disk
// cannot be symlink-resolved, so normalize returns the lexical absolute
// form rather than erroring. Trusting then looking up the same
// nonexistent path must round-trip.
func TestNormalize_NonexistentPathFallsBackToLexical(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	// A path that definitely does not exist (EvalSymlinks will fail).
	ghost := filepath.Join(dir, "does", "not", "exist", "ws")
	if err := s.Trust(ghost); err != nil {
		t.Fatalf("Trust nonexistent path: %v", err)
	}
	ok, err := s.IsTrusted(ghost)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if !ok {
		t.Error("nonexistent path should round-trip via lexical-abs fallback")
	}
}
