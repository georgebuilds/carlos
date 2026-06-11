package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func bashExec(t *testing.T, tool *BashTool, cmd string) []byte {
	t.Helper()
	in, _ := json.Marshal(map[string]any{"cmd": cmd})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	return out
}

// TestBash_WorkingDir — an explicit WorkingDir is the command's cwd.
func TestBash_WorkingDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "marker.txt"), "")
	tool := &BashTool{WorkingDir: dir}
	out := bashExec(t, tool, "ls")
	if !bytes.Contains(out, []byte("marker.txt")) {
		t.Errorf("WorkingDir not honoured; ls = %q", out)
	}
}

// TestBash_BaseDirFallback — when WorkingDir is empty, BaseDir is used as
// the command cwd.
func TestBash_BaseDirFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base-marker.txt"), "")
	tool := &BashTool{BaseDir: dir}
	out := bashExec(t, tool, "ls")
	if !bytes.Contains(out, []byte("base-marker.txt")) {
		t.Errorf("BaseDir fallback not honoured; ls = %q", out)
	}
}

// TestBash_DefaultTimeout — a tool with no Timeout configured still runs a
// quick command to completion (exercising the timeout<=0 default branch).
func TestBash_DefaultTimeout(t *testing.T) {
	tool := &BashTool{} // Timeout defaulted to 30s
	out := bashExec(t, tool, "echo defaulted")
	if !bytes.Contains(out, []byte("defaulted")) {
		t.Errorf("default-timeout run failed; got %q", out)
	}
}

// TestBash_EmptyCmd — an empty command is rejected before exec.
func TestBash_EmptyCmd(t *testing.T) {
	tool := &BashTool{}
	in, _ := json.Marshal(map[string]any{"cmd": ""})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Error("empty cmd should be rejected")
	}
}
