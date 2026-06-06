package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_LoadAbsentFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "missing.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("Load absent: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("absent file should yield empty list; got %d", len(list))
	}
}

func TestStore_TrustPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")
	ws := t.TempDir()
	s := NewStore(path)
	if err := s.Trust(ws); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	ok, err := s.IsTrusted(ws)
	if err != nil || !ok {
		t.Fatalf("IsTrusted = (%v, %v); want (true, nil)", ok, err)
	}

	// Fresh store off the same file should see the entry.
	s2 := NewStore(path)
	ok, err = s2.IsTrusted(ws)
	if err != nil || !ok {
		t.Errorf("reload IsTrusted = (%v, %v); want (true, nil)", ok, err)
	}
}

func TestStore_UntrustRemoves(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	_ = s.Trust(ws)
	if err := s.Untrust(ws); err != nil {
		t.Fatalf("Untrust: %v", err)
	}
	ok, _ := s.IsTrusted(ws)
	if ok {
		t.Error("Untrust should remove entry")
	}
}

func TestStore_TrustTwiceIdempotent(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	_ = s.Trust(ws)
	_ = s.Trust(ws)
	list, _ := s.List()
	if len(list) != 1 {
		t.Errorf("double-trust: want 1 entry got %d", len(list))
	}
}

func TestStore_NormalizeAbsolute(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	// Trust via the absolute path.
	if err := s.Trust(ws); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	// Look it up via a relative path that resolves to the same
	// absolute. (Build via filepath.Rel for portability.)
	cwd, _ := os.Getwd()
	rel, err := filepath.Rel(cwd, ws)
	if err != nil {
		t.Skipf("Rel failed: %v", err)
	}
	ok, err := s.IsTrusted(rel)
	if err != nil {
		t.Fatalf("IsTrusted rel: %v", err)
	}
	if !ok {
		t.Errorf("relative path should resolve to trusted absolute")
	}
}

func TestStore_FilePerms0600(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	path := filepath.Join(dir, "t.json")
	s := NewStore(path)
	if err := s.Trust(ws); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}
}

func TestStore_DirAutoCreated0700(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "fresh-carlos-dir")
	ws := t.TempDir()
	s := NewStore(filepath.Join(subdir, "t.json"))
	if err := s.Trust(ws); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	info, err := os.Stat(subdir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o, want 0700", mode)
	}
}

func TestStore_ListIsSorted(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	a := t.TempDir()
	b := t.TempDir()
	_ = s.Trust(b)
	_ = s.Trust(a)
	list, _ := s.List()
	if len(list) != 2 {
		t.Fatalf("want 2 entries got %d", len(list))
	}
	if list[0].Path >= list[1].Path {
		t.Errorf("List should be sorted ascending; got %q then %q",
			list[0].Path, list[1].Path)
	}
}

func TestStore_EmptyPathErrors(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	if err := s.Trust(""); err == nil {
		t.Error("Trust(\"\") should error")
	}
	if _, err := s.IsTrusted(""); err == nil {
		t.Error("IsTrusted(\"\") should error")
	}
}

func TestStore_CorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if err := s.Load(); err == nil {
		t.Error("Load corrupt JSON should error")
	}
}

func TestDefaultPath_NonEmpty(t *testing.T) {
	if p := DefaultPath(); p == "" {
		t.Error("DefaultPath should never be empty")
	}
}
