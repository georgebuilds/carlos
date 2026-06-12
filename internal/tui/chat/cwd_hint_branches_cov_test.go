package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTryInterceptCd_BareCdGoesHome covers the "cd" alone branch which
// resolves to the user's home directory.
func TestTryInterceptCd_BareCdGoesHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir resolvable in this environment")
	}
	m, mgr := newHintModel(t, nil)
	handled, msg := m.tryInterceptCdCommand("cd")
	if !handled {
		t.Fatal("bare cd should be intercepted")
	}
	if !strings.Contains(msg, home) {
		t.Errorf("bare cd should land in home %q; got %q", home, msg)
	}
	if mgr.Cwd() != filepath.Clean(home) {
		t.Errorf("manager cwd should be home; got %q", mgr.Cwd())
	}
}

// TestTryInterceptCd_TildeGoesHome covers the "cd ~" branch.
func TestTryInterceptCd_TildeGoesHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir resolvable in this environment")
	}
	m, mgr := newHintModel(t, nil)
	handled, _ := m.tryInterceptCdCommand("cd ~")
	if !handled {
		t.Fatal("cd ~ should be intercepted")
	}
	if mgr.Cwd() != filepath.Clean(home) {
		t.Errorf("cd ~ should land in home; got %q", mgr.Cwd())
	}
}

// TestTryInterceptCd_TildeSubdir covers the "cd ~/sub" expansion branch.
func TestTryInterceptCd_TildeSubdir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir resolvable in this environment")
	}
	// Build a real subdir under home so the stat succeeds.
	sub, err := os.MkdirTemp(home, "carlos-cdtest-")
	if err != nil {
		t.Skipf("cannot create temp dir under home: %v", err)
	}
	defer os.RemoveAll(sub)
	leaf := "~/" + filepath.Base(sub)

	m, mgr := newHintModel(t, nil)
	handled, _ := m.tryInterceptCdCommand("cd " + leaf)
	if !handled {
		t.Fatal("cd ~/sub should be intercepted")
	}
	if mgr.Cwd() != filepath.Clean(sub) {
		t.Errorf("cd ~/sub should expand to %q; got %q", sub, mgr.Cwd())
	}
}

// TestTryInterceptCd_NotADirectoryReportsError covers the
// info.IsDir()==false branch.
func TestTryInterceptCd_NotADirectoryReportsError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := newHintModel(t, nil)
	handled, msg := m.tryInterceptCdCommand("cd " + file)
	if !handled {
		t.Fatal("cd onto a file should still be handled")
	}
	if !strings.Contains(msg, "not a directory") {
		t.Errorf("cd onto a file should report 'not a directory'; got %q", msg)
	}
}
