package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPath_UsesHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/carlos-test")
	got := DefaultPath()
	want := filepath.Join("/tmp/carlos-test", ".carlos", "trusted-workspaces.json")
	if got != want {
		t.Errorf("DefaultPath=%q want %q", got, want)
	}
}

func TestNormalize_EmptyPath(t *testing.T) {
	_, err := normalize("")
	if err == nil {
		t.Fatal("normalize(\"\") should fail")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("err=%q should mention empty", err)
	}
}

func TestNormalize_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	got, err := normalize(dir)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got == "" {
		t.Error("normalize returned empty")
	}
}

func TestStore_Untrust_AbsentIsNoop(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "trusted.json"))
	if err := s.Untrust(dir); err != nil {
		t.Errorf("Untrust on empty store: %v", err)
	}
}

func TestStore_Untrust_RemovesExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	s := NewStore(filepath.Join(dir, "trusted.json"))
	if err := s.Trust(target); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	ok, _ := s.IsTrusted(target)
	if !ok {
		t.Fatal("trust didn't stick")
	}
	if err := s.Untrust(target); err != nil {
		t.Fatalf("Untrust: %v", err)
	}
	ok, _ = s.IsTrusted(target)
	if ok {
		t.Error("Untrust didn't remove")
	}
}

func TestStore_Trust_Idempotent(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "trusted.json"))
	target := t.TempDir()
	if err := s.Trust(target); err != nil {
		t.Fatalf("first Trust: %v", err)
	}
	if err := s.Trust(target); err != nil {
		t.Fatalf("second Trust: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List has %d entries after dual-Trust, want 1", len(list))
	}
}

func TestStore_Trust_EmptyPathRejected(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "trusted.json"))
	if err := s.Trust(""); err == nil {
		t.Error("Trust(\"\") should fail")
	}
}

func TestStore_Untrust_EmptyPathRejected(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "trusted.json"))
	if err := s.Untrust(""); err == nil {
		t.Error("Untrust(\"\") should fail")
	}
}

func TestStore_Save_CreatesDir700FileMode600(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "deep", "nest")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "deep", "nest", "trusted.json")
	s := NewStore(storePath)
	if err := s.Trust(target); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode=%o want 0600", info.Mode().Perm())
	}
}

func TestPolicy_CwdAndStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "trusted.json"))
	p := NewPolicy(store, dir)
	if p.Store() != store {
		t.Error("Store accessor did not return original")
	}
	if p.Cwd() == "" {
		t.Error("Cwd empty after NewPolicy")
	}
}

func TestPolicy_NilCwdYieldsEmptyPolicy(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "trusted.json"))
	p := NewPolicy(store, "")
	if p.Cwd() != "" {
		t.Errorf("empty-cwd policy Cwd=%q want empty", p.Cwd())
	}
}
