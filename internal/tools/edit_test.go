package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEdit_Single(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{
		"path": p, "search": "world", "replace": "carlos",
	})
	out, err := NewEditTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "replaced") {
		t.Errorf("receipt missing 'replaced': %q", out)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "hello carlos\n" {
		t.Errorf("edit wrong: %q", got)
	}
}

func TestEdit_CountMismatchRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("foo foo foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Default expect_match_count is 1, file has 3.
	in, _ := json.Marshal(map[string]any{
		"path": p, "search": "foo", "replace": "bar",
	})
	if _, err := NewEditTool().Execute(context.Background(), in); err == nil {
		t.Fatal("expected count-mismatch error")
	}
	// File must be unchanged.
	got, _ := os.ReadFile(p)
	if string(got) != "foo foo foo\n" {
		t.Errorf("file mutated despite refusal: %q", got)
	}
}

func TestEdit_ExpectCountThree(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("foo foo foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{
		"path": p, "search": "foo", "replace": "bar",
		"expect_match_count": 3,
	})
	if _, err := NewEditTool().Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "bar bar bar\n" {
		t.Errorf("expected all replaced: %q", got)
	}
}

func TestEdit_BinaryRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bin")
	if err := os.WriteFile(p, []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{
		"path": p, "search": "\x01", "replace": "\x02",
	})
	if _, err := NewEditTool().Execute(context.Background(), in); err == nil {
		t.Fatal("expected binary refusal")
	}
}

func TestEdit_NoMatchExpectZero(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	zero := 0
	in, _ := json.Marshal(map[string]any{
		"path": p, "search": "zzz", "replace": "yyy",
		"expect_match_count": zero,
	})
	out, err := NewEditTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("expect=0 with no matches should succeed: %v", err)
	}
	if !strings.Contains(string(out), "no matches") {
		t.Errorf("expected 'no matches' receipt: %q", out)
	}
}

func TestEdit_BadInput(t *testing.T) {
	tool := NewEditTool()
	if _, err := tool.Execute(context.Background(), []byte(`bad`)); err == nil {
		t.Error("expected parse error")
	}
	if _, err := tool.Execute(context.Background(), []byte(`{"path":""}`)); err == nil {
		t.Error("expected empty-path error")
	}
	if _, err := tool.Execute(context.Background(),
		[]byte(`{"path":"x","search":"","replace":"y"}`)); err == nil {
		t.Error("expected empty-search error")
	}
}

func TestEdit_SchemaIsValidJSON(t *testing.T) {
	s := string(NewEditTool().Schema())
	for _, k := range []string{`"path"`, `"search"`, `"replace"`, `"expect_match_count"`} {
		if !strings.Contains(s, k) {
			t.Errorf("schema missing %s: %s", k, s)
		}
	}
}
