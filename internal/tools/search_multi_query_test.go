package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- shared parseQueries tests ---------------------------------------------

func TestParseQueries_LegacySingle(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"query": "foo"})
	queries, isBatch, err := parseQueries("x_search", raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if isBatch {
		t.Error("isBatch true on single query")
	}
	if len(queries) != 1 || queries[0] != "foo" {
		t.Errorf("queries = %v, want [foo]", queries)
	}
}

func TestParseQueries_BatchedThree(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"queries": []string{"a", "b", "c"}})
	queries, isBatch, err := parseQueries("x_search", raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !isBatch {
		t.Error("isBatch false on batched input")
	}
	if len(queries) != 3 || queries[0] != "a" || queries[1] != "b" || queries[2] != "c" {
		t.Errorf("queries = %v, want [a b c]", queries)
	}
}

func TestParseQueries_BothSet_Errors(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"query": "foo", "queries": []string{"a", "b"}})
	_, _, err := parseQueries("x_search", raw)
	if err == nil {
		t.Fatal("expected error when both query and queries set")
	}
	if !strings.Contains(err.Error(), "either") || !strings.Contains(err.Error(), "not both") {
		t.Errorf("error = %v, want substring 'either ... not both'", err)
	}
}

func TestParseQueries_BothEmpty_Errors(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{})
	_, _, err := parseQueries("x_search", raw)
	if err == nil {
		t.Fatal("expected error when neither query nor queries set")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %v, want substring 'empty'", err)
	}
}

func TestParseQueries_WhitespaceQueryTreatedEmpty(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"query": "   "})
	_, _, err := parseQueries("x_search", raw)
	if err == nil {
		t.Fatal("expected error on whitespace-only query")
	}
}

func TestParseQueries_FiltersEmptyBatchEntries(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"queries": []string{"a", "", " ", "b"}})
	queries, isBatch, err := parseQueries("x_search", raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !isBatch {
		t.Error("isBatch false")
	}
	if len(queries) != 2 || queries[0] != "a" || queries[1] != "b" {
		t.Errorf("queries = %v, want [a b] after empty-string filter", queries)
	}
}

func TestParseQueries_BadJSON(t *testing.T) {
	_, _, err := parseQueries("x_search", []byte("not json"))
	if err == nil {
		t.Fatal("expected JSON parse error")
	}
}

// --- web_search batched ----------------------------------------------------

func TestWebSearchTool_BatchedReturnsBlocks(t *testing.T) {
	be := &fakeBackend{
		name: "fake",
		results: []SearchResult{
			{Title: "T", URL: "https://t", Snippet: "s"},
		},
	}
	tool := &WebSearchTool{Backend: be}
	raw, _ := json.Marshal(map[string]any{"queries": []string{"a", "b", "c"}})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var resp webSearchBatchedOutput
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Backend != "fake" {
		t.Errorf("backend = %q, want fake", resp.Backend)
	}
	if len(resp.Queries) != 3 {
		t.Errorf("queries echo = %v", resp.Queries)
	}
	if len(resp.Blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(resp.Blocks))
	}
	wantQ := []string{"a", "b", "c"}
	for i, blk := range resp.Blocks {
		if blk.Query != wantQ[i] {
			t.Errorf("blocks[%d].Query = %q, want %q", i, blk.Query, wantQ[i])
		}
		if blk.Error != "" {
			t.Errorf("blocks[%d].Error = %q, want empty", i, blk.Error)
		}
		if len(blk.Results) != 1 {
			t.Errorf("blocks[%d].Results = %d, want 1", i, len(blk.Results))
		}
	}
}

func TestWebSearchTool_SingleQueryBackwardCompat(t *testing.T) {
	be := &fakeBackend{
		name: "fake",
		results: []SearchResult{
			{Title: "T", URL: "https://t"},
		},
	}
	tool := &WebSearchTool{Backend: be}
	raw, _ := json.Marshal(map[string]any{"query": "single"})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	// Single-query envelope: webSearchOutput (Query top-level) not
	// webSearchBatchedOutput (Blocks). Decode loosely into a map and
	// assert on shape.
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := m["blocks"]; ok {
		t.Error("single-query path leaked 'blocks' field")
	}
	if q, ok := m["query"].(string); !ok || q != "single" {
		t.Errorf("query field = %v, want 'single'", m["query"])
	}
}

func TestWebSearchTool_BothSetErrors(t *testing.T) {
	tool := &WebSearchTool{Backend: &fakeBackend{name: "fake"}}
	raw, _ := json.Marshal(map[string]any{"query": "x", "queries": []string{"a", "b"}})
	_, err := tool.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error when both set")
	}
}

func TestWebSearchTool_BothEmptyErrors(t *testing.T) {
	tool := &WebSearchTool{Backend: &fakeBackend{name: "fake"}}
	raw, _ := json.Marshal(map[string]any{})
	_, err := tool.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error when neither set")
	}
}

