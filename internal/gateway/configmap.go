package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/config"
)

// RoutingFromConfig translates the YAML-side GatewayRouting (string
// channel lists) into the typed runtime RoutingConfig the broker
// consumes. Unknown channel names are skipped with no error — the
// daemon-side loader should log them, but a bad value in one routing
// entry should not stop the broker from coming up. A nil/empty cfg
// returns DefaultRoutingConfig so users with `gateway.enabled: true`
// but no routing block still get the spec defaults.
func RoutingFromConfig(cfg config.GatewayRouting) RoutingConfig {
	if isEmptyRouting(cfg) {
		return DefaultRoutingConfig()
	}
	return RoutingConfig{
		Notifications: parseSources(cfg.Notifications),
		Approvals:     parseSources(cfg.Approvals),
		Conversations: parseSources(cfg.Conversations),
	}
}

// isEmptyRouting reports whether every list in cfg is empty. Used so
// RoutingFromConfig can substitute defaults without forcing the user
// to repeat them.
func isEmptyRouting(cfg config.GatewayRouting) bool {
	return len(cfg.Notifications) == 0 && len(cfg.Approvals) == 0 && len(cfg.Conversations) == 0
}

// parseSources converts a slice of channel-name strings into a slice
// of typed Source values, dropping unknown entries. Whitespace around
// names is trimmed; case is preserved (Source values are lowercase by
// convention, so a "Telegram" with a capital letter is treated as
// unknown — the daemon's config loader should normalize at load time
// if we ever decide to be lenient).
func parseSources(names []string) []Source {
	if len(names) == 0 {
		return nil
	}
	out := make([]Source, 0, len(names))
	for _, n := range names {
		s := Source(strings.TrimSpace(n))
		if !s.Valid() {
			continue
		}
		out = append(out, s)
	}
	return out
}

// RetryFromConfig translates the YAML retry block into the typed
// RetryConfig. Duration strings ("1s", "60s") are parsed via
// time.ParseDuration; an unparseable value returns the spec default
// for that field — the daemon should surface this as a config
// warning, but it should never crash the broker.
//
// A zero-value cfg returns DefaultRetryConfig so the broker is
// usable even without an explicit retry block.
func RetryFromConfig(cfg config.GatewayRetry) (RetryConfig, error) {
	d := DefaultRetryConfig()
	out := RetryConfig{
		MaxAttempts:    cfg.MaxAttempts,
		BackoffInitial: d.BackoffInitial,
		BackoffMax:     d.BackoffMax,
	}
	if cfg.BackoffInitial != "" {
		v, err := time.ParseDuration(cfg.BackoffInitial)
		if err != nil {
			return RetryConfig{}, fmt.Errorf("retry: backoff_initial %q: %w", cfg.BackoffInitial, err)
		}
		out.BackoffInitial = v
	}
	if cfg.BackoffMax != "" {
		v, err := time.ParseDuration(cfg.BackoffMax)
		if err != nil {
			return RetryConfig{}, fmt.Errorf("retry: backoff_max %q: %w", cfg.BackoffMax, err)
		}
		out.BackoffMax = v
	}
	return out.Normalize(), nil
}

// EnabledChannels reports which Source values are enabled in cfg.
// Used by the daemon at startup to decide which adapter constructors
// to call. Returned in stable order: ntfy, telegram, signal, custom.
func EnabledChannels(cfg config.GatewayConfig) []Source {
	if !cfg.Enabled {
		return nil
	}
	var out []Source
	if cfg.Ntfy.Enabled {
		out = append(out, SourceNtfy)
	}
	if cfg.Telegram.Enabled {
		out = append(out, SourceTelegram)
	}
	if cfg.Signal.Enabled {
		out = append(out, SourceSignal)
	}
	if cfg.Custom.Enabled {
		out = append(out, SourceCustom)
	}
	return out
}
