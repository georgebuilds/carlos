// Shared HTTP retry helper for the search backends (and any other tool
// that round-trips a third party HTTP API). Three retries max with
// exponential backoff + jitter, capped at ~30s total wall time. Honors
// the server's Retry-After header on 429 / 503. Retries only on the
// classic transient signals: 429, 502, 503, 504, plus network errors
// (connection reset, timeout, EOF mid-stream).
//
// Why a tools-package helper instead of pulling in a third party retry
// library: the policy here is intentionally small and tailored to the
// search APIs we already talk to, and the zero-dep stance from the
// other tools in this package (web_search, arxiv, wikipedia) means we
// keep the import graph free of an extra retry dep. The
// internal/gateway retry helper is broker-oriented (envelope sends,
// not HTTP) and has a different MaxAttempts / per-step semantics, so
// reusing it would be a stretch.
//
// The helper takes an attempt closure that returns (*http.Response,
// error). On success (resp.StatusCode is non-retryable) it returns the
// response unchanged so the caller can read the body normally. On
// retry the closure is invoked again; the caller is expected to build
// a fresh request each time (so headers / bodies stay valid).
package tools

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// retryPolicy captures the knobs for doWithRetry. Defaults match the
// project policy: 3 retries on top of the first attempt, total wall
// time capped at ~30s, base step 500ms doubling each attempt with
// +/- 25% jitter.
type retryPolicy struct {
	// MaxAttempts is the total number of attempts including the first.
	// MaxAttempts=1 disables retry. Default 4 (one initial + three
	// retries).
	MaxAttempts int
	// BaseDelay is the first inter-attempt wait. Doubles each attempt.
	BaseDelay time.Duration
	// MaxDelay caps any single inter-attempt wait. Retry-After can
	// exceed this; the caller policy is "if the server asks for X, we
	// honor X" because ignoring Retry-After is what gets us banned.
	MaxDelay time.Duration
	// MaxTotalWait caps cumulative wall time across all retries. The
	// helper stops retrying when adding the next wait would exceed it.
	MaxTotalWait time.Duration
	// JitterFrac is the +/- jitter applied to BaseDelay. 0.25 means
	// each computed delay is multiplied by a uniform random value in
	// [0.75, 1.25]. Set to 0 for deterministic delays (tests).
	JitterFrac float64
	// Now / Sleep are seams for tests so they can advance a fake clock
	// instead of waiting real time. nil → time.Now / time.Sleep.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	// Rand is the RNG used for jitter; nil → package-level shared.
	Rand *rand.Rand
}

// defaultRetryPolicy returns the policy used by the search backends
// out of the box.
func defaultRetryPolicy() retryPolicy {
	return retryPolicy{
		MaxAttempts:  4,
		BaseDelay:    500 * time.Millisecond,
		MaxDelay:     8 * time.Second,
		MaxTotalWait: 30 * time.Second,
		JitterFrac:   0.25,
	}
}

// sharedRetryRand is the package-wide RNG used when a policy doesn't
// inject one. Seeded once on first use; protected by a sync.Mutex so
// concurrent backends can share it without racing.
var (
	sharedRetryRandOnce sync.Once
	sharedRetryRandMu   sync.Mutex
	sharedRetryRand     *rand.Rand
)

func sharedRand() *rand.Rand {
	sharedRetryRandOnce.Do(func() {
		sharedRetryRand = rand.New(rand.NewSource(time.Now().UnixNano()))
	})
	return sharedRetryRand
}

// retryableStatusCodes is the set of HTTP status codes we retry. 429
// is the canonical "back off" signal; 502/503/504 indicate upstream
// flakiness that a retry routinely fixes. We deliberately do NOT
// retry other 4xx codes - 400/401/403/404 are caller errors and
// re-issuing the same request will just burn time.
var retryableStatusCodes = map[int]bool{
	http.StatusTooManyRequests:     true, // 429
	http.StatusBadGateway:          true, // 502
	http.StatusServiceUnavailable:  true, // 503
	http.StatusGatewayTimeout:      true, // 504
}

// isRetryableError reports whether err looks like a transient network
// failure worth retrying. ctx cancellation is NOT retryable - the
// caller asked us to stop. Errors are matched on substrings rather
// than typed because net/http wraps errors deeply and the underlying
// types are mostly unexported.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := err.Error()
	// Lowercase once; substring checks below assume lower case.
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "connection reset"),
		strings.Contains(low, "connection refused"),
		strings.Contains(low, "connection closed"),
		strings.Contains(low, "no such host"),
		strings.Contains(low, "i/o timeout"),
		strings.Contains(low, "timeout awaiting"),
		strings.Contains(low, "unexpected eof"),
		strings.Contains(low, "broken pipe"),
		strings.Contains(low, "tls handshake timeout"):
		return true
	}
	return false
}

