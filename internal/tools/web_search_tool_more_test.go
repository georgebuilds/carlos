package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// TestWebSearchTool_MultiBackendPartialFailures — wrapping a MultiBackend
// where one backend fails surfaces a partial_failures map alongside the
// surviving results, and the Backends list names every contributor.
func TestWebSearchTool_MultiBackendPartialFailures(t *testing.T) {
	good := &fakeMulti{name: "good", results: []SearchResult{
		mkResult(1, "https://good.example/1", "g1"),
	}}
	bad := &fakeMulti{name: "bad", err: errors.New("backend down")}
	multi := NewMultiBackend(good, bad)
	tool := &WebSearchTool{Backend: multi}

	in, _ := json.Marshal(map[string]any{"query": "q"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("partial failure should not be a hard error: %v", err)
	}
	var resp webSearchOutput
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Error("expected surviving results from the good backend")
	}
	if resp.PartialFailures["bad"] == "" {
		t.Errorf("expected partial_failures to name the bad backend; got %+v", resp.PartialFailures)
	}
	if len(resp.Backends) != 2 {
		t.Errorf("Backends should list both contributors; got %v", resp.Backends)
	}
}

// TestWebSearchTool_MultiBackendAllFail — when every backend errors, the
// MultiBackend returns an error which the tool wraps with the backend name.
func TestWebSearchTool_MultiBackendAllFail(t *testing.T) {
	a := &fakeMulti{name: "a", err: errors.New("a-down")}
	b := &fakeMulti{name: "b", err: errors.New("b-down")}
	tool := &WebSearchTool{Backend: NewMultiBackend(a, b)}
	in, _ := json.Marshal(map[string]any{"query": "q"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("all-fail should surface a hard error")
	}
}

// TestWebSearchTool_BatchedQueries — the batched `queries` path returns a
// blocks envelope with one block per query, and a per-block error when a
// query's backend fails.
func TestWebSearchTool_BatchedQueries(t *testing.T) {
	// A backend that fails only for the query "boom".
	be := &queryAwareBackend{
		name:    "qb",
		failOn:  map[string]error{"boom": errors.New("boom failed")},
		results: []SearchResult{mkResult(1, "https://x/ok", "ok")},
	}
	tool := &WebSearchTool{Backend: be}
	in, _ := json.Marshal(map[string]any{"queries": []string{"ok", "boom"}})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("batched execute: %v", err)
	}
	var resp webSearchBatchedOutput
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(resp.Blocks))
	}
	byQuery := map[string]webSearchBatchedBlock{}
	for _, b := range resp.Blocks {
		byQuery[b.Query] = b
	}
	if byQuery["ok"].Error != "" {
		t.Errorf("ok block should have no error: %q", byQuery["ok"].Error)
	}
	if byQuery["boom"].Error == "" {
		t.Error("boom block should carry an error")
	}
}
