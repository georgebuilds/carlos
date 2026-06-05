package gateway

import (
	"testing"
	"time"
)

func TestDefaultRetryConfig_Sane(t *testing.T) {
	d := DefaultRetryConfig()
	if d.MaxAttempts != 5 {
		t.Errorf("MaxAttempts: want 5 got %d", d.MaxAttempts)
	}
	if d.BackoffInitial != time.Second {
		t.Errorf("BackoffInitial: want 1s got %v", d.BackoffInitial)
	}
	if d.BackoffMax != 60*time.Second {
		t.Errorf("BackoffMax: want 60s got %v", d.BackoffMax)
	}
}

func TestRetryConfig_Normalize(t *testing.T) {
	zero := RetryConfig{}
	got := zero.Normalize()
	want := DefaultRetryConfig()
	if got != want {
		t.Errorf("Normalize(zero) = %+v want %+v", got, want)
	}

	partial := RetryConfig{MaxAttempts: 2}
	got = partial.Normalize()
	if got.MaxAttempts != 2 {
		t.Errorf("MaxAttempts preserved: got %d want 2", got.MaxAttempts)
	}
	if got.BackoffInitial != time.Second {
		t.Errorf("BackoffInitial defaulted: got %v want 1s", got.BackoffInitial)
	}

	inverted := RetryConfig{MaxAttempts: 1, BackoffInitial: 10 * time.Second, BackoffMax: 1 * time.Second}
	got = inverted.Normalize()
	if got.BackoffMax < got.BackoffInitial {
		t.Errorf("Normalize must clamp BackoffMax >= BackoffInitial: got %+v", got)
	}
}

func TestRetryConfig_Validate(t *testing.T) {
	good := DefaultRetryConfig()
	if err := good.Validate(); err != nil {
		t.Errorf("default: %v", err)
	}
	cases := []RetryConfig{
		{MaxAttempts: 0, BackoffInitial: time.Second, BackoffMax: time.Second},
		{MaxAttempts: 1, BackoffInitial: -1, BackoffMax: time.Second},
		{MaxAttempts: 1, BackoffInitial: time.Second, BackoffMax: -1},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: expected error for %+v", i, c)
		}
	}
}

func TestRetryConfig_backoffFor_Curve(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts:    5,
		BackoffInitial: time.Second,
		BackoffMax:     10 * time.Second,
	}
	cases := []struct {
		idx  int
		want time.Duration
	}{
		{0, 0},
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 10 * time.Second}, // capped
		{6, 10 * time.Second}, // capped
	}
	for _, tc := range cases {
		if got := cfg.backoffFor(tc.idx); got != tc.want {
			t.Errorf("backoffFor(%d) = %v want %v", tc.idx, got, tc.want)
		}
	}
}
