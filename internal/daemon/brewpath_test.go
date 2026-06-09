package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBrewSymlinkFor_AppleSilicon mirrors the Apple Silicon brew
// layout: a Cellar/<formula>/<version>/bin/<exe> path resolves to
// the stable <prefix>/bin/<exe> symlink. Verified-exists gate is
// satisfied by building the tree in t.TempDir.
func TestBrewSymlinkFor_AppleSilicon(t *testing.T) {
	root := t.TempDir()
	// Simulate /opt/homebrew on Apple Silicon.
	prefix := filepath.Join(root, "opt", "homebrew")
	resolved := filepath.Join(prefix, "Cellar", "carlos", "0.7.0", "bin", "carlos")
	sym := filepath.Join(prefix, "bin", "carlos")
	mustMkdir(t, filepath.Dir(resolved))
	mustWrite(t, resolved, "fake binary")
	mustMkdir(t, filepath.Dir(sym))
	mustWrite(t, sym, "fake symlink target")

	got := brewSymlinkFor(resolved)
	if got != sym {
		t.Errorf("brewSymlinkFor = %q, want %q", got, sym)
	}
}

// TestBrewSymlinkFor_IntelMac covers the legacy /usr/local prefix
// brew uses on Intel Macs. Layout is identical; just a different
// root.
func TestBrewSymlinkFor_IntelMac(t *testing.T) {
	root := t.TempDir()
	prefix := filepath.Join(root, "usr", "local")
	resolved := filepath.Join(prefix, "Cellar", "carlos", "0.7.0", "bin", "carlos")
	sym := filepath.Join(prefix, "bin", "carlos")
	mustMkdir(t, filepath.Dir(resolved))
	mustWrite(t, resolved, "fake binary")
	mustMkdir(t, filepath.Dir(sym))
	mustWrite(t, sym, "fake symlink target")

	if got := brewSymlinkFor(resolved); got != sym {
		t.Errorf("brewSymlinkFor = %q, want %q", got, sym)
	}
}

// TestBrewSymlinkFor_NonBrewReturnsEmpty is the negative case:
// a go-install / dev-build path that doesn't pass through Cellar
// must return "" so the caller falls back to the resolved path.
func TestBrewSymlinkFor_NonBrewReturnsEmpty(t *testing.T) {
	cases := []string{
		"",
		"/Users/george/go/bin/carlos",
		"/tmp/go-build/carlos",
		filepath.Join(t.TempDir(), "carlos"),
	}
	for _, p := range cases {
		if got := brewSymlinkFor(p); got != "" {
			t.Errorf("brewSymlinkFor(%q) = %q, want empty", p, got)
		}
	}
}

// TestBrewSymlinkFor_MissingSymlinkFallback proves the Stat gate:
// when the binary IS under Cellar/ but the prefix/bin symlink isn't
// there (broken brew install? user removed the link?), the helper
// returns "" so the caller falls back to the resolved path rather
// than baking a dangling reference into the unit file.
func TestBrewSymlinkFor_MissingSymlinkFallback(t *testing.T) {
	root := t.TempDir()
	prefix := filepath.Join(root, "opt", "homebrew")
	resolved := filepath.Join(prefix, "Cellar", "carlos", "0.7.0", "bin", "carlos")
	mustMkdir(t, filepath.Dir(resolved))
	mustWrite(t, resolved, "fake binary")
	// Intentionally skip creating prefix/bin/carlos.

	if got := brewSymlinkFor(resolved); got != "" {
		t.Errorf("missing symlink should return empty; got %q", got)
	}
}

// TestResolveBinaryPath_ReturnsExecutableOrSymlink covers the public
// entrypoint by smoke-testing the no-panic + non-empty contract.
// We can't drive os.Executable, so just confirm the returned path
// exists on disk.
func TestResolveBinaryPath_ReturnsExecutableOrSymlink(t *testing.T) {
	got, err := resolveBinaryPath()
	if err != nil {
		t.Fatalf("resolveBinaryPath: %v", err)
	}
	if got == "" {
		t.Fatal("resolveBinaryPath returned empty path")
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("returned path does not exist: %v", err)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}
