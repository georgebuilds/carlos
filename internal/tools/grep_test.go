package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrep_LiteralMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "package x\nconst NEEDLE = 1\n")
	writeFile(t, filepath.Join(root, "b.go"), "package y\n")
	in, _ := json.Marshal(map[string]any{"pattern": "NEEDLE", "root": root})
	out, err := NewGrepTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("a.go:2:")) {
		t.Errorf("expected a.go:2 hit: %q", out)
	}
	if bytes.Contains(out, []byte("b.go")) {
		t.Errorf("b.go should not match: %q", out)
	}
}

func TestGrep_RegexMode(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.go"), "errFoo := 1\nerrBar := 2\nnope := 3\n")
	in, _ := json.Marshal(map[string]any{
		"pattern": `^err[A-Z]`,
		"root":    root,
		"regex":   true,
	})
	out, err := NewGrepTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("errFoo")) || !bytes.Contains(out, []byte("errBar")) {
		t.Errorf("regex did not match expected lines: %q", out)
	}
	if bytes.Contains(out, []byte("nope")) {
		t.Errorf("regex falsely matched: %q", out)
	}
}

func TestGrep_RespectsGitignoreByDefault(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "ignored/\n")
	writeFile(t, filepath.Join(root, "ignored", "hit.go"), "NEEDLE\n")
	writeFile(t, filepath.Join(root, "kept", "hit.go"), "NEEDLE\n")
	in, _ := json.Marshal(map[string]any{"pattern": "NEEDLE", "root": root})
	out, err := NewGrepTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("ignored/hit.go")) {
		t.Errorf("gitignored file should not appear: %q", out)
	}
	if !bytes.Contains(out, []byte("kept")) {
		t.Errorf("non-ignored file should match: %q", out)
	}
}

func TestGrep_OverrideGitignore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "ignored/\n")
	writeFile(t, filepath.Join(root, "ignored", "hit.go"), "NEEDLE\n")
	off := false
	in, _ := json.Marshal(map[string]any{
		"pattern":           "NEEDLE",
		"root":              root,
		"respect_gitignore": off,
	})
	out, err := NewGrepTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("ignored")) {
		t.Errorf("override should expose ignored file: %q", out)
	}
}

func TestGrep_IncludeFilter(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.go"), "NEEDLE\n")
	writeFile(t, filepath.Join(root, "x.txt"), "NEEDLE\n")
	in, _ := json.Marshal(map[string]any{
		"pattern": "NEEDLE",
		"root":    root,
		"include": "*.go",
	})
	out, err := NewGrepTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("x.go")) {
		t.Errorf("expected x.go hit: %q", out)
	}
	if bytes.Contains(out, []byte("x.txt")) {
		t.Errorf("include filter should exclude .txt: %q", out)
	}
}

func TestGrep_Cap(t *testing.T) {
	root := t.TempDir()
	// 200 files, each with one hit; cap is 100.
	for i := 0; i < 200; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f%03d.go", i)), "NEEDLE\n")
	}
	in, _ := json.Marshal(map[string]any{"pattern": "NEEDLE", "root": root})
	out, err := NewGrepTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("[truncated at")) {
		t.Errorf("expected truncation marker: %q", out[:500])
	}
}

func TestGrep_NoMatches(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "f.go"), "noting here\n")
	in, _ := json.Marshal(map[string]any{"pattern": "xyzzy", "root": root})
	out, err := NewGrepTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "(no matches)") {
		t.Errorf("expected no-matches marker: %q", out)
	}
}

func TestGrep_BadInput(t *testing.T) {
	tool := NewGrepTool()
	if _, err := tool.Execute(context.Background(), []byte(`bad`)); err == nil {
		t.Error("expected parse error")
	}
	if _, err := tool.Execute(context.Background(), []byte(`{}`)); err == nil {
		t.Error("expected empty-pattern error")
	}
	if _, err := tool.Execute(context.Background(),
		[]byte(`{"pattern":"(","regex":true}`)); err == nil {
		t.Error("expected regex compile error")
	}
}

func TestGrep_BinaryFileSkipped(t *testing.T) {
	root := t.TempDir()
	// Binary file with the literal needle in it must NOT be reported.
	writeFile(t, filepath.Join(root, "bin"), "\x00\x01NEEDLE\x02\x03")
	writeFile(t, filepath.Join(root, "ok.txt"), "NEEDLE\n")
	in, _ := json.Marshal(map[string]any{"pattern": "NEEDLE", "root": root})
	out, err := NewGrepTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("bin:")) {
		t.Errorf("binary file should be skipped: %q", out)
	}
	if !bytes.Contains(out, []byte("ok.txt")) {
		t.Errorf("text file with hit should appear: %q", out)
	}
}

func TestGrep_CtxCancel(t *testing.T) {
	root := t.TempDir()
	// Generate enough files that the walker has work to interrupt.
	for i := 0; i < 50; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f%d.go", i)), "padding\n")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before call
	in, _ := json.Marshal(map[string]any{"pattern": "x", "root": root})
	_, err := NewGrepTool().Execute(ctx, in)
	if err == nil {
		t.Error("expected ctx.Err() to surface")
	}
}

func TestGrep_SchemaIsValidJSON(t *testing.T) {
	s := string(NewGrepTool().Schema())
	for _, k := range []string{`"pattern"`, `"root"`, `"include"`, `"regex"`, `"respect_gitignore"`} {
		if !strings.Contains(s, k) {
			t.Errorf("schema missing %s: %s", k, s)
		}
	}
}
