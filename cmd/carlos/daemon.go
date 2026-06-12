// Phase 8a + 8b: carlos daemon + schedule CLI surfaces.
//
// `carlos daemon run` is the entry point the launchd / systemd unit
// invokes; the other verbs (enable/disable/status) talk to the running
// daemon over UDS or manage the platform-specific unit file.
//
// `carlos schedule list|add|rm` edits the user's config.yaml directly
// so the change is picked up by the daemon on its next SIGHUP /
// `daemon reload` IPC command.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/daemon"
	"github.com/georgebuilds/carlos/internal/farewell"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/anthropic"
	"github.com/georgebuilds/carlos/internal/providers/gemini"
	"github.com/georgebuilds/carlos/internal/providers/ollama"
	"github.com/georgebuilds/carlos/internal/providers/openai"
	"github.com/georgebuilds/carlos/internal/providers/openrouter"
	"github.com/georgebuilds/carlos/internal/schedule"
	"github.com/georgebuilds/carlos/internal/tools"
)

// runDaemon dispatches `carlos daemon <subcommand>`.
func runDaemon(args []string) error {
	if len(args) == 0 {
		return errors.New("daemon: subcommand required (run | enable | disable | status)")
	}
	switch args[0] {
	case "run":
		return runDaemonRun()
	case "enable":
		return runDaemonEnable()
	case "disable":
		return runDaemonDisable()
	case "status":
		return runDaemonStatus()
	default:
		return fmt.Errorf("daemon: unknown subcommand %q (expected run | enable | disable | status)", args[0])
	}
}

// runDaemonRun is what the launchd plist / systemd unit invokes. It
// blocks until SIGTERM/SIGINT cancel the daemon ctx OR an IPC `stop`
// command arrives.
func runDaemonRun() error {
	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("daemon run: load config: %w", err)
	}
	if !config.IsComplete(cfg) {
		return errors.New("daemon run: config incomplete (run `carlos onboard` first)")
	}

	d, err := buildDispatch(cfg, pleaseOptions{})
	if err != nil {
		return fmt.Errorf("daemon run: build dispatch: %w", err)
	}

	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".carlos", "state.db")

	if err := daemon.EnsureCarlosDir(); err != nil {
		return err
	}

	dmn, err := daemon.New(daemon.Options{
		ConfigPath:      cfgPath,
		StateDBPath:     dbPath,
		SocketPath:      daemon.DefaultSocketPath(),
		Provider:        d.provider,
		BaseTools:       tools.NewDefaultRegistryWithBaseDirAndFrames("", cfg.Vault, cfg.Frames, cfg.Frames.Active),
		TickInterval:    30 * time.Second,
		Notifier:        &daemon.SystemNotifier{}, // slice 8d: desktop banners on fire
		Home:            home,
		ProviderBuilder: buildProviderForFrame,
	})
	if err != nil {
		return fmt.Errorf("daemon run: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Fprintf(os.Stderr, "carlos daemon: starting (config=%s db=%s socket=%s)\n",
		cfgPath, dbPath, daemon.DefaultSocketPath())
	return dmn.Run(ctx)
}

// runDaemonEnable installs the platform unit and starts it. Persists
// the unit path in config.Daemon.UnitPath so `disable` knows what to
// remove later.
func runDaemonEnable() error {
	home, _ := os.UserHomeDir()
	unitPath, err := daemon.InstallUnit(home)
	if err != nil {
		return fmt.Errorf("daemon enable: %w", err)
	}
	// Persist daemon.enabled=true + unit_path for clean disable later.
	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("daemon enable: load config: %w", err)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.Daemon.Enabled = true
	cfg.Daemon.UnitPath = unitPath
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("daemon enable: save config: %w", err)
	}
	fmt.Printf("carlos daemon enabled: unit installed at %s\n", unitPath)
	return nil
}

