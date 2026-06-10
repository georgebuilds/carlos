package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newWebFetchToolForHost rebuilds a WebFetchTool that points at the
// given httptest.Server. We override AllowPrivate=true because the
// httptest server binds on 127.0.0.1 which the private-address guard
// would otherwise refuse.
//
// The injected RetryPolicy uses a 1ms BaseDelay, zero jitter, and a
// generous MaxTotalWait so tests can exercise retry behaviour without
// burning real wall-clock time on the shared default's exponential
// schedule.
func newWebFetchToolForHost(t *testing.T) *WebFetchTool {
	t.Helper()
	return &WebFetchTool{
		AllowPrivate: true,
		Timeout:      3 * time.Second,
		RetryPolicy:  fastTestRetryPolicy(),
	}
}

// fastTestRetryPolicy returns a retry policy that mirrors the default
// (4 attempts, retryable status set) but with negligible inter-attempt
// waits. Used by every test in this file that touches a 5xx / 429 path
// so the retry loop doesn't dominate test runtime.
func fastTestRetryPolicy() *retryPolicy {
	return &retryPolicy{
		MaxAttempts:  4,
		BaseDelay:    1 * time.Millisecond,
		MaxDelay:     5 * time.Millisecond,
		MaxTotalWait: 1 * time.Second,
		JitterFrac:   0,
	}
}

// rewriteToServerURL takes a logical URL the model might pass (e.g.
// "http://example.com/page") and rewrites the host to point at srv.URL
// so the test can intercept the request. Implementation trick: we
// install a custom http.Transport on the tool's client that swaps the
// host before dispatch.
func (t *WebFetchTool) routeViaTestServer(srv *httptest.Server) {
	su, _ := url.Parse(srv.URL)
	t.Client = &http.Client{
		Timeout: 3 * time.Second,
		Transport: &rewritingTransport{toScheme: su.Scheme, toHost: su.Host, base: srv.Client().Transport},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= defaultWebFetchMaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

type rewritingTransport struct {
	toScheme string
	toHost   string
	base     http.RoundTripper
}

func (r *rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = r.toScheme
	req.URL.Host = r.toHost
	if req.Host != "" {
		req.Host = r.toHost
	}
	base := r.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func TestWebFetch_HappyPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Hello World</title></head>
		<body>
		<nav>nav garbage</nav>
		<main><p>The quick brown fox.</p><p>Jumps over the lazy dog.</p></main>
		<footer>footer chrome</footer>
		<script>var x = 1;</script>
		</body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	in := fmt.Sprintf(`{"url":%q}`, srv.URL+"/page")
	out, err := tool.Execute(context.Background(), []byte(in))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res webFetchResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if res.Title != "Hello World" {
		t.Errorf("title = %q, want %q", res.Title, "Hello World")
	}
	if !strings.Contains(res.Content, "quick brown fox") {
		t.Errorf("content missing main text: %q", res.Content)
	}
	if !strings.Contains(res.Content, "lazy dog") {
		t.Errorf("content missing 2nd paragraph: %q", res.Content)
	}
	if strings.Contains(res.Content, "nav garbage") {
		t.Errorf("nav not stripped: %q", res.Content)
	}
	if strings.Contains(res.Content, "footer chrome") {
		t.Errorf("footer not stripped: %q", res.Content)
	}
	if strings.Contains(res.Content, "var x") {
		t.Errorf("script not stripped: %q", res.Content)
	}
	if res.FetchedAt == "" {
		t.Errorf("fetched_at empty")
	}
}

func TestWebFetch_NonTextContentRefused(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG\r\n\x1a\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	_, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/img")))
	if err == nil {
		t.Fatal("expected non-text refusal")
	}
	if !strings.Contains(err.Error(), "non-text content") {
		t.Errorf("error should mention non-text: %v", err)
	}
}

func TestWebFetch_OversizedBodyTruncated(t *testing.T) {
	// Use a small cap so the test runs fast.
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		// Body is 100 KiB of "a"; cap will be 10 KiB.
		w.Write(bytes.Repeat([]byte("a"), 100*1024))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.MaxBodyBytes = 10 * 1024
	tool.MaxTextBytes = 5 * 1024
	tool.routeViaTestServer(srv)
	out, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/big")))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res webFetchResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(res.Content, "extracted text truncated") {
		t.Errorf("expected text-truncation marker, got: %s", res.Content)
	}
	// Body cap kicks in too — original body was 100 KiB, body cap 10 KiB.
	if len(res.Content) > 5*1024+200 { // text cap + truncation marker
		t.Errorf("content not truncated to text cap: len=%d", len(res.Content))
	}
}

func TestWebFetch_RedirectTracked(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/mid", http.StatusFound)
	})
	mux.HandleFunc("/mid", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/end", http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><head><title>End</title></head><body><p>landed</p></body></html>")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	out, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/start")))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res webFetchResult
	json.Unmarshal(out, &res)
	if !strings.HasSuffix(res.FinalURL, "/end") {
		t.Errorf("final_url = %q, want suffix /end", res.FinalURL)
	}
	if !strings.Contains(res.Content, "landed") {
		t.Errorf("did not follow to final body: %q", res.Content)
	}
}

