package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitBlame_BadJSON(t *testing.T) {
	_, err := NewGitBlameTool().Execute(context.Background(), []byte(`{bad`))
	if err == nil || !strings.Contains(err.Error(), "parse input") {
		t.Errorf("want parse error, got %v", err)
	}
}

func TestGitBlame_EmptyPath(t *testing.T) {
	in, _ := json.Marshal(map[string]any{"path": ""})
	if _, err := NewGitBlameTool().Execute(context.Background(), in); err == nil ||
		!strings.Contains(err.Error(), "empty path") {
		t.Errorf("want empty-path error, got %v", err)
	}
}

func TestGitBlame_NegativeLines(t *testing.T) {
	in, _ := json.Marshal(map[string]any{"path": "x", "start_line": -1})
	if _, err := NewGitBlameTool().Execute(context.Background(), in); err == nil ||
		!strings.Contains(err.Error(), "non-negative") {
		t.Errorf("want non-negative error, got %v", err)
	}
}

func TestGitBlame_EndBeforeStart(t *testing.T) {
	in, _ := json.Marshal(map[string]any{"path": "x", "start_line": 5, "end_line": 2})
	if _, err := NewGitBlameTool().Execute(context.Background(), in); err == nil ||
		!strings.Contains(err.Error(), "end_line < start_line") {
		t.Errorf("want end<start error, got %v", err)
	}
}

func TestGitBlame_RefusesNonRepo(t *testing.T) {
	gitOrSkip(t)
	dir := t.TempDir()
	in, _ := json.Marshal(map[string]any{"dir": dir, "path": "x.txt"})
	if _, err := NewGitBlameTool().Execute(context.Background(), in); err == nil ||
		!strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("want not-a-repo error, got %v", err)
	}
}

// TestGitBlame_ExplicitLineRange exercises the -L start,end branch.
func TestGitBlame_ExplicitLineRange(t *testing.T) {
	dir := mkGitRepo(t)
	// Give first.txt multiple lines of history.
	if err := os.WriteFile(filepath.Join(dir, "first.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"dir": dir, "path": "first.txt", "start_line": 1, "end_line": 1})
	out, err := NewGitBlameTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("alpha")) {
		t.Errorf("range blame missing alpha: %q", out)
	}
}

// TestGitBlame_StartOnlyRange exercises the -L start,+1000 branch.
func TestGitBlame_StartOnlyRange(t *testing.T) {
	dir := mkGitRepo(t)
	in, _ := json.Marshal(map[string]any{"dir": dir, "path": "first.txt", "start_line": 1})
	out, err := NewGitBlameTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("alpha")) {
		t.Errorf("start-only blame missing alpha: %q", out)
	}
}