// runDaemonDisable stops + removes the platform unit. Updates the
// config so daemon.enabled=false.
func runDaemonDisable() error {
	home, _ := os.UserHomeDir()
	if err := daemon.UninstallUnit(home); err != nil {
		return fmt.Errorf("daemon disable: %w", err)
	}
	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("daemon disable: load config: %w", err)
	}
	if cfg != nil {
		cfg.Daemon.Enabled = false
		cfg.Daemon.UnitPath = ""
		if err := config.Save(cfgPath, cfg); err != nil {
			return fmt.Errorf("daemon disable: save config: %w", err)
		}
	}
	fmt.Println("carlos daemon disabled")
	return nil
}

// warnGatewayOrphaned writes a one-line banner to stderr when the
// user has configured cfg.Gateway.Enabled but the daemon isn't
// running to act on it. Called from the TUI + `carlos please` entry
// points so the user doesn't silently miss notifications.
//
// We probe by dialing the daemon UDS. A successful dial implies a
// running daemon - that's enough; we don't need to round-trip a
// status request just to render the banner. Silent on success.
//
// Honest about the limitation: the gateway is single-owner by design
// (see internal/daemon/gateway.go), so a TUI-only session can't pick
// up the slack. The user has to start the daemon.
func warnGatewayOrphaned(cfg *config.Config) {
	if cfg == nil || !cfg.Gateway.Enabled {
		return
	}
	conn, err := daemon.Dial("")
	if err == nil {
		_ = conn.Close()
		return
	}
	fmt.Fprintln(os.Stderr,
		"carlos: gateway is configured but the daemon isn't running - push/HITL routing is off. "+
			"Start it with `carlos daemon enable` (installs auto-start) or `carlos daemon run` (foreground).")
}

// queueGatewayOrphaned is the farewell-panel-aware variant. Used by
// the TUI + headless entry points that route end-of-session notes
// through a single bordered box instead of bare stderr lines. Same
// probe as warnGatewayOrphaned (UDS dial); silent on success.
func queueGatewayOrphaned(cfg *config.Config, panel *farewell.Panel) {
	if cfg == nil || !cfg.Gateway.Enabled || panel == nil {
		return
	}
	conn, err := daemon.Dial("")
	if err == nil {
		_ = conn.Close()
		return
	}
	panel.AddWithDetail("🛰️", "daemon offline — push/HITL routing is off",
		"`carlos daemon enable` to auto-start it (or `carlos daemon run` foreground)")
}

// runDaemonStatus dials the running daemon's UDS and prints a human-
// readable summary. If no daemon is running, surfaces that cleanly.
func runDaemonStatus() error {
	conn, err := daemon.Dial("")
	if err != nil {
		fmt.Println("carlos daemon: not running (use `carlos daemon enable` to install + start it)")
		return nil
	}
	defer conn.Close()
	resp, err := daemon.SendRequest(conn, daemon.Request{Cmd: "status"})
	if err != nil {
		return fmt.Errorf("daemon status: %w", err)
	}
	if !resp.Ok {
		return fmt.Errorf("daemon status: %s", resp.Msg)
	}
	fmt.Println(resp.Msg)
	if resp.StartedAt != nil {
		fmt.Printf("  started: %s\n", resp.StartedAt.Local().Format("2006-01-02 15:04:05 MST"))
	}
	if resp.NextFireAt != nil {
		fmt.Printf("  next fire: %s\n", resp.NextFireAt.Local().Format("2006-01-02 15:04:05 MST"))
	}
	for _, s := range resp.Schedules {
		fmt.Printf("  - %-20s  %-20s  next=%s  once=%v\n",
			s.Name, s.Spec, s.NextFireAt.Local().Format("01-02 15:04"), s.Once)
		if s.LastRunAt != nil {
			fmt.Printf("       last=%s  ok=%v\n", s.LastRunAt.Local().Format("01-02 15:04"), s.LastRunOK)
		}
	}
	return nil
}

// --- carlos schedule ---------------------------------------------------

// runSchedule dispatches `carlos schedule <subcommand>`.
func runSchedule(args []string) error {
	if len(args) == 0 {
		return errors.New("schedule: subcommand required (list | add | rm)")
	}
	switch args[0] {
	case "list":
		return runScheduleList()
	case "add":
		return runScheduleAdd(args[1:])
	case "rm":
		return runScheduleRm(args[1:])
	default:
		return fmt.Errorf("schedule: unknown subcommand %q (expected list | add | rm)", args[0])
	}
}

