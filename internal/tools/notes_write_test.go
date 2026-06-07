package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

func newWriteEnv(t *testing.T) (*notesEnv, string) {
	t.Helper()
	dir := t.TempDir()
	return newNotesEnvWithFrames(
		config.VaultConfig{Path: dir},
		frame.Config{
			Default: "personal",
			Active:  "personal",
			List: []frame.Frame{
				{Name: "personal", VaultSubtree: "personal"},
				{Name: "work", VaultSubtree: "work"},
			},
		},
		"personal",
	), dir
}

func mustWrite(t *testing.T, tool *NotesWriteTool, in notesWriteInput) notesWriteResponse {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(context.Background(), b)
	if err != nil {
		t.Fatalf("notes_write: %v", err)
	}
	var resp notesWriteResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestNotesWrite_DefaultsToActiveFrameSubtree(t *testing.T) {
	env, vault := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	resp := mustWrite(t, tool, notesWriteInput{Path: "devices.md", Content: "# Devices\n"})
	if resp.Path != "personal/devices.md" {
		t.Errorf("path = %q, want personal/devices.md", resp.Path)
	}
	if resp.Frame != "personal" {
		t.Errorf("frame = %q, want personal", resp.Frame)
	}
	abs := filepath.Join(vault, "personal", "devices.md")
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(body) != "# Devices\n" {
		t.Errorf("content mismatch: %q", body)
	}
}

func TestNotesWrite_NestedRelativePath(t *testing.T) {
	env, vault := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	mustWrite(t, tool, notesWriteInput{Path: "journal/2026-06-07.md", Content: "today"})
	abs := filepath.Join(vault, "personal", "journal", "2026-06-07.md")
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("nested file should exist: %v", err)
	}
}

func TestNotesWrite_AutoAppendsMdExtension(t *testing.T) {
	env, vault := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	resp := mustWrite(t, tool, notesWriteInput{Path: "devices", Content: "x"})
	if !strings.HasSuffix(resp.Path, ".md") {
		t.Errorf("path should default to .md; got %q", resp.Path)
	}
	if _, err := os.Stat(filepath.Join(vault, "personal", "devices.md")); err != nil {
		t.Errorf("auto-extended file should exist: %v", err)
	}
}

func TestNotesWrite_CreateFailsOnExisting(t *testing.T) {
	env, _ := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	mustWrite(t, tool, notesWriteInput{Path: "devices.md", Content: "first"})
	b, _ := json.Marshal(notesWriteInput{Path: "devices.md", Content: "second"})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Error("create-on-existing should error")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention 'already exists'; got %v", err)
	}
}

func TestNotesWrite_OverwriteReplacesContent(t *testing.T) {
	env, vault := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	mustWrite(t, tool, notesWriteInput{Path: "devices.md", Content: "first"})
	mustWrite(t, tool, notesWriteInput{Path: "devices.md", Content: "second", Mode: "overwrite"})
	body, err := os.ReadFile(filepath.Join(vault, "personal", "devices.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "second" {
		t.Errorf("overwrite content wrong: %q", body)
	}
}

func TestNotesWrite_AbsolutePathInsideSubtreeAccepted(t *testing.T) {
	env, vault := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	target := filepath.Join(vault, "personal", "abs.md")
	resp := mustWrite(t, tool, notesWriteInput{Path: target, Content: "x"})
	if resp.Path != "personal/abs.md" {
		t.Errorf("path = %q, want personal/abs.md", resp.Path)
	}
}

func TestNotesWrite_AbsolutePathOutsideVaultRejected(t *testing.T) {
	env, _ := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	tmp := t.TempDir() + "/outside.md"
	b, _ := json.Marshal(notesWriteInput{Path: tmp, Content: "x"})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Error("absolute path outside vault should reject")
	} else if !strings.Contains(err.Error(), "outside") {
		t.Errorf("error should mention 'outside'; got %v", err)
	}
}

func TestNotesWrite_RelativeWithDotDotRejected(t *testing.T) {
	env, _ := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	b, _ := json.Marshal(notesWriteInput{Path: "../escape.md", Content: "x"})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Error("../escape should reject")
	}
}

func TestNotesWrite_RejectsEmptyVault(t *testing.T) {
	env := newNotesEnv(config.VaultConfig{})
	tool := NewNotesWriteTool(env)
	b, _ := json.Marshal(notesWriteInput{Path: "x.md", Content: "x"})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Error("empty vault should reject")
	}
}

func TestNotesWrite_RejectsEmptyPath(t *testing.T) {
	env, _ := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	b, _ := json.Marshal(notesWriteInput{Path: "", Content: "x"})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Error("empty path should reject")
	}
}

func TestNotesWrite_RejectsEmptyContent(t *testing.T) {
	env, _ := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	b, _ := json.Marshal(notesWriteInput{Path: "x.md", Content: ""})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Error("empty content should reject")
	}
}

func TestNotesWrite_RejectsBadMode(t *testing.T) {
	env, _ := newWriteEnv(t)
	tool := NewNotesWriteTool(env)
	b, _ := json.Marshal(notesWriteInput{Path: "x.md", Content: "x", Mode: "append"})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Error("unknown mode should reject")
	}
}

func TestNotesWrite_LegacySingleShelfWritesToVaultRoot(t *testing.T) {
	dir := t.TempDir()
	env := newNotesEnv(config.VaultConfig{Path: dir})
	tool := NewNotesWriteTool(env)
	resp := mustWrite(t, tool, notesWriteInput{Path: "devices.md", Content: "x"})
	if resp.Path != "devices.md" {
		t.Errorf("legacy single-shelf should land at vault root; got %q", resp.Path)
	}
	if resp.Frame != "" {
		t.Errorf("legacy should not stamp a frame; got %q", resp.Frame)
	}
}

func TestNotesWrite_ResolvePathHelper(t *testing.T) {
	cases := []struct {
		vault, subtree, in string
		wantSuffix         string
		wantErr            bool
	}{
		{"/v", "personal", "devices.md", "/v/personal/devices.md", false},
		{"/v", "", "devices.md", "/v/devices.md", false},
		{"/v", "personal", "/v/personal/x.md", "/v/personal/x.md", false},
		{"/v", "personal", "/etc/passwd", "", true},
		{"/v", "personal", "../escape.md", "", true},
		{"/v", "work", "/v/personal/x.md", "", true},
	}
	for _, c := range cases {
		got, err := resolveNotesWritePath(c.vault, c.subtree, c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("resolveNotesWritePath(%q,%q,%q) want error, got %q", c.vault, c.subtree, c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveNotesWritePath(%q,%q,%q) unexpected error: %v", c.vault, c.subtree, c.in, err)
			continue
		}
		if !strings.HasSuffix(got, c.wantSuffix) {
			t.Errorf("resolveNotesWritePath(%q,%q,%q) = %q, want suffix %q", c.vault, c.subtree, c.in, got, c.wantSuffix)
		}
	}
}

func TestNotesWrite_IsInsideHelper(t *testing.T) {
	cases := []struct {
		path, root string
		want       bool
	}{
		{"/root/a", "/root/a", true},
		{"/root/a/b", "/root/a", true},
		{"/root/a-extra", "/root/a", false},
		{"/other", "/root/a", false},
	}
	for _, c := range cases {
		if got := isInside(c.path, c.root); got != c.want {
			t.Errorf("isInside(%q,%q) = %v, want %v", c.path, c.root, got, c.want)
		}
	}
}
