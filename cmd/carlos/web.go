// web.go - the `carlos web` command. Serves the localhost HTTP + SSE
// console (a Vue SPA) as a projection over the agent event log, mirroring
// what the chat TUI is to the same log. v1 is read-only over the detached
// transcript + thread groups; the interactive attach path (W-2) swaps the
// read-only backend for a runtime-backed one. See the vault spec
// (web-spec.md) and web-implementation-plan.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/web"
)

// defaultWebPort is the fixed default (bookmarkable); override with
// --port. Leaning fixed-default per spec §15 Q6.
const defaultWebPort = 7777

// runWeb parses flags, opens the shared state.db, mints a per-launch
// token, and serves the console on 127.0.0.1. Blocks until SIGINT/SIGTERM,
// then shuts down gracefully.
func runWeb(args []string, cfg *config.Config) error {
	port := defaultWebPort
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port", "-p":
			if i+1 >= len(args) {
				return fmt.Errorf("--port needs a value")
			}
			p, err := strconv.Atoi(args[i+1])
			if err != nil || p < 1 || p > 65535 {
				return fmt.Errorf("invalid --port %q", args[i+1])
			}
			port = p
			i++
		default:
			return fmt.Errorf("unknown flag %q (try: carlos web [--port N])", args[i])
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home: %w", err)
	}
	dbPath := filepath.Join(home, ".carlos", "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		return fmt.Errorf("open state.db: %w", err)
	}
	defer log.Close()

	groups, err := web.OpenGroupStore(dbPath)
	if err != nil {
		return fmt.Errorf("open group store: %w", err)
	}
	defer groups.Close()

	token, err := web.NewToken()
	if err != nil {
		return fmt.Errorf("mint token: %w", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	srv := web.NewServer(web.Options{
		Log:       log,
		Groups:    groups,
		Token:     token,
		BoundAddr: addr,
		MetaFn:    func() web.Meta { return webMeta(cfg) },
	})

	// Wire the interactive backend (attach/send/approve over real chatglue
	// loops). On any failure, fall back to read-only so the console still
	// serves the transcript + groups.
	backend, berr := newCarlosBackend(ctx, cfg, log, srv)
	if berr != nil {
		fmt.Fprintf(os.Stderr, "carlos web: interactive backend unavailable (%v); serving read-only.\n", berr)
	} else {
		srv.SetBackend(backend)
		defer backend.Shutdown()
	}

	// Top-level mux: /api/* is token-gated (srv.Handler wraps the auth
	// middleware); everything else serves the embedded SPA without the
	// gate (the bundle is non-secret and bootstraps from the URL
	// fragment, D9).
	top := http.NewServeMux()
	top.Handle("/api/", srv.Handler())
	top.Handle("/", web.SPA())

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           top,
		ReadHeaderTimeout: 10 * time.Second,
	}

	mode := "interactive"
	if berr != nil {
		mode = "read-only"
	}
	url := fmt.Sprintf("http://%s/#token=%s", addr, token)
	bannerW, bannerTTY := stderrTerminalWidth()
	fmt.Fprintln(os.Stderr, webBanner(url, mode, bannerW, bannerTTY, webBannerPalette(cfg)))

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = httpSrv.Shutdown(shutCtx)
		fmt.Fprintln(os.Stderr, "carlos web - stopped.")
		return nil
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	}
}

// webMeta builds the /api/meta payload from config. Model/provider are
// resolved best-effort from the active frame's dispatch; failures leave
// them blank (cosmetic in the read-only surface).
func webMeta(cfg *config.Config) web.Meta {
	m := web.Meta{
		Version:     versionString(),
		Frames:      cfg.Frames.Names(),
		ActiveFrame: cfg.Frames.Active,
	}
	if d, err := buildDispatchForFrame(cfg, pleaseOptions{}, activeFrameForDispatch(cfg, "")); err == nil {
		m.Provider = d.name
		m.Model = d.model
	}
	return m
}
