package tools

import (
	"context"
	"strings"
	"testing"
)

// TestNotesSearchHappy — `notes_search skill induction` returns hits.
func TestNotesSearchHappy(t *testing.T) {
	tool := NewNotesSearchTool(newTestEnv(t))
	out, err := tool.Execute(context.Background(), []byte(`{"query": "skill induction"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["vault"] == "" || m["vault"] == nil {
		t.Errorf("vault field missing: %+v", m)
	}
	matches, _ := m["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("expected ≥1 match; got %+v", m)
	}
	first, _ := matches[0].(map[string]any)
	title, _ := first["title"].(string)
	if !strings.Contains(strings.ToLower(title), "skill induction") {
		t.Errorf("first match should be skill induction note; got %v", first)
	}
}

// TestNotesSearchEmptyQueryRejected — `query: ""` returns the
// missing-field envelope rather than crashing.
func TestNotesSearchEmptyQueryRejected(t *testing.T) {
	tool := NewNotesSearchTool(newTestEnv(t))
	out, _ := tool.Execute(context.Background(), []byte(`{"query": ""}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); !strings.Contains(errMsg, "query") {
		t.Errorf("expected query-missing envelope; got %+v", m)
	}
}

// TestObsidianSearchVaultDifferentTotals — Phase T-1 split:
// arbitrary vault queries go through obsidian_search.
func TestObsidianSearchVaultDifferentTotals(t *testing.T) {
	env := newTestEnv(t)
	primary, _ := NewNotesSearchTool(env).Execute(context.Background(),
		[]byte(`{"query": "carlos"}`))
	pm := asMap(t, primary)
	pTotal, _ := pm["total"].(float64)

	altInput := []byte(`{"query": "carlos", "vault": "` + testAltVaultPath(t) + `"}`)
	alt, _ := NewObsidianSearchTool(env).Execute(context.Background(), altInput)
	am := asMap(t, alt)
	aTotal, _ := am["total"].(float64)
	if pTotal == aTotal {
		t.Errorf("primary + alt vaults should have different totals; both %v", pTotal)
	}
	vault, _ := am["vault"].(string)
	if !strings.HasSuffix(vault, "vault_alt") {
		t.Errorf("vault field should point at alt vault; got %q", vault)
	}
}