// --- wikipedia_search batched ----------------------------------------------

func TestWikipediaSearchTool_BatchedConcurrentCap(t *testing.T) {
	// Track concurrent in-flight calls; assert the peak stays <= 3.
	var inflight, peak int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cur := atomic.AddInt32(&inflight, 1)
		// Update peak as a max.
		for {
			p := atomic.LoadInt32(&peak)
			if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
				break
			}
		}
		// Hold briefly so concurrent queries overlap.
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		_, _ = w.Write([]byte(`{"pages":[{"id":1,"key":"X","title":"X","excerpt":"x"}]}`))
	}))
	defer srv.Close()

	noRetry := retryPolicy{MaxAttempts: 1}
	tool := &WikipediaSearchTool{
		Backend: &WikipediaBackend{Endpoint: srv.URL, RetryPolicy: &noRetry},
	}
	raw, _ := json.Marshal(map[string]any{"queries": []string{"a", "b", "c", "d", "e", "f"}})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var resp wikipediaBatchedOutput
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Blocks) != 6 {
		t.Errorf("blocks = %d, want 6", len(resp.Blocks))
	}
	if peak > 3 {
		t.Errorf("peak concurrent = %d, want <= %d (wikipediaBatchConcurrency)", peak, wikipediaBatchConcurrency)
	}
}

func TestWikipediaSearchTool_BatchedKeyedByQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		// Echo the query in the title so we can verify results map back.
		_, _ = w.Write([]byte(`{"pages":[{"id":1,"key":"K","title":"hit-for-` + q + `","excerpt":"x"}]}`))
	}))
	defer srv.Close()

	noRetry := retryPolicy{MaxAttempts: 1}
	tool := &WikipediaSearchTool{
		Backend: &WikipediaBackend{Endpoint: srv.URL, RetryPolicy: &noRetry},
	}
	raw, _ := json.Marshal(map[string]any{"queries": []string{"alpha", "beta", "gamma"}})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var resp wikipediaBatchedOutput
	_ = json.Unmarshal(out, &resp)
	if len(resp.Blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(resp.Blocks))
	}
	for _, blk := range resp.Blocks {
		if len(blk.Results) != 1 {
			t.Errorf("query %q: results = %d, want 1", blk.Query, len(blk.Results))
			continue
		}
		want := "hit-for-" + blk.Query
		if blk.Results[0].Title != want {
			t.Errorf("query %q: title = %q, want %q", blk.Query, blk.Results[0].Title, want)
		}
	}
}

func TestWikipediaSearchTool_BothSetErrors(t *testing.T) {
	tool := NewWikipediaSearchTool()
	raw, _ := json.Marshal(map[string]any{"query": "x", "queries": []string{"a", "b"}})
	_, err := tool.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error when both set")
	}
}

func TestWikipediaSearchTool_BothEmptyErrors(t *testing.T) {
	tool := NewWikipediaSearchTool()
	raw, _ := json.Marshal(map[string]any{})
	_, err := tool.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error when neither set")
	}
}

// --- arxiv_search batched + throttle ---------------------------------------

// TestArxivSearchTool_BatchedRespects3sSpacing verifies the production
// MinInterval gate spaces serialized batched calls at least 3s apart.
// To keep the test fast we drop MinInterval to 60ms (still in real time)
// and assert the gap between the first and second request is >= 50ms.
//
// A stricter, slower variant of this test (MinInterval: 3s) is gated
// behind the -tags=slow build tag in arxiv_3s_test.go below.
func TestArxivSearchTool_BatchedSerializesWithThrottle(t *testing.T) {
	var hits []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits = append(hits, time.Now())
		_, _ = w.Write([]byte(arxivAtomEmpty))
	}))
	defer srv.Close()

	noRetry := retryPolicy{MaxAttempts: 1}
	be := &ArxivBackend{
		Endpoint:    srv.URL,
		UserAgent:   "carlos-test",
		MinInterval: 60 * time.Millisecond,
		RetryPolicy: &noRetry,
	}
	tool := &ArxivSearchTool{Backend: be, Timeout: 10 * time.Second}
	raw, _ := json.Marshal(map[string]any{"queries": []string{"a", "b"}})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var resp arxivBatchedOutput
	_ = json.Unmarshal(out, &resp)
	if len(resp.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(resp.Blocks))
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	gap := hits[1].Sub(hits[0])
	if gap < 50*time.Millisecond {
		t.Errorf("gap between calls = %v, want >= 50ms (MinInterval 60ms)", gap)
	}
}

func TestArxivSearchTool_BatchedReturnsBlocks(t *testing.T) {
	stub := &stubBackend{
		name: "arxiv",
		results: []SearchResult{
			{Rank: 1, Title: "P1", URL: "http://arxiv/1", Snippet: "s"},
		},
	}
	tool := &ArxivSearchTool{Backend: stub}
	raw, _ := json.Marshal(map[string]any{"queries": []string{"a", "b"}})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var resp arxivBatchedOutput
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Backend != "arxiv" {
		t.Errorf("backend = %q", resp.Backend)
	}
	if len(resp.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(resp.Blocks))
	}
	for i, blk := range resp.Blocks {
		if blk.Query != []string{"a", "b"}[i] {
			t.Errorf("blocks[%d].Query = %q", i, blk.Query)
		}
	}
}

