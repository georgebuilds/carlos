package tools

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"testing"
	"time"
)

// TestComputeBackoff_JitterInjectedRand — with a seeded Rand and 50%
// jitter the delay stays within [0.5, 1.5]x of the base step and is
// reproducible.
func TestComputeBackoff_JitterInjectedRand(t *testing.T) {
	p := retryPolicy{
		BaseDelay:  100 * time.Millisecond,
		MaxDelay:   10 * time.Second,
		JitterFrac: 0.5,
		Rand:       rand.New(rand.NewSource(42)),
	}
	got := computeBackoff(p, 1)
	lo := 50 * time.Millisecond
	hi := 150 * time.Millisecond
	if got < lo || got > hi {
		t.Errorf("jittered backoff %v out of [%v,%v]", got, lo, hi)
	}
}

// TestComputeBackoff_JitterSharedRand — JitterFrac>0 with a nil policy
// Rand exercises the shared-RNG path (and sharedRand's lazy init).
func TestComputeBackoff_JitterSharedRand(t *testing.T) {
	p := retryPolicy{
		BaseDelay:  100 * time.Millisecond,
		MaxDelay:   10 * time.Second,
		JitterFrac: 0.25,
		// Rand nil -> shared.
	}
	got := computeBackoff(p, 2)
	// attempt=2 doubles base to 200ms, +/-25% -> [150ms, 250ms].
	if got < 150*time.Millisecond || got > 250*time.Millisecond {
		t.Errorf("shared-rand backoff %v out of [150ms,250ms]", got)
	}
}

// TestComputeBackoff_ZeroAndCap — attempt<1 yields 0; growth is capped at
// MaxDelay.
func TestComputeBackoff_ZeroAndCap(t *testing.T) {
	p := retryPolicy{BaseDelay: 1 * time.Second, MaxDelay: 4 * time.Second}
	if got := computeBackoff(p, 0); got != 0 {
		t.Errorf("attempt 0 backoff = %v, want 0", got)
	}
	// 1s,2s,4s,4s(capped),4s(capped)...
	if got := computeBackoff(p, 10); got != 4*time.Second {
		t.Errorf("capped backoff = %v, want 4s", got)
	}
}

// TestSharedRand_Singleton — sharedRand returns a non-nil reusable RNG.
func TestSharedRand_Singleton(t *testing.T) {
	a := sharedRand()
	b := sharedRand()
	if a == nil || b == nil {
		t.Fatal("sharedRand returned nil")
	}
	if a != b {
		t.Error("sharedRand should return the same instance")
	}
}

// TestSleepWithCtx_ReturnsImmediatelyForNonPositive — d<=0 returns nil
// without arming a timer.
func TestSleepWithCtx_NonPositive(t *testing.T) {
	if err := sleepWithCtx(context.Background(), 0); err != nil {
		t.Errorf("d=0 should return nil; got %v", err)
	}
	if err := sleepWithCtx(context.Background(), -5*time.Second); err != nil {
		t.Errorf("negative d should return nil; got %v", err)
	}
}

// TestSleepWithCtx_CancelledReturnsErr — a cancelled ctx wins over the
// timer.
func TestSleepWithCtx_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepWithCtx(ctx, time.Hour); err == nil {
		t.Error("cancelled ctx should return its error")
	}
}

// TestSleepWithCtx_TimerFires — a short positive delay returns nil after
// the timer fires.
func TestSleepWithCtx_TimerFires(t *testing.T) {
	if err := sleepWithCtx(context.Background(), time.Millisecond); err != nil {
		t.Errorf("short sleep should complete; got %v", err)
	}
}

// TestDoWithRetry_MaxAttemptsClampedToOne — a policy with MaxAttempts<=0
// is treated as a single attempt (no retry).
func TestDoWithRetry_MaxAttemptsClampedToOne(t *testing.T) {
	calls := 0
	p := retryPolicy{MaxAttempts: 0, BaseDelay: time.Millisecond}
	_, err := doWithRetry(context.Background(), p, func(ctx context.Context) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Body: http.NoBody}, nil
	})
	if calls != 1 {
		t.Errorf("MaxAttempts<=0 should clamp to 1 attempt; got %d calls", calls)
	}
	if err == nil {
		t.Error("a retryable status with one attempt should still error out")
	}
}

// TestDoWithRetry_SleepErrorAborts — when the injected sleep returns an
// error (e.g. ctx cancelled during backoff), doWithRetry returns it.
func TestDoWithRetry_SleepErrorAborts(t *testing.T) {
	sleepErr := errors.New("sleep interrupted")
	p := retryPolicy{
		MaxAttempts:  3,
		BaseDelay:    time.Millisecond,
		MaxDelay:     time.Millisecond,
		MaxTotalWait: time.Hour,
		Sleep: func(context.Context, time.Duration) error {
			return sleepErr
		},
	}
	_, err := doWithRetry(context.Background(), p, func(ctx context.Context) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: http.NoBody}, nil
	})
	if !errors.Is(err, sleepErr) {
		t.Errorf("want the sleep error to propagate; got %v", err)
	}
}

// TestErrOrCode — prefers the error, falls back to HTTP status, then to
// the unknown sentinel.
func TestErrOrCode(t *testing.T) {
	e := errors.New("boom")
	if got := errOrCode(e, nil); got != e {
		t.Errorf("errOrCode should prefer the error; got %v", got)
	}
	resp := &http.Response{StatusCode: 503}
	if got := errOrCode(nil, resp); got == nil || got.Error() != "HTTP 503" {
		t.Errorf("errOrCode = %v, want HTTP 503", got)
	}
	if got := errOrCode(nil, nil); got == nil || got.Error() != "unknown failure" {
		t.Errorf("errOrCode = %v, want unknown failure", got)
	}
}
