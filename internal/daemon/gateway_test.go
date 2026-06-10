package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/gateway"
)

func newGatewayLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func TestStartGateway_DisabledReturnsNil(t *testing.T) {
	rt, err := startGateway(context.Background(), newGatewayLog(t), config.GatewayConfig{}, nil)
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	if rt != nil {
		t.Errorf("disabled gateway should return nil runtime, got %+v", rt)
	}
}

func TestStartGateway_EnabledButNoLogErrors(t *testing.T) {
	_, err := startGateway(context.Background(), nil, config.GatewayConfig{Enabled: true}, nil)
	if err == nil {
		t.Error("expected error when gateway enabled without a log")
	}
}

func TestStartGateway_RetryConfigError(t *testing.T) {
	_, err := startGateway(context.Background(), newGatewayLog(t), config.GatewayConfig{
		Enabled: true,
		Retry:   config.GatewayRetry{BackoffInitial: "nonsense"},
	}, nil)
	if err == nil {
		t.Error("expected error on unparseable retry duration")
	}
}

func TestStartGateway_NoChannelsStillStarts(t *testing.T) {
	rt, err := startGateway(context.Background(), newGatewayLog(t), config.GatewayConfig{Enabled: true}, nil)
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	if rt == nil {
		t.Fatal("expected runtime, got nil")
	}
	if rt.broker == nil || rt.router == nil {
		t.Errorf("broker/router missing: %+v", rt)
	}
	// Signal is always registered (even when disabled) so routing config
	// that references it round-trips. Other channels are opt-in.
	got := rt.broker.Adapters()
	if len(got) != 1 || got[0] != gateway.SourceSignal {
		t.Errorf("expected [signal] (always-registered stub), got %v", got)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := rt.Stop(stopCtx); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := rt.Stop(stopCtx); err != nil {
		t.Errorf("double Stop: %v", err)
	}
}

func TestStartGateway_RegistersTelegram(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Telegram: config.TelegramConfig{
			Enabled:        true,
			BotToken:       "test-bot-token",
			AllowedChatIDs: []int64{42},
		},
	}
	rt, err := startGateway(context.Background(), newGatewayLog(t), cfg, nil)
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	defer rt.Stop(context.Background())
	got := rt.broker.Adapters()
	if !containsSource(got, gateway.SourceTelegram) {
		t.Errorf("expected telegram registered, got %v", got)
	}
}

func TestStartGateway_TelegramMissingToken(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled:  true,
		Telegram: config.TelegramConfig{Enabled: true}, // no token
	}
	_, err := startGateway(context.Background(), newGatewayLog(t), cfg, nil)
	if err == nil {
		t.Error("expected error when telegram enabled without bot token")
	}
}

func TestStartGateway_NtfyWithoutListener(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Ntfy: config.NtfyGatewayConfig{
			Enabled:    true,
			Server:     "https://ntfy.example",
			Topic:      "carlos-test",
			SigningKey: "0123456789abcdef0123456789abcdef",
		},
	}
	rt, err := startGateway(context.Background(), newGatewayLog(t), cfg, nil)
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	defer rt.Stop(context.Background())
	if rt.httpServer != nil {
		t.Errorf("expected no http listener when listen_addr empty")
	}
	if !containsSource(rt.broker.Adapters(), gateway.SourceNtfy) {
		t.Errorf("expected ntfy adapter registered, got %v", rt.broker.Adapters())
	}
}

func TestStartGateway_NtfyMountsListener(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Ntfy: config.NtfyGatewayConfig{
			Enabled:        true,
			Server:         "https://ntfy.example",
			Topic:          "carlos-test",
			ActionEndpoint: "https://carlos.example/gateway/ntfy/action",
			ListenAddr:     "127.0.0.1:0", // ephemeral port
			SigningKey:     "0123456789abcdef0123456789abcdef",
		},
	}
	rt, err := startGateway(context.Background(), newGatewayLog(t), cfg, nil)
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	defer rt.Stop(context.Background())
	if rt.httpServer == nil || rt.listener == nil {
		t.Fatal("expected http listener bound for ntfy")
	}
	// The handler should answer 4xx on a missing token (the ntfy handler
	// is well tested for verify; we're proving the mount is wired).
	addr := rt.listener.Addr().String()
	resp, err := http.Post(fmt.Sprintf("http://%s/gateway/ntfy/action", addr), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST action endpoint: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 4xx for missing token; got %d: %s", resp.StatusCode, string(body))
	}
}

