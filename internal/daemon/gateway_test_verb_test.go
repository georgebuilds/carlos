package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/gateway/fake"
)

// newGatewayTestBroker stands up a real broker with one fake adapter
// registered as the named channel. Used by the gateway-test verb tests
// to assert that dispatch hands the envelope through to broker.SendTo.
func newGatewayTestBroker(t *testing.T, channel gateway.Source) (*gateway.Broker, *fake.Adapter) {
	t.Helper()
	log := newGatewayLog(t)
	b, err := gateway.New(gateway.Options{
		Log:   log,
		Retry: gateway.RetryConfig{MaxAttempts: 1, BackoffInitial: time.Millisecond, BackoffMax: 2 * time.Millisecond},
		Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	f := fake.New(channel)
	if err := b.Register(f); err != nil {
		t.Fatalf("register: %v", err)
	}
	return b, f
}

// daemonWithGateway constructs a Daemon whose dispatch path can be
// exercised in isolation. The broker is wired in via d.gw without
// going through Run, so the test doesn't have to stand up a UDS
// listener or a state.db.
func daemonWithGateway(t *testing.T, cfg config.GatewayConfig, broker *gateway.Broker) *Daemon {
	t.Helper()
	d, err := New(Options{
		ConfigPath:     "unused-in-dispatch-test.yaml",
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.gatewayCfg = cfg
	d.gw = &gatewayRuntime{broker: broker}
	return d
}

func TestDispatchGatewayTest_DeliversThroughBroker(t *testing.T) {
	broker, adapter := newGatewayTestBroker(t, gateway.SourceTelegram)
	cfg := config.GatewayConfig{
		Enabled:  true,
		Telegram: config.TelegramConfig{Enabled: true},
	}
	d := daemonWithGateway(t, cfg, broker)

	resp := d.dispatch(Request{Cmd: "gateway-test", Channel: "telegram"})
	if !resp.Ok {
		t.Fatalf("expected ok=true, got %+v", resp)
	}
	if !strings.Contains(resp.Msg, "telegram") {
		t.Errorf("msg does not mention channel: %q", resp.Msg)
	}

	sent := adapter.Sent()
	if len(sent) != 1 {
		t.Fatalf("adapter received %d envelopes, want 1", len(sent))
	}
	env := sent[0]
	if env.Kind != gateway.OutboundNotification {
		t.Errorf("kind: want notification, got %q", env.Kind)
	}
	if env.Title != "carlos: gateway test" {
		t.Errorf("title: %q", env.Title)
	}
	if !strings.Contains(env.Body, "telegram") {
		t.Errorf("body does not mention channel: %q", env.Body)
	}
}

func TestDispatchGatewayTest_SignalIsStubOnly(t *testing.T) {
	broker, _ := newGatewayTestBroker(t, gateway.SourceTelegram)
	cfg := config.GatewayConfig{
		Enabled: true,
		Signal:  config.SignalConfig{Enabled: true},
	}
	d := daemonWithGateway(t, cfg, broker)

	resp := d.dispatch(Request{Cmd: "gateway-test", Channel: "signal"})
	if resp.Ok {
		t.Errorf("expected ok=false for signal stub, got %+v", resp)
	}
	if !strings.Contains(resp.Msg, "stub") {
		t.Errorf("msg should mention stub-only: %q", resp.Msg)
	}
}

func TestDispatchGatewayTest_UnknownChannel(t *testing.T) {
	broker, _ := newGatewayTestBroker(t, gateway.SourceTelegram)
	cfg := config.GatewayConfig{
		Enabled:  true,
		Telegram: config.TelegramConfig{Enabled: true},
	}
	d := daemonWithGateway(t, cfg, broker)

	resp := d.dispatch(Request{Cmd: "gateway-test", Channel: "bogus"})
	if resp.Ok {
		t.Errorf("expected ok=false for unknown channel, got %+v", resp)
	}
	if !strings.Contains(resp.Msg, "unknown channel") {
		t.Errorf("msg should call out unknown channel: %q", resp.Msg)
	}
}

func TestDispatchGatewayTest_ChannelDisabled(t *testing.T) {
	broker, _ := newGatewayTestBroker(t, gateway.SourceTelegram)
	cfg := config.GatewayConfig{
		Enabled: true,
		// Telegram absent from config -> Enabled=false.
	}
	d := daemonWithGateway(t, cfg, broker)

	resp := d.dispatch(Request{Cmd: "gateway-test", Channel: "telegram"})
	if resp.Ok {
		t.Errorf("expected ok=false for disabled channel, got %+v", resp)
	}
	if !strings.Contains(resp.Msg, "not enabled") {
		t.Errorf("msg should mention not enabled: %q", resp.Msg)
	}
}

func TestDispatchGatewayTest_MissingChannel(t *testing.T) {
	broker, _ := newGatewayTestBroker(t, gateway.SourceTelegram)
	d := daemonWithGateway(t, config.GatewayConfig{Enabled: true}, broker)

	resp := d.dispatch(Request{Cmd: "gateway-test", Channel: ""})
	if resp.Ok {
		t.Errorf("expected ok=false for empty channel, got %+v", resp)
	}
}

func TestDispatchGatewayTest_GatewayNotRunning(t *testing.T) {
	d, err := New(Options{
		ConfigPath:     "unused.yaml",
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// d.gw left nil — simulates the daemon being up but gateway disabled
	// in config.

	resp := d.dispatch(Request{Cmd: "gateway-test", Channel: "telegram"})
	if resp.Ok {
		t.Errorf("expected ok=false when gateway is not running, got %+v", resp)
	}
	if !strings.Contains(resp.Msg, "not running") {
		t.Errorf("msg should mention gateway not running: %q", resp.Msg)
	}
}

func TestDispatchGatewayTest_NoAdapterRegisteredForEnabledChannel(t *testing.T) {
	// Channel marked enabled in cfg, but the broker has no adapter for
	// it (the corresponding buildXAdapter never ran). Surfaces the same
	// failure path as "wired but broken" deployments.
	broker, _ := newGatewayTestBroker(t, gateway.SourceTelegram)
	cfg := config.GatewayConfig{
		Enabled: true,
		Ntfy:    config.NtfyGatewayConfig{Enabled: true},
	}
	d := daemonWithGateway(t, cfg, broker)

	resp := d.dispatch(Request{Cmd: "gateway-test", Channel: "ntfy"})
	if resp.Ok {
		t.Errorf("expected ok=false when adapter not registered, got %+v", resp)
	}
}
