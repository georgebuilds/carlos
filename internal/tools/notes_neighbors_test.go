package tools

import (
	"context"
	"strings"
	"testing"
)

// TestNotesNeighborsHappy — outgoing, incoming, unresolved buckets
// are all populated for carlos.md (which links to mvp-roadmap +
// hermes-distillation + skill-induction + the ghost link unresolved-
// target).
func TestNotesNeighborsHappy(t *testing.T) {
	tool := NewNotesNeighborsTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(), []byte(`{"note": "carlos"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["note"] != "carlos.md" {
		t.Errorf("note: %v", m["note"])
	}
	if m["vault"] == nil {
		t.Error("vault missing")
	}
	out2, _ := m["outgoing"].([]any)
	if len(out2) < 3 {
		t.Errorf("expected ≥3 outgoing; got %d", len(out2))
	}
	in, _ := m["incoming"].([]any)
	if len(in) == 0 {
		t.Errorf("expected ≥1 incoming; got 0")
	}
	unres, _ := m["unresolved_out"].([]any)
	if len(unres) == 0 {
		t.Errorf("expected ≥1 unresolved out; got 0")
	}
	// Each unresolved entry has display + line.
	first, _ := unres[0].(map[string]any)
	if _, ok := first["display"].(string); !ok {
		t.Errorf("unresolved missing display: %+v", first)
	}
}

// TestNotesNeighborsNotFound — bad note → not-found envelope.
func TestNotesNeighborsNotFound(t *testing.T) {
	tool := NewNotesNeighborsTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"note": "phase 99"}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "not found") {
		t.Errorf("expected not-found envelope; got %+v", m)
	}
}
