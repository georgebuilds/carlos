package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sampleBody is a trimmed but realistic /api/v1/models response: two
// rows, mixed pricing, mixed context-window sizes.
const sampleBody = `{
  "data": [
    {
      "id": "google/gemini-3.5-flash",
      "name": "Gemini 3.5 Flash",
      "context_length": 1048576,
      "pricing": { "prompt": "0.0000001", "completion": "0.0000004" }
    },
    {
      "id": "anthropic/claude-sonnet-4-6",
      "name": "Claude Sonnet 4.6",
      "context_length": 200000,
      "pricing": { "prompt": "0.000003", "completion": "0.000015" }
    }
  ]
}`

// fakeServer mints an httptest.Server returning sampleBody. The test
// returns the cleanup func and rewires ModelsEndpoint to the server URL.
func fakeServer(t *testing.T, body string, status int) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	prev := ModelsEndpoint
	ModelsEndpoint = srv.URL
	return func() {
		ModelsEndpoint = prev
		srv.Close()
	}
}

func TestFetchModels_CacheMissHitsHTTP(t *testing.T) {
	defer fakeServer(t, sampleBody, http.StatusOK)()
	dir := t.TempDir()

	models, err := FetchModels(context.Background(), dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("want 2 models, got %d", len(models))
	}
	// Sorted ascending by prompt price: gemini ($0.10/M) before sonnet ($3/M).
	if models[0].ID != "google/gemini-3.5-flash" {
		t.Errorf("want gemini first by price, got %q", models[0].ID)
	}
	if got := models[0].PromptUSDPerM; got < 0.099 || got > 0.101 {
		t.Errorf("gemini prompt price: want ~0.10, got %v", got)
	}
	if got := models[1].PromptUSDPerM; got < 2.99 || got > 3.01 {
		t.Errorf("sonnet prompt price: want ~3.00, got %v", got)
	}
	if models[1].CtxLen != 200000 {
		t.Errorf("sonnet ctxlen: want 200000 got %d", models[1].CtxLen)
	}
	// Cache file written.
	if _, err := os.Stat(filepath.Join(dir, cacheFileName)); err != nil {
		t.Errorf("cache file not written: %v", err)
	}
}

func TestFetchModels_CacheHitSkipsHTTP(t *testing.T) {
	dir := t.TempDir()
	// Pre-seed the cache with a single fake row.
	want := []ModelInfo{{ID: "fake/model", Name: "Fake", PromptUSDPerM: 1.0, CtxLen: 8192}}
	b, _ := json.Marshal(want)
	if err := os.WriteFile(filepath.Join(dir, cacheFileName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	// Point the endpoint at a server that would error if called.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()
	prev := ModelsEndpoint
	ModelsEndpoint = srv.URL
	defer func() { ModelsEndpoint = prev }()

	got, err := FetchModels(context.Background(), dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if called {
		t.Error("fresh cache should have prevented HTTP fetch")
	}
	if len(got) != 1 || got[0].ID != "fake/model" {
		t.Errorf("want cached row returned, got %+v", got)
	}
}

func TestFetchModels_CorruptCacheFallsBackToFetch(t *testing.T) {
	defer fakeServer(t, sampleBody, http.StatusOK)()
	dir := t.TempDir()
	// Garbage in the cache file.
	if err := os.WriteFile(filepath.Join(dir, cacheFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := FetchModels(context.Background(), dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("corrupt cache should refetch; got %d models", len(got))
	}
}

func TestFetchModels_ExpiredCacheFallsBackToFetch(t *testing.T) {
	defer fakeServer(t, sampleBody, http.StatusOK)()
	dir := t.TempDir()
	path := filepath.Join(dir, cacheFileName)
	// Seed cache then backdate mtime past the TTL.
	b, _ := json.Marshal([]ModelInfo{{ID: "stale"}})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	got, err := FetchModels(context.Background(), dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(got) != 2 || got[0].ID == "stale" {
		t.Errorf("stale cache should refetch; got %+v", got)
	}
}

func TestFetchModels_FetchErrorBubbles(t *testing.T) {
	// 500 response → caller gets non-nil error.
	defer fakeServer(t, "boom", http.StatusInternalServerError)()
	dir := t.TempDir()
	_, err := FetchModels(context.Background(), dir, 24*time.Hour)
	if err == nil {
		t.Fatal("want error on 500, got nil")
	}
}

func TestParsePricePerMillion(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"0.000003", 3.0},
		{"0.0000001", 0.1},
		{"", 0},
		{"not-a-number", 0},
	}
	for _, c := range cases {
		got := parsePricePerMillion(c.in)
		if got < c.want-0.001 || got > c.want+0.001 {
			t.Errorf("parsePricePerMillion(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
