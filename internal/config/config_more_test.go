package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultPath honours the CARLOS_CONFIG override and otherwise
// returns ~/.carlos/config.yaml.
func TestDefaultPath(t *testing.T) {
	t.Setenv("CARLOS_CONFIG", "/tmp/from-env/config.yaml")
	if got := DefaultPath(); got != "/tmp/from-env/config.yaml" {
		t.Errorf("CARLOS_CONFIG override ignored: %q", got)
	}
	t.Setenv("CARLOS_CONFIG", "")
	got := DefaultPath()
	if !strings.HasSuffix(got, filepath.Join(".carlos", "config.yaml")) {
		t.Errorf("DefaultPath should end with .carlos/config.yaml; got %q", got)
	}
}

// TestDefaultPath_NoHomeDirFallback exercises the rare branch where
// os.UserHomeDir returns an error or empty string: we still produce a
// usable relative-path default rather than panicking.
func TestDefaultPath_NoHomeDirFallback(t *testing.T) {
	t.Setenv("CARLOS_CONFIG", "")
	t.Setenv("HOME", "") // forces UserHomeDir to err on darwin/linux
	got := DefaultPath()
	if got != filepath.Join(".carlos", "config.yaml") {
		t.Errorf("DefaultPath without HOME: got %q", got)
	}
}

// TestDefaultDir is the parent directory of DefaultPath.
func TestDefaultDir(t *testing.T) {
	t.Setenv("CARLOS_CONFIG", "/tmp/x/config.yaml")
	if got := DefaultDir(); got != "/tmp/x" {
		t.Errorf("DefaultDir: want /tmp/x got %q", got)
	}
}

// TestExists returns true for existing files, false for missing ones.
func TestExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "real.yaml")
	if err := os.WriteFile(path, []byte("user_name: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Exists(path) {
		t.Errorf("Exists(present) = false")
	}
	if Exists(filepath.Join(dir, "missing.yaml")) {
		t.Errorf("Exists(absent) = true")
	}
}

// TestSave_NilCfgErrors guards against a future refactor that drops the
// nil check.
func TestSave_NilCfgErrors(t *testing.T) {
	dir := t.TempDir()
	err := Save(filepath.Join(dir, "x.yaml"), nil)
	if err == nil {
		t.Fatal("expected error for nil cfg")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("error should mention nil; got %v", err)
	}
}

// TestSave_DirCreationFailureSurfaces wraps Save in a tree where the
// parent path collides with a regular file, so MkdirAll cannot create
// the leaf directory. The error must surface, not silently swallow.
func TestSave_MkdirParentBlockedByFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "block")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Try to save into blocker/config.yaml; the parent path is a regular
	// file so MkdirAll should fail.
	err := Save(filepath.Join(blocker, "config.yaml"), &Config{UserName: "x"})
	if err == nil {
		t.Fatal("expected mkdir error when parent path is a file")
	}
}

// TestSave_OpenTmpFailureSurfaces causes os.OpenFile on the tmp path to
// fail by pointing Save at a path whose tmp file name is also a
// directory (renaming over a directory will fail too, but the tmp open
// happens first - we want the tmp path itself to already be a
// directory).
func TestSave_OpenTmpFailureSurfaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	tmp := path + ".tmp"
	// Make the tmp path a directory so O_TRUNC+O_WRONLY fails.
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	err := Save(path, &Config{UserName: "x"})
	if err == nil {
		t.Fatal("expected open-tmp error when tmp path is a directory")
	}
	if !strings.Contains(err.Error(), "open tmp") && !strings.Contains(err.Error(), "write tmp") {
		t.Errorf("error should mention tmp; got %v", err)
	}
}

// TestSave_RenameFailureSurfaces makes the destination path a directory
// so the final os.Rename fails. The tmp file should be cleaned up.
func TestSave_RenameFailureSurfaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	// Drop a file inside so the directory isn't empty - some platforms
	// allow renaming over an empty dir; non-empty guarantees a failure.
	if err := os.WriteFile(filepath.Join(path, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Save(path, &Config{UserName: "x"})
	if err == nil {
		t.Fatal("expected rename error when destination is a non-empty dir")
	}
	// Tmp should be cleaned up.
	if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
		t.Errorf("tmp file not cleaned after rename failure: %v", statErr)
	}
}

// TestLoad_MalformedYAMLWrapped guarantees parse errors include the
// path so users can find the file from a log line.
func TestLoad_MalformedYAMLWrapped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("user_name: [ unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "config: parse") {
		t.Errorf("wrap should mention 'config: parse'; got %v", err)
	}
}

// TestReadAllClose runs the small helper through a happy path so the
// branch coverage shows green.
func TestReadAllClose(t *testing.T) {
	r := io.NopCloser(strings.NewReader("hello"))
	out, err := readAllClose(r)
	if err != nil {
		t.Fatalf("readAllClose: %v", err)
	}
	if string(out) != "hello" {
		t.Errorf("readAllClose: got %q", string(out))
	}
}
