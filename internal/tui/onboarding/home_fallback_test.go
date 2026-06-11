package onboarding

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultVaultPath_EmptyHomeFallback covers the err/empty-home arm of
// DefaultVaultPath. On Unix, an empty $HOME makes os.UserHomeDir error.
func TestDefaultVaultPath_EmptyHomeFallback(t *testing.T) {
	t.Setenv("HOME", "")
	got := DefaultVaultPath()
	want := filepath.Join(".carlos", "notes")
	if got != want {
		t.Errorf("empty HOME: DefaultVaultPath() = %q want %q", got, want)
	}
}

// TestOpenrouterCacheDir_EmptyHomeFallback covers the temp-dir fallback
// when the home dir can't be resolved.
func TestOpenrouterCacheDir_EmptyHomeFallback(t *testing.T) {
	t.Setenv("HOME", "")
	got := openrouterCacheDir()
	if !strings.HasSuffix(got, filepath.Join("carlos-cache")) {
		t.Errorf("empty HOME: cache dir should fall back to temp/carlos-cache; got %q", got)
	}
}

// TestExpandTilde_BareTildeEmptyHome covers expandTilde's "~" arm when
// the home dir is unresolvable: it returns the input verbatim.
func TestExpandTilde_BareTildeEmptyHome(t *testing.T) {
	t.Setenv("HOME", "")
	if got := expandTilde("~"); got != "~" {
		t.Errorf("bare ~ with empty HOME should return ~ verbatim; got %q", got)
	}
}

// TestExpandTilde_NoTildeVerbatim covers the verbatim (no-tilde) arm.
func TestExpandTilde_NoTildeVerbatim(t *testing.T) {
	if got := expandTilde("/abs/path"); got != "/abs/path" {
		t.Errorf("non-tilde path should be returned verbatim; got %q", got)
	}
}