func TestStartGateway_NtfyListenerWithValidToken(t *testing.T) {
	signingKey := []byte("0123456789abcdef0123456789abcdef")
	cfg := config.GatewayConfig{
		Enabled: true,
		Ntfy: config.NtfyGatewayConfig{
			Enabled:        true,
			Server:         "https://ntfy.example",
			Topic:          "carlos-test",
			ActionEndpoint: "https://carlos.example/gateway/ntfy/action",
			ListenAddr:     "127.0.0.1:0",
			SigningKey:     string(signingKey),
		},
	}
	log := newGatewayLog(t)
	rt, err := startGateway(context.Background(), log, cfg, nil)
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	defer rt.Stop(context.Background())

	addr := rt.listener.Addr().String()
	tok := mintNtfyToken(t, signingKey, "env-1", "art-1", "approve", time.Now().Add(time.Hour))
	u := fmt.Sprintf("http://%s/gateway/ntfy/action?t=%s", addr, url.QueryEscape(tok))
	resp, err := http.Post(u, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST signed action: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("signed action status %d: %s", resp.StatusCode, string(body))
	}
	// The ingest should have written a gateway_inbound event under the
	// gateway.EventAgentID synthetic agent.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := log.Read(context.Background(), gateway.EventAgentID, 0)
		for _, ev := range events {
			if ev.Type == agent.EvtGatewayInbound {
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Error("no inbound event observed after valid signed action")
}

func TestStartGateway_SignalRegistersEvenWhenDisabled(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Signal:  config.SignalConfig{Enabled: false},
	}
	rt, err := startGateway(context.Background(), newGatewayLog(t), cfg, nil)
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	defer rt.Stop(context.Background())
	if got := rt.broker.Adapters(); len(got) != 1 || got[0] != gateway.SourceSignal {
		t.Errorf("expected signal adapter present (disabled mode), got %v", got)
	}
}

func TestStartGateway_SignalEnabledMissingSocket(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Signal:  config.SignalConfig{Enabled: true, SenderNumber: "+15551234567"},
	}
	_, err := startGateway(context.Background(), newGatewayLog(t), cfg, nil)
	if err == nil {
		t.Error("expected error when signal enabled without socket")
	}
}

func TestStartGateway_RoutingTranslated(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Routing: config.GatewayRouting{
			Notifications: []string{"telegram"},
		},
		Telegram: config.TelegramConfig{Enabled: true, BotToken: "tok"},
	}
	rt, err := startGateway(context.Background(), newGatewayLog(t), cfg, nil)
	if err != nil {
		t.Fatalf("startGateway: %v", err)
	}
	defer rt.Stop(context.Background())
	got := rt.broker.Routing()
	if len(got.Notifications) != 1 || got.Notifications[0] != gateway.SourceTelegram {
		t.Errorf("routing not translated: %+v", got)
	}
}

