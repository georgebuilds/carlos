package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRetry_StopsOnSuccess: a 200 response on the first attempt
// returns immediately. The attempt closure must be called exactly once.
func TestRetry_StopsOnSuccess(t *testing.T) {
	var calls int32
	resp, err := doWithRetry(context.Background(), retryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxTotalWait: time.Second}, func(ctx context.Context) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

// TestRetry_429ThenSuccess: first attempt returns 429, second returns
// 200. The helper should sleep, retry, and surface the 200.
func TestRetry_429ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	policy := retryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxTotalWait: time.Second}
	resp, err := doWithRetry(context.Background(), policy, func(ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200 after retry", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2 (1 retry)", got)
	}
}

// TestRetry_400NotRetried: a 400 response must NOT trigger a retry.
// The attempt closure is called exactly once and the 400 is returned.
func TestRetry_400NotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	policy := retryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxTotalWait: time.Second}
	resp, err := doWithRetry(context.Background(), policy, func(ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 400)", got)
	}
}

// TestRetry_HonorsRetryAfter: when the server emits a Retry-After
// header the helper waits at least that long before the next attempt.
// Uses a tiny Retry-After (1s) bounded by the test budget; verifies
// elapsed time crosses the threshold.
func TestRetry_HonorsRetryAfter(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1") // 1 second
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Capture the actual sleeps requested through the injectable seam
	// so we can assert on Retry-After honoring without burning real
	// wall time.
	var slept []time.Duration
	policy := retryPolicy{
		MaxAttempts:  3,
		BaseDelay:    10 * time.Millisecond,
		MaxDelay:     20 * time.Millisecond,
		MaxTotalWait: 5 * time.Second,
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	}
	resp, err := doWithRetry(context.Background(), policy, func(ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if len(slept) != 1 {
		t.Fatalf("slept calls = %d, want 1: %v", len(slept), slept)
	}
	if slept[0] < time.Second {
		t.Errorf("first sleep = %v, want >= 1s (Retry-After)", slept[0])
	}
}

// TestRetry_BudgetExhaustion: every attempt returns 503; after
// MaxAttempts the helper returns a "retry budget exhausted" error.
func TestRetry_BudgetExhaustion(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	policy := retryPolicy{
		MaxAttempts:  3,
		BaseDelay:    time.Millisecond,
		MaxDelay:     time.Millisecond,
		MaxTotalWait: time.Second,
		Sleep:        func(_ context.Context, _ time.Duration) error { return nil }, // no real sleep
	}
	_, err := doWithRetry(context.Background(), policy, func(ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	if err == nil {
		t.Fatal("expected budget-exhausted error")
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("error = %v, want substring 'exhausted'", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3 (MaxAttempts)", got)
	}
}

// TestRetry_NetworkErrorRetried: simulate a network error (server
// closed mid-request) and verify the helper retries. We use a
// stubbed attempt closure rather than a real socket-tear so the test
// stays deterministic.
func TestRetry_NetworkErrorRetried(t *testing.T) {
	var calls int32
	policy := retryPolicy{
		MaxAttempts:  3,
		BaseDelay:    time.Millisecond,
		MaxDelay:     time.Millisecond,
		MaxTotalWait: time.Second,
		Sleep:        func(_ context.Context, _ time.Duration) error { return nil },
	}
	_, err := doWithRetry(context.Background(), policy, func(ctx context.Context) (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return nil, errors.New("connection reset by peer")
		}
		return nil, errors.New("connection reset by peer")
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3 (every connection-reset retried)", got)
	}
}

// TestRetry_CtxCancelNotRetried: ctx cancellation is not retryable
// and is surfaced immediately.
func TestRetry_CtxCancelNotRetried(t *testing.T) {
	var calls int32
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel up-front

	policy := retryPolicy{
		MaxAttempts:  3,
		BaseDelay:    time.Millisecond,
		MaxDelay:     time.Millisecond,
		MaxTotalWait: time.Second,
		Sleep:        func(_ context.Context, _ time.Duration) error { return nil },
	}
	_, err := doWithRetry(ctx, policy, func(ctx context.Context) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return nil, ctx.Err()
	})
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if got := atomic.LoadInt32(&calls); got > 1 {
		t.Errorf("calls = %d, want <= 1 (ctx cancel must not retry)", got)
	}
}

// TestRetry_TotalWaitCeiling: when adding the next backoff would
// exceed MaxTotalWait the helper short-circuits.
func TestRetry_TotalWaitCeiling(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// BaseDelay 600ms, MaxAttempts large enough to require many
	// retries; MaxTotalWait 1s caps total wait time so after the
	// first retry the second retry's projected wait of 1200ms
	// pushes total past the ceiling and the helper stops.
	policy := retryPolicy{
		MaxAttempts:  5,
		BaseDelay:    600 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		MaxTotalWait: 1 * time.Second,
		Sleep:        func(_ context.Context, _ time.Duration) error { return nil },
	}
	_, err := doWithRetry(context.Background(), policy, func(ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	if err == nil {
		t.Fatal("expected budget error")
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("error = %v, want exhausted", err)
	}
	if got := atomic.LoadInt32(&calls); got > 3 {
		t.Errorf("calls = %d, want <= 3 (ceiling cut off later retries)", got)
	}
}

// TestComputeBackoff_Deterministic verifies the no-jitter path.
func TestComputeBackoff_Deterministic(t *testing.T) {
	p := retryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: 500 * time.Millisecond, JitterFrac: 0}
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 0},
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 500 * time.Millisecond}, // capped
		{5, 500 * time.Millisecond}, // capped
	}
	for _, c := range cases {
		got := computeBackoff(p, c.attempt)
		if got != c.want {
			t.Errorf("computeBackoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

// TestParseRetryAfter spot-checks the seconds-form parser.
func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"5", 5 * time.Second},
		{"  10  ", 10 * time.Second},
		{"invalid", 0},
		{"0", 0},
		{"-3", 0},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.header != "" {
			h.Set("Retry-After", c.header)
		}
		got := parseRetryAfter(h)
		if got != c.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}

// TestIsRetryableError covers the substring matches that classify a
// network error as transient.
func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("read: connection reset by peer"), true},
		{errors.New("dial: connection refused"), true},
		{errors.New("net/http: TLS handshake timeout"), true},
		{errors.New("unexpected EOF"), true},
		{errors.New("no such host"), true},
		{errors.New("i/o timeout"), true},
		{errors.New("broken pipe"), true},
		{errors.New("400 bad request"), false},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{fmt.Errorf("wrapped: %w", context.Canceled), false},
	}
	for _, c := range cases {
		got := isRetryableError(c.err)
		if got != c.want {
			t.Errorf("isRetryableError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
