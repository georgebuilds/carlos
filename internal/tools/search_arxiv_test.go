package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// canned Atom XML payloads used across the happy-path tests below.
// arxiv responses are namespaced under xmlns="http://www.w3.org/2005/Atom"
// but encoding/xml is namespace-tolerant when no prefix is requested,
// so the canned bodies stay legible.

const arxivAtomTwo = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <title>Attention Is All You Need</title>
    <id>http://arxiv.org/abs/1706.03762v5</id>
    <summary>The dominant sequence transduction models are based on complex recurrent or convolutional neural networks that include an encoder and a decoder.</summary>
    <published>2017-06-12T17:57:34Z</published>
    <author><name>Ashish Vaswani</name></author>
  </entry>
  <entry>
    <title>BERT: Pre-training of Deep Bidirectional Transformers</title>
    <id>http://arxiv.org/abs/1810.04805v2</id>
    <summary>We introduce a new language representation model called BERT.</summary>
    <published>2018-10-11T00:50:01Z</published>
    <author><name>Jacob Devlin</name></author>
  </entry>
</feed>`

const arxivAtomEmpty = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>arXiv Query: search_query=all:asdfqwerty</title>
</feed>`

// arxivAtomFive builds an Atom feed with n entries for cap tests.
func arxivAtomN(n int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">` + "\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<entry>
  <title>Paper %d</title>
  <id>http://arxiv.org/abs/2024.%04dv1</id>
  <summary>Summary %d</summary>
  <published>2024-01-01T00:00:00Z</published>
</entry>`, i+1, i+1, i+1)
	}
	b.WriteString(`</feed>`)
	return b.String()
}

// newTestBackend wires an ArxivBackend whose Endpoint points at srv and
// whose rate-limit interval is essentially zero so the per-test
// scheduling isn't slowed by the 3s production guideline.
func newTestBackend(srv *httptest.Server) *ArxivBackend {
	return &ArxivBackend{
		Endpoint:    srv.URL,
		UserAgent:   "carlos-test",
		MinInterval: 0, // disable inter-call wait for non-rate-limit tests
	}
}

func TestArxivBackend_Name(t *testing.T) {
	a := NewArxivBackend()
	if a.Name() != "arxiv" {
		t.Errorf("Name() = %q, want arxiv", a.Name())
	}
}

func TestArxivBackend_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity-check the outgoing query shape.
		q := r.URL.Query()
		if q.Get("search_query") != "all:transformers" {
			t.Errorf("search_query = %q, want all:transformers", q.Get("search_query"))
		}
		if q.Get("sortBy") != "relevance" {
			t.Errorf("sortBy = %q, want relevance", q.Get("sortBy"))
		}
		if q.Get("sortOrder") != "descending" {
			t.Errorf("sortOrder = %q, want descending", q.Get("sortOrder"))
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(arxivAtomTwo))
	}))
	defer srv.Close()

	a := newTestBackend(srv)
	got, err := a.Search(context.Background(), "transformers", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Rank != 1 || got[1].Rank != 2 {
		t.Errorf("ranks not 1-indexed: %+v", got)
	}
	if got[0].Title != "Attention Is All You Need" {
		t.Errorf("Title = %q", got[0].Title)
	}
	if got[0].URL != "http://arxiv.org/abs/1706.03762v5" {
		t.Errorf("URL = %q", got[0].URL)
	}
	if !strings.Contains(got[0].Snippet, "dominant sequence transduction") {
		t.Errorf("Snippet missing expected content: %q", got[0].Snippet)
	}
	if got[0].PublishedAt != "2017-06-12T17:57:34Z" {
		t.Errorf("PublishedAt = %q", got[0].PublishedAt)
	}
	if got[0].Source != "arxiv" {
		t.Errorf("Source = %q, want arxiv", got[0].Source)
	}
}

func TestArxivBackend_EmptyFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(arxivAtomEmpty))
	}))
	defer srv.Close()
	a := newTestBackend(srv)
	got, err := a.Search(context.Background(), "asdfqwerty", 10)
	if err != nil {
		t.Fatalf("Search returned error on empty feed: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty results, got %d", len(got))
	}
}

func TestArxivBackend_HTTP429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	a := newTestBackend(srv)
	_, err := a.Search(context.Background(), "x", 1)
	if err == nil {
		t.Fatal("expected error on 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error missing 429: %v", err)
	}
}

func TestArxivBackend_HTTP503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	a := newTestBackend(srv)
	_, err := a.Search(context.Background(), "x", 1)
	if err == nil {
		t.Fatal("expected error on 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error missing 503: %v", err)
	}
}

func TestArxivBackend_MalformedXML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<feed><entry><title>unterminated`))
	}))
	defer srv.Close()
	a := newTestBackend(srv)
	_, err := a.Search(context.Background(), "x", 1)
	if err == nil {
		t.Fatal("expected XML parse error")
	}
}

