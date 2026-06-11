package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestGrep_BaseDirDefaultRoot — with no root and a BaseDir set, grep runs
// inside BaseDir.
func TestGrep_BaseDirDefaultRoot(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "f.txt"), "needle here\n")
	tool := &GrepTool{BaseDir: base}
	in, _ := json.Marshal(map[string]any{"pattern": "needle"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("needle")) {
		t.Errorf("BaseDir-default grep should find the match: %q", out)
	}
}

// TestGrep_BaseDirRelativeRoot — a relative root argument resolves against
// BaseDir.
func TestGrep_BaseDirRelativeRoot(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "sub", "g.txt"), "findme\n")
	tool := &GrepTool{BaseDir: base}
	in, _ := json.Marshal(map[string]any{"pattern": "findme", "root": "sub"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("findme")) {
		t.Errorf("BaseDir-relative root grep should find the match: %q", out)
	}
}
