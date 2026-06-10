package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// === Brave backend tests ===================================================

func TestBraveBackend_ParsesResponseAndStripsTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "test-key" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"web": {
				"results": [
					{"title": "Go std library", "url": "https://pkg.go.dev/std",
					 "description": "the <strong>Go</strong> standard library", "age": "2024-01-02"},
					{"title": "Brave Search API", "url": "https://brave.com/search/api/",
					 "description": "private search"}
				]
			}
		}`))
	}))
	defer srv.Close()
	b := &BraveBackend{APIKey: "test-key", Endpoint: srv.URL}
	got, err := b.Search(context.Background(), "go", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got[0].Rank != 1 || got[1].Rank != 2 {
		t.Errorf("ranks not 1-indexed sequential: %+v", got)
	}
	if strings.Contains(got[0].Snippet, "<strong>") {
		t.Errorf("snippet still has HTML tags: %q", got[0].Snippet)
	}
	if got[0].Snippet != "the Go standard library" {
		t.Errorf("snippet = %q, want stripped+collapsed", got[0].Snippet)
	}
	if got[0].PublishedAt != "2024-01-02" {
		t.Errorf("age not propagated to PublishedAt: %q", got[0].PublishedAt)
	}
}

func TestBraveBackend_NonOKErrorsWithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	b := &BraveBackend{APIKey: "x", Endpoint: srv.URL}
	_, err := b.Search(context.Background(), "x", 1)
	if err == nil {
		t.Fatal("expected error on 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error missing status: %v", err)
	}
}

func TestBraveBackend_MaxCaps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"web":{"results":[
			{"title":"a","url":"https://a"},
			{"title":"b","url":"https://b"},
			{"title":"c","url":"https://c"}
		]}}`))
	}))
	defer srv.Close()
	b := &BraveBackend{APIKey: "x", Endpoint: srv.URL}
	got, _ := b.Search(context.Background(), "x", 2)
	if len(got) != 2 {
		t.Errorf("max=2 returned %d results", len(got))
	}
}

// === SearXNG backend tests =================================================

func TestSearXNGBackend_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			http.Error(w, "format param missing", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"first","url":"https://first.com","content":"snippet one","publishedDate":"2025-12-01"},
			{"title":"second","url":"https://second.com","content":"snippet two"}
		]}`))
	}))
	defer srv.Close()
	s := &SearXNGBackend{InstanceURL: srv.URL}
	got, err := s.Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].PublishedAt != "2025-12-01" {
		t.Errorf("PublishedAt not propagated: %q", got[0].PublishedAt)
	}
	if got[0].Snippet != "snippet one" {
		t.Errorf("Snippet = %q", got[0].Snippet)
	}
}

func TestSearXNGBackend_RequiresInstanceURL(t *testing.T) {
	s := &SearXNGBackend{}
	_, err := s.Search(context.Background(), "q", 5)
	if err == nil {
		t.Fatal("expected error with no InstanceURL")
	}
}

// === DuckDuckGo HTML backend tests =========================================

func TestDuckDuckGoBackend_ParsesHTML(t *testing.T) {
	// Minimal SERP shape: <div class="result"> containing
	// <a class="result__a"> + <a class="result__snippet">.
	html := `<!DOCTYPE html><html><body>
		<div class="result">
			<h2><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa">Example A</a></h2>
			<a class="result__snippet" href="x">snippet A text</a>
		</div>
		<div class="result">
			<h2><a class="result__a" href="https://example.com/b">Example B</a></h2>
			<div class="result__snippet">snippet B text</div>
		</div>
		<div class="not-a-result"><a class="result__a" href="https://noise.com">noise</a></div>
	</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()
	d := &DuckDuckGoBackend{Endpoint: srv.URL}
	got, err := d.Search(context.Background(), "q", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("expected at least 2 results, got %d: %+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].URL, "https://example.com/a") {
		t.Errorf("uddg redirect not normalized: %q", got[0].URL)
	}
	if got[0].Title != "Example A" {
		t.Errorf("title not extracted: %q", got[0].Title)
	}
	if !strings.Contains(got[0].Snippet, "snippet A") {
		t.Errorf("snippet not extracted: %q", got[0].Snippet)
	}
}

