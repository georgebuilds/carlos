package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// noRetryPolicy returns a single-attempt policy so transport-error tests
// don't spend wall-clock time on retries.
func noRetryPolicy() *retryPolicy {
	return &retryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxTotalWait: time.Second}
}

// TestWikipediaBackend_TransportError — a dial failure surfaces as an
// http error.
func TestWikipediaBackend_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close()
	w := &WikipediaBackend{Endpoint: dead, Client: &http.Client{}, RetryPolicy: noRetryPolicy()}
	if _, err := w.Search(context.Background(), "q", 5); err == nil ||
		!strings.Contains(err.Error(), "http") {
		t.Errorf("want http transport error, got %v", err)
	}
}

// TestWikipediaSearchTool_BackendErrorWraps — when the backend errors, the
// single-query tool path wraps it with the tool name.
func TestWikipediaSearchTool_BackendErrorWraps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close()
	tool := &WikipediaSearchTool{
		Backend: &WikipediaBackend{Endpoint: dead, Client: &http.Client{}, RetryPolicy: noRetryPolicy()},
	}
	raw, _ := json.Marshal(map[string]any{"query": "q"})
	if _, err := tool.Execute(context.Background(), raw); err == nil ||
		!strings.Contains(err.Error(), "wikipedia_search") {
		t.Errorf("want wrapped wikipedia_search error, got %v", err)
	}
}

// TestWikipediaSearchTool_BatchedBlocks — the batched form returns a block
// per query.
func TestWikipediaSearchTool_BatchedBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pages":[{"id":1,"key":"K","title":"T","excerpt":"e","description":"d"}]}`))
	}))
	defer srv.Close()
	tool := &WikipediaSearchTool{
		Backend: &WikipediaBackend{Endpoint: srv.URL, Client: srv.Client(), Lang: "en"},
	}
	raw, _ := json.Marshal(map[string]any{"queries": []string{"a", "b"}})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var resp wikipediaBatchedOutput
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(resp.Blocks))
	}
}

// TestArxivBackend_TransportError — a dial failure surfaces as an http
// error (no retry budget burned).
func TestArxivBackend_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close()
	a := &ArxivBackend{Endpoint: dead, Client: &http.Client{}, RetryPolicy: noRetryPolicy()}
	if _, err := a.Search(context.Background(), "q", 5); err == nil ||
		!strings.Contains(err.Error(), "http") {
		t.Errorf("want http transport error, got %v", err)
	}
}
