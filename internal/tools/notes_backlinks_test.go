package tools

import (
	"context"
	"strings"
	"testing"
)

// TestNotesBacklinksHappy — every note that wikilinks carlos appears.
func TestNotesBacklinksHappy(t *testing.T) {
	tool := NewNotesBacklinksTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(), []byte(`{"note": "carlos"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["target"] != "carlos.md" {
		t.Errorf("target: %v", m["target"])
	}
	if m["vault"] == "" || m["vault"] == nil {
		t.Error("vault field missing")
	}
	bl, _ := m["backlinks"].([]any)
	if len(bl) < 3 {
		t.Errorf("expected at least 3 backlinks; got %d", len(bl))
	}
	wantPaths := map[string]bool{
		"mvp-roadmap.md":         false,
		"skill-induction.md":     false,
		"hermes-distillation.md": false,
	}
	for _, e := range bl {
		em, _ := e.(map[string]any)
		path, _ := em["path"].(string)
		if _, ok := wantPaths[path]; ok {
			wantPaths[path] = true
		}
	}
	for p, hit := range wantPaths {
		if !hit {
			t.Errorf("backlinks missing %s", p)
		}
	}
}

// TestNotesBacklinksUnresolved — target not in vault → not-found envelope.
func TestNotesBacklinksUnresolved(t *testing.T) {
	tool := NewNotesBacklinksTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"note": "phase 99"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "not found") {
		t.Errorf("expected not-found envelope; got %+v", m)
	}
}
