package tools

import (
	"context"
	"strings"
	"testing"
)

// TestNotesResolveUnambiguous — `link: carlos` → carlos.md, ambiguous=false.
func TestNotesResolveUnambiguous(t *testing.T) {
	tool := NewNotesResolveTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(), []byte(`{"link": "carlos"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["resolved"] != "carlos.md" {
		t.Errorf("resolved: %v", m["resolved"])
	}
	if m["ambiguous"].(bool) {
		t.Errorf("expected ambiguous=false; got true")
	}
	if m["vault"] == nil {
		t.Error("vault missing")
	}
}

// TestNotesResolveAmbiguous — `notes` has two candidates (root + sub/).
// Resolver picks shortest path; ambiguous=true; candidates list populated.
func TestNotesResolveAmbiguous(t *testing.T) {
	tool := NewNotesResolveTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"link": "notes"}`))
	m := asMap(t, out)
	if m["resolved"] != "notes.md" {
		t.Errorf("resolved should be shortest 'notes.md'; got %v", m["resolved"])
	}
	if !m["ambiguous"].(bool) {
		t.Errorf("expected ambiguous=true; got false")
	}
	cands, _ := m["candidates"].([]any)
	if len(cands) < 2 {
		t.Errorf("expected ≥2 candidates; got %d", len(cands))
	}
}

// TestNotesResolveBrackets — `[[carlos]]` resolves same as `carlos`.
func TestNotesResolveBrackets(t *testing.T) {
	tool := NewNotesResolveTool(newTestEnv(t))
	a, _ := tool.Execute(context.Background(), []byte(`{"link": "carlos"}`))
	b, _ := tool.Execute(context.Background(), []byte(`{"link": "[[carlos]]"}`))
	am := asMap(t, a)
	bm := asMap(t, b)
	if am["resolved"] != bm["resolved"] {
		t.Errorf("brackets should not change resolution; got %v vs %v", am["resolved"], bm["resolved"])
	}
}

// TestNotesResolveNotFound — phase 99 → not-found envelope.
func TestNotesResolveNotFound(t *testing.T) {
	tool := NewNotesResolveTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"link": "phase 99"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "not found") {
		t.Errorf("expected not-found envelope; got %+v", m)
	}
}
