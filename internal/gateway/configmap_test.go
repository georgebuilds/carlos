package gateway_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/gateway"
)

func TestRoutingFromConfig_Empty_UsesDefaults(t *testing.T) {
	got := gateway.RoutingFromConfig(config.GatewayRouting{})
	want := gateway.DefaultRoutingConfig()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("empty -> defaults: got %+v want %+v", got, want)
	}
}

func TestRoutingFromConfig_TypedTranslation(t *testing.T) {
	in := config.GatewayRouting{
		Notifications: []string{"ntfy", "telegram"},
		Approvals:     []string{"telegram", "ntfy"},
		Conversations: []string{"telegram"},
	}
	got := gateway.RoutingFromConfig(in)
	if len(got.Notifications) != 2 || got.Notifications[0] != gateway.SourceNtfy {
		t.Errorf("notifications: %+v", got.Notifications)
	}
	if len(got.Approvals) != 2 || got.Approvals[0] != gateway.SourceTelegram {
		t.Errorf("approvals: %+v", got.Approvals)
	}
	if len(got.Conversations) != 1 || got.Conversations[0] != gateway.SourceTelegram {
		t.Errorf("conversations: %+v", got.Conversations)
	}
}

func TestRoutingFromConfig_UnknownChannelsDropped(t *testing.T) {
	in := config.GatewayRouting{
		Notifications: []string{"ntfy", "smoke-signals", "Telegram"}, // "smoke-signals" + case-mismatched name dropped
		Approvals:     []string{"  telegram  "},                      // trimmed
	}
	got := gateway.RoutingFromConfig(in)
	if len(got.Notifications) != 1 || got.Notifications[0] != gateway.SourceNtfy {
		t.Errorf("unknowns not dropped: %+v", got.Notifications)
	}
	if len(got.Approvals) != 1 || got.Approvals[0] != gateway.SourceTelegram {
		t.Errorf("whitespace not trimmed: %+v", got.Approvals)
	}
}

func TestRetryFromConfig_DefaultsWhenEmpty(t *testing.T) {
	got, err := gateway.RetryFromConfig(config.GatewayRetry{})
	if err != nil {
		t.Fatal(err)
	}
	want := gateway.DefaultRetryConfig()
	if got != want {
		t.Errorf("empty -> defaults: %+v want %+v", got, want)
	}
}

func TestRetryFromConfig_ParsesDurations(t *testing.T) {
	got, err := gateway.RetryFromConfig(config.GatewayRetry{
		MaxAttempts:    7,
		BackoffInitial: "500ms",
		BackoffMax:     "30s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxAttempts != 7 {
		t.Errorf("MaxAttempts: got %d", got.MaxAttempts)
	}
	if got.BackoffInitial != 500*time.Millisecond {
		t.Errorf("BackoffInitial: got %v", got.BackoffInitial)
	}
	if got.BackoffMax != 30*time.Second {
		t.Errorf("BackoffMax: got %v", got.BackoffMax)
	}
}

func TestRetryFromConfig_BadDurationErrors(t *testing.T) {
	if _, err := gateway.RetryFromConfig(config.GatewayRetry{BackoffInitial: "nonsense"}); err == nil {
		t.Error("expected error for unparseable initial")
	}
	if _, err := gateway.RetryFromConfig(config.GatewayRetry{BackoffMax: "nonsense"}); err == nil {
		t.Error("expected error for unparseable max")
	}
}

func TestEnabledChannels(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled:  true,
		Ntfy:     config.NtfyGatewayConfig{Enabled: true},
		Telegram: config.TelegramConfig{Enabled: false},
		Signal:   config.SignalConfig{Enabled: true},
		Custom:   config.CustomGatewayConfig{Enabled: true},
	}
	got := gateway.EnabledChannels(cfg)
	want := []gateway.Source{gateway.SourceNtfy, gateway.SourceSignal, gateway.SourceCustom}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("enabled channels: got %v want %v", got, want)
	}
}

func TestEnabledChannels_MasterSwitchOff(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: false,
		Ntfy:    config.NtfyGatewayConfig{Enabled: true},
	}
	if got := gateway.EnabledChannels(cfg); len(got) != 0 {
		t.Errorf("expected zero channels with master off: %v", got)
	}
}
