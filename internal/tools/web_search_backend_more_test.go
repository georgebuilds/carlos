package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBraveBackend_DecodeError — a 200 with non-JSON body surfaces a
// decode error.
func TestBraveBackend_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json at all"))
	}))
	defer srv.Close()
	b := &BraveBackend{Endpoint: srv.URL, Client: srv.Client()}
	if _, err := b.Search(context.Background(), "q", 5); err == nil ||
		!strings.Contains(err.Error(), "decode") {
		t.Errorf("want decode error, got %v", err)
	}
}

// TestSearXNGBackend_NonOK — non-200 surfaces an HTTP error with the body.
func TestSearXNGBackend_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	s := &SearXNGBackend{InstanceURL: srv.URL, Client: srv.Client()}
	if _, err := s.Search(context.Background(), "q", 5); err == nil ||
		!strings.Contains(err.Error(), "429") {
		t.Errorf("want HTTP 429 error, got %v", err)
	}
}

// TestSearXNGBackend_DecodeError — malformed JSON surfaces a decode error.
func TestSearXNGBackend_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{ broken"))
	}))
	defer srv.Close()
	s := &SearXNGBackend{InstanceURL: srv.URL, Client: srv.Client()}
	if _, err := s.Search(context.Background(), "q", 5); err == nil ||
		!strings.Contains(err.Error(), "decode") {
		t.Errorf("want decode error, got %v", err)
	}
}

// TestSearXNGBackend_MaxCaps — more results than max are truncated, and
// ranks are 1-indexed.
func TestSearXNGBackend_MaxCaps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[
			{"title":"a","url":"https://a.com","content":"x"},
			{"title":"b","url":"https://b.com","content":"y"},
			{"title":"c","url":"https://c.com","content":"z"}
		]}`))
	}))
	defer srv.Close()
	s := &SearXNGBackend{InstanceURL: srv.URL, Client: srv.Client()}
	got, err := s.Search(context.Background(), "q", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("max=2 should cap to 2; got %d", len(got))
	}
	if got[0].Rank != 1 || got[1].Rank != 2 {
		t.Errorf("ranks should be 1,2; got %d,%d", got[0].Rank, got[1].Rank)
	}
}

// deadEndpoint returns a URL that is syntactically valid but refuses
// connections (a server that has already been closed), so cli.Do fails.
func deadEndpoint(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // close immediately so subsequent dials are refused
	return url
}

// TestBraveBackend_TransportError — a connection failure surfaces as an
// http error from Search.
func TestBraveBackend_TransportError(t *testing.T) {
	b := &BraveBackend{Endpoint: deadEndpoint(t), Client: &http.Client{}}
	if _, err := b.Search(context.Background(), "q", 5); err == nil ||
		!strings.Contains(err.Error(), "http") {
		t.Errorf("want http transport error, got %v", err)
	}
}

// TestSearXNGBackend_TransportError — same for SearXNG.
func TestSearXNGBackend_TransportError(t *testing.T) {
	s := &SearXNGBackend{InstanceURL: deadEndpoint(t), Client: &http.Client{}}
	if _, err := s.Search(context.Background(), "q", 5); err == nil ||
		!strings.Contains(err.Error(), "http") {
		t.Errorf("want http transport error, got %v", err)
	}
}

// TestDuckDuckGoBackend_TransportError — same for DuckDuckGo.
func TestDuckDuckGoBackend_TransportError(t *testing.T) {
	d := &DuckDuckGoBackend{Endpoint: deadEndpoint(t), Client: &http.Client{}}
	if _, err := d.Search(context.Background(), "q", 5); err == nil ||
		!strings.Contains(err.Error(), "http") {
		t.Errorf("want http transport error, got %v", err)
	}
}

// TestDuckDuckGoBackend_NonOK — a non-200 from the HTML endpoint surfaces
// an error rather than parsing garbage.
func TestDuckDuckGoBackend_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusForbidden)
	}))
	defer srv.Close()
	d := &DuckDuckGoBackend{Endpoint: srv.URL, Client: srv.Client()}
	if _, err := d.Search(context.Background(), "q", 5); err == nil {
		t.Error("expected error on non-200 DDG response")
	}
}
