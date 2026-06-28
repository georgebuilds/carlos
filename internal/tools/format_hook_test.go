package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
)

var errFakeFormat = errors.New("fake formatter failure")

// fakeFoundFormatter returns a Formatter whose binaries are always "present"
// and whose runner is a no-op success, so the write/edit hook can be tested
// without touching real formatters.
func fakeFoundFormatter() *Formatter {
	f := NewFormatter(config.FormatterConfig{})
	f.lookPath = func(bin string) (string, error) { return "/fake/" + bin, nil }
	f.run = func(_ context.Context, _ string, _ []string) error { return nil }
	return f
}

func TestWriteToolAppendsFormatterNote(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	wt := NewWriteTool()
	wt.Formatter = fakeFoundFormatter()
	in, _ := json.Marshal(map[string]any{"path": p, "content": "package main\n"})
	out, err := wt.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "wrote ") {
		t.Errorf("receipt missing write line: %q", out)
	}
	if !strings.Contains(string(out), "formatted with gofmt") {
		t.Errorf("receipt missing formatter note: %q", out)
	}
}

func TestWriteToolNilFormatterUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	in, _ := json.Marshal(map[string]any{"path": p, "content": "package main\n"})
	out, err := NewWriteTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "formatted") {
		t.Errorf("nil formatter should add no note; got %q", out)
	}
}

func TestWriteToolNoNoteForUnmatchedExtension(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.txt")
	wt := NewWriteTool()
	wt.Formatter = fakeFoundFormatter()
	in, _ := json.Marshal(map[string]any{"path": p, "content": "hello\n"})
	out, err := wt.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "formatted") {
		t.Errorf(".txt has no formatter; receipt should be clean: %q", out)
	}
}

func TestEditToolAppendsFormatterNote(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	if err := os.WriteFile(p, []byte("package main // OLD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	et := NewEditTool()
	et.Formatter = fakeFoundFormatter()
	in, _ := json.Marshal(map[string]any{"path": p, "search": "OLD", "replace": "NEW"})
	out, err := et.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "edited ") {
		t.Errorf("receipt missing edit line: %q", out)
	}
	if !strings.Contains(string(out), "formatted with gofmt") {
		t.Errorf("receipt missing formatter note: %q", out)
	}
}

func TestWriteToolFormatterFailureStillSucceeds(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	wt := NewWriteTool()
	f := NewFormatter(config.FormatterConfig{})
	f.lookPath = func(bin string) (string, error) { return "/fake/" + bin, nil }
	f.run = func(_ context.Context, _ string, _ []string) error { return errFakeFormat }
	wt.Formatter = f
	in, _ := json.Marshal(map[string]any{"path": p, "content": "package main\n"})
	out, err := wt.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("a formatter failure must not fail the write: %v", err)
	}
	if _, statErr := os.Stat(p); statErr != nil {
		t.Fatalf("file must still be written despite formatter failure: %v", statErr)
	}
	if !strings.Contains(string(out), "formatter gofmt failed") {
		t.Errorf("receipt should carry the failure note; got %q", out)
	}
}

func TestEnableFormatterWiresWriteAndEdit(t *testing.T) {
	r := NewDefaultRegistry()
	EnableFormatter(r, fakeFoundFormatter())

	wt, _ := r.Get("write")
	if wt.(*WriteTool).Formatter == nil {
		t.Error("write tool not wired with formatter")
	}
	et, _ := r.Get("edit")
	if et.(*EditTool).Formatter == nil {
		t.Error("edit tool not wired with formatter")
	}
}

func TestEnableFormatterNilSafe(t *testing.T) {
	// None of these should panic.
	EnableFormatter(nil, fakeFoundFormatter())
	r := NewDefaultRegistry()
	EnableFormatter(r, nil)
	if wt, _ := r.Get("write"); wt.(*WriteTool).Formatter != nil {
		t.Error("nil formatter should leave write tool unwired")
	}
}