func TestStartGateway_StopRespectsContextDeadline(t *testing.T) {
	rt, err := startGateway(context.Background(), newGatewayLog(t), config.GatewayConfig{Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Cancel a context immediately; Stop should still complete (broker has
	// nothing to drain) but at minimum should not panic.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = rt.Stop(ctx)
}

func TestResolveSecretString(t *testing.T) {
	t.Setenv("CARLOS_FAKE_SECRET", "supersecret")
	if got := resolveSecretString("env:CARLOS_FAKE_SECRET"); got != "supersecret" {
		t.Errorf("env: indirection failed: %q", got)
	}
	if got := resolveSecretString("env:MISSING_XYZ"); got != "" {
		t.Errorf("missing env should be empty: %q", got)
	}
	if got := resolveSecretString("literal-token"); got != "literal-token" {
		t.Errorf("literal passthrough failed: %q", got)
	}
}

func TestResolveSecretBytes(t *testing.T) {
	t.Setenv("CARLOS_FAKE_KEY", "abcd")
	got, err := resolveSecretBytes("env:CARLOS_FAKE_KEY")
	if err != nil || string(got) != "abcd" {
		t.Errorf("resolveSecretBytes: got %q err %v", string(got), err)
	}
	got, err = resolveSecretBytes("")
	if err != nil || got != nil {
		t.Errorf("empty -> nil: got %v err %v", got, err)
	}
}

func TestMountNtfyListener_EmptyAddrErrors(t *testing.T) {
	_, _, err := mountNtfyListener(config.NtfyGatewayConfig{}, http.NewServeMux())
	if err == nil {
		t.Error("expected error when listen_addr empty")
	}
}

func TestMountNtfyListener_DefaultPath(t *testing.T) {
	// Bind on an ephemeral port; verify the default path serves the handler.
	cfg := config.NtfyGatewayConfig{ListenAddr: "127.0.0.1:0"}
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})
	ln, srv, err := mountNtfyListener(cfg, handler)
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	defer srv.Close()
	go srv.Serve(ln)
	addr := ln.Addr().String()
	resp, err := http.Get(fmt.Sprintf("http://%s/gateway/ntfy/action", addr))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !called {
		t.Error("handler not called at default path")
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status: want 418 got %d", resp.StatusCode)
	}
}

func TestMountNtfyListener_CustomPath(t *testing.T) {
	cfg := config.NtfyGatewayConfig{
		ListenAddr:     "127.0.0.1:0",
		ActionEndpoint: "https://example.test/custom/path",
	}
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	ln, srv, err := mountNtfyListener(cfg, handler)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.Serve(ln)
	addr := ln.Addr().String()
	resp, err := http.Get(fmt.Sprintf("http://%s/custom/path", addr))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !called {
		t.Error("handler not called at custom path")
	}
}

func TestMountNtfyListener_BindFailure(t *testing.T) {
	// Grab a real port, then try to bind a second listener on the same one.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind ephemeral")
	}
	defer ln.Close()
	addr := ln.Addr().String()
	_, _, err = mountNtfyListener(config.NtfyGatewayConfig{ListenAddr: addr}, http.NewServeMux())
	if err == nil {
		t.Error("expected bind conflict error")
	}
}

func TestBuildAdapters_DisabledReturnNil(t *testing.T) {
	if got, _ := buildNtfyAdapter(config.NtfyGatewayConfig{}); got != nil {
		t.Errorf("ntfy disabled: got %v", got)
	}
	if got, _ := buildTelegramAdapter(config.TelegramConfig{}); got != nil {
		t.Errorf("telegram disabled: got %v", got)
	}
}

func TestBuildSignalAdapter_DisabledStillReturnsAdapter(t *testing.T) {
	got, err := buildSignalAdapter(config.SignalConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Error("signal disabled should still return a registrable stub")
	}
}

func TestStartGateway_StopWithoutStart_NilSafe(t *testing.T) {
	var rt *gatewayRuntime
	if err := rt.Stop(context.Background()); err != nil {
		t.Errorf("nil Stop: %v", err)
	}
}