func TestWebFetch_RobotsDisallowedByDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n")
	})
	mux.HandleFunc("/private/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body>secret</body></html>")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	_, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/private/page")))
	if err == nil {
		t.Fatal("expected robots-disallowed refusal")
	}
	if !strings.Contains(err.Error(), "robots.txt") {
		t.Errorf("error should mention robots.txt: %v", err)
	}
}

func TestWebFetch_RobotsBypassWhenOptedOut(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "User-agent: *\nDisallow: /\n")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><head><title>OK</title></head><body><p>got through</p></body></html>")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	body := fmt.Sprintf(`{"url":%q,"respect_robots":false}`, srv.URL+"/page")
	out, err := tool.Execute(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(string(out), "got through") {
		t.Errorf("opt-out path failed: %s", out)
	}
}

func TestWebFetch_PrivateAddressRefusedByDefault(t *testing.T) {
	tool := NewWebFetchTool() // AllowPrivate = false
	_, err := tool.Execute(context.Background(),
		[]byte(`{"url":"http://127.0.0.1:9/never"}`))
	if err == nil {
		t.Fatal("expected private-address refusal")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("error should mention refusal: %v", err)
	}
}

func TestWebFetch_LocalhostHostnameRefused(t *testing.T) {
	tool := NewWebFetchTool()
	_, err := tool.Execute(context.Background(),
		[]byte(`{"url":"http://localhost/foo"}`))
	if err == nil {
		t.Fatal("expected localhost refusal")
	}
}

