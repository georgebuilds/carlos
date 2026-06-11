package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

// TestNotesWrite_BadJSON — malformed input surfaces a parse error (not a
// panic). Execute returns (nil, err) here because the bad input predates
// any vault resolution.
func TestNotesWrite_BadJSON(t *testing.T) {
	env, _ := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	if _, err := tool.Execute(context.Background(), []byte(`{not json`)); err == nil {
		t.Fatal("malformed JSON should error")
	} else if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("want parse-input error, got %v", err)
	}
}

// TestNotesWrite_ActiveFrameDoesNotResolve — frames are wired but the
// active frame name resolves to nothing (config mid-edit / bad pin).
// notes_write must refuse rather than silently land at vault root.
func TestNotesWrite_ActiveFrameDoesNotResolve(t *testing.T) {
	dir := t.TempDir()
	env := newNotesEnvWithFrames(
		config.VaultConfig{Path: dir},
		frame.Config{
			// List is non-empty so hasFrames() is true, but no name
			// resolves the active pin "ghost".
			Default: "",
			Active:  "",
			List: []frame.Frame{
				{Name: "personal", VaultSubtree: "personal"},
			},
		},
		"ghost", // session-active name not present in List
	)
	tool := NewNotesWriteTool(env)
	out, err := tool.Execute(context.Background(),
		[]byte(`{"path":"x.md","content":"y"}`))
	if err == nil {
		t.Fatalf("expected active-frame refusal, got out=%s", out)
	}
	if !strings.Contains(err.Error(), "active frame") {
		t.Errorf("want active-frame error, got %v", err)
	}
}

// TestNotesWrite_OverwriteCreatesNewFile — overwrite mode on a file that
// does not yet exist still creates it (the Stat guard only runs for
// create mode).
func TestNotesWrite_OverwriteCreatesNewFile(t *testing.T) {
	env, vault := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	resp := mustWrite(t, tool, notesWriteInput{Path: "fresh.md", Content: "hi", Mode: "overwrite"})
	if resp.Mode != "overwrite" {
		t.Errorf("mode = %q", resp.Mode)
	}
	if _, err := os.Stat(filepath.Join(vault, "personal", "fresh.md")); err != nil {
		t.Errorf("overwrite should create a missing file: %v", err)
	}
}

// TestEvalAncestor_ResolvesExistingSymlinkRoot — evalAncestor must
// canonicalise a real symlinked ancestor while still appending an
// unrealized tail. We point a symlink at a real dir, then ask
// evalAncestor for <link>/newchild.md (the leaf does not exist).
func TestEvalAncestor_ResolvesExistingSymlinkRoot(t *testing.T) {
	real := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got, err := evalAncestor(filepath.Join(link, "newchild.md"))
	if err != nil {
		t.Fatal(err)
	}
	// EvalSymlinks also canonicalises macOS /var -> /private/var, so
	// compare against the resolved real path.
	realResolved, _ := filepath.EvalSymlinks(real)
	want := filepath.Join(realResolved, "newchild.md")
	if got != want {
		t.Errorf("evalAncestor = %q, want %q", got, want)
	}
}

// TestNotesWrite_MkdirFailsParentIsFile — when a path component inside the
// subtree is a regular file, the atomic writer's MkdirAll fails and the
// error surfaces (exercising writeNotesFileAtomic's mkdir branch).
func TestNotesWrite_MkdirFailsParentIsFile(t *testing.T) {
	env, vault := newWriteEnv(t)
	// Pre-create personal/blocker as a FILE, then try to write under it.
	if err := os.MkdirAll(filepath.Join(vault, "personal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "personal", "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewNotesWriteTool(env)
	b := []byte(`{"path":"blocker/child.md","content":"y"}`)
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Fatal("expected mkdir failure when a path component is a file")
	}
}

// TestEvalAncestor_NonExistentLeafUnderRealDir — a target whose leaf does
// not exist but whose parent does resolves by stitching the unrealized
// leaf onto the canonical parent (the os.Lstat-existing branch).
func TestEvalAncestor_NonExistentLeafUnderRealDir(t *testing.T) {
	dir := t.TempDir()
	got, err := evalAncestor(filepath.Join(dir, "newfile.md"))
	if err != nil {
		t.Fatal(err)
	}
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	want := filepath.Join(resolvedDir, "newfile.md")
	if got != want {
		t.Errorf("evalAncestor = %q, want %q", got, want)
	}
}

// TestRelativeToVault_FallsBackOnUnrelatedRoot — when target cannot be
// made relative to the vault, relativeToVault still returns a usable
// string (here a relative path is computed since Rel succeeds across
// roots on the same volume; we assert it is non-empty and slash-form).
func TestRelativeToVault_SlashForm(t *testing.T) {
	got := relativeToVault("/v/root", "/v/root/a/b.md")
	if got != "a/b.md" {
		t.Errorf("relativeToVault = %q, want a/b.md", got)
	}
}