// runScheduleList prints every configured schedule + its next fire
// time. Reads config straight off disk so the output reflects what
// the daemon will see on its next reload (whether the daemon is up
// or not).
func runScheduleList() error {
	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("schedule list: load config: %w", err)
	}
	if len(cfg.Schedules) == 0 {
		fmt.Println("no schedules configured (try `carlos schedule add \"every weekday at 9am\" \"summarize my unread Slack DMs\"`)")
		return nil
	}
	now := time.Now()
	for _, s := range cfg.Schedules {
		next := s.Next(now)
		fmt.Printf("- %-20s  %-20s  next=%s  once=%v\n",
			s.Name, s.Spec, next.Local().Format("2006-01-02 15:04"), s.Once)
		fmt.Printf("    prompt: %s\n", s.Prompt)
		if s.BudgetTokens > 0 || s.BudgetCents > 0 {
			fmt.Printf("    budget: tokens=%d cents=%d\n", s.BudgetTokens, s.BudgetCents)
		}
	}
	return nil
}

// runScheduleAdd accepts:
//
//	carlos schedule add <natural-language-when> <prompt words...>
//
// The first arg is the natural-language spec ("every weekday at 9am");
// remaining args are joined and used as the prompt. A name is auto-
// generated from a slug of the prompt + a timestamp suffix.
func runScheduleAdd(args []string) error {
	if len(args) < 2 {
		return errors.New(`schedule add: usage - carlos schedule add "<when>" <prompt...>`)
	}
	when := args[0]
	prompt := strings.Join(args[1:], " ")
	sch, err := schedule.ParseNatural(when)
	if err != nil {
		return fmt.Errorf("schedule add: %w", err)
	}
	sch.Prompt = prompt
	sch.Name = autoScheduleName(prompt)

	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("schedule add: load config: %w", err)
	}
	known := make(map[string]bool, len(cfg.Frames.List))
	for _, f := range cfg.Frames.List {
		known[f.Name] = true
	}
	if err := sch.Validate(known); err != nil {
		return fmt.Errorf("schedule add: %w", err)
	}
	for _, existing := range cfg.Schedules {
		if existing.Name == sch.Name {
			return fmt.Errorf("schedule add: name %q already in use", sch.Name)
		}
	}
	cfg.Schedules = append(cfg.Schedules, sch)
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("schedule add: save config: %w", err)
	}
	fmt.Printf("added schedule %q (spec=%q)\n", sch.Name, sch.Spec)
	if !signalDaemonReload() {
		fmt.Println("  (daemon not running - it'll pick this up on next start)")
	}
	return nil
}

// runScheduleRm removes the named schedule (or prints an error if not
// present) and saves the config back.
func runScheduleRm(args []string) error {
	if len(args) != 1 {
		return errors.New("schedule rm: usage - carlos schedule rm <name>")
	}
	name := args[0]
	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("schedule rm: load config: %w", err)
	}
	out := cfg.Schedules[:0]
	found := false
	for _, s := range cfg.Schedules {
		if s.Name == name {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		return fmt.Errorf("schedule rm: no schedule named %q", name)
	}
	cfg.Schedules = out
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("schedule rm: save config: %w", err)
	}
	fmt.Printf("removed schedule %q\n", name)
	if !signalDaemonReload() {
		fmt.Println("  (daemon not running - change applies on next start)")
	}
	return nil
}

// autoScheduleName derives a short slug from the prompt + a 4-char
// time suffix so multiple schedules with the same prompt don't
// collide. Slug rules: keep [a-zA-Z0-9], collapse other chars to '-',
// trim to 20 chars max, lowercase.
func autoScheduleName(prompt string) string {
	var b strings.Builder
	prev := byte(0)
	for i := 0; i < len(prompt) && b.Len() < 20; i++ {
		c := prompt[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
		default:
			if prev != '-' && b.Len() > 0 {
				b.WriteByte('-')
				c = '-'
			} else {
				continue
			}
		}
		prev = c
	}
	slug := strings.TrimRight(b.String(), "-")
	if slug == "" {
		slug = "sched"
	}
	suffix := fmt.Sprintf("%04d", time.Now().UnixNano()%10000)
	return slug + "-" + suffix
}

