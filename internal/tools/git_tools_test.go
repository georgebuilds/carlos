package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitOrSkip skips when git is unavailable on PATH.
func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// mkGitRepo initialises a temp git repo with one committed file and
// returns the repo root. The committer identity is set inline so tests
// don't depend on the user's global git config.
func mkGitRepo(t *testing.T) string {
	t.Helper()
	gitOrSkip(t)
	dir := t.TempDir()
	must := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	must("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "first.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must("add", "first.txt")
	must("commit", "-q", "-m", "init")
	return dir
}

func TestGitStatus_HappyPath(t *testing.T) {
	dir := mkGitRepo(t)
	// Make a change so the porcelain output is non-trivial.
	if err := os.WriteFile(filepath.Join(dir, "second.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"dir": dir})
	out, err := NewGitStatusTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("second.txt")) {
		t.Errorf("output missing untracked file: %q", out)
	}
	if !bytes.Contains(out, []byte("# branch.head")) {
		t.Errorf("output missing --branch porcelain header: %q", out)
	}
}

func TestGitStatus_RefusesNonRepo(t *testing.T) {
	gitOrSkip(t)
	dir := t.TempDir()
	in, _ := json.Marshal(map[string]any{"dir": dir})
	_, err := NewGitStatusTool().Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected not-a-repo error")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error should mention 'not a git repository': %v", err)
	}
}

func TestGitDiff_WorkingTree(t *testing.T) {
	dir := mkGitRepo(t)
	// Modify the tracked file.
	if err := os.WriteFile(filepath.Join(dir, "first.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"dir": dir})
	out, err := NewGitDiffTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("+beta")) {
		t.Errorf("diff should show +beta: %q", out)
	}
}

func TestGitDiff_PathFilter(t *testing.T) {
	dir := mkGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "first.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// other.txt is untracked → diff HEAD wouldn't show it anyway, but
	// validate the path filter narrowing works for tracked files.
	in, _ := json.Marshal(map[string]any{"dir": dir, "path": "first.txt"})
	out, err := NewGitDiffTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("first.txt")) {
		t.Errorf("scoped diff missing first.txt: %q", out)
	}
}

func TestGitLog_OnelineLimit(t *testing.T) {
	dir := mkGitRepo(t)
	in, _ := json.Marshal(map[string]any{"dir": dir, "limit": 5})
	out, err := NewGitLogTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("init")) {
		t.Errorf("log missing init commit: %q", out)
	}
}

func TestGitLog_DefaultLimit(t *testing.T) {
	dir := mkGitRepo(t)
	in, _ := json.Marshal(map[string]any{"dir": dir})
	if _, err := NewGitLogTool().Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestGitBlame_HappyPath(t *testing.T) {
	dir := mkGitRepo(t)
	in, _ := json.Marshal(map[string]any{"dir": dir, "path": "first.txt"})
	out, err := NewGitBlameTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("alpha")) {
		t.Errorf("blame missing line content: %q", out)
	}
}

func TestGitBlame_RefusesNoHistory(t *testing.T) {
	dir := mkGitRepo(t)
	// untracked.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"dir": dir, "path": "new.txt"})
	_, err := NewGitBlameTool().Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected no-history refusal")
	}
	if !strings.Contains(err.Error(), "no git history") {
		t.Errorf("error should mention no git history: %v", err)
	}
}

func TestGitShow_HappyPath(t *testing.T) {
	dir := mkGitRepo(t)
	in, _ := json.Marshal(map[string]any{"dir": dir, "ref": "HEAD"})
	out, err := NewGitShowTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("first.txt")) {
		t.Errorf("show missing first.txt: %q", out)
	}
}

func TestGitShow_InvalidRef(t *testing.T) {
	dir := mkGitRepo(t)
	in, _ := json.Marshal(map[string]any{"dir": dir, "ref": "definitely_not_a_ref_xyz"})
	out, _ := NewGitShowTool().Execute(context.Background(), in)
	// Bad ref → git exits non-zero; we surface the output, not an
	// infrastructure error.
	if !bytes.Contains(out, []byte("unknown revision")) && !bytes.Contains(out, []byte("ambiguous argument")) {
		t.Errorf("output should mention unknown/ambiguous revision: %q", out)
	}
}

func TestGitTools_BadInput(t *testing.T) {
	gitOrSkip(t)
	cases := []struct {
		name string
		tool Tool
		body string
	}{
		{"status_bad_json", NewGitStatusTool(), `bad`},
		{"diff_bad_json", NewGitDiffTool(), `bad`},
		{"log_bad_json", NewGitLogTool(), `bad`},
		{"blame_empty_path", NewGitBlameTool(), `{}`},
		{"show_empty_ref", NewGitShowTool(), `{}`},
		{"blame_bad_json", NewGitBlameTool(), `bad`},
		{"show_bad_json", NewGitShowTool(), `bad`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.tool.Execute(context.Background(), []byte(c.body)); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestGitTools_SchemasValid(t *testing.T) {
	tools := []Tool{
		NewGitStatusTool(), NewGitDiffTool(), NewGitLogTool(),
		NewGitBlameTool(), NewGitShowTool(),
	}
	for _, tl := range tools {
		var v any
		if err := json.Unmarshal(tl.Schema(), &v); err != nil {
			t.Errorf("%s: schema is not valid JSON: %v", tl.Name(), err)
		}
		if tl.Description() == "" {
			t.Errorf("%s: empty description", tl.Name())
		}
	}
}
