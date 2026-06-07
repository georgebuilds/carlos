package gateway

import (
	"errors"
	"math"
	"time"
)

// RetryConfig governs the broker's exponential-backoff loop for Send.
// Defaults come from the spec § Config shape § retry block; callers can
// override via DefaultRetryConfig().With...() builders or the YAML
// config block.
type RetryConfig struct {
	// MaxAttempts caps the total number of Send tries per envelope per
	// channel. A value of 1 means "no retry" - the first failure is
	// final. Spec default is 5.
	MaxAttempts int

	// BackoffInitial is the wait between attempt 1 → attempt 2. Each
	// subsequent attempt doubles (capped at BackoffMax). Spec default
	// is 1s.
	BackoffInitial time.Duration

	// BackoffMax bounds the per-attempt wait so an adapter outage
	// doesn't extend the retry tail indefinitely. Spec default 60s.
	BackoffMax time.Duration
}

// DefaultRetryConfig matches the spec § Config shape retry block.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:    5,
		BackoffInitial: 1 * time.Second,
		BackoffMax:     60 * time.Second,
	}
}

// Normalize returns a copy of cfg with zero / nonsense fields filled
// from DefaultRetryConfig. Used by the broker constructor so callers
// that hand-build a RetryConfig don't have to remember every default.
func (cfg RetryConfig) Normalize() RetryConfig {
	d := DefaultRetryConfig()
	out := cfg
	if out.MaxAttempts <= 0 {
		out.MaxAttempts = d.MaxAttempts
	}
	if out.BackoffInitial <= 0 {
		out.BackoffInitial = d.BackoffInitial
	}
	if out.BackoffMax <= 0 {
		out.BackoffMax = d.BackoffMax
	}
	if out.BackoffMax < out.BackoffInitial {
		out.BackoffMax = out.BackoffInitial
	}
	return out
}

// Validate reports any structural problem with cfg. Used by the
// config-loader to surface a bad block at startup instead of at the
// first send failure.
func (cfg RetryConfig) Validate() error {
	if cfg.MaxAttempts <= 0 {
		return errors.New("retry: max_attempts must be > 0")
	}
	if cfg.BackoffInitial < 0 {
		return errors.New("retry: backoff_initial must be >= 0")
	}
	if cfg.BackoffMax < 0 {
		return errors.New("retry: backoff_max must be >= 0")
	}
	return nil
}

// backoffFor returns the wait duration before the attemptIndex'th retry
// (attemptIndex=0 is "before attempt 1" → returns 0; attemptIndex=1 is
// "before attempt 2" → returns BackoffInitial; attemptIndex=2 returns
// 2*BackoffInitial; capped at BackoffMax).
//
// Deterministic - no jitter today. If we see thundering herd against a
// rate-limited Telegram bot, the next iteration adds jitter ±25%.
func (cfg RetryConfig) backoffFor(attemptIndex int) time.Duration {
	if attemptIndex <= 0 {
		return 0
	}
	// 2^(attemptIndex-1) * BackoffInitial, capped at BackoffMax.
	factor := math.Pow(2, float64(attemptIndex-1))
	d := time.Duration(factor) * cfg.BackoffInitial
	if d <= 0 || d > cfg.BackoffMax {
		return cfg.BackoffMax
	}
	return d
}
