package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlob_BasicMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "")
	writeFile(t, filepath.Join(root, "b.go"), "")
	writeFile(t, filepath.Join(root, "c.txt"), "")
	in, _ := json.Marshal(map[string]any{"pattern": "*.go", "root": root})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("a.go")) || !bytes.Contains(out, []byte("b.go")) {
		t.Errorf("missing .go entries: %q", out)
	}
	if bytes.Contains(out, []byte("c.txt")) {
		t.Errorf("c.txt should not match: %q", out)
	}
}

func TestGlob_DoubleStarRecursive(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "top.go"), "")
	writeFile(t, filepath.Join(root, "src", "x.go"), "")
	writeFile(t, filepath.Join(root, "src", "deep", "y.go"), "")
	in, _ := json.Marshal(map[string]any{"pattern": "**/*.go", "root": root})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"top.go", "src/x.go", "src/deep/y.go"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("missing %s in output: %q", want, out)
		}
	}
}

func TestGlob_RespectsGitignore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "node_modules/\n")
	writeFile(t, filepath.Join(root, "node_modules", "x.js"), "")
	writeFile(t, filepath.Join(root, "src", "y.js"), "")
	in, _ := json.Marshal(map[string]any{"pattern": "**/*.js", "root": root})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("node_modules")) {
		t.Errorf("gitignored path leaked: %q", out)
	}
	if !bytes.Contains(out, []byte("src/y.js")) {
		t.Errorf("expected src/y.js: %q", out)
	}
}

func TestGlob_NoMatches(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.go"), "")
	in, _ := json.Marshal(map[string]any{"pattern": "*.rs", "root": root})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "(no matches)") {
		t.Errorf("expected no-match marker: %q", out)
	}
}

func TestGlob_BadInput(t *testing.T) {
	tool := NewGlobTool()
	if _, err := tool.Execute(context.Background(), []byte(`bad`)); err == nil {
		t.Error("expected parse error")
	}
	if _, err := tool.Execute(context.Background(), []byte(`{}`)); err == nil {
		t.Error("expected empty-pattern error")
	}
}

func TestGlob_SchemaIsValidJSON(t *testing.T) {
	s := string(NewGlobTool().Schema())
	for _, k := range []string{`"pattern"`, `"root"`, `"respect_gitignore"`} {
		if !strings.Contains(s, k) {
			t.Errorf("schema missing %s: %s", k, s)
		}
	}
}
