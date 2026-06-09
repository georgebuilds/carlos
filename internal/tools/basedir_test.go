package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
		name             string
		baseDir, path    string
		want             string
		wantErrSubstring string
	}{
		{"empty-base-relative", "", "foo.go", "foo.go", ""},
		{"empty-base-absolute", "", "/etc/hosts", "/etc/hosts", ""},
		{"join-relative", "/tmp/wt", "foo.go", "/tmp/wt/foo.go", ""},
		{"join-relative-subdir", "/tmp/wt", "sub/foo.go", "/tmp/wt/sub/foo.go", ""},
		{"absolute-honoured-with-base", "/tmp/wt", "/etc/hosts", "/etc/hosts", ""},
		{"clean-redundant-dot", "/tmp/wt", "./sub/foo.go", "/tmp/wt/sub/foo.go", ""},
		{"inside-dotdot-back-in", "/tmp/wt", "sub/../foo.go", "/tmp/wt/foo.go", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveBaseDir(c.baseDir, c.path)
			if c.wantErrSubstring != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil (path = %q)", c.wantErrSubstring, got)
				}
				if !strings.Contains(err.Error(), c.wantErrSubstring) {
					t.Errorf("error = %q, want substring %q", err.Error(), c.wantErrSubstring)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestResolveBaseDir_RejectsSandboxEscape is the security regression
// guard: any relative path that resolves outside baseDir (via leading
// `..` or nested `..` that overshoots) must be rejected with an error,
// not silently rewritten to escape the sandbox.
func TestResolveBaseDir_RejectsSandboxEscape(t *testing.T) {
	cases := []string{
		"../etc/passwd",
		"../../etc/passwd",
		"../../../etc/passwd",
		"sub/../../../etc/passwd",
		"..",
		"./../outside",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			got, err := resolveBaseDir("/tmp/wt", p)
			if err == nil {
				t.Fatalf("path %q escaped baseDir, resolved to %q without error", p, got)
			}
			if !strings.Contains(err.Error(), "escapes sandbox base") {
				t.Errorf("error = %q, want \"escapes sandbox base\" wording", err.Error())
			}
		})
	}
}

// TestBaseDir_RelativeDotDotIsRejected wires the escape-rejection guard
// through one of the actual file tools (Read) so we cover the
// end-to-end model path: a model that submits `../../../etc/passwd`
// while the agent runs under a sandbox baseDir gets a clean error
// instead of a file from the host fs.
func TestBaseDir_RelativeDotDotIsRejected(t *testing.T) {
	sandbox := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("sensitive\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Build a relative path that, under filepath.Join semantics, would
	// resolve to `outside/secret.txt`. The fix must reject this before
	// the open syscall ever runs.
	rel, err := filepath.Rel(sandbox, secret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rel, "..") {
		t.Fatalf("test setup wrong: rel %q does not begin with ..", rel)
	}

	r := NewReadTool()
	r.BaseDir = sandbox
	in, _ := json.Marshal(map[string]any{"path": rel})
	out, execErr := r.Execute(context.Background(), in)
	if execErr == nil {
		t.Fatalf("relative ../ path read succeeded with %q; sandbox escape", string(out))
	}
	if !strings.Contains(execErr.Error(), "escapes sandbox base") {
		t.Errorf("error = %q, want \"escapes sandbox base\" wording", execErr.Error())
	}
}
