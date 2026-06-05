package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRead_HappyPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(p, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool()
	out, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"path":%q}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("hello")) || !bytes.Contains(out, []byte("world")) {
		t.Errorf("output missing payload: %q", out)
	}
}

func TestRead_LineRange(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(p, []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool()
	out, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"path":%q,"start_line":2,"end_line":4}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "b\nc\nd\n") {
		t.Errorf("line range wrong: %q", got)
	}
	if strings.Contains(got, "a\n") || strings.Contains(got, "e\n") {
		t.Errorf("line range leaked outside [2,4]: %q", got)
	}
}

func TestRead_BinaryRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bin")
	if err := os.WriteFile(p, []byte{0x7f, 0x45, 0x4c, 0x46, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool()
	_, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"path":%q}`, p)))
	if err == nil {
		t.Fatal("expected binary-refusal error")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Errorf("error should mention binary: %v", err)
	}
}

func TestRead_Truncation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	// 100 lines of 100 bytes each — well over a 1 KiB cap.
	var buf bytes.Buffer
	for i := 0; i < 100; i++ {
		buf.WriteString(strings.Repeat("x", 100))
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{MaxOutputLen: 1024}
	out, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"path":%q}`, p)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("[truncated at")) {
		t.Errorf("expected truncation marker, got: %q", out)
	}
}

func TestRead_BadInput(t *testing.T) {
	tool := NewReadTool()
	if _, err := tool.Execute(context.Background(), []byte(`not json`)); err == nil {
		t.Error("expected parse error on bad JSON")
	}
	if _, err := tool.Execute(context.Background(), []byte(`{}`)); err == nil {
		t.Error("expected empty-path error")
	}
	if _, err := tool.Execute(context.Background(),
		[]byte(`{"path":"x","start_line":5,"end_line":2}`)); err == nil {
		t.Error("expected end_line<start_line error")
	}
}

func TestRead_MissingFile(t *testing.T) {
	tool := NewReadTool()
	_, err := tool.Execute(context.Background(),
		[]byte(`{"path":"/definitely/does/not/exist/abc"}`))
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestRead_SchemaIsValidJSON(t *testing.T) {
	s := string(NewReadTool().Schema())
	for _, k := range []string{`"path"`, `"start_line"`, `"end_line"`, `"required"`} {
		if !strings.Contains(s, k) {
			t.Errorf("schema missing %s: %s", k, s)
		}
	}
}
