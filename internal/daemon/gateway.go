// Daemon-side wiring for the messaging gateway. This file is the
// glue between ~/.carlos/config.yaml's `gateway:` block and the
// runtime broker + adapters in internal/gateway/*.
//
// Lifecycle:
//
//   - startGateway is called once from Daemon.Run after the event log
//     and supervisor are alive but before the tick loop starts. It
//     constructs the broker, builds + registers each enabled adapter,
//     binds the optional HTTP listener for ntfy action callbacks, and
//     kicks off the broker's Start + the approvals router's Run loops
//     in background goroutines. The returned gatewayRuntime is stashed
//     on the Daemon so Stop can unwind everything cleanly.
//
//   - (*gatewayRuntime).Stop cancels both goroutines, shuts the HTTP
//     server with the caller's context, and waits up to the same ctx
//     for the goroutines to drain. Idempotent.
//
// Gateway config is read at startup only. SIGHUP reloads schedules but
// does NOT rebuild the gateway — changing adapters mid-flight is
// nuanced (in-flight retries, decision races) and the v0 stance is
// "restart the daemon to pick up new channels". The CLI surfaces this
// in the reload response.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/gateway/approvals"
	"github.com/georgebuilds/carlos/internal/gateway/ntfy"
	"github.com/georgebuilds/carlos/internal/gateway/signal"
	"github.com/georgebuilds/carlos/internal/gateway/telegram"
)

// gatewayRuntime bundles every runtime piece the gateway owns. The
// Daemon stores one of these in d.gw for the lifetime of Run.
type gatewayRuntime struct {
	broker     *gateway.Broker
	router     *approvals.Router
	httpServer *http.Server
	listener   net.Listener

	cancel context.CancelFunc
	wg     sync.WaitGroup

	// once guards Stop so a SIGTERM + UDS-stop race doesn't double-close
	// the listener.
	once sync.Once
}

// startGateway is the daemon's entry point into the gateway subsystem.
// Returns (nil, nil) when cfg.Enabled is false — the caller should
// treat that as "gateway disabled, nothing to do".
//
// log MAY be nil in test mode; we error out in that case because the
// broker has no SQLite to write events to. parent is the daemon's
// shutdown context; the gateway derives its own child context so the
// daemon can cancel us in isolation if it ever wants to.
func startGateway(parent context.Context, log *agent.SQLiteEventLog, cfg config.GatewayConfig) (*gatewayRuntime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if log == nil {
		return nil, errors.New("gateway: log is required when gateway is enabled")
	}

	routing := gateway.RoutingFromConfig(cfg.Routing)
	retry, err := gateway.RetryFromConfig(cfg.Retry)
	if err != nil {
		return nil, fmt.Errorf("gateway: retry config: %w", err)
	}

	broker, err := gateway.New(gateway.Options{
		Log:     log,
		Routing: routing,
		Retry:   retry,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway: new broker: %w", err)
	}

	rt := &gatewayRuntime{broker: broker}

	// Construct + register each enabled adapter. We collect adapter
	// construction errors and bail out with cleanup so a half-wired
	// runtime never escapes this function.
	ntfyAdapter, err := buildNtfyAdapter(cfg.Ntfy)
	if err != nil {
		return nil, fmt.Errorf("gateway: ntfy: %w", err)
	}
	if ntfyAdapter != nil {
		if err := broker.Register(ntfyAdapter); err != nil {
			return nil, fmt.Errorf("gateway: register ntfy: %w", err)
		}
		if cfg.Ntfy.ListenAddr != "" {
			ln, srv, err := mountNtfyListener(cfg.Ntfy, ntfyAdapter.Handler())
			if err != nil {
				return nil, fmt.Errorf("gateway: ntfy listener: %w", err)
			}
			rt.listener = ln
			rt.httpServer = srv
		}
	}

	telegramAdapter, err := buildTelegramAdapter(cfg.Telegram)
	if err != nil {
		return nil, fmt.Errorf("gateway: telegram: %w", err)
	}
	if telegramAdapter != nil {
		if err := broker.Register(telegramAdapter); err != nil {
			return nil, fmt.Errorf("gateway: register telegram: %w", err)
		}
	}

	signalAdapter, err := buildSignalAdapter(cfg.Signal)
	if err != nil {
		return nil, fmt.Errorf("gateway: signal: %w", err)
	}
	if signalAdapter != nil {
		if err := broker.Register(signalAdapter); err != nil {
			return nil, fmt.Errorf("gateway: register signal: %w", err)
		}
	}

	router, err := approvals.New(approvals.Config{
		Log:    log,
		Broker: broker,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway: approvals router: %w", err)
	}
	rt.router = router

	// Derive a cancellable context for our background goroutines.
	ctx, cancel := context.WithCancel(parent)
	rt.cancel = cancel

	// HTTP listener first so a transient bind failure doesn't leave
	// goroutines orphaned. http.Server.Serve returns http.ErrServerClosed
	// on graceful shutdown — treat that as the happy path.
	if rt.httpServer != nil {
		rt.wg.Add(1)
		go func() {
			defer rt.wg.Done()
			if err := rt.httpServer.Serve(rt.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "carlos gateway: http serve: %v\n", err)
			}
		}()
	}

	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		if err := broker.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "carlos gateway: broker: %v\n", err)
		}
	}()

	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		if err := router.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "carlos gateway: approvals router: %v\n", err)
		}
	}()

	return rt, nil
}

