package usershell

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHistory_Exists covers the Exists() helper both ways: false before
// any persist, true after a successful Add writes the file to disk.
func TestHistory_Exists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hist")
	h := NewHistory(path)
	if h.Exists() {
		t.Error("Exists() should be false before any command is persisted")
	}
	if err := h.Add("ls"); err != nil {
		t.Fatal(err)
	}
	if !h.Exists() {
		t.Error("Exists() should be true after a successful persist")
	}
}

// TestHistory_Exists_TrueOnNonNotExistError pins the documented edge:
// Exists() reports true for any stat error that ISN'T "not found"
// (a path that exists but can't be stat-walked still counts as present).
// We use a path whose parent is a regular file: stat returns ENOTDIR,
// which is not fs.ErrNotExist, so Exists() must report true.
func TestHistory_Exists_TrueOnNonNotExistError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHistory(filepath.Join(blocker, "child", "hist"))
	if !h.Exists() {
		t.Error("Exists() should report true for a non-NotExist stat error (ENOTDIR)")
	}
}

// TestNewHistory_EmptyPathUsesDefault covers the path == "" branch of
// NewHistory, which falls back to DefaultHistoryPath(). We just confirm
// the resulting history is usable and its path is the default.
func TestNewHistory_EmptyPathUsesDefault(t *testing.T) {
	h := NewHistory("")
	if h.path == "" {
		t.Fatal("NewHistory(\"\") left an empty path; default fallback didn't fire")
	}
	if h.path != DefaultHistoryPath() {
		t.Errorf("NewHistory(\"\") path = %q, want default %q", h.path, DefaultHistoryPath())
	}
}

// TestHistory_LoadSkipsBlankLines covers the load() branch that drops
// empty lines while scanning the on-disk file. We seed a file with
// interleaved blank lines and confirm only the non-empty commands load.
func TestHistory_LoadSkipsBlankLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hist")
	content := "ls\n\ngit status\n\n\ncargo test\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHistory(path)
	if h.Len() != 3 {
		t.Fatalf("blank lines should be skipped on load; want 3 entries got %d", h.Len())
	}
	if got := h.Prev(); got != "cargo test" {
		t.Errorf("newest after load: %q", got)
	}
}

// TestHistory_PersistAfterRotationRewritesFile exercises the full
// persist() success path (write + flush + sync + close + rename) under
// rotation: once entries exceed maxLines, the on-disk file is rewritten
// to match the trimmed in-memory slice. We reload from disk to confirm
// the rotated file holds exactly the retained tail.
func TestHistory_PersistAfterRotationRewritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hist")
	h := NewHistory(path)
	h.maxLines = 2
	for _, c := range []string{"a", "b", "c", "d"} {
		if err := h.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	// In-memory should hold the last two.
	if h.Len() != 2 {
		t.Fatalf("rotation cap: want 2 got %d", h.Len())
	}
	// On disk should ALSO hold exactly the last two (persist rewrites
	// the full trimmed slice each call).
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if string(got) != "c\nd\n" {
		t.Errorf("rotated file content: want %q got %q", "c\nd\n", string(got))
	}
	// A fresh instance reloading the file sees only the retained tail.
	h2 := NewHistory(path)
	if h2.Len() != 2 {
		t.Errorf("reloaded len: want 2 got %d", h2.Len())
	}
}

// TestHistory_PersistRenameFailureRetainsInMemory covers persist()'s
// rename-failure branch: the dir mkdir + tmp write + flush + sync +
// close all succeed, but the final rename onto h.path fails because a
// non-empty directory occupies that name. Per the documented contract
// the in-memory entry still lands and Add returns nil. We assert the
// tmp file is cleaned up on the failure path.
func TestHistory_PersistRenameFailureRetainsInMemory(t *testing.T) {
	_ = captureSlog(t) // swallow the warning so test output stays clean
	dir := t.TempDir()
	path := filepath.Join(dir, "hist")
	// A non-empty directory at the history path makes rename fail.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHistory(path)
	if err := h.Add("ls"); err != nil {
		t.Fatalf("Add must not propagate persist failure: %v", err)
	}
	if h.Len() != 1 {
		t.Errorf("in-memory entry should land despite rename failure; len=%d", h.Len())
	}
	// The tmp file must have been removed on the failure path.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("history tmp should be cleaned up after rename failure; stat err=%v", err)
	}
}

// TestHistory_PersistOpenTmpFailureRetainsInMemory covers the
// OpenFile-failure branch of persist(): a directory occupying the
// <path>.tmp name makes O_WRONLY|O_CREATE fail. Add still returns nil
// and the in-memory entry survives.
func TestHistory_PersistOpenTmpFailureRetainsInMemory(t *testing.T) {
	_ = captureSlog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "hist")
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	h := NewHistory(path)
	if err := h.Add("git status"); err != nil {
		t.Fatalf("Add must not propagate persist failure: %v", err)
	}
	if h.Len() != 1 {
		t.Errorf("in-memory entry should land despite open failure; len=%d", h.Len())
	}
}

// TestHistory_PersistFileMode pins 0600 on the history file — shell
// history may contain echoed secrets, so a world-readable file is a
// leak. Covers the successful rename target's permissions.
func TestHistory_PersistFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hist")
	h := NewHistory(path)
	if err := h.Add("export TOKEN=hunter2"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("history file mode: want 0600 got %o", mode)
	}
}
