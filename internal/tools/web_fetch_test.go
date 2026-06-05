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
	"testing"
	"time"
)

// newWebFetchToolForHost rebuilds a WebFetchTool that points at the
// given httptest.Server. We override AllowPrivate=true because the
// httptest server binds on 127.0.0.1 which the private-address guard
// would otherwise refuse.
func newWebFetchToolForHost(t *testing.T) *WebFetchTool {
	t.Helper()
	return &WebFetchTool{
		AllowPrivate: true,
		Timeout:      3 * time.Second,
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
	if !strings.Contains(string(s), `"url"`) || !strings.Contains(string(s), `"respect_robots"`) {
		t.Errorf("schema missing fields: %s", s)
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
