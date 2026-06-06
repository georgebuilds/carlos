package onboarding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDefaultVaultPath_UsesCarlosNotes(t *testing.T) {
	got := DefaultVaultPath()
	if !strings.HasSuffix(got, filepath.Join(".carlos", "notes")) {
		t.Errorf("default vault path: want suffix .carlos/notes, got %q", got)
	}
}

func TestVaultScreen_EnterAcceptsDefault(t *testing.T) {
	// Redirect HOME so MkdirAll lands in a tempdir we own.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	m := newVaultModel()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected nextScreen cmd on enter")
	}
	mm := next.(vaultModel)
	if mm.mkdirErr != nil {
		t.Fatalf("mkdir failed: %v", mm.mkdirErr)
	}

	want := filepath.Join(tmp, ".carlos", "notes")
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("vault dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("vault path is not a directory: %s", want)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("vault dir mode: want 0700 got %o", mode)
	}
}

func TestVaultScreen_EmptyInputUsesDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	m := newVaultModel()
	m.input.SetValue("")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected nextScreen cmd")
	}
	if mm := next.(vaultModel); mm.mkdirErr != nil {
		t.Fatalf("mkdir: %v", mm.mkdirErr)
	}
	want := filepath.Join(tmp, ".carlos", "notes")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("default dir not created: %v", err)
	}
}

func TestVaultScreen_TildeExpansion(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	m := newVaultModel()
	m.input.SetValue("~/some/nested/path")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if mm := next.(vaultModel); mm.mkdirErr != nil {
		t.Fatalf("mkdir: %v", mm.mkdirErr)
	}
	want := filepath.Join(tmp, "some", "nested", "path")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expanded path not created: %v", err)
	}
}

func TestVaultScreen_BareTilde(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if got := expandTilde("~"); got != tmp {
		t.Errorf("bare tilde: want %q got %q", tmp, got)
	}
}

func TestVaultScreen_AbsolutePathRespected(t *testing.T) {
	tmp := t.TempDir()
	custom := filepath.Join(tmp, "my-vault")
	m := newVaultModel()
	m.input.SetValue(custom)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	if mm := next.(vaultModel); mm.mkdirErr != nil {
		t.Fatalf("mkdir: %v", mm.mkdirErr)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Errorf("custom path not created: %v", err)
	}
}

func TestVaultScreen_MkdirErrorKeepsScreen(t *testing.T) {
	// Construct an unreachable path: a file masquerading as a dir
	// parent. os.MkdirAll fails predictably on "<file>/sub".
	tmp := t.TempDir()
	blockerPath := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blockerPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newVaultModel()
	m.input.SetValue(filepath.Join(blockerPath, "child"))
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(vaultModel)
	if cmd != nil {
		t.Error("expected NO advance on mkdir error")
	}
	if mm.mkdirErr == nil {
		t.Error("expected mkdirErr to be set")
	}
	if !strings.Contains(mm.View(), "couldn't create that path") {
		t.Errorf("view should surface the mkdir error: %q", mm.View())
	}
}

func TestFlow_VaultStepIncluded(t *testing.T) {
	if totalScreens != 8 {
		t.Errorf("totalScreens should be 8 after vault+gateway addition, got %d", totalScreens)
	}
	titles := map[Screen]string{
		ScreenName:     "What should I call you?",
		ScreenProvider: "Wire up your providers",
		ScreenModel:    "Pick default models",
		ScreenSkills:   "Skills convention",
		ScreenVault:    "Notes vault",
		ScreenDaemon:   "Background daemon",
		ScreenGateway:  "Messaging gateway",
		ScreenDone:     "Ready",
	}
	for s, want := range titles {
		if got := screenTitle(s); got != want {
			t.Errorf("screenTitle(%v) = %q, want %q", s, got, want)
		}
	}
}