// parseRetryAfter reads a Retry-After header value. Per RFC 7231 the
// value is either an integer number of seconds or an HTTP-date; we
// only support the seconds form because every search API we talk to
// emits seconds. Returns 0 on parse failure so the caller falls back
// to the computed backoff.
func parseRetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// computeBackoff returns the wait for the given retry attempt
// (attempt=1 → first retry, attempt=2 → second, …). Pure function so
// tests can pin the policy and assert specific delays.
func computeBackoff(p retryPolicy, attempt int) time.Duration {
	if attempt < 1 {
		return 0
	}
	// Exponential: BaseDelay * 2^(attempt-1).
	delay := p.BaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > p.MaxDelay {
			delay = p.MaxDelay
			break
		}
	}
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	if p.JitterFrac > 0 {
		r := p.Rand
		if r == nil {
			sharedRetryRandMu.Lock()
			defer sharedRetryRandMu.Unlock()
			r = sharedRand()
		}
		// Uniform on [1 - JitterFrac, 1 + JitterFrac].
		mult := 1 + p.JitterFrac*(2*r.Float64()-1)
		delay = time.Duration(float64(delay) * mult)
		if delay < 0 {
			delay = 0
		}
	}
	return delay
}

// sleepWithCtx waits for d or returns ctx.Err() if the ctx finishes
// first. Extracted so tests can inject a fake.
func sleepWithCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// doWithRetry runs attempt repeatedly under the supplied policy until
// it returns a non-retryable result (success, 4xx other than 429, or a
// non-transient error) or the policy budget is exhausted. The caller's
// closure must rebuild any per-attempt state (a fresh *http.Request,
// fresh body reader) on each call.
//
// Returns the final response + the final error. If retry exhausted on
// a retryable status, the last response is returned alongside a
// "retry budget exhausted" error so the caller can still log the body.
func doWithRetry(ctx context.Context, p retryPolicy, attempt func(ctx context.Context) (*http.Response, error)) (*http.Response, error) {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 1
	}
	sleep := p.Sleep
	if sleep == nil {
		sleep = sleepWithCtx
	}

	var lastResp *http.Response
	var lastErr error
	totalWait := time.Duration(0)
	for i := 0; i < p.MaxAttempts; i++ {
		if err := ctx.Err(); err != nil {
			return lastResp, err
		}
		resp, err := attempt(ctx)
		// Decide whether to retry.
		retryable, retryAfter := false, time.Duration(0)
		switch {
		case err != nil:
			retryable = isRetryableError(err)
		case resp != nil && retryableStatusCodes[resp.StatusCode]:
			retryable = true
			retryAfter = parseRetryAfter(resp.Header)
		default:
			// Non-retryable success or non-retryable failure.
			return resp, err
		}
		lastResp, lastErr = resp, err
		// Out of attempts → return what we have.
		if i == p.MaxAttempts-1 {
			break
		}
		if !retryable {
			return resp, err
		}
		// On a retry we don't need the body of the failed response;
		// close it so we don't leak the connection.
		if resp != nil {
			_ = resp.Body.Close()
			lastResp = nil
		}
		wait := computeBackoff(p, i+1)
		if retryAfter > 0 {
			// Honor server hint even when it exceeds MaxDelay. Ignoring
			// Retry-After is what gets the client banned.
			wait = retryAfter
		}
		// Stop if the next wait would push us past the total budget.
		if p.MaxTotalWait > 0 && totalWait+wait > p.MaxTotalWait {
			return resp, fmt.Errorf("retry budget exhausted after %d attempts: %w", i+1, errOrCode(err, resp))
		}
		if err := sleep(ctx, wait); err != nil {
			return resp, err
		}
		totalWait += wait
	}
	return lastResp, fmt.Errorf("retry budget exhausted after %d attempts: %w", p.MaxAttempts, errOrCode(lastErr, lastResp))
}

// errOrCode normalises the "what failed" message for the final retry
// error. Prefers the wrapped error when present; falls back to "HTTP
// %d" otherwise.
func errOrCode(err error, resp *http.Response) error {
	if err != nil {
		return err
	}
	if resp != nil {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return errors.New("unknown failure")
}
