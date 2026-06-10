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

// === Backend tests ==========================================================

func TestWikipediaBackend_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity-check the request shape.
		if r.URL.Query().Get("q") != "einstein" {
			t.Errorf("missing/incorrect q param: %q", r.URL.Query().Get("q"))
		}
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("missing/incorrect limit param: %q", r.URL.Query().Get("limit"))
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("missing Accept header")
		}
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("missing User-Agent header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"pages": [
				{"id": 736, "key": "Albert_Einstein", "title": "Albert Einstein",
				 "excerpt": "German-born theoretical physicist", "description": "physicist (1879-1955)"},
				{"id": 1, "key": "Einstein_(disambiguation)", "title": "Einstein (disambiguation)",
				 "excerpt": "may refer to", "description": "Wikimedia disambiguation page"}
			]
		}`))
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL, Lang: "en", UserAgent: "test-ua"}
	got, err := b.Search(context.Background(), "einstein", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got[0].Rank != 1 || got[1].Rank != 2 {
		t.Errorf("ranks not 1-indexed sequential: %+v", got)
	}
	if got[0].Title != "Albert Einstein" {
		t.Errorf("title = %q", got[0].Title)
	}
	if got[0].URL != "https://en.wikipedia.org/wiki/Albert_Einstein" {
		t.Errorf("URL = %q, want canonical wiki URL", got[0].URL)
	}
	if got[1].URL != "https://en.wikipedia.org/wiki/Einstein_%28disambiguation%29" {
		t.Errorf("URL key with parens not properly escaped: %q", got[1].URL)
	}
	for i, r := range got {
		if r.Source != "wikipedia" {
			t.Errorf("result[%d].Source = %q, want wikipedia", i, r.Source)
		}
	}
}

func TestWikipediaBackend_StripsSearchMatchSpans(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pages":[
			{"id":1,"key":"Go_(programming_language)","title":"Go",
			 "excerpt":"<span class=\"searchmatch\">Go</span> is a statically typed <span class=\"searchmatch\">language</span>",
			 "description":"programming language"}
		]}`))
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL}
	got, err := b.Search(context.Background(), "go", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	if strings.Contains(got[0].Snippet, "<span") || strings.Contains(got[0].Snippet, "searchmatch") {
		t.Errorf("snippet still has span markup: %q", got[0].Snippet)
	}
	if got[0].Snippet != "Go is a statically typed language" {
		t.Errorf("snippet = %q, want inner text only", got[0].Snippet)
	}
}

func TestWikipediaBackend_FallsBackToDescription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pages":[
			{"id":1,"key":"Foo","title":"Foo","excerpt":"","description":"a placeholder name"}
		]}`))
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL}
	got, err := b.Search(context.Background(), "foo", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].Snippet != "a placeholder name" {
		t.Errorf("Snippet = %q, want fallback to description", got[0].Snippet)
	}
}

func TestWikipediaBackend_EmptyPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pages":[]}`))
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL}
	got, err := b.Search(context.Background(), "qzxyabcnomatch", 10)
	if err != nil {
		t.Fatalf("expected nil err on empty results, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d results", len(got))
	}
}

func TestWikipediaBackend_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL}
	_, err := b.Search(context.Background(), "q", 1)
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "wikipedia") {
		t.Errorf("error missing backend name: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error missing status code: %v", err)
	}
}

func TestWikipediaBackend_HTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL}
	_, err := b.Search(context.Background(), "q", 1)
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error missing status code: %v", err)
	}
}

func TestWikipediaBackend_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pages": [not json}`))
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL}
	_, err := b.Search(context.Background(), "q", 1)
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestWikipediaBackend_ContextCancellation(t *testing.T) {
	// Server hangs forever; the canceled ctx should short-circuit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	b := &WikipediaBackend{Endpoint: srv.URL}
	_, err := b.Search(ctx, "q", 1)
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if ctx.Err() == nil {
		t.Errorf("expected ctx to be done, ctx.Err()=%v", ctx.Err())
	}
}

func TestWikipediaBackend_MaxZero(t *testing.T) {
	// Server should not even be hit; but guard against accidental calls.
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{"pages":[]}`))
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL}
	got, err := b.Search(context.Background(), "q", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d results, want 0", len(got))
	}
	if hit {
		t.Errorf("max=0 should short-circuit without hitting the server")
	}
}

func TestWikipediaBackend_MaxCaps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pages":[
			{"id":1,"key":"A","title":"A","excerpt":"a"},
			{"id":2,"key":"B","title":"B","excerpt":"b"},
			{"id":3,"key":"C","title":"C","excerpt":"c"},
			{"id":4,"key":"D","title":"D","excerpt":"d"},
			{"id":5,"key":"E","title":"E","excerpt":"e"}
		]}`))
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL}
	got, err := b.Search(context.Background(), "q", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("len=%d, want 2 (capped)", len(got))
	}
}