func TestWebFetch_BogusURL(t *testing.T) {
	tool := NewWebFetchTool()
	cases := []struct {
		name string
		body string
	}{
		{"empty", `{"url":""}`},
		{"no scheme", `{"url":"example.com/foo"}`},
		{"ftp scheme", `{"url":"ftp://example.com/file"}`},
		{"file scheme", `{"url":"file:///etc/passwd"}`},
		{"bad json", `not json`},
		{"missing url", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), []byte(tc.body))
			if err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestWebFetch_5xxStatusError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(503)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	_, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/dead")))
	if err == nil {
		t.Fatal("expected 503 error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestWebFetch_TimeoutError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.Timeout = 200 * time.Millisecond
	tool.routeViaTestServer(srv)
	// Override the client timeout the route helper installed.
	tool.Client.Timeout = 200 * time.Millisecond
	_, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/slow")))
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWebFetch_JSOnlyHint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// 8 KiB of script with no visible text → triggers the JS hint.
		fmt.Fprint(w, `<html><head><title></title></head><body><div id="root"></div><script>`)
		w.Write(bytes.Repeat([]byte("/* heavy js */ "), 600))
		fmt.Fprint(w, `</script></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	out, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/spa")))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(string(out), "JavaScript") {
		t.Errorf("expected JS-only hint, got: %s", out)
	}
}

func TestWebFetch_SchemaIsValidJSON(t *testing.T) {
	s := NewWebFetchTool().Schema()
	var v any
	if err := json.Unmarshal(s, &v); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	for _, field := range []string{`"url"`, `"urls"`, `"respect_robots"`} {
		if !strings.Contains(string(s), field) {
			t.Errorf("schema missing field %s: %s", field, s)
		}
	}
}

func TestWebFetch_DescriptionMentionsBatch(t *testing.T) {
	d := NewWebFetchTool().Description()
	// The model only picks the batched shape if the description states it
	// plainly; keep this assertion tight so refactors don't drop the
	// hint and silently regress orchestration efficiency.
	for _, frag := range []string{"multiple URLs", "up to 3"} {
		if !strings.Contains(d, frag) {
			t.Errorf("description missing %q: %s", frag, d)
		}
	}
}

func TestWebFetch_ExtractText_TableAndList(t *testing.T) {
	body := []byte(`<html><body>
		<h1>Title One</h1>
		<ul><li>alpha</li><li>beta</li></ul>
		<p>Some prose.</p>
		<table><tr><td>cell1</td><td>cell2</td></tr></table>
	</body></html>`)
	title, text := extractHTML(body)
	if title != "" {
		t.Errorf("title should be empty (no <title> tag), got %q", title)
	}
	for _, frag := range []string{"Title One", "alpha", "beta", "Some prose", "cell1", "cell2"} {
		if !strings.Contains(text, frag) {
			t.Errorf("extracted text missing %q: %q", frag, text)
		}
	}
}

func TestWebFetch_NormalizeWhitespace(t *testing.T) {
	in := "  hello   world\n\n\n  next  para  "
	got := normalizeWhitespace(in)
	want := "hello world\n\nnext para"
	if got != want {
		t.Errorf("normalize: got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------
// Batched (multi-URL) mode tests
// ---------------------------------------------------------------------

// TestWebFetch_BatchedHappyPath verifies the new `urls` shape returns a
// per-URL block keyed by the input URL for a 3-URL same-host batch.
func TestWebFetch_BatchedHappyPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>A Page</title></head><body><p>alpha body</p></body></html>`)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>B Page</title></head><body><p>bravo body</p></body></html>`)
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>C Page</title></head><body><p>charlie body</p></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	body := fmt.Sprintf(`{"urls":[%q,%q,%q]}`, srv.URL+"/a", srv.URL+"/b", srv.URL+"/c")
	out, err := tool.Execute(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res webFetchBatchResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(res.Results) != 3 {
		t.Fatalf("expected 3 result blocks, got %d: %s", len(res.Results), out)
	}
	// Per-block assertions, in input order.
	wants := []struct {
		urlSuffix string
		title     string
		bodyFrag  string
	}{
		{"/a", "A Page", "alpha body"},
		{"/b", "B Page", "bravo body"},
		{"/c", "C Page", "charlie body"},
	}
	for i, w := range wants {
		got := res.Results[i]
		if got.Error != "" {
			t.Errorf("results[%d] unexpected error: %s", i, got.Error)
		}
		if !strings.HasSuffix(got.URL, w.urlSuffix) {
			t.Errorf("results[%d].url = %q, want suffix %q", i, got.URL, w.urlSuffix)
		}
		if got.Title != w.title {
			t.Errorf("results[%d].title = %q, want %q", i, got.Title, w.title)
		}
		if !strings.Contains(got.Content, w.bodyFrag) {
			t.Errorf("results[%d].content missing %q: %q", i, w.bodyFrag, got.Content)
		}
		if got.FetchedAt == "" {
			t.Errorf("results[%d].fetched_at empty", i)
		}
	}
}

// TestWebFetch_BatchedValidation covers the three error paths around
// the `url` / `urls` mutual-exclusion and batch-size cap.
func TestWebFetch_BatchedValidation(t *testing.T) {
	tool := newWebFetchToolForHost(t)
	cases := []struct {
		name       string
		body       string
		errFrag    string
	}{
		{
			name:    "both url and urls set",
			body:    `{"url":"http://example.com/a","urls":["http://example.com/b"]}`,
			errFrag: "either",
		},
		{
			name:    "empty urls array",
			body:    `{"urls":[]}`,
			errFrag: "missing url",
		},
		{
			name:    "too many urls",
			body:    `{"urls":["http://a/","http://b/","http://c/","http://d/"]}`,
			errFrag: "at most 3",
		},
		{
			name:    "urls contains empty string",
			body:    `{"urls":["http://a/",""]}`,
			errFrag: "urls[1]",
		},
		{
			name:    "neither url nor urls",
			body:    `{}`,
			errFrag: "missing url",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), []byte(tc.body))
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.errFrag) {
				t.Errorf("error %q missing fragment %q", err, tc.errFrag)
			}
		})
	}
}

// TestWebFetch_BatchedPerURLCapReduced asserts the per-URL extracted-text
// cap drops to defaultWebFetchBatchedMaxTextBytes (64 KiB) when batched,
// keeping the combined response under the single-URL budget.
func TestWebFetch_BatchedPerURLCapReduced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		// 200 KiB of "x" - exceeds the 64 KiB batched cap but well under
		// the 256 KiB single-URL cap and the 5 MiB body cap.
		w.Write(bytes.Repeat([]byte("x"), 200*1024))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Use the production cap defaults (not the test-shrunk values some
	// other tests pin). Leaving MaxTextBytes==0 means batchedModeMaxText
	// returns defaultWebFetchBatchedMaxTextBytes.
	tool := &WebFetchTool{
		AllowPrivate: true,
		Timeout:      3 * time.Second,
		RetryPolicy:  fastTestRetryPolicy(),
	}
	body := fmt.Sprintf(`{"urls":[%q]}`, srv.URL+"/big")
	out, err := tool.Execute(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res webFetchBatchResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(res.Results))
	}
	entry := res.Results[0]
	if entry.Error != "" {
		t.Fatalf("unexpected error block: %s", entry.Error)
	}
	if !strings.Contains(entry.Content, "extracted text truncated") {
		t.Errorf("expected truncation marker (batched cap should fire), got len=%d", len(entry.Content))
	}
	// Sanity: content should sit at ~64 KiB, not 200 KiB.
	if len(entry.Content) > defaultWebFetchBatchedMaxTextBytes+256 {
		t.Errorf("content not capped at batched limit: len=%d > %d", len(entry.Content), defaultWebFetchBatchedMaxTextBytes+256)
	}
}

// TestWebFetch_BatchedSameHostSerialized records the per-request arrival
// times for two URLs on one host and asserts the inter-request gap is
// at least webFetchSameHostDelay - 50ms (small jitter window).
func TestWebFetch_BatchedSameHostSerialized(t *testing.T) {
	var (
		mu     sync.Mutex
		stamps []time.Time
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/p", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><p>p</p></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	body := fmt.Sprintf(`{"urls":[%q,%q]}`, srv.URL+"/p?one", srv.URL+"/p?two")
	start := time.Now()
	out, err := tool.Execute(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	total := time.Since(start)
	var res webFetchBatchResult
	json.Unmarshal(out, &res)
	if len(res.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(res.Results))
	}
	if total < webFetchSameHostDelay-50*time.Millisecond {
		t.Errorf("total time %v < expected same-host delay %v; serialization not enforced", total, webFetchSameHostDelay)
	}
	// The handler records its arrival time; we filter to GET requests
	// because robots.txt and HEAD pre-checks also hit but on /robots.txt.
	mu.Lock()
	defer mu.Unlock()
	if len(stamps) < 2 {
		t.Fatalf("want >=2 recorded arrivals, got %d", len(stamps))
	}
	// Gap between the first and last recorded arrival should be at
	// least the courtesy delay (minus a small allowance for jitter).
	gap := stamps[len(stamps)-1].Sub(stamps[0])
	if gap < webFetchSameHostDelay-50*time.Millisecond {
		t.Errorf("inter-request gap %v < expected %v", gap, webFetchSameHostDelay)
	}
}

// TestWebFetch_BatchedCrossHostParallel asserts that URLs on different
// hosts dispatch concurrently. Each server stalls `slowGetDelay` on
// GET (HEAD pre-checks fast-path through so the per-goroutine cost is
// roughly one delay). Serial execution would take ~2 * delay, parallel
// ~1 * delay; the threshold sits in between.
func TestWebFetch_BatchedCrossHostParallel(t *testing.T) {
	const slowGetDelay = 300 * time.Millisecond
	makeServer := func() *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				time.Sleep(slowGetDelay)
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><p>ok</p></body></html>`)
		})
		return httptest.NewServer(mux)
	}
	srvA := makeServer()
	srvB := makeServer()
	t.Cleanup(srvA.Close)
	t.Cleanup(srvB.Close)

	tool := newWebFetchToolForHost(t)
	body := fmt.Sprintf(`{"urls":[%q,%q]}`, srvA.URL+"/page", srvB.URL+"/page")
	start := time.Now()
	out, err := tool.Execute(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	total := time.Since(start)
	var res webFetchBatchResult
	json.Unmarshal(out, &res)
	if len(res.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(res.Results))
	}
	for i, r := range res.Results {
		if r.Error != "" {
			t.Errorf("results[%d] error: %s", i, r.Error)
		}
	}
	// Parallel: total ~= slowGetDelay (plus overhead).
	// Serial: total ~= 2 * slowGetDelay.
	// Threshold sits at 1.5 * slowGetDelay to be robust against CI noise.
	threshold := slowGetDelay + slowGetDelay/2
	if total > threshold {
		t.Errorf("total time %v > threshold %v; cross-host parallelism not enforced (slowGetDelay=%v)", total, threshold, slowGetDelay)
	}
}

