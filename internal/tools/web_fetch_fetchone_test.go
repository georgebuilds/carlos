package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWebFetch_RefusedScheme — a non-http(s) scheme is rejected before any
// network access.
func TestWebFetch_RefusedScheme(t *testing.T) {
	tool := newWebFetchToolForHost(t)
	_, err := tool.Execute(context.Background(), []byte(`{"url":"ftp://example.com/x"}`))
	if err == nil || !strings.Contains(err.Error(), "refused scheme") {
		t.Errorf("want refused-scheme error, got %v", err)
	}
}

// TestWebFetch_NoHost — a URL with no host is rejected.
func TestWebFetch_NoHost(t *testing.T) {
	tool := newWebFetchToolForHost(t)
	_, err := tool.Execute(context.Background(), []byte(`{"url":"http:///pathonly"}`))
	if err == nil || !strings.Contains(err.Error(), "no host") {
		t.Errorf("want no-host error, got %v", err)
	}
}

// TestWebFetch_PrivateHostRefused — with AllowPrivate disabled a loopback
// target is refused by the SSRF guard.
func TestWebFetch_PrivateHostRefusedExplicit(t *testing.T) {
	tool := &WebFetchTool{} // AllowPrivate defaults to false
	_, err := tool.Execute(context.Background(), []byte(`{"url":"http://127.0.0.1:9/x"}`))
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("want private-host refusal, got %v", err)
	}
}

// TestWebFetch_GET4xxErrors — a 4xx response from the GET surfaces an
// explicit HTTP-status error.
func TestWebFetch_GET4xxErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone) // 410, not retryable
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	_, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/missing")))
	if err == nil || !strings.Contains(err.Error(), "HTTP 410") {
		t.Errorf("want HTTP 410 error, got %v", err)
	}
}

// TestWebFetch_HeadContentLengthExceedsCap — a HEAD that declares a
// Content-Length above 4x the body cap is refused before the GET.
func TestWebFetch_HeadContentLengthExceedsCap(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "text/plain")
			// Declare a body far larger than 4x the (small) cap below.
			w.Header().Set("Content-Length", "10000000")
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(bytes.Repeat([]byte("a"), 100))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.MaxBodyBytes = 1024 // 4x = 4096; declared 10MB exceeds it
	tool.routeViaTestServer(srv)
	_, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/huge")))
	if err == nil || !strings.Contains(err.Error(), "Content-Length") {
		t.Errorf("want Content-Length cap refusal, got %v", err)
	}
}

// TestWebFetch_BatchedPerURLError — a batch where one URL 404s records
// that URL's error in its result slot while the others succeed, and the
// shared-host delay path runs (all URLs route to the same test host).
func TestWebFetch_BatchedPerURLError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/good", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Good</title></head><body><p>ok</p></body></html>`)
	})
	mux.HandleFunc("/bad", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	body := fmt.Sprintf(`{"urls":[%q,%q]}`, srv.URL+"/good", srv.URL+"/bad")
	out, err := tool.Execute(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res webFetchBatchResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(res.Results) != 2 {
		t.Fatalf("want 2 result slots, got %d", len(res.Results))
	}
	if res.Results[0].Error != "" {
		t.Errorf("good URL should have no error: %q", res.Results[0].Error)
	}
	if res.Results[1].Error == "" {
		t.Errorf("bad URL should carry an error: %+v", res.Results[1])
	}
}

// TestWebFetch_Robots403Disallows — a 401/403 on robots.txt makes the
// tool cautiously DISALLOW the fetch (stricter than the spec, matching a
// polite bot).
func TestWebFetch_Robots403Disallows(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body>page</body></html>")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	_, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/page")))
	if err == nil || !strings.Contains(err.Error(), "robots.txt") {
		t.Errorf("403 on robots.txt should disallow; got %v", err)
	}
}

// TestWebFetch_GETNonTextAfterHeadOK — when HEAD is silent on content type
// but GET returns a binary type, the GET-side content-type guard refuses.
func TestWebFetch_GETNonTextAfterHeadOK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// HEAD: no Content-Type header at all -> HEAD probe passes.
			w.Header()["Content-Type"] = nil
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Write([]byte("PK\x03\x04"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	_, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/file")))
	if err == nil || !strings.Contains(err.Error(), "non-text content") {
		t.Errorf("GET-side non-text guard should refuse; got %v", err)
	}
}

// TestWebFetch_HeadNonTextContentType — a HEAD declaring a binary content
// type is refused before the GET runs.
func TestWebFetch_HeadNonTextContentType(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("should not be reached"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tool := newWebFetchToolForHost(t)
	tool.routeViaTestServer(srv)
	_, err := tool.Execute(context.Background(),
		[]byte(fmt.Sprintf(`{"url":%q}`, srv.URL+"/bin")))
	if err == nil || !strings.Contains(err.Error(), "non-text content") {
		t.Errorf("want non-text refusal from HEAD probe, got %v", err)
	}
}