func TestArxivBackend_ContextCancellation(t *testing.T) {
	// Server intentionally sleeps; client ctx cancels mid-flight.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			_, _ = w.Write([]byte(arxivAtomTwo))
		}
	}))
	defer srv.Close()

	a := newTestBackend(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := a.Search(ctx, "x", 1)
	if err == nil {
		t.Fatal("expected ctx cancellation error")
	}
	// Either ctx.Err() bubbled directly or the http client wrapped it -
	// either way the error chain should mention deadline/canceled.
	msg := err.Error()
	if !strings.Contains(msg, "deadline") && !strings.Contains(msg, "canceled") && !strings.Contains(msg, "context") {
		t.Errorf("error doesn't look like ctx cancellation: %v", err)
	}
}

func TestArxivBackend_MaxZeroReturnsEmpty(t *testing.T) {
	// Server should not even be hit when max=0; harden the contract by
	// failing if we do see a request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("server unexpectedly hit on max=0")
		_, _ = w.Write([]byte(arxivAtomEmpty))
	}))
	defer srv.Close()
	a := newTestBackend(srv)
	got, err := a.Search(context.Background(), "x", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("max=0 returned %d results", len(got))
	}
}

func TestArxivBackend_CapsToMax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(arxivAtomN(5)))
	}))
	defer srv.Close()
	a := newTestBackend(srv)
	got, err := a.Search(context.Background(), "x", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("max=3 returned %d results", len(got))
	}
	// Sequential 1-indexed ranks.
	for i, r := range got {
		if r.Rank != i+1 {
			t.Errorf("got[%d].Rank = %d, want %d", i, r.Rank, i+1)
		}
	}
}

func TestArxivBackend_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(arxivAtomEmpty))
	}))
	defer srv.Close()
	a := &ArxivBackend{
		Endpoint:    srv.URL,
		UserAgent:   "carlos-test",
		MinInterval: 50 * time.Millisecond,
	}
	// First call goes through immediately (lastCallAt zero value).
	if _, err := a.Search(context.Background(), "first", 1); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call must wait at least MinInterval before contacting the
	// server. We allow a little scheduler slack on the lower bound.
	start := time.Now()
	if _, err := a.Search(context.Background(), "second", 1); err != nil {
		t.Fatalf("second call: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Errorf("second call took %v, expected >= 40ms (MinInterval=50ms)", elapsed)
	}
}

func TestArxivBackend_TitleNewlinesNormalized(t *testing.T) {
	// arxiv routinely wraps long titles across multiple lines with
	// leading whitespace. Make sure we collapse them.
	xmlBody := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <title>A Very Long
    Wrapped
    Title</title>
    <id>http://arxiv.org/abs/9999.99999v1</id>
    <summary>short summary</summary>
    <published>2024-01-01T00:00:00Z</published>
  </entry>
</feed>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(xmlBody))
	}))
	defer srv.Close()
	a := newTestBackend(srv)
	got, err := a.Search(context.Background(), "x", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if strings.ContainsAny(got[0].Title, "\n\t") {
		t.Errorf("title contains raw whitespace: %q", got[0].Title)
	}
	if got[0].Title != "A Very Long Wrapped Title" {
		t.Errorf("title = %q, want collapsed single-line", got[0].Title)
	}
}

