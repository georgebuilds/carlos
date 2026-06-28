package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An over-long regex is rejected before compilation, bounding the
// (context-unaware) compile cost of a pathological pattern.
func TestGrep_RejectsOverlongRegexPattern(t *testing.T) {
	dir := t.TempDir()
	pattern := strings.Repeat("a", maxRegexPatternLen+1)
	in, _ := json.Marshal(map[string]any{
		"root": dir, "pattern": pattern, "regex": true,
	})
	_, err := NewGrepTool().Execute(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("want too-long error, got %v", err)
	}
}

// A normal regex still compiles and matches.
func TestGrep_AcceptsNormalRegex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{
		"root": dir, "pattern": "h.llo", "regex": true,
	})
	out, err := NewGrepTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "hello world") {
		t.Errorf("expected match, got %q", out)
	}
}

// A literal pattern of any length is unaffected by the regex cap.
func TestGrep_LongLiteralPatternAllowed(t *testing.T) {
	dir := t.TempDir()
	in, _ := json.Marshal(map[string]any{
		"root": dir, "pattern": strings.Repeat("x", maxRegexPatternLen+1), "regex": false,
	})
	if _, err := NewGrepTool().Execute(context.Background(), in); err != nil {
		t.Fatalf("literal search should not be length-capped: %v", err)
	}
}