// TestWebFetch_BatchedRetryOn429 wires a handler that returns 429 with
// Retry-After: 1 on the first hit, 200 on the second, and asserts the
// batched call succeeds after a single retry and honours the header.
func TestWebFetch_BatchedRetryOn429(t *testing.T) {
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		// Only count GETs - HEAD pre-checks shouldn't trip the retry
		// branch we're trying to exercise.
		if r.Method != http.MethodGet {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(200)
			return
		}
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>OK</title></head><body><p>after retry</p></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	body := fmt.Sprintf(`{"urls":[%q]}`, srv.URL+"/page")
	start := time.Now()
	out, err := tool.Execute(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	elapsed := time.Since(start)

	var res webFetchBatchResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(res.Results))
	}
	entry := res.Results[0]
	if entry.Error != "" {
		t.Fatalf("unexpected error block after retry: %s", entry.Error)
	}
	if !strings.Contains(entry.Content, "after retry") {
		t.Errorf("retry did not surface successful body: %q", entry.Content)
	}
	// Retry-After: 1 → at least ~1s of wait, even with the fast policy.
	// Generous lower bound (700ms) handles the case where the helper's
	// timer fires slightly early.
	if elapsed < 700*time.Millisecond {
		t.Errorf("elapsed %v < 700ms suggests Retry-After header was ignored", elapsed)
	}
	if got := atomic.LoadInt32(&hits); got < 2 {
		t.Errorf("expected >=2 GET hits (one 429 + one 200), got %d", got)
	}
}

