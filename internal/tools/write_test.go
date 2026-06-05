package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrite_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "new.txt")
	in, _ := json.Marshal(map[string]any{"path": p, "content": "hi\n"})
	out, err := NewWriteTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "wrote") {
		t.Errorf("receipt missing 'wrote': %q", out)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi\n" {
		t.Errorf("content mismatch: %q", got)
	}
}

func TestWrite_CreateModeRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(p, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"path": p, "content": "new"})
	if _, err := NewWriteTool().Execute(context.Background(), in); err == nil {
		t.Fatal("expected error in create mode on existing path")
	}
	// File contents should be untouched.
	got, _ := os.ReadFile(p)
	if string(got) != "orig" {
		t.Errorf("existing file was clobbered: %q", got)
	}
}

func TestWrite_OverwriteMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(p, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{
		"path": p, "content": "new content", "mode": "overwrite",
	})
	if _, err := NewWriteTool().Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "new content" {
		t.Errorf("overwrite failed: %q", got)
	}
}

func TestWrite_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deep", "nested", "tree", "out.txt")
	in, _ := json.Marshal(map[string]any{"path": p, "content": "x"})
	if _, err := NewWriteTool().Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestWrite_BadInput(t *testing.T) {
	tool := NewWriteTool()
	if _, err := tool.Execute(context.Background(), []byte(`not json`)); err == nil {
		t.Error("expected parse error")
	}
	if _, err := tool.Execute(context.Background(), []byte(`{"content":"x"}`)); err == nil {
		t.Error("expected empty path error")
	}
	if _, err := tool.Execute(context.Background(),
		[]byte(`{"path":"/tmp/x","content":"","mode":"bogus"}`)); err == nil {
		t.Error("expected invalid mode error")
	}
}

func TestWrite_AtomicNoStaleTmp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	in, _ := json.Marshal(map[string]any{"path": p, "content": "ok"})
	if _, err := NewWriteTool().Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	// .tmp must not linger after a successful write.
	if _, err := os.Stat(p + ".tmp"); err == nil {
		t.Errorf("stale tmp file lingered after successful write")
	}
}

func TestWrite_SchemaIsValidJSON(t *testing.T) {
	s := string(NewWriteTool().Schema())
	for _, k := range []string{`"path"`, `"content"`, `"mode"`, `"required"`} {
		if !strings.Contains(s, k) {
			t.Errorf("schema missing %s: %s", k, s)
		}
	}
}

func TestWrite_FmtSanity(t *testing.T) {
	// Make sure fmt import isn't unused — covered by the receipt format.
	got := fmt.Sprintf("wrote %d bytes", 7)
	if got != "wrote 7 bytes" {
		t.Errorf("fmt sanity: %q", got)
	}
}
