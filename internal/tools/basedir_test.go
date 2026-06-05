package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestBaseDir_RelativePathResolvesAgainstBase confirms that when a tool
// is constructed with BaseDir set, a relative path input lands inside
// BaseDir rather than the test process's cwd. The behavior all six
// file tools (read/write/edit/grep/glob/bash) rely on for the slice-7f
// worktree-per-coding-task sandbox.
func TestBaseDir_RelativePathResolvesAgainstBase(t *testing.T) {
	tmp := t.TempDir()
	want := "sandbox content\n"
	if err := os.WriteFile(filepath.Join(tmp, "hello.txt"), []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewReadTool()
	r.BaseDir = tmp
	in, _ := json.Marshal(map[string]any{"path": "hello.txt"})
	out, err := r.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(out) != want {
		t.Errorf("content = %q, want %q (BaseDir resolution failed)", string(out), want)
	}
}

// TestBaseDir_AbsolutePathHonouredAsIs confirms that an absolute path
// is NOT silently rerouted under BaseDir. The slice-7f architectural
// commitment: a model that explicitly asks for /etc/hosts gets it
// (subject to the file's own permissions), so the redirect stays a
// transparent default rather than a stealthy policy.
func TestBaseDir_AbsolutePathHonouredAsIs(t *testing.T) {
	tmp := t.TempDir()
	otherTmp := t.TempDir()
	want := "outside the sandbox\n"
	target := filepath.Join(otherTmp, "out.txt")
	if err := os.WriteFile(target, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewReadTool()
	r.BaseDir = tmp
	in, _ := json.Marshal(map[string]any{"path": target})
	out, err := r.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(out) != want {
		t.Errorf("absolute path content = %q, want %q (BaseDir wrongly rerouted)", string(out), want)
	}
}

// TestBaseDir_ZeroValuePreservesOldBehavior is the back-compat
// guarantee: every existing call site that constructs a tool without
// touching BaseDir gets the original "use cwd / honour absolute paths"
// behavior bit-identically.
func TestBaseDir_ZeroValuePreservesOldBehavior(t *testing.T) {
	tmp := t.TempDir()
	want := "old behavior\n"
	target := filepath.Join(tmp, "f.txt")
	if err := os.WriteFile(target, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewReadTool()
	// BaseDir intentionally left zero.
	in, _ := json.Marshal(map[string]any{"path": target})
	out, err := r.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(out) != want {
		t.Errorf("zero-BaseDir absolute-path read got %q, want %q", string(out), want)
	}
}

// TestResolveBaseDir_Direct exercises the helper directly for the
// permutations the tool-level tests don't surface. Pure function — no
// fs involvement.
func TestResolveBaseDir_Direct(t *testing.T) {
	cases := []struct {
		baseDir, path, want string
	}{
		{"", "foo.go", "foo.go"},
		{"", "/etc/hosts", "/etc/hosts"},
		{"/tmp/wt", "foo.go", "/tmp/wt/foo.go"},
		{"/tmp/wt", "sub/foo.go", "/tmp/wt/sub/foo.go"},
		{"/tmp/wt", "/etc/hosts", "/etc/hosts"},
	}
	for _, c := range cases {
		got := resolveBaseDir(c.baseDir, c.path)
		if got != c.want {
			t.Errorf("resolveBaseDir(%q,%q) = %q, want %q", c.baseDir, c.path, got, c.want)
		}
	}
}
