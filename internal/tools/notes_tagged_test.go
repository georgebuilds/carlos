package tools

import (
	"context"
	"strings"
	"testing"
)

// TestNotesTaggedHappy — every note carrying #project appears.
func TestNotesTaggedHappy(t *testing.T) {
	tool := NewNotesTaggedTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(), []byte(`{"tag": "project"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["tag"] != "project" {
		t.Errorf("tag echo: %v", m["tag"])
	}
	if m["vault"] == "" || m["vault"] == nil {
		t.Error("vault missing")
	}
	notes, _ := m["notes"].([]any)
	if len(notes) < 2 {
		t.Errorf("expected ≥2 notes tagged 'project'; got %d", len(notes))
	}
	// Each entry should have title, path, description, modified.
	for _, e := range notes {
		em, _ := e.(map[string]any)
		if _, ok := em["title"].(string); !ok {
			t.Errorf("entry missing title: %+v", em)
		}
		if _, ok := em["modified"].(string); !ok {
			t.Errorf("entry missing modified: %+v", em)
		}
	}
}

// TestNotesTaggedStripHash — `tag: "#project"` resolves identically to
// `tag: "project"`.
func TestNotesTaggedStripHash(t *testing.T) {
	tool := NewNotesTaggedTool(newTestEnv(t))
	a, _ := tool.Execute(context.Background(), []byte(`{"tag": "project"}`))
	b, _ := tool.Execute(context.Background(), []byte(`{"tag": "#project"}`))
	am := asMap(t, a)
	bm := asMap(t, b)
	if am["total"] != bm["total"] {
		t.Errorf("# strip should not change totals; got %v vs %v", am["total"], bm["total"])
	}
}

// TestNotesTaggedEmpty — missing tag returns missing-field envelope.
func TestNotesTaggedEmpty(t *testing.T) {
	tool := NewNotesTaggedTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "tag") {
		t.Errorf("expected required-field envelope; got %+v", m)
	}
}