func TestArxivSearchTool_BothSetErrors(t *testing.T) {
	tool := &ArxivSearchTool{Backend: &stubBackend{name: "arxiv"}}
	raw, _ := json.Marshal(map[string]any{"query": "x", "queries": []string{"a", "b"}})
	_, err := tool.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error when both set")
	}
}

func TestArxivSearchTool_BothEmptyErrors(t *testing.T) {
	tool := &ArxivSearchTool{Backend: &stubBackend{name: "arxiv"}}
	raw, _ := json.Marshal(map[string]any{})
	_, err := tool.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error when neither set")
	}
}

// --- gh_search batched -----------------------------------------------------

func TestGHSearch_BatchedReturnsBlocks(t *testing.T) {
	calls := int32(0)
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return []byte(fakeGHCodeJSON), nil
	}}
	tool := &GHSearchTool{Runner: runner, Timeout: 30 * time.Second}
	raw, _ := json.Marshal(map[string]any{"queries": []string{"a", "b", "c"}, "kind": "code"})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var resp ghSearchBatchedOutput
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Backend != "github" {
		t.Errorf("backend = %q", resp.Backend)
	}
	if resp.Kind != "code" {
		t.Errorf("kind = %q", resp.Kind)
	}
	if len(resp.Blocks) != 3 {
		t.Fatalf("blocks = %d", len(resp.Blocks))
	}
	for i, blk := range resp.Blocks {
		if blk.Query != []string{"a", "b", "c"}[i] {
			t.Errorf("blocks[%d].Query = %q", i, blk.Query)
		}
		if blk.Error != "" {
			t.Errorf("blocks[%d].Error = %q", i, blk.Error)
		}
		if blk.Count != 2 {
			t.Errorf("blocks[%d].Count = %d, want 2 (from fakeGHCodeJSON)", i, blk.Count)
		}
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("gh runner calls = %d, want 3", calls)
	}
}

func TestGHSearch_BatchedSerial(t *testing.T) {
	// Verify each call completes before the next begins (the courtesy
	// pause + serial loop). We record start times and assert the
	// gap between them clears the courtesy delay.
	var starts []time.Time
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		starts = append(starts, time.Now())
		time.Sleep(20 * time.Millisecond)
		return []byte("[]"), nil
	}}
	tool := &GHSearchTool{Runner: runner, Timeout: 30 * time.Second}
	raw, _ := json.Marshal(map[string]any{"queries": []string{"a", "b"}, "kind": "code"})
	if _, err := tool.Execute(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if len(starts) != 2 {
		t.Fatalf("starts = %d, want 2", len(starts))
	}
	// With a 1s courtesy delay between calls + 20ms server-side work,
	// the second call must start at least ~1s after the first.
	gap := starts[1].Sub(starts[0])
	if gap < 900*time.Millisecond {
		t.Errorf("gap = %v, want >= 900ms (courtesy delay applies)", gap)
	}
}

func TestGHSearch_BatchedPropagatesPerQueryError(t *testing.T) {
	calls := int32(0)
	runner := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 2 {
			return nil, errors.New("rate limit exceeded")
		}
		return []byte("[]"), nil
	}}
	tool := &GHSearchTool{Runner: runner, Timeout: 30 * time.Second}
	raw, _ := json.Marshal(map[string]any{"queries": []string{"a", "b", "c"}, "kind": "code"})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("batched call should not abort on per-query error: %v", err)
	}
	var resp ghSearchBatchedOutput
	_ = json.Unmarshal(out, &resp)
	if len(resp.Blocks) != 3 {
		t.Fatalf("blocks = %d", len(resp.Blocks))
	}
	if resp.Blocks[1].Error == "" {
		t.Error("second block should carry the rate-limit error")
	}
	if resp.Blocks[0].Error != "" || resp.Blocks[2].Error != "" {
		t.Errorf("blocks 0 and 2 should be error-free: %+v", resp.Blocks)
	}
}

func TestGHSearch_BothSetErrors(t *testing.T) {
	tool := &GHSearchTool{Runner: &fakeGHRunner{}}
	raw, _ := json.Marshal(map[string]any{"query": "x", "queries": []string{"a", "b"}, "kind": "code"})
	_, err := tool.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error when both set")
	}
}

func TestGHSearch_BothEmptyErrors(t *testing.T) {
	tool := &GHSearchTool{Runner: &fakeGHRunner{}}
	raw, _ := json.Marshal(map[string]any{"kind": "code"})
	_, err := tool.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error when neither query nor queries set")
	}
}