func TestWikipediaBackend_KeyWithSpacesURLEncoded(t *testing.T) {
	// Hypothetical: if a key were to contain a space (unusual but
	// possible for redirects/etc), the URL must be escaped.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pages":[
			{"id":1,"key":"New York City","title":"New York City","excerpt":"big apple"}
		]}`))
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL}
	got, err := b.Search(context.Background(), "nyc", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	if strings.Contains(got[0].URL, " ") {
		t.Errorf("raw space leaked into URL: %q", got[0].URL)
	}
	if !strings.Contains(got[0].URL, "New%20York%20City") {
		t.Errorf("spaces not percent-escaped: %q", got[0].URL)
	}
}

func TestWikipediaBackend_DefaultLang(t *testing.T) {
	// Endpoint empty + Lang empty → backend should build the "en"
	// URL. We can't easily hit the live API in a unit test, so just
	// verify the construction by intercepting via a transport.
	// Instead: when Endpoint is set, Lang is still used to build
	// the result URL prefix - check that an empty Lang defaults to
	// "en" in the synthesized result URLs.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pages":[{"id":1,"key":"X","title":"X","excerpt":"x"}]}`))
	}))
	defer srv.Close()

	b := &WikipediaBackend{Endpoint: srv.URL} // Lang intentionally blank
	got, err := b.Search(context.Background(), "x", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	if !strings.HasPrefix(got[0].URL, "https://en.wikipedia.org/wiki/") {
		t.Errorf("default lang not en: %q", got[0].URL)
	}
}

func TestStripSearchMatchSpans(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"plain text", "plain text"},
		{`<span class="searchmatch">match</span>ing text`, "matching text"},
		{`a <span class="searchmatch">b</span> c <span class="searchmatch">d</span> e`, "a b c d e"},
		{"  whitespace  ", "whitespace"},
	}
	for _, c := range cases {
		got := stripSearchMatchSpans(c.in)
		if got != c.want {
			t.Errorf("stripSearchMatchSpans(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// === Tool-level tests =======================================================

func TestWikipediaSearchTool_Identity(t *testing.T) {
	tool := NewWikipediaSearchTool()
	if tool.Name() != "wikipedia_search" {
		t.Errorf("Name = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description is empty")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("Schema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v", schema["type"])
	}
}

func TestWikipediaSearchTool_ExecuteHappy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"pages":[
			{"id":1,"key":"Foo","title":"Foo","excerpt":"the foo article","description":"d"},
			{"id":2,"key":"Bar","title":"Bar","excerpt":"the bar article","description":"d"}
		]}`))
	}))
	defer srv.Close()

	tool := &WikipediaSearchTool{
		Backend: &WikipediaBackend{Endpoint: srv.URL, Lang: "en"},
	}
	in, _ := json.Marshal(map[string]any{"query": "foo", "max_results": 5})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var resp wikipediaSearchOutput
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Backend != "wikipedia" {
		t.Errorf("Backend = %q", resp.Backend)
	}
	if resp.Query != "foo" {
		t.Errorf("Query = %q", resp.Query)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("Results len = %d", len(resp.Results))
	}
	if resp.Results[0].Title != "Foo" {
		t.Errorf("Results[0].Title = %q", resp.Results[0].Title)
	}
	if resp.Results[0].Source != "wikipedia" {
		t.Errorf("Results[0].Source = %q", resp.Results[0].Source)
	}
}

func TestWikipediaSearchTool_EmptyQuery(t *testing.T) {
	tool := NewWikipediaSearchTool()
	in, _ := json.Marshal(map[string]any{"query": "   "})
	_, err := tool.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected error on empty query")
	}
}

func TestWikipediaSearchTool_BadInputJSON(t *testing.T) {
	tool := NewWikipediaSearchTool()
	_, err := tool.Execute(context.Background(), []byte("not json"))
	if err == nil {
		t.Fatal("expected error on malformed input JSON")
	}
}

func TestWikipediaSearchTool_MaxResultsCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Tool should clamp max_results to maxWikipediaSearchMaxResults
		// before forwarding to the backend.
		if r.URL.Query().Get("limit") != "20" {
			t.Errorf("limit = %q, want 20 (clamped)", r.URL.Query().Get("limit"))
		}
		_, _ = w.Write([]byte(`{"pages":[]}`))
	}))
	defer srv.Close()

	tool := &WikipediaSearchTool{
		Backend: &WikipediaBackend{Endpoint: srv.URL},
	}
	in, _ := json.Marshal(map[string]any{"query": "q", "max_results": 999})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestWikipediaSearchTool_DefaultMaxResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("limit = %q, want 10 (default)", r.URL.Query().Get("limit"))
		}
		_, _ = w.Write([]byte(`{"pages":[]}`))
	}))
	defer srv.Close()

	tool := &WikipediaSearchTool{
		Backend: &WikipediaBackend{Endpoint: srv.URL},
	}
	in, _ := json.Marshal(map[string]any{"query": "q"})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}