// signalDaemonReload best-effort tries to ping the running daemon to
// reload its config. Returns true iff the daemon was reachable and
// returned ok. Used by /schedule add|rm so the change picks up
// immediately rather than waiting for the daemon's next 30s tick.
func signalDaemonReload() bool {
	conn, err := daemon.Dial("")
	if err != nil {
		return false
	}
	defer conn.Close()
	resp, err := daemon.SendRequest(conn, daemon.Request{Cmd: "reload"})
	if err != nil {
		return false
	}
	return resp.Ok
}

// buildProviderForFrame mirrors buildDispatch's switch so the daemon's per-fire provider matches the chat path's set of backends.
func buildProviderForFrame(r frame.ResolvedProvider) (providers.Provider, error) {
	switch r.Provider {
	case "anthropic":
		return anthropic.New(r.APIKey), nil
	case "openai":
		return openai.New(r.APIKey), nil
	case "gemini":
		return gemini.New(r.APIKey), nil
	case "openrouter":
		return openrouter.New(r.APIKey), nil
	case "ollama":
		return ollama.New(r.BaseURL), nil
	default:
		return nil, fmt.Errorf("daemon: unknown provider %q", r.Provider)
	}
}

// _ keeps the json import live for a future enhancement (the daemon
// status verb may grow a `--json` flag).
var _ = json.Marshal

// --- carlos gateway ----------------------------------------------------

// validGatewayChannels is the surface advertised to the user when they
// pass a missing or unknown channel name. Kept in sync by hand with the
// gateway.Source set; signal stays in the list so the user sees the
// stub-only response rather than a "unknown channel" error.
var validGatewayChannels = []string{"ntfy", "telegram", "signal", "custom"}

// runGateway dispatches `carlos gateway <subcommand>`. v0 ships one
// subcommand (test); the verb-style switch leaves room for future
// gateway-namespaced verbs (e.g. `carlos gateway status`) without
// breaking the CLI shape.
func runGateway(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("gateway: subcommand required (test <%s>)", strings.Join(validGatewayChannels, " | "))
	}
	switch args[0] {
	case "test":
		return runGatewayTest(args[1:])
	case "add":
		return runGatewayAdd(args[1:])
	default:
		return fmt.Errorf("gateway: unknown subcommand %q (expected: test, add)", args[0])
	}
}

// runGatewayTest sends a fixed test envelope through one named gateway
// channel by issuing a `gateway-test` verb against the running daemon's
// UDS. Surfaces a friendly error when the channel is missing/unknown or
// the daemon isn't reachable.
func runGatewayTest(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("gateway test: channel required (one of: %s)", strings.Join(validGatewayChannels, ", "))
	}
	channel := args[0]
	if !isValidGatewayChannel(channel) {
		return fmt.Errorf("gateway test: unknown channel %q (one of: %s)", channel, strings.Join(validGatewayChannels, ", "))
	}
	conn, err := daemon.Dial("")
	if err != nil {
		return errors.New("daemon not running. Run `carlos daemon enable` first")
	}
	defer conn.Close()
	resp, err := daemon.SendRequest(conn, daemon.Request{Cmd: "gateway-test", Channel: channel})
	if err != nil {
		return fmt.Errorf("gateway test: %w", err)
	}
	if !resp.Ok {
		return errors.New(resp.Msg)
	}
	fmt.Println(resp.Msg)
	return nil
}

// isValidGatewayChannel reports whether name is one of the recognised
// gateway channel surfaces. Case-sensitive on purpose; config keys and
// Source constants are both lowercase.
func isValidGatewayChannel(name string) bool {
	for _, c := range validGatewayChannels {
		if c == name {
			return true
		}
	}
	return false
}
