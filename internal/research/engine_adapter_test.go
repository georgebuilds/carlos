package research_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// TestWebFetchAdapter_RoundTrip exercises the adapter against an
// httptest server, asserting the JSON envelope produced by
// WebFetchTool.Execute maps cleanly onto a research.Source.
func TestWebFetchAdapter_RoundTrip(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Adapter Test</title></head><body><p>Hello from adapter.</p></body></html>`))
	}))
	defer ts.Close()

	tool := &tools.WebFetchTool{AllowPrivate: true}
	adapter := &research.WebFetchAdapter{Tool: tool}
	src, err := adapter.Fetch(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if src.Title != "Adapter Test" {
		t.Errorf("Title = %q want Adapter Test", src.Title)
	}
	if !strings.Contains(src.Content, "Hello from adapter") {
		t.Errorf("Content missing expected text: %q", src.Content)
	}
	if src.URL == "" {
		t.Errorf("URL empty")
	}
	if src.FetchedAt.IsZero() {
		t.Errorf("FetchedAt zero")
	}
}

func TestWebFetchAdapter_NilToolErrors(t *testing.T) {
	adapter := &research.WebFetchAdapter{Tool: nil}
	_, err := adapter.Fetch(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("expected error for nil Tool")
	}
}