// Stop cancels the goroutines and shuts the HTTP server. stopCtx
// bounds how long we wait for them to drain; if it expires first we
// return its error so the caller can log it.
func (rt *gatewayRuntime) Stop(stopCtx context.Context) error {
	if rt == nil {
		return nil
	}
	var stopErr error
	rt.once.Do(func() {
		if rt.cancel != nil {
			rt.cancel()
		}
		if rt.httpServer != nil {
			// Best-effort: a closed Listener turns Shutdown into a fast
			// no-op since Serve has already returned.
			_ = rt.httpServer.Shutdown(stopCtx)
		}
		if rt.broker != nil {
			_ = rt.broker.Stop(stopCtx)
		}
		done := make(chan struct{})
		go func() { rt.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-stopCtx.Done():
			stopErr = stopCtx.Err()
		}
	})
	return stopErr
}

// buildNtfyAdapter constructs an *ntfy.Adapter if the config block is
// enabled. Returns (nil, nil) when disabled so callers can skip the
// Register step. Secret indirection (env:VAR) is resolved here.
func buildNtfyAdapter(cfg config.NtfyGatewayConfig) (*ntfy.Adapter, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	signingKey, err := resolveSecretBytes(cfg.SigningKey)
	if err != nil {
		return nil, fmt.Errorf("signing_key: %w", err)
	}
	priorityMap := make(map[string]int, len(cfg.PriorityMap))
	for k, v := range cfg.PriorityMap {
		priorityMap[k] = v
	}
	headers := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		headers[k] = resolveSecretString(v)
	}
	return ntfy.New(ntfy.Config{
		Server:         cfg.Server,
		Topic:          resolveSecretString(cfg.Topic),
		Token:          resolveSecretString(cfg.Token),
		ActionEndpoint: cfg.ActionEndpoint,
		SigningKey:     signingKey,
		PriorityMap:    priorityMap,
		Headers:        headers,
	})
}

// buildTelegramAdapter constructs a *telegram.Adapter if the config
// block is enabled.
func buildTelegramAdapter(cfg config.TelegramConfig) (*telegram.Adapter, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	token := resolveSecretString(cfg.BotToken)
	allowed := append([]int64(nil), cfg.AllowedChatIDs...)
	return telegram.New(telegram.Config{
		BotToken:       token,
		APIBaseURL:     cfg.APIBaseURL,
		AllowedChatIDs: allowed,
		ParseMode:      cfg.ParseMode,
		PollTimeoutSec: cfg.PollTimeoutSec,
	})
}

// buildSignalAdapter constructs a *signal.Adapter. We always
// construct (passing Enabled through) so the broker can register the
// channel even when it's a no-op — routing configs that reference
// "signal" stay valid post-G6 without a config rewrite.
func buildSignalAdapter(cfg config.SignalConfig) (*signal.Adapter, error) {
	if !cfg.Enabled {
		// Disabled mode is still a registrable adapter (Send returns a
		// "disabled" receipt). Constructing it lets the broker carry the
		// channel so the user sees consistent routing behavior.
		return signal.New(signal.Config{})
	}
	return signal.New(signal.Config{
		Enabled:         true,
		SignalCLISocket: cfg.SignalCLISocket,
		SenderNumber:    cfg.SenderNumber,
	})
}

// mountNtfyListener binds an HTTP listener at cfg.ListenAddr and wires
// the adapter's action handler under the path component of
// ActionEndpoint (defaulting to /gateway/ntfy/action when the URL is
// not parseable).
//
// The daemon intentionally does NOT terminate TLS — Tailscale Funnel
// or a reverse proxy in front of this listener handles that. Carlos
// stays a single binary, no certificate management surface.
func mountNtfyListener(cfg config.NtfyGatewayConfig, handler http.Handler) (net.Listener, *http.Server, error) {
	if cfg.ListenAddr == "" {
		return nil, nil, errors.New("listen_addr required")
	}
	path := "/gateway/ntfy/action"
	if cfg.ActionEndpoint != "" {
		if u, err := url.Parse(cfg.ActionEndpoint); err == nil && u.Path != "" {
			path = u.Path
		}
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen %s: %w", cfg.ListenAddr, err)
	}
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return ln, srv, nil
}

// resolveSecretString resolves the "env:VAR_NAME" indirection used in
// config values that carry credentials. A plain string passes through
// unchanged; "env:FOO" reads from os.Getenv("FOO") and returns the
// value (empty if the env var is unset).
func resolveSecretString(v string) string {
	const prefix = "env:"
	if !strings.HasPrefix(v, prefix) {
		return v
	}
	return os.Getenv(strings.TrimPrefix(v, prefix))
}

// resolveSecretBytes is the []byte analogue of resolveSecretString.
// Used for the ntfy signing key, which is documented as 32+ bytes but
// users typically express as a string in env vars.
func resolveSecretBytes(v string) ([]byte, error) {
	s := resolveSecretString(v)
	if s == "" {
		return nil, nil
	}
	return []byte(s), nil
}
