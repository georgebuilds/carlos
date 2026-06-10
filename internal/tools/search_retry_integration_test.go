package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestWikipediaBackend_RetryOn429 verifies the wikipedia backend
// reuses the shared retry helper: a 429 on the first attempt is
// followed by a 200 and the caller sees the parsed results.
func TestWikipediaBackend_RetryOn429(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"pages":[{"id":1,"key":"K","title":"K","excerpt":"k"}]}`))
	}))
	defer srv.Close()

	// Inject a zero-jitter policy with the sleep seam so the test
	// doesn't burn real wall time on backoff.
	policy := retryPolicy{
		MaxAttempts:  3,
		BaseDelay:    time.Millisecond,
		MaxDelay:     time.Millisecond,
		MaxTotalWait: time.Second,
		Sleep:        func(_ context.Context, _ time.Duration) error { return nil },
	}
	b := &WikipediaBackend{Endpoint: srv.URL, RetryPolicy: &policy}
	got, err := b.Search(context.Background(), "x", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1 after retry", len(got))
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Errorf("hits = %d, want 2 (one retry)", hits)
	}
}

// TestWikipediaBackend_404NoRetry: a 404 is a hard failure and is
// returned immediately.
func TestWikipediaBackend_404NoRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	policy := retryPolicy{
		MaxAttempts:  3,
		BaseDelay:    time.Millisecond,
		MaxDelay:     time.Millisecond,
		MaxTotalWait: time.Second,
		Sleep:        func(_ context.Context, _ time.Duration) error { return nil },
	}
	b := &WikipediaBackend{Endpoint: srv.URL, RetryPolicy: &policy}
	_, err := b.Search(context.Background(), "x", 5)
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %v, want substring '404'", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("hits = %d, want 1 (404 must not retry)", hits)
	}
}

// TestArxivBackend_RetryOn503 verifies arxiv's backend retries on
// 503. The MinInterval gate is set to 0 so the test runs fast.
func TestArxivBackend_RetryOn503(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(arxivAtomEmpty))
	}))
	defer srv.Close()

	policy := retryPolicy{
		MaxAttempts:  3,
		BaseDelay:    time.Millisecond,
		MaxDelay:     time.Millisecond,
		MaxTotalWait: time.Second,
		Sleep:        func(_ context.Context, _ time.Duration) error { return nil },
	}
	a := &ArxivBackend{
		Endpoint:    srv.URL,
		MinInterval: 0,
		RetryPolicy: &policy,
	}
	_, err := a.Search(context.Background(), "x", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
}

// TestArxivBackend_RetryAfterHonored verifies the backend defers to
// the server's Retry-After header. We capture the sleeps requested
// through the policy's Sleep seam to make the assertion without
// burning the actual wall time.
func TestArxivBackend_RetryAfterHonored(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "back off", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(arxivAtomEmpty))
	}))
	defer srv.Close()

	var slept []time.Duration
	policy := retryPolicy{
		MaxAttempts:  3,
		BaseDelay:    10 * time.Millisecond,
		MaxDelay:     20 * time.Millisecond,
		MaxTotalWait: 10 * time.Second,
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	}
	a := &ArxivBackend{
		Endpoint:    srv.URL,
		MinInterval: 0,
		RetryPolicy: &policy,
	}
	_, err := a.Search(context.Background(), "x", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(slept) != 1 {
		t.Fatalf("slept = %v, want exactly one sleep", slept)
	}
	if slept[0] < 5*time.Second {
		t.Errorf("sleep = %v, want >= 5s (Retry-After)", slept[0])
	}
}