func TestArxivBackend_SnippetTruncation(t *testing.T) {
	// Build a summary >> 280 chars so we exercise truncation +
	// ellipsis suffix.
	longSummary := strings.Repeat("abcdefghij ", 40) // 440 chars
	xmlBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <title>X</title>
    <id>http://arxiv.org/abs/9999.99998v1</id>
    <summary>%s</summary>
    <published>2024-01-01T00:00:00Z</published>
  </entry>
</feed>`, longSummary)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(xmlBody))
	}))
	defer srv.Close()
	a := newTestBackend(srv)
	got, err := a.Search(context.Background(), "x", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if !strings.HasSuffix(got[0].Snippet, "…") {
		t.Errorf("truncated snippet should end with ellipsis: %q", got[0].Snippet)
	}
	runeCount := len([]rune(got[0].Snippet))
	if runeCount > arxivSnippetMaxRunes+1 {
		t.Errorf("snippet rune count = %d, want <= %d", runeCount, arxivSnippetMaxRunes+1)
	}
}

// === ArxivSearchTool tests =================================================

// stubBackend is a hand-controlled SearchBackend used to test the tool
// surface without spinning up httptest.
type stubBackend struct {
	name    string
	results []SearchResult
	err     error

	gotQuery string
	gotMax   int
}

func (s *stubBackend) Name() string { return s.name }
func (s *stubBackend) Search(_ context.Context, q string, max int) ([]SearchResult, error) {
	s.gotQuery = q
	s.gotMax = max
	if s.err != nil {
		return nil, s.err
	}
	if max < len(s.results) {
		return s.results[:max], nil
	}
	return s.results, nil
}

func TestArxivSearchTool_NameAndDescription(t *testing.T) {
	tool := NewArxivSearchTool()
	if tool.Name() != "arxiv_search" {
		t.Errorf("Name = %q, want arxiv_search", tool.Name())
	}
	if !strings.Contains(tool.Description(), "arxiv") {
		t.Errorf("Description missing 'arxiv': %q", tool.Description())
	}
	if len(tool.Schema()) == 0 {
		t.Error("Schema returned empty")
	}
	// Schema must parse as JSON.
	var anyMap map[string]any
	if err := json.Unmarshal(tool.Schema(), &anyMap); err != nil {
		t.Errorf("Schema not valid JSON: %v", err)
	}
}

func TestArxivSearchTool_ExecuteHappyPath(t *testing.T) {
	stub := &stubBackend{
		name: "arxiv",
		results: []SearchResult{
			{Rank: 1, Title: "Paper A", URL: "http://arxiv.org/abs/1", Snippet: "abstract", Source: "arxiv"},
		},
	}
	tool := &ArxivSearchTool{Backend: stub}
	in, _ := json.Marshal(map[string]any{"query": "test", "max_results": 5})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	var resp arxivSearchOutput
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if resp.Backend != "arxiv" {
		t.Errorf("backend = %q, want arxiv", resp.Backend)
	}
	if resp.Query != "test" {
		t.Errorf("query = %q, want test", resp.Query)
	}
	if len(resp.Results) != 1 {
		t.Errorf("results len = %d, want 1", len(resp.Results))
	}
	if stub.gotMax != 5 {
		t.Errorf("max passed to backend = %d, want 5", stub.gotMax)
	}
}

func TestArxivSearchTool_DefaultMaxResults(t *testing.T) {
	stub := &stubBackend{name: "arxiv"}
	tool := &ArxivSearchTool{Backend: stub}
	in, _ := json.Marshal(map[string]any{"query": "x"})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if stub.gotMax != defaultArxivSearchMaxResults {
		t.Errorf("default max = %d, want %d", stub.gotMax, defaultArxivSearchMaxResults)
	}
}

func TestArxivSearchTool_MaxResultsCapped(t *testing.T) {
	stub := &stubBackend{name: "arxiv"}
	tool := &ArxivSearchTool{Backend: stub}
	in, _ := json.Marshal(map[string]any{"query": "x", "max_results": 999})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if stub.gotMax != maxArxivSearchMaxResults {
		t.Errorf("capped max = %d, want %d", stub.gotMax, maxArxivSearchMaxResults)
	}
}

func TestArxivSearchTool_EmptyQueryErrors(t *testing.T) {
	tool := &ArxivSearchTool{Backend: &stubBackend{name: "arxiv"}}
	in, _ := json.Marshal(map[string]any{"query": "   "})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error on empty query")
	}
}

func TestArxivSearchTool_NoBackend(t *testing.T) {
	tool := &ArxivSearchTool{}
	in, _ := json.Marshal(map[string]any{"query": "x"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error with nil backend")
	}
}

func TestArxivSearchTool_BadJSON(t *testing.T) {
	tool := &ArxivSearchTool{Backend: &stubBackend{name: "arxiv"}}
	if _, err := tool.Execute(context.Background(), []byte("not json")); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestArxivSearchTool_BackendErrorWraps(t *testing.T) {
	tool := &ArxivSearchTool{Backend: &stubBackend{name: "arxiv", err: fmt.Errorf("boom")}}
	in, _ := json.Marshal(map[string]any{"query": "x"})
	_, err := tool.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected wrapped error")
	}
	if !strings.Contains(err.Error(), "arxiv") {
		t.Errorf("wrapped error missing backend name: %v", err)
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in    string
		n     int
		want  string
		runes int
	}{
		{"short", 100, "short", 5},
		{"", 100, "", 0},
		{"hello", 0, "", 0},
		{"hello world", 5, "hello…", 6},
		// Multi-byte: each rune is one position, no slicing mid-codepoint.
		{"héllo wörld", 6, "héllo …", 7},
	}
	for _, c := range cases {
		got := truncateRunes(c.in, c.n)
		if got != c.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if r := len([]rune(got)); r != c.runes {
			t.Errorf("truncateRunes(%q, %d) rune count = %d, want %d", c.in, c.n, r, c.runes)
		}
	}
}