// TestWebFetch_BatchedRetryOn503Recovers asserts the retry path also
// applies to 503 (one of the canonical "transient upstream" codes) and
// surfaces the eventual 200 body.
func TestWebFetch_BatchedRetryOn503Recovers(t *testing.T) {
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(200)
			return
		}
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><p>recovered</p></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	body := fmt.Sprintf(`{"urls":[%q]}`, srv.URL+"/page")
	out, err := tool.Execute(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res webFetchBatchResult
	json.Unmarshal(out, &res)
	if len(res.Results) != 1 || res.Results[0].Error != "" {
		t.Fatalf("retry should have recovered: %s", out)
	}
	if !strings.Contains(res.Results[0].Content, "recovered") {
		t.Errorf("retry did not surface the recovered body: %q", res.Results[0].Content)
	}
}

// TestWebFetch_SingleURLBackwardCompat re-asserts that the legacy
// `{"url": ...}` payload still produces the original single-block JSON
// shape after the batched refactor.
func TestWebFetch_SingleURLBackwardCompat(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Legacy</title></head><body><p>legacy body</p></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	in := fmt.Sprintf(`{"url":%q}`, srv.URL+"/page")
	out, err := tool.Execute(context.Background(), []byte(in))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Result MUST decode as the single-URL shape (webFetchResult), not
	// the batched wrapper.
	var single webFetchResult
	if err := json.Unmarshal(out, &single); err != nil {
		t.Fatalf("single-URL shape no longer decodes: %v\n%s", err, out)
	}
	if single.Title != "Legacy" {
		t.Errorf("title = %q, want %q", single.Title, "Legacy")
	}
	if !strings.Contains(single.Content, "legacy body") {
		t.Errorf("content missing legacy body: %q", single.Content)
	}
	// Should NOT have a `results` field. Decode as the batched shape
	// and verify it produces an empty list (proving the keys are absent
	// in the JSON).
	var batch webFetchBatchResult
	_ = json.Unmarshal(out, &batch)
	if len(batch.Results) != 0 {
		t.Errorf("single-URL response leaked batched `results` field: %s", out)
	}
}

// TestWebFetch_BatchedRespectsRobots checks that the global
// respect_robots flag fans out to every URL in a batch.
func TestWebFetch_BatchedRespectsRobots(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "User-agent: *\nDisallow: /no/\n")
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>fine</body></html>`)
	})
	mux.HandleFunc("/no/blocked", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>nope</body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	body := fmt.Sprintf(`{"urls":[%q,%q]}`, srv.URL+"/ok", srv.URL+"/no/blocked")
	out, err := tool.Execute(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res webFetchBatchResult
	json.Unmarshal(out, &res)
	if len(res.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(res.Results))
	}
	if res.Results[0].Error != "" {
		t.Errorf("/ok should succeed, got error: %s", res.Results[0].Error)
	}
	if !strings.Contains(res.Results[1].Error, "robots") {
		t.Errorf("/no/blocked should be refused with a robots error, got: %q", res.Results[1].Error)
	}
}

// TestWebFetch_HostKeyNormalisation pins the batching helper because it
// drives same-host serialization and we don't want a regression to
// silently put two different-case hosts in different buckets.
func TestWebFetch_HostKeyNormalisation(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://Example.COM/a", "example.com"},
		{"https://EXAMPLE.COM:443/x", "example.com:443"},
		{"http://foo.bar/", "foo.bar"},
		{"not-a-url", "__invalid__:not-a-url"},
	}
	for _, tc := range cases {
		got := hostKey(tc.in)
		if got != tc.want {
			t.Errorf("hostKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