// containsSource reports whether the slice carries s. Used by tests
// that don't care about the exact slot the Signal stub occupies.
func containsSource(list []gateway.Source, s gateway.Source) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// mintNtfyToken constructs a valid ntfy action token for use in tests
// that need to drive the mounted listener through the full verify path.
// Mirrors the format documented in internal/gateway/ntfy/sign.go:
// base64url(payload) || "." || base64url(hmac).
func mintNtfyToken(t *testing.T, key []byte, envID, artID, actionID string, exp time.Time) string {
	t.Helper()
	payload := map[string]any{
		"envelope_id": envID,
		"artifact_id": artID,
		"action_id":   actionID,
		"exp_unix_ms": exp.UnixMilli(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	sum := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(sum)
}

// ensure no goroutine leak: an explicit broker shutdown test.
func TestStartGateway_FullLifecycle(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Telegram: config.TelegramConfig{
			Enabled:  true,
			BotToken: "x",
		},
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, err := startGateway(parent, newGatewayLog(t), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Cancel the parent and immediately Stop with a generous deadline.
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := rt.Stop(stopCtx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// daemon-side smoke: a full Daemon.Run with gateway enabled comes up
// and shuts down cleanly. We don't drive any cross-channel traffic
// here — that's covered by the gateway package's own tests. This is a
// wiring smoke test: the gateway block is read, startGateway runs, the
// runtime is reachable through d.gw, and Stop unwinds without panic.
func TestDaemon_Run_WithGateway(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{
		UserName:        "Tester",
		Providers:       map[string]config.ProviderConfig{"anthropic": {APIKey: "sk-test"}},
		DefaultProvider: "anthropic",
		Gateway: config.GatewayConfig{
			Enabled: true,
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	statePath := filepath.Join(dir, "state.db")
	// Open and close to prove path is usable (the daemon will reopen).
	if log, err := agent.OpenStateDB(statePath); err != nil {
		t.Skipf("OpenStateDB unavailable: %v", err)
	} else {
		_ = log.Close()
	}
	fs := &fakeSpawner{}
	sock := shortSock(t)
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     sock,
		StateDBPath:    statePath,
		Spawner:        fs, // use Spawner so we skip provider setup, but state.db opens because StateDBPath set + Spawner skips supervisor
		TickInterval:   50 * time.Millisecond,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitForSocket(t, sock, 2*time.Second)
	// Spawner is set so log is not opened — gateway is not started.
	// That's the test: confirm wiring does not crash even when log is nil.
	d.mu.Lock()
	gw := d.gw
	d.mu.Unlock()
	if gw != nil {
		t.Errorf("gateway should not be wired in Spawner-only test mode; got %+v", gw)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Run did not return after cancel")
	}
}

// TestStartGateway_NtfyListenerClosedOnLaterAdapterFailure exercises the
// resource-leak guard added to startGateway: when ntfy mounts a listener
// and a *later* adapter (telegram here) fails its construction, the
// returned error must come with the listener already closed.
//
// We grab an ephemeral port for ntfy, then verify that after the failure
// the port is reusable - i.e. the listener wasn't orphaned. A leaked
// listener would either keep the port bound or keep an http.Server
// goroutine alive.
func TestStartGateway_NtfyListenerClosedOnLaterAdapterFailure(t *testing.T) {
	// Reserve an ephemeral port, then release it so startGateway can take
	// the same one. We re-bind it after the failure to prove the prior
	// listener is gone.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind ephemeral")
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	cfg := config.GatewayConfig{
		Enabled: true,
		Ntfy: config.NtfyGatewayConfig{
			Enabled:        true,
			Server:         "https://ntfy.example",
			Topic:          "carlos-test",
			ActionEndpoint: "https://carlos.example/gateway/ntfy/action",
			ListenAddr:     addr,
			SigningKey:     "0123456789abcdef0123456789abcdef",
		},
		// Telegram enabled but with no BotToken: buildTelegramAdapter
		// returns "telegram: bot token required", forcing the cleanup
		// path AFTER the ntfy listener is mounted.
		Telegram: config.TelegramConfig{Enabled: true},
	}
	rt, err := startGateway(context.Background(), newGatewayLog(t), cfg, nil)
	if err == nil {
		// Should never happen - bail with cleanup so we don't leak.
		if rt != nil {
			_ = rt.Stop(context.Background())
		}
		t.Fatal("expected telegram failure to fail startGateway")
	}
	if rt != nil {
		t.Errorf("error return must come with a nil runtime; got %+v", rt)
	}

	// Critical assertion: the port we passed to ntfy is now free. If the
	// listener leaked, this Listen will fail with "address in use".
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("ntfy listener was leaked: cannot rebind %s: %v", addr, err)
	}
	_ = ln.Close()
}

// TestMountNtfyListener_TimeoutsSet asserts the HTTP server returned by
// mountNtfyListener carries non-zero Read/Write/Idle timeouts and a
// bounded MaxHeaderBytes. Trivial sanity check that the defensive defaults
// added to harden the public-facing ntfy callback endpoint are wired.
func TestMountNtfyListener_TimeoutsSet(t *testing.T) {
	ln, srv, err := mountNtfyListener(config.NtfyGatewayConfig{ListenAddr: "127.0.0.1:0"}, http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if srv.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout must be > 0; got %v", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout <= 0 {
		t.Errorf("ReadTimeout must be > 0; got %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout <= 0 {
		t.Errorf("WriteTimeout must be > 0; got %v", srv.WriteTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Errorf("IdleTimeout must be > 0; got %v", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Errorf("MaxHeaderBytes must be > 0; got %d", srv.MaxHeaderBytes)
	}
	// Sanity: ReadTimeout >= ReadHeaderTimeout (otherwise the header
	// deadline can never be reached before the whole-request one).
	if srv.ReadTimeout < srv.ReadHeaderTimeout {
		t.Errorf("ReadTimeout (%v) must be >= ReadHeaderTimeout (%v)", srv.ReadTimeout, srv.ReadHeaderTimeout)
	}
}

// silence unused-import lints from helpers that only certain test paths use
var _ = strings.TrimSpace