func TestDuckDuckGoBackend_NoResultsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><p>no results</p></body></html>`))
	}))
	defer srv.Close()
	d := &DuckDuckGoBackend{Endpoint: srv.URL}
	_, err := d.Search(context.Background(), "q", 10)
	if err == nil {
		t.Fatal("expected parse error on empty SERP")
	}
}

func TestNormalizeDuckDuckGoURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://plain.com/path", "https://plain.com/path"},
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa", "https://example.com/a"},
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa&rut=abc", "https://example.com/a"},
	}
	for _, c := range cases {
		got := normalizeDuckDuckGoURL(c.in)
		if got != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// === Tool-level tests =======================================================

// fakeBackend is a hand-controlled SearchBackend used by tool-level
// tests so we don't have to spin up httptest for input validation.
type fakeBackend struct {
	name    string
	results []SearchResult
	err     error
}

func (f *fakeBackend) Name() string { return f.name }
func (f *fakeBackend) Search(_ context.Context, _ string, max int) ([]SearchResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	if max < len(f.results) {
		return f.results[:max], nil
	}
	return f.results, nil
}

func TestWebSearchTool_HappyPath(t *testing.T) {
	tool := &WebSearchTool{Backend: &fakeBackend{
		name: "fake",
		results: []SearchResult{
			{Title: "A", URL: "https://a", Snippet: "snippet"},
		},
	}}
	in, _ := json.Marshal(map[string]any{"query": "test"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	var resp webSearchOutput
	_ = json.Unmarshal(out, &resp)
	if resp.Backend != "fake" {
		t.Errorf("backend = %q, want fake", resp.Backend)
	}
	if len(resp.Results) != 1 {
		t.Errorf("results len = %d, want 1", len(resp.Results))
	}
}

func TestWebSearchTool_EmptyQueryErrors(t *testing.T) {
	tool := &WebSearchTool{Backend: &fakeBackend{name: "fake"}}
	in, _ := json.Marshal(map[string]any{"query": "   "})
	_, err := tool.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected error on empty query")
	}
}

func TestWebSearchTool_MaxResultsCap(t *testing.T) {
	results := make([]SearchResult, 30)
	for i := range results {
		results[i].Rank = i + 1
	}
	tool := &WebSearchTool{Backend: &fakeBackend{name: "fake", results: results}}
	in, _ := json.Marshal(map[string]any{"query": "q", "max_results": 100})
	out, _ := tool.Execute(context.Background(), in)
	var resp webSearchOutput
	_ = json.Unmarshal(out, &resp)
	if len(resp.Results) != maxWebSearchMaxResults {
		t.Errorf("len = %d, want hard cap %d", len(resp.Results), maxWebSearchMaxResults)
	}
}

func TestWebSearchTool_BackendErrorWraps(t *testing.T) {
	tool := &WebSearchTool{Backend: &fakeBackend{name: "brave", err: http.ErrServerClosed}}
	in, _ := json.Marshal(map[string]any{"query": "x"})
	_, err := tool.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected wrapped error")
	}
	if !strings.Contains(err.Error(), "brave") {
		t.Errorf("error missing backend name: %v", err)
	}
}

func TestWebSearchTool_NoBackend(t *testing.T) {
	tool := &WebSearchTool{}
	in, _ := json.Marshal(map[string]any{"query": "x"})
	_, err := tool.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected error with nil backend")
	}
}

// TestNewWebSearchTool_PicksBackendFromEnv pins the primary backend
// selection. Specialty backends (arxiv, wikipedia, github) are disabled
// here so we can read the primary name off tool.Backend directly — when
// they're enabled the default factory wraps everything in MultiBackend
// and Backend.Name() returns "multi". A separate test below covers
// that wrap.
func TestNewWebSearchTool_PicksBackendFromEnv(t *testing.T) {
	t.Setenv("CARLOS_DISABLE_ARXIV", "1")
	t.Setenv("CARLOS_DISABLE_WIKIPEDIA", "1")
	t.Setenv("CARLOS_DISABLE_GITHUB", "1")

	t.Setenv("BRAVE_API_KEY", "k")
	t.Setenv("SEARXNG_URL", "https://searx")
	tool := NewWebSearchTool()
	if tool.Backend.Name() != "brave" {
		t.Errorf("brave key set → backend = %q, want brave", tool.Backend.Name())
	}

	t.Setenv("BRAVE_API_KEY", "")
	tool = NewWebSearchTool()
	if tool.Backend.Name() != "searxng" {
		t.Errorf("searxng URL set → backend = %q, want searxng", tool.Backend.Name())
	}

	t.Setenv("SEARXNG_URL", "")
	tool = NewWebSearchTool()
	if tool.Backend.Name() != "duckduckgo" {
		t.Errorf("no env → backend = %q, want duckduckgo", tool.Backend.Name())
	}
}

// TestNewWebSearchTool_WrapsWithMultiByDefault confirms that with no
// env vars set, the factory wraps the primary in MultiBackend with
// arxiv + wikipedia (and optionally github if the gh CLI is on PATH)
// layered on. This is the default UX out of the box.
func TestNewWebSearchTool_WrapsWithMultiByDefault(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("SEARXNG_URL", "")
	t.Setenv("CARLOS_DISABLE_ARXIV", "")
	t.Setenv("CARLOS_DISABLE_WIKIPEDIA", "")
	// Force github off so the test outcome doesn't depend on whether
	// the test machine has `gh` installed.
	t.Setenv("CARLOS_DISABLE_GITHUB", "1")
	tool := NewWebSearchTool()
	multi, ok := tool.Backend.(*MultiBackend)
	if !ok {
		t.Fatalf("default factory should wrap with MultiBackend; got %T", tool.Backend)
	}
	names := multi.Names()
	if len(names) != 3 || names[0] != "duckduckgo" || names[1] != "arxiv" || names[2] != "wikipedia" {
		t.Errorf("default backend names = %v, want [duckduckgo arxiv wikipedia]", names)
	}
}

// TestNewWebSearchTool_DisableAuxRestoresSingleBackend is the byte-
// identical-to-v0.7.x escape hatch: power users who want only the
// primary backend can disable every specialty and the factory returns
// the bare backend (no Multi wrapper).
func TestNewWebSearchTool_DisableAuxRestoresSingleBackend(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("SEARXNG_URL", "")
	t.Setenv("CARLOS_DISABLE_ARXIV", "1")
	t.Setenv("CARLOS_DISABLE_WIKIPEDIA", "1")
	t.Setenv("CARLOS_DISABLE_GITHUB", "1")
	tool := NewWebSearchTool()
	if _, isMulti := tool.Backend.(*MultiBackend); isMulti {
		t.Fatal("all aux disabled → factory must return primary directly, no Multi wrapper")
	}
	if tool.Backend.Name() != "duckduckgo" {
		t.Errorf("primary = %q, want duckduckgo", tool.Backend.Name())
	}
}

// TestNewWebSearchTool_PartialAuxOptOut: only arxiv disabled, wikipedia
// still wraps. Confirms each toggle is independent.
func TestNewWebSearchTool_PartialAuxOptOut(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("SEARXNG_URL", "")
	t.Setenv("CARLOS_DISABLE_ARXIV", "1")
	t.Setenv("CARLOS_DISABLE_WIKIPEDIA", "")
	t.Setenv("CARLOS_DISABLE_GITHUB", "1")
	tool := NewWebSearchTool()
	multi, ok := tool.Backend.(*MultiBackend)
	if !ok {
		t.Fatalf("wikipedia still enabled → Multi expected; got %T", tool.Backend)
	}
	names := multi.Names()
	if len(names) != 2 || names[0] != "duckduckgo" || names[1] != "wikipedia" {
		t.Errorf("arxiv-disabled names = %v, want [duckduckgo wikipedia]", names)
	}
}
