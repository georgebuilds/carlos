// carlos — entrypoint.
//
// Subcommands:
//
//	carlos                       → if config is missing/incomplete, run
//	                               onboarding; otherwise drop into the TUI.
//	carlos please [-y] <prompt>  → headless tool-use loop. Joins the arg
//	                               string as the prompt, runs the model
//	                               with the bash tool registered, streams
//	                               text to stdout, prompts (or auto-
//	                               approves with -y/--yes) for each tool
//	                               call.
//	carlos chat                  → DEV-AID (Slice 1e): opens a temp event
//	                               log and drops into the chat TUI.
//	carlos manage                → DEV-AID (Slice 4):  opens a temp event
//	                               log, seeds 5-10 sample agents in various
//	                               states, drops into the manage TUI for
//	                               smoke-testing the supervisor view.
//	carlos onboard               → re-run onboarding, overwriting existing
//	                               config after a confirm prompt.
//	carlos daemon <run|enable|disable|status>
//	                             → Phase 8a daemon CLI. `run` is what the
//	                               platform unit calls; the others manage
//	                               the unit + dial the running daemon.
//	carlos schedule <list|add|rm>
//	                             → Phase 8b schedule CLI. Edits
//	                               ~/.carlos/config.yaml directly so the
//	                               daemon picks up changes on next reload.
//	carlos version               → print the build version string.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/memory"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/anthropic"
	"github.com/georgebuilds/carlos/internal/providers/gemini"
	"github.com/georgebuilds/carlos/internal/providers/ollama"
	"github.com/georgebuilds/carlos/internal/providers/openai"
	"github.com/georgebuilds/carlos/internal/providers/openrouter"
	"github.com/georgebuilds/carlos/internal/projectctx"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/sandbox"
	"github.com/georgebuilds/carlos/internal/theme"
	"github.com/georgebuilds/carlos/internal/tools"
	"github.com/georgebuilds/carlos/internal/tui/chat"
	"github.com/georgebuilds/carlos/internal/tui/chatglue"
	"github.com/georgebuilds/carlos/internal/tui/manage"
	"github.com/georgebuilds/carlos/internal/tui/onboarding"
	"github.com/georgebuilds/carlos/internal/usershell"
	"github.com/georgebuilds/carlos/internal/workspace"
)

// fallbackVersion is what we print when runtime/debug.ReadBuildInfo
// returns nothing useful — typically when carlos was built via
// `go run` or `go build` from a working tree rather than installed
// from a tag (`go install` / goreleaser). Production builds always
// have BuildInfo.Main.Version populated; this string is for dev.
const fallbackVersion = "dev"

// versionString resolves the build version. Order:
//
//  1. BuildInfo.Main.Version when set + not "(devel)" → real semver
//     (e.g. "v0.3.1" from `go install` or a goreleaser-stamped build).
//  2. The VCS commit hash from BuildInfo.Settings when available —
//     short form, helps when bisecting dev builds.
//  3. fallbackVersion as the universal default.
func versionString() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return fallbackVersion
	}
	v := bi.Main.Version
	if v != "" && v != "(devel)" {
		return v
	}
	// Dev build: try to surface the short commit + dirty flag so
	// `carlos version` reads as "dev (abc1234)" or "dev (abc1234-dirty)".
	var rev, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev != "" {
		short := rev
		if len(short) > 7 {
			short = short[:7]
		}
		out := fallbackVersion + " (" + short
		if modified == "true" {
			out += "-dirty"
		}
		return out + ")"
	}
	return fallbackVersion
}

// applyTheme loads the Phase 9 slice-9a palette from cfg + env and
// pushes it into every TUI sub-package's ApplyPalette entry point.
//
// Call site discipline: invoke this once per TUI mount (runDefault,
// runHeadless, runChatDevAid, runManageDevAid) before constructing
// any chat.Model / manage.Model / onboarding.Flow. Cheap (one env
// read, three field assignments per package).
//
// Pass a nil cfg when the config hasn't been loaded yet (early
// onboarding, please-mode without a config file); the palette
// autodetects from env only in that case.
func applyTheme(cfg *config.Config) {
	opts := theme.Options{}
	if cfg != nil {
		opts.ForcedVariant = cfg.Theme.Variant
		opts.AccentOverride = cfg.Theme.Accent
	}
	p := theme.Load(opts)
	chat.ApplyPalette(p)
	manage.ApplyPalette(p)
	onboarding.ApplyPalette(p)
}

func main() {
	args := os.Args[1:]
	// Phase R — session resume flags. Strip before the verb switch
	// so `carlos -c` / `carlos -r` land in the default chat path
	// instead of falling through to the help case. The flag is
	// ignored when followed by a verb (`carlos -c onboard` runs
	// onboarding); the default chat path is where it has meaning.
	resumeMode := ""
	if len(args) > 0 {
		switch args[0] {
		case "-c", "--continue":
			resumeMode = "continue"
			args = args[1:]
		case "-r", "--resume":
			resumeMode = "resume"
			args = args[1:]
		}
	}
	if len(args) > 0 {
		switch args[0] {
		case "version", "-v", "--version":
			fmt.Println("carlos " + versionString())
			return
		case "onboard":
			if err := runOnboard(true); err != nil {
				exit(err)
			}
			return
		case "please":
			// carlos please [-y|--yes] [-p|--provider <name>] [-m|--model <id>] <prompt words...>
			//
			// Flag parsing is intentionally hand-rolled (no flag package)
			// so the prompt can contain arbitrary words without quoting
			// rules — we strip recognized leading flags and treat the
			// remainder as the prompt.
			pleaseOpts, prompt, perr := parsePleaseArgs(args[1:])
			if perr != nil {
				fmt.Fprintln(os.Stderr, "carlos:", perr)
				os.Exit(2)
			}
			if strings.TrimSpace(prompt) == "" {
				fmt.Fprintln(os.Stderr, `carlos: "please" needs something to do — e.g. carlos please summarize ~/notes/today.md`)
				os.Exit(2)
			}
			if err := runHeadless(prompt, pleaseOpts); err != nil {
				exit(err)
			}
			return
		case "chat":
			// DEV-AID (Slice 1e): not a public surface. Opens a temp
			// SQLite event log, seeds a sample agent, and drops into the
			// chat TUI so the read/write paths can be smoke-tested by
			// hand. Removed or replaced when the real default-mode TUI
			// dispatch lands in a later slice.
			if err := runChatDevAid(); err != nil {
				exit(err)
			}
			return
		case "manage":
			// DEV-AID (Slice 4): not a public surface. Opens a temp
			// SQLite event log, seeds ~8 sample agents spanning the
			// SPEC state set + a 3-deep lineage, and drops into the
			// manage TUI so the roster / focus pane / verbs can be
			// smoke-tested by hand. Removed once the real default-mode
			// dispatch composes chat + manage + plan.
			if err := runManageDevAid(); err != nil {
				exit(err)
			}
			return
		case "approvals":
			// `carlos approvals list|accept|reject ...` — Phase 4h v0
			// CLI surface for the pending-approval queue. The TUI
			// pane that consumes the same agent.ListPendingApprovals
			// API lands in a Phase-4h-follow-up slice; this gives
			// scripts + the user a working accept/reject path today.
			if err := runApprovals(args[1:]); err != nil {
				exit(err)
			}
			return
		case "memory":
			// `carlos memory search <query>` — Phase 7h CLI surface
			// for the FTS5 summary index. Other memory subcommands
			// (list-facts, etc.) follow when needed.
			if err := runMemory(args[1:]); err != nil {
				exit(err)
			}
			return
		case "research-internal":
			// Phase 11 slice 11c — DEV-AID smoke harness for the
			// research orchestrator engine. The user-facing
			// `/research` slash command + `carlos research <q>`
			// headless variant land in slice 11f (see below).
			if err := runResearchInternal(args[1:]); err != nil {
				exit(err)
			}
			return
		case "research":
			// Phase 11 slice 11f — user-facing headless variant.
			// Mirrors runResearchInternal but with friendlier output,
			// a markdown report saved to ~/.carlos/research/<slug>-<ts>.md,
			// and a banner line that surfaces both on stderr and at
			// the end of stdout.
			if err := runResearch(args[1:]); err != nil {
				exit(err)
			}
			return
		case "daemon":
			// `carlos daemon run|enable|disable|status` — Phase 8a
			// CLI surface. `run` is what the launchd plist / systemd
			// unit calls; enable/disable manage the platform unit;
			// status dials the running daemon over UDS.
			if err := runDaemon(args[1:]); err != nil {
				exit(err)
			}
			return
		case "schedule":
			// `carlos schedule list|add|rm` — Phase 8b CLI surface.
			// Edits ~/.carlos/config.yaml directly so the change is
			// picked up by the daemon on next SIGHUP / reload.
			if err := runSchedule(args[1:]); err != nil {
				exit(err)
			}
			return
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}

	// Default: load config; trigger onboarding if missing/incomplete.
	path := config.DefaultPath()
	cfg, err := config.Load(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// First run — no message, just launch onboarding.
		if err := runOnboard(false); err != nil {
			exit(err)
		}
		return
	case err != nil:
		fmt.Fprintf(os.Stderr, "carlos: cannot read %s: %v\n", path, err)
		os.Exit(1)
	}

	if !config.IsComplete(cfg) {
		fmt.Fprintln(os.Stderr, "carlos: config incomplete, re-running onboarding")
		if err := runOnboard(false); err != nil {
			exit(err)
		}
		return
	}

	// Default-mode TUI: chat backed by the configured provider, with
	// `/agents` swapping to the manage TUI on the same state.db.
	//
	// Phase R: resolve the session BEFORE entering chat. Fresh by
	// default (empty sessionID → runDefault mints a new ULID); -c
	// resumes the most recent; -r opens a picker.
	sessionID, err := resolveSessionFromFlag(resumeMode)
	if err != nil {
		if errors.Is(err, errPickerCancelled) {
			// User backed out of the picker — exit 0, no message.
			return
		}
		exit(err)
	}
	if err := runDefault(cfg, sessionID); err != nil {
		exit(err)
	}
}

// resolveSessionFromFlag turns the -c / -r flag into an agent ID.
// Empty mode (default) returns "" so runDefault mints a fresh ULID.
// "continue" resolves the most recent session; "resume" opens the
// interactive picker. ErrNoSessions on either path degrades silently
// to "" so the user gets a fresh session without an annoying error.
func resolveSessionFromFlag(mode string) (string, error) {
	if mode == "" {
		return "", nil
	}
	ctx := context.Background()
	switch mode {
	case "continue":
		home, _ := os.UserHomeDir()
		dbPath := filepath.Join(home, ".carlos", "state.db")
		if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "carlos: no past sessions — starting a fresh one")
			return "", nil
		}
		log, err := agent.OpenStateDB(dbPath)
		if err != nil {
			return "", err
		}
		defer log.Close()
		sess, err := agent.MostRecentUserSession(ctx, log)
		if errors.Is(err, agent.ErrNoSessions) {
			fmt.Fprintln(os.Stderr, "carlos: no past sessions — starting a fresh one")
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return sess.ID, nil
	case "resume":
		id, err := runSessionPicker(ctx)
		if errors.Is(err, agent.ErrNoSessions) {
			fmt.Fprintln(os.Stderr, "carlos: no past sessions — starting a fresh one")
			return "", nil
		}
		return id, err
	}
	return "", nil
}

// runOnboard runs the six-screen flow and persists the resulting config.
// If force is true (carlos onboard), prompts before overwriting an existing
// config. If force is false (auto-trigger), runs unconditionally.
//
// A ctrl-c during the flow returns onboarding.ErrAborted, which we treat as
// a clean exit (code 0, no message) — the user opted out, no half-written
// state should remain.
func runOnboard(force bool) error {
	// Onboarding runs BEFORE the config exists, so apply with nil
	// (env-only autodetect: NO_COLOR + COLORFGBG). The post-onboarding
	// chat/manage paths re-apply with the user's saved Theme settings.
	applyTheme(nil)
	path := config.DefaultPath()
	if force && config.Exists(path) {
		if !confirmOverwrite(path) {
			fmt.Println("Aborted; existing config left in place.")
			return nil
		}
	}
	flow := onboarding.New()
	cfg, err := flow.Run()
	if err != nil {
		if errors.Is(err, onboarding.ErrAborted) {
			return nil
		}
		return err
	}
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if cfg.Daemon.Enabled {
		// We record the preference here; the platform unit install
		// (launchd / systemd) happens behind `carlos daemon enable`
		// because it may need to ask the user about an autostart slot.
		fmt.Fprintln(os.Stderr,
			"note: daemon preference saved — run `carlos daemon enable` to install the autostart unit.")
	}
	// Drop straight into the TUI so the user doesn't have to re-launch
	// to start working. This mirrors what the default-mode path does
	// when config already exists. Fresh-session — onboarding just
	// finished, the user's first launch deserves a clean slate.
	return runDefault(cfg, "")
}

// pleaseOptions collects parsed flags from `carlos please [flags] <prompt>`.
type pleaseOptions struct {
	autoApprove bool
	provider    string // "" means use cfg.DefaultProvider
	model       string // "" means use the provider's default
	// worktree opens a sandbox.Worktree at session start and registers
	// PlanTool. Off by default in v0 — opt-in until field experience
	// (Slice 7e/7f) shakes out the rough edges of the apply gate.
	worktree bool
	// Phase F-18: frame override for this invocation. "" falls through to
	// CARLOS_FRAME, then to the cwd-hint match, then to the persisted
	// active frame.
	frame string
}

// parseLeadingFrameFlag pulls a single optional "-f <name>" / "--frame
// <name>" off the front of args. Returns the resolved frame name (or ""
// when no flag was passed), the remaining args, and any error if the
// flag was used without a value. Used by subcommands whose only Phase F
// surface is the frame selector (e.g. carlos research). Subcommands
// with richer flag sets (please) parse -f inline in their own parser.
func parseLeadingFrameFlag(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", args, nil
	}
	switch args[0] {
	case "-f", "--frame":
		if len(args) < 2 {
			return "", nil, errors.New("--frame requires a name (e.g. personal, work)")
		}
		return args[1], args[2:], nil
	}
	return "", args, nil
}

// parsePleaseArgs strips recognized leading flags from args and returns
// the options plus the remaining joined prompt. Unknown leading tokens
// stop the scan — anything after is treated as part of the prompt
// (preserving the "no quoting rules" property of `carlos please`).
func parsePleaseArgs(args []string) (pleaseOptions, string, error) {
	var opts pleaseOptions
	for len(args) > 0 {
		switch args[0] {
		case "-y", "--yes":
			opts.autoApprove = true
			args = args[1:]
		case "-p", "--provider":
			if len(args) < 2 {
				return opts, "", errors.New("--provider requires a name (anthropic|openai|openrouter|ollama)")
			}
			opts.provider = args[1]
			args = args[2:]
		case "-m", "--model":
			if len(args) < 2 {
				return opts, "", errors.New("--model requires a model id")
			}
			opts.model = args[1]
			args = args[2:]
		case "-w", "--worktree":
			opts.worktree = true
			args = args[1:]
		case "-f", "--frame":
			if len(args) < 2 {
				return opts, "", errors.New("--frame requires a name (e.g. personal, work)")
			}
			opts.frame = args[1]
			args = args[2:]
		default:
			return opts, strings.Join(args, " "), nil
		}
	}
	return opts, "", nil
}

// dispatch is the resolved provider + model carlos please will use.
type dispatch struct {
	provider providers.Provider
	name     string
	model    string
}

// buildDispatch picks the provider based on (flag override → cfg.DefaultProvider
// → first configured), constructs the matching client from the per-provider
// config entry, and resolves the model the same way (flag → config → built-in
// default per provider).
func buildDispatch(cfg *config.Config, opts pleaseOptions) (*dispatch, error) {
	name := opts.provider
	if name == "" {
		name = cfg.DefaultProvider
	}
	if name == "" {
		// First configured wins. Iteration order is the YAML map order,
		// which Go randomizes — but with a single configured provider this
		// is deterministic, and with multiple the user explicitly picked
		// DefaultProvider, so we're only here in degenerate cases.
		for n, pc := range cfg.Providers {
			if pc.APIKey != "" || pc.BaseURL != "" {
				name = n
				break
			}
		}
	}
	if name == "" {
		return nil, errors.New("no provider configured — run `carlos onboard`")
	}

	pc, ok := cfg.Providers[name]
	if !ok || (pc.APIKey == "" && pc.BaseURL == "") {
		return nil, fmt.Errorf("provider %q not configured — run `carlos onboard`", name)
	}

	var p providers.Provider
	switch name {
	case "anthropic":
		p = anthropic.New(pc.APIKey)
	case "openai":
		p = openai.New(pc.APIKey)
	case "gemini":
		p = gemini.New(pc.APIKey)
	case "openrouter":
		p = openrouter.New(pc.APIKey)
	case "ollama":
		p = ollama.New(pc.BaseURL)
	default:
		return nil, fmt.Errorf("unknown provider %q (expected anthropic | openai | gemini | openrouter | ollama)", name)
	}

	model := opts.model
	if model == "" {
		model = pc.DefaultModel
	}
	if model == "" {
		model = providerDefaultModel(name)
	}
	return &dispatch{provider: p, name: name, model: model}, nil
}

// extractCapabilityBackends collapses a frame's full Capabilities map
// (capability name -> per-frame settings) into the flat
// capability->backend lookup the chat FrameUI needs for /capabilities.
// Settings without a `backend` key are dropped. Phase C-7.
func extractCapabilityBackends(f frame.Frame) map[string]string {
	if len(f.Capabilities) == 0 {
		return nil
	}
	out := make(map[string]string, len(f.Capabilities))
	for name, settings := range f.Capabilities {
		if settings == nil {
			continue
		}
		if v, ok := settings["backend"].(string); ok && v != "" {
			out[name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// providerDefaultModel is the last-resort model when neither --model nor
// cfg.Providers[name].DefaultModel is set. Onboarding already prompts for
// one; this is a belt-and-braces guard for a brand-new config.
func providerDefaultModel(name string) string {
	switch name {
	case "anthropic":
		return "claude-3-5-sonnet-latest"
	case "openai":
		return "gpt-4o"
	case "gemini":
		return "gemini-3.5-flash"
	case "openrouter":
		return "anthropic/claude-3.5-sonnet"
	case "ollama":
		return "llama3.1:latest"
	}
	return ""
}

// runHeadless dispatches a tool-use loop with no TUI: loads config,
// builds the registry (bash for now), constructs the provider client
// per the resolved dispatch, drives the loop until stop_reason !=
// "tool_use", streams text to stdout, prompts (or auto-approves) each
// tool call.
//
// SIGINT cancels the in-flight stream AND any running tool. The agent
// loop and provider goroutine return within a few ms once the context
// cancels.
func runHeadless(prompt string, opts pleaseOptions) error {
	path := config.DefaultPath()
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "carlos: run `carlos onboard` first — headless mode needs a configured provider.")
		os.Exit(1)
	}
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !config.IsComplete(cfg) {
		fmt.Fprintln(os.Stderr, "carlos: config incomplete — run `carlos onboard`.")
		os.Exit(1)
	}

	// Phase 9 slice 9a: pull cfg.Theme + env into every TUI surface
	// before anything renders. please-mode shows the chat-approver
	// prompt with these colors.
	applyTheme(cfg)
	warnGatewayOrphaned(cfg)

	d, err := buildDispatch(cfg, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "carlos:", err)
		os.Exit(1)
	}

	// State.db lifecycle (Phase 1h): open ~/.carlos/state.db (created
	// at 0700 if absent), recover any leftover non-terminal agents
	// from a prior process kill into `orphaned`. Logged but not auto-
	// retried — the user explicitly retries via the TUI.
	home, _ := os.UserHomeDir()
	migrateFrameLayout(home)
	// Phase F-17: resolve the active frame NAME early so the worktree
	// + usershell scoping below use frame-aware paths. The full
	// FrameInfo (with SystemPromptAppend) is computed lower; this is
	// just the name for path construction.
	activeFrameName := ""
	if dispatchCwd, derr := os.Getwd(); derr == nil {
		if res, ok := frame.ResolveActive(&cfg.Frames, frame.Input{
			Env:  os.Getenv("CARLOS_FRAME"),
			Flag: opts.frame,
			Cwd:  dispatchCwd,
		}); ok {
			activeFrameName = res.Frame
		}
	}
	dbPath := filepath.Join(home, ".carlos", "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		return fmt.Errorf("open state.db: %w", err)
	}
	defer log.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if report, err := agent.Recover(ctx, log); err == nil && len(report.Orphaned) > 0 {
		fmt.Fprintf(os.Stderr, "carlos: recovered %d orphaned agent(s) from prior session\n", len(report.Orphaned))
	}

	// Slice 7f: worktree-per-coding-task. When `-w/--worktree` is set
	// the bash + file tools resolve relative paths against the
	// sandbox dir so the model's edits land inside the worktree
	// instead of the user's cwd. Apply / Discard happen via the
	// approval gate (see worktree wiring below).
	var wt *sandbox.Worktree
	baseDir := ""
	if opts.worktree {
		repoRoot, err := gitRepoRoot()
		if err != nil {
			return fmt.Errorf("--worktree: %w", err)
		}
		// F-17: scope the worktree base to the active frame so each
		// frame's sub-agent sandboxes live alongside its other artifacts.
		// Empty frame falls back to the legacy location.
		wtBase := ""
		if activeFrameName != "" && home != "" {
			wtBase = frame.PathsFor(home, activeFrameName).WorktreesDir
		}
		wt, err = sandbox.NewWorktreeIn(repoRoot, "HEAD", wtBase)
		if err != nil {
			return fmt.Errorf("--worktree: open sandbox: %w", err)
		}
		baseDir = wt.Root
		// Discard the worktree on session-end unless an Accept fired
		// (the apply handler clears the supervisor mapping after a
		// successful Apply or Discard, so this is a no-op in that
		// case). Defensive: if anything else aborts the session, the
		// worktree directory still gets cleaned up.
		defer func() {
			_ = wt.Close()
		}()
	}

	// Base registry — what children inherit (filtered by their spawn
	// contract's tool_allowlist). Phase 7 adds the full coding-agent
	// tool set: bash + file ops + git read-only. The Agent tool
	// itself is added to the PARENT'S registry only (below), NOT the
	// base — children at depth 1 (the v0 cap) can't further spawn.
	baseReg := tools.NewDefaultRegistryWithBaseDirAndFrames(baseDir, cfg.Vault, cfg.Frames, cfg.Frames.Active)

	// Supervisor takes the resolved provider + base registry. Its
	// goroutine sweep + per-agent heartbeats run for the lifetime of
	// this process; Shutdown stops them cleanly on exit.
	sup := agent.NewSupervisor(log, d.provider, baseReg)
	sup.Run(ctx)
	defer sup.Shutdown()

	// Parent's registry = base + Agent tool (Phase 3e). The parent
	// can delegate to sub-agents; the description steers strongly
	// toward single-agent default per SPEC § goals.
	parentReg := tools.NewRegistry()
	for _, t := range baseReg.All() {
		parentReg.Register(t)
	}
	agentTool := agent.NewAgentTool(sup)
	parentReg.Register(agentTool)

	// Slice 7e: plan/preview/apply gate. With --worktree the parent
	// gets PlanTool so the model can queue its accumulated edits for
	// the user's review, and the apply handler watches the resolver
	// namespace to dispatch accept→Apply / reject→Discard.
	if opts.worktree && wt != nil {
		headlessID, err := seedHeadlessParentAgent(ctx, log, d.name, d.model)
		if err != nil {
			return fmt.Errorf("--worktree: seed parent: %w", err)
		}
		parentReg.Register(agent.NewPlanTool(headlessID, wt, log))
		sup.SetAgentWorktree(headlessID, wt)
		ah := &agent.ApplyHandler{Supervisor: sup, Log: log}
		go func() { _ = ah.Run(ctx) }()
	}

	toolSpecs := make([]providers.ToolSpec, 0, len(parentReg.All()))
	for _, t := range parentReg.All() {
		toolSpecs = append(toolSpecs, providers.ToolSpec{
			Name: t.Name(), Description: t.Description(), Schema: t.Schema(),
		})
	}

	approver := agent.Approver(agent.AutoApprover{})
	if !opts.autoApprove {
		approver = newStdinApprover()
	}
	// Phase T-1: wrap the fallback in the LayeredApprover so the
	// built-in read-only allowlist (notes_*, read, grep, glob, ls,
	// git_status, …) bypasses any prompt. Same policy in the headless
	// runDispatch path as in the chat path below — the model can't
	// learn one set of approval rules from one entry point and a
	// different set from another.
	layered := agent.NewLayeredApprover(approver, agent.DefaultBuiltinAllow, nil)
	// Phase T-2: wire workspace-trust. When the cwd is in the
	// trusted-workspaces store the policy allows a small set of
	// read-only bash verbs (git status/diff/log/…, ls, pwd, cat,
	// head, tail, wc, file, which, echo). cwd is normalized at
	// policy construction and held for the run's lifetime.
	if cwd, err := os.Getwd(); err == nil {
		layered.SetWorkspacePolicy(workspace.NewPolicy(
			workspace.NewStore(workspace.DefaultPath()), cwd,
		))
	}
	approver = layered

	// Surface which provider/model we're using on stderr so scripts and
	// users both see it.
	caps := d.provider.Capabilities()
	fmt.Fprintf(os.Stderr, "carlos: provider=%s model=%s (parallel-tool=%t, caching=%t, vision=%t)\n",
		d.name, d.model, caps.ParallelToolUse, caps.PromptCaching, caps.Vision)
	if wt != nil {
		fmt.Fprintf(os.Stderr, "carlos: worktree=%s branch=%s\n", wt.Root, wt.Branch)
	}

	// Identity prompt: same builder the chat path uses so headless
	// `carlos please ...` runs see the same "you are carlos"
	// framing and the same AGENTS.md / CLAUDE.md context.
	hcwd := ""
	hctx := ""
	if dispatchCwd, err := os.Getwd(); err == nil {
		hcwd = dispatchCwd
		if pc, err := projectctx.LoadFromCwd(dispatchCwd); err == nil && pc != nil {
			hctx = pc.Combined
		}
	}
	// Phase F-18: resolve the active frame for this please run. Honours
	// CARLOS_FRAME, then --frame, then cwd-hint match, then persisted
	// active. Headless run, no TTY picker — the persisted active is
	// the canonical fallback for cron / pipe invocations.
	pleaseFrameInfo := agent.FrameInfo{}
	if res, ok := frame.ResolveActive(&cfg.Frames, frame.Input{
		Env:  os.Getenv("CARLOS_FRAME"),
		Flag: opts.frame,
		Cwd:  hcwd,
	}); ok {
		if f := cfg.Frames.Find(res.Frame); f != nil {
			pleaseFrameInfo = agent.FrameInfo{Name: f.Name, Append: f.SystemPromptAppend}
			if opts.frame != "" || os.Getenv("CARLOS_FRAME") != "" {
				fmt.Fprintf(os.Stderr, "carlos: frame=%s (via %s)\n", res.Frame, res.Reason)
			}
		}
	}
	system := agent.SystemPromptWithFrame(cfg.UserName, hcwd, hctx, pleaseFrameInfo)

	initial := []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: prompt}}},
	}
	_, err = agent.Run(ctx, d.provider, parentReg, agent.LoopOptions{
		Model:    d.model,
		System:   system,
		Tools:    toolSpecs,
		Approver: approver,
		TextSink: os.Stdout,
	}, initial)
	// Newline keeps the terminal prompt clean regardless of how the loop
	// finished — the model's last text may not end with one.
	fmt.Println()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// runResearchInternal is the Phase 11 slice 11c smoke harness for the
// research orchestrator engine. Loads config, picks the default
// provider, wires the env-based search backend + WebFetch adapter,
// runs the six phases, prints the Report as markdown.
//
// The `-internal` suffix is deliberate: this is a dev surface for
// shaking out the engine. The user-facing `/research` slash command +
// `carlos research <q>` headless variant land in a follow-up slice
// that wires the engine into chat + the approval queue.
func runResearchInternal(args []string) error {
	question := strings.TrimSpace(strings.Join(args, " "))
	if question == "" {
		fmt.Fprintln(os.Stderr, `carlos: research-internal needs a question — e.g. carlos research-internal "what is the current state of WebGPU in Safari?"`)
		os.Exit(2)
	}
	path := config.DefaultPath()
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "carlos: run `carlos onboard` first — research-internal needs a configured provider.")
		os.Exit(1)
	}
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !config.IsComplete(cfg) {
		fmt.Fprintln(os.Stderr, "carlos: config incomplete — run `carlos onboard`.")
		os.Exit(1)
	}
	d, err := buildDispatch(cfg, pleaseOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "carlos:", err)
		os.Exit(1)
	}

	searchTool := tools.NewWebSearchTool()
	fetchTool := tools.NewWebFetchTool()
	engine := &research.Engine{
		Provider: d.provider,
		Model:    d.model,
		Search:   searchTool.Backend,
		Fetcher:  &research.WebFetchAdapter{Tool: fetchTool},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Fprintf(os.Stderr, "carlos: research-internal provider=%s model=%s search=%s\n",
		d.name, d.model, searchTool.Backend.Name())
	fmt.Fprintf(os.Stderr, "carlos: question=%q\n", question)

	report, runErr := engine.Run(ctx, question)
	if report != nil {
		fmt.Println(chat.RenderReportMarkdown(report))
	}
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) {
			return nil
		}
		fmt.Fprintf(os.Stderr, "carlos: research run ended with: %v\n", runErr)
	}
	return nil
}

// runResearch is the Phase 11 slice 11f user-facing headless command:
// `carlos research <question>`. Mirrors runResearchInternal's setup —
// load config, build dispatch, construct engine — but with friendlier
// stderr framing, and saves the rendered markdown report to
// ~/.carlos/research/<slug>-<unix-ts>.md so the user can reference it
// later or pipe to other tooling.
//
// Like research-internal, this is synchronous: the chat-side `/research`
// slash command shares the same engine but runs in a goroutine so the
// TUI stays interactive. The headless variant blocks because there's
// nothing else competing for the terminal — the user explicitly asked
// for a research arc.
func runResearch(args []string) error {
	frameOverride, rest, ferr := parseLeadingFrameFlag(args)
	if ferr != nil {
		fmt.Fprintln(os.Stderr, "carlos:", ferr)
		os.Exit(2)
	}
	question := strings.TrimSpace(strings.Join(rest, " "))
	if question == "" {
		fmt.Fprintln(os.Stderr, `carlos: research needs a question — e.g. carlos research "what is the current state of WebGPU in Safari?"`)
		os.Exit(2)
	}
	path := config.DefaultPath()
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "carlos: run `carlos onboard` first — research needs a configured provider.")
		os.Exit(1)
	}
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !config.IsComplete(cfg) {
		fmt.Fprintln(os.Stderr, "carlos: config incomplete — run `carlos onboard`.")
		os.Exit(1)
	}
	// F-17: resolve the active frame so the saved report lands under
	// ~/.carlos/frames/<frame>/research/. Also run the one-shot
	// migration so a standalone `carlos research` call before any chat
	// session still folds the legacy layout into the personal frame.
	rcwd, _ := os.Getwd()
	if home, herr := os.UserHomeDir(); herr == nil {
		migrateFrameLayout(home)
	}
	researchFrameName := ""
	if res, ok := frame.ResolveActive(&cfg.Frames, frame.Input{
		Env:  os.Getenv("CARLOS_FRAME"),
		Flag: frameOverride,
		Cwd:  rcwd,
	}); ok {
		researchFrameName = res.Frame
	}
	d, err := buildDispatch(cfg, pleaseOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "carlos:", err)
		os.Exit(1)
	}

	searchTool := tools.NewWebSearchTool()
	fetchTool := tools.NewWebFetchTool()
	// `carlos research` is user-initiated; the user typing the
	// command IS the consent the polite defaults are gating on.
	// Two adjustments the model's bash-tool path deliberately
	// doesn't make:
	//
	//  - Realistic browser User-Agent. Listing sites (Yelp,
	//    DoorDash, Superpages, YellowPages) return HTTP 403 to
	//    anything advertising as a bot — the model's tool calls
	//    keep the polite-bot UA so site logs see "carlos"
	//    clearly, but the user-facing research command uses a
	//    realistic Chrome-on-macOS UA to actually get content.
	//  - respect_robots = false. The polite-bot default fails
	//    most listing sites via robots.txt before the HTTP
	//    request even fires; without this the read phase has
	//    nothing to extract from.
	fetchTool.UserAgent = researchBrowserUA
	respectRobotsFalse := false
	engine := &research.Engine{
		Provider: d.provider,
		Model:    d.model,
		Search:   searchTool.Backend,
		Fetcher: &research.WebFetchAdapter{
			Tool:          fetchTool,
			RespectRobots: &respectRobotsFalse,
		},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Fprintf(os.Stderr, "carlos: provider=%s model=%s search=%s\n",
		d.name, d.model, searchTool.Backend.Name())

	// Live status panel via bubbletea inline (no AltScreen) —
	// matches the TUI feel without taking over the terminal. On
	// completion the panel clears itself and the rendered report
	// prints inline below.
	report, runErr := runResearchWithStatus(ctx, engine, question)
	if runErr != nil && errors.Is(runErr, context.Canceled) {
		return nil
	}
	if report == nil {
		// Engine.Run promises a non-nil report on every return path
		// where it could enter the phase loop; only nil-engine /
		// missing-input pre-flight errors leave it nil. Surface those
		// without crashing.
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "carlos: research failed: %v\n", runErr)
			return runErr
		}
		return errors.New("carlos: research produced no report (unexpected)")
	}
	rendered := chat.RenderReportMarkdown(report)
	fmt.Print(rendered)

	// Persist the markdown under the active frame's research dir so the
	// user has a stable artifact to share or revisit. Errors here are
	// surfaced but don't fail the whole command — the rendered report
	// is already on stdout and the user got what they asked for.
	saved, saveErr := saveResearchReport(question, rendered, researchFrameName, time.Now())
	if saveErr != nil {
		fmt.Fprintf(os.Stderr, "\ncarlos: could not save report to disk: %v\n", saveErr)
	} else {
		fmt.Printf("\nsaved to %s\n", saved)
	}

	if runErr != nil {
		// Engine returned partial Report + a non-cancellation error
		// (budget exceeded, phase failure). The rendered markdown
		// already encodes the Concerns; we just surface the top-line
		// error so scripts can see something went sideways.
		fmt.Fprintf(os.Stderr, "carlos: research run ended with: %v\n", runErr)
	}
	return nil
}

// buildResearchEngine constructs a *research.Engine from the tools the
// chat surface already has registered. Returns nil if either web tool
// is missing — the chat surface still works fine without /research, so
// the option-injection point treats nil as "feature disabled".
//
// Pulled out as a helper so cmd/carlos.runDefault stays under one
// screen and so a future daemon-side wiring (which uses a different
// registry) can call the same builder with its own *tools.Registry.
func buildResearchEngine(provider providers.Provider, model string, reg *tools.Registry) *research.Engine {
	if provider == nil || reg == nil {
		return nil
	}
	wsRaw, ok := reg.Get("web_search")
	if !ok {
		return nil
	}
	ws, ok := wsRaw.(*tools.WebSearchTool)
	if !ok || ws.Backend == nil {
		return nil
	}
	wfRaw, ok := reg.Get("web_fetch")
	if !ok {
		return nil
	}
	wf, ok := wfRaw.(*tools.WebFetchTool)
	if !ok {
		return nil
	}
	return &research.Engine{
		Provider:        provider,
		Model:           model,
		Search:          ws.Backend,
		Fetcher:         &research.WebFetchAdapter{Tool: wf},
		MaxSubQueries:   research.DefaultMaxSubQueries,
		SourcesPerQuery: research.DefaultSourcesPerQuery,
	}
}

// saveResearchReport writes the rendered markdown to
// ~/.carlos/frames/<frame>/research/<slug>-<unix-ts>.md (0600 perms
// inside a 0700 directory) and returns the absolute path. When
// frameName is empty, falls back to the legacy ~/.carlos/research/
// path so tests + callers that haven't been threaded through Phase F-17
// keep working. The slug + timestamp combo keeps successive runs of the
// same question distinguishable without requiring a per-run UUID —
// humans recognize the question text first and use the timestamp to
// pick the right version.
func saveResearchReport(question, markdown, frameName string, now time.Time) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	dir := researchDirFor(home, frameName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	name := slugifyQuestion(question) + "-" + fmt.Sprintf("%d", now.Unix()) + ".md"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(markdown), 0o600); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return path, nil
}

// researchDirFor returns the per-frame research output directory, or
// the legacy ~/.carlos/research/ path when frameName is empty.
func researchDirFor(home, frameName string) string {
	if frameName == "" {
		return filepath.Join(home, ".carlos", "research")
	}
	return frame.PathsFor(home, frameName).ResearchDir
}

// migrateFrameLayout runs the one-shot Phase F-17 migration that moves
// legacy ~/.carlos/{research,usershell,worktrees}/ into the personal
// frame's subtree. Idempotent: a re-run on already-migrated state is a
// silent no-op. Migrations that touch files emit a single stderr line
// so the user knows what carlos just did to their home dir.
//
// Empty home (UserHomeDir failed) is a hard skip: there's nothing to
// migrate and we shouldn't fabricate a path. Errors during migration
// are surfaced but never fatal — every file we can't move stays where
// it was and the user can re-run carlos after fixing the cause.
func migrateFrameLayout(home string) {
	if home == "" {
		return
	}
	report, err := frame.Migrate(home, frame.DefaultPersonalName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "carlos: frame migration error: %v\n", err)
		return
	}
	if report.HasMovement() {
		fmt.Fprintf(os.Stderr,
			"carlos: migrated to per-frame layout (research:%d jobs:%d worktrees:%d)\n",
			report.ResearchMoved, report.JobsMoved, report.WorktreesMoved,
		)
	}
	for _, e := range report.Errors {
		fmt.Fprintf(os.Stderr, "carlos: %v\n", e)
	}
}

// slugifyQuestion turns a free-form question into a filesystem-safe
// slug: lowercase, [a-z0-9] runs joined by '-', collapsed dashes, max
// 60 chars (so the full filename including the -<unix-ts>.md suffix
// stays well under common 255-byte filename limits). Empty input falls
// back to "research" — the caller still gets a usable name even when
// the question was somehow all punctuation.
func slugifyQuestion(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	var b strings.Builder
	prevDash := false
	for _, r := range q {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
		if b.Len() >= 60 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "research"
	}
	if len(slug) > 60 {
		slug = slug[:60]
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}

// gitRepoRoot returns the absolute path of the git repo containing the
// current working directory. Used by --worktree to anchor the sandbox.
// Returns a wrapped error when the cwd isn't inside a repo so the
// caller can surface a clean message to the user.
func gitRepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("not inside a git repo: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// seedHeadlessParentAgent inserts a minimal agent row + state-change
// event for the synthetic headless parent so the artifacts FK
// constraint is satisfied when PlanTool writes its diff + metadata
// artifacts. Returns the generated agent id.
//
// Slice 7e: PlanTool needs AgentID to be a real row. The headless `please`
// command otherwise has no persistent agent identity — agent.Run runs
// anonymously. Once a unified default-mode TUI lands, the parent agent
// the chat session represents will replace this seed.
func seedHeadlessParentAgent(ctx context.Context, log *agent.SQLiteEventLog, provider, model string) (string, error) {
	id := fmt.Sprintf("headless-%d", time.Now().UnixNano())
	title := "headless: " + provider
	payload, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: id, RootID: id, Title: title, Model: model,
	})
	if err != nil {
		return "", fmt.Errorf("marshal state-change: %w", err)
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: payload,
	}); err != nil {
		return "", fmt.Errorf("append state-change: %w", err)
	}
	now := time.Now().UTC()
	if err := log.InsertAgent(ctx, agent.AgentRow{
		ID: id, RootID: id, State: agent.StateRunning, Attempt: 1,
		Title: title, Model: model, CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		return "", fmt.Errorf("insert agent row: %w", err)
	}
	return id, nil
}

// stdinApprover prompts on stderr and reads y/N/Always from stdin. An
// "always" answer is remembered for the rest of the session — common
// pattern when a model wants to run a few related commands.
type stdinApprover struct {
	always map[string]bool
	rd     *bufio.Reader
}

func newStdinApprover() *stdinApprover {
	return &stdinApprover{
		always: map[string]bool{},
		rd:     bufio.NewReader(os.Stdin),
	}
}

func (a *stdinApprover) ApproveToolCall(name string, input []byte) bool {
	if a.always[name] {
		return true
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "carlos wants to run tool %q with input:\n  %s\n", name, string(input))
	fmt.Fprint(os.Stderr, "approve? [Y/n/a (always for this tool)]: ")
	line, err := a.rd.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	switch line {
	case "n", "no":
		return false
	case "a", "always":
		a.always[name] = true
		return true
	default:
		return true // empty / y / yes
	}
}

// runChatDevAid is a Slice-1e development aid — NOT a stable public
// surface. It opens (or creates) a temp SQLite event log under
// runDefault is the no-args TUI entrypoint: opens ~/.carlos/state.db,
// reuses or seeds the persistent default agent, wires chatglue.Loop so
// the configured provider drives the assistant turns, and drops into
// the chat TUI. On `/agents` the chat exits with OpenManageRequested
// and we relaunch into the manage TUI on the SAME state.db so the
// chat agent appears in the roster. Closing manage drops back to a
// fresh chat session (same agent id, same conversation).
//
// This replaces the "TUI not wired yet" placeholder. The full
// composed-single-program TUI (chat + manage + plan in one bubbletea
// Program) is a future polish slice; today's swap-on-/agents loop is
// the functional bridge.
func runDefault(cfg *config.Config, sessionID string) error {
	// Phase 9 slice 9a: load the user's theme before anything renders.
	applyTheme(cfg)
	warnGatewayOrphaned(cfg)
	home, _ := os.UserHomeDir()
	migrateFrameLayout(home)
	dbPath := filepath.Join(home, ".carlos", "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		return fmt.Errorf("open state.db: %w", err)
	}
	defer log.Close()

	d, err := buildDispatch(cfg, pleaseOptions{})
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if report, err := agent.Recover(ctx, log); err == nil && len(report.Orphaned) > 0 {
		fmt.Fprintf(os.Stderr, "carlos: recovered %d orphaned agent(s) from prior session\n", len(report.Orphaned))
	}

	// Phase R: fresh session per invocation by default. -c/-r flag
	// path resolved sessionID upstream; empty means "mint a new
	// ULID-keyed session". The conversation persists in the event
	// log forever; the user picks which one to continue via the
	// `carlos -r` picker or `/resume` slash command in chat.
	defaultAgentID := sessionID
	if defaultAgentID == "" {
		minted, err := mintSessionID(time.Now().UTC())
		if err != nil {
			return fmt.Errorf("mint session id: %w", err)
		}
		defaultAgentID = minted
	}
	if err := ensureDefaultAgent(ctx, log, defaultAgentID, d.name, d.model, cfg.UserName); err != nil {
		return err
	}

	// Phase F-11: thread the frame list + active frame through so the
	// notes_* / obsidian_* tools can default to the active frame's
	// vault_subtree and fan out across every configured frame on
	// cross-frame queries.
	baseReg := tools.NewDefaultRegistryWithBaseDirAndFrames("", cfg.Vault, cfg.Frames, cfg.Frames.Active)
	sup := agent.NewSupervisor(log, d.provider, baseReg)
	sup.Run(ctx)
	defer sup.Shutdown()

	// Keep the chat-default agent alive so Recover doesn't orphan
	// it on the next carlos invocation. The supervisor's heartbeat
	// ticker (5s emit, 10s stale tolerance) ticks for as long as
	// this process is up.
	sup.StartHeartbeat(ctx, defaultAgentID)

	// Phase 11 slice 11f: wire the research engine off the same
	// provider + web tools the chat already uses. nil-safe — the
	// chat-side /research handler echoes "not wired" when this is
	// missing, so a degenerate registry (e.g. without web tools)
	// just disables the feature rather than crashing.
	researchEngine := buildResearchEngine(d.provider, d.model, baseReg)

	src := chat.NewMemTextSource()
	approver := chat.NewTUIApprover()
	defer approver.Close()
	// Phase T-1/T-2: the loop sees a LayeredApprover that auto-
	// approves the hardcoded read-only allowlist (notes_*,
	// read/grep/glob/ls, git_status, …) AND — when the cwd is
	// trusted via the workspace store — a small set of read-only
	// bash verbs (git status/diff/log/…, ls, pwd, cat, head, tail,
	// wc, file, which, echo). Everything else falls through to the
	// TUI prompt. The TUI surface still gets the bare approver via
	// WithTUIApprover so the in-process y/N channel stays wired.
	layered := agent.NewLayeredApprover(approver, agent.DefaultBuiltinAllow, nil)
	var trustPolicy *workspace.Policy
	cwd, err := os.Getwd()
	if err == nil {
		trustPolicy = workspace.NewPolicy(
			workspace.NewStore(workspace.DefaultPath()), cwd,
		)
		layered.SetWorkspacePolicy(trustPolicy)
	}
	// Identity prompt: tells the model it is carlos (Gemini in
	// particular otherwise answers "I am Gemini" to "what's your
	// name?"). Also folds in AGENTS.md / CLAUDE.md from cwd up to
	// the git root via projectctx so the model sees house rules
	// without the user having to paste them per session.
	chatCwd := ""
	chatProjectCtx := ""
	if cwd != "" {
		chatCwd = cwd
		if pc, err := projectctx.LoadFromCwd(cwd); err == nil && pc != nil {
			chatProjectCtx = pc.Combined
		}
	}

	// Phase F: resolve the session's active frame. The chat path has no
	// CLI flag of its own (users switch with /frame), so ResolveActive
	// here walks CARLOS_FRAME env -> cwd_hint match -> persisted
	// active -> default. The frame supplies the sysprompt append + the
	// header pill. When migration has just synthesised a personal
	// frame, this always returns it.
	resolution, frameOK := frame.ResolveActive(&cfg.Frames, frame.Input{
		Env: os.Getenv("CARLOS_FRAME"),
		Cwd: cwd,
	})
	var activeFrame frame.Frame
	frameInfo := agent.FrameInfo{}
	frameUI := chat.FrameUI{}
	if frameOK {
		if f := cfg.Frames.Find(resolution.Frame); f != nil {
			activeFrame = *f
			frameInfo = agent.FrameInfo{
				Name:   activeFrame.Name,
				Append: activeFrame.SystemPromptAppend,
				Mode:   frame.EffectiveMode(activeFrame),
			}
			frameUI = chat.FrameUI{
				Active:    activeFrame.Name,
				Glyph:     activeFrame.Glyph,
				Accent:    activeFrame.Accent,
				Mode:      frame.EffectiveMode(activeFrame),
				Available: cfg.Frames.Names(),
				SwitchActive: func(name string) error {
					if cfg.Frames.Find(name) == nil {
						return fmt.Errorf("unknown frame: %s", name)
					}
					cfg.Frames.Active = name
					return config.Save(config.DefaultPath(), cfg)
				},
				SwitchMode: func(mode string) error {
					f := cfg.Frames.Find(activeFrame.Name)
					if f == nil {
						return fmt.Errorf("active frame %q vanished", activeFrame.Name)
					}
					f.Mode = mode
					return config.Save(config.DefaultPath(), cfg)
				},
				Capabilities: extractCapabilityBackends(activeFrame),
			}
		}
	}
	_ = activeFrame // referenced by Phase F-9 provider re-resolution slice

	systemPrompt := agent.SystemPromptWithFrame(cfg.UserName, chatCwd, chatProjectCtx, frameInfo)

	loop := chatglue.NewLoop(chatglue.Config{
		Provider: d.provider,
		Model:    d.model,
		Tools:    baseReg,
		Approver: layered,
		System:   systemPrompt,
	}, log, src, defaultAgentID)
	if err := loop.Start(ctx); err != nil {
		return err
	}
	defer loop.Stop()

	// Phase 9 slice 9j: build the summarizer the chat-side /compact verb
	// uses. The provider is always non-nil at this point (runDefault
	// returns early if buildDispatch failed), so we can always wire an
	// LLM-backed summarizer; chat.WithSummarizer's nil-check is the
	// safety net for tests + dev-aid callers, not this path.
	summarizer := memory.LLMSummarizer{Provider: d.provider, Model: d.model}

	// Phase U: user-shell manager scoped to this chat session. The
	// chat surface routes "!cmd" submissions here; the Manager writes
	// EvtUserShellStart/End events into the same log the chat is
	// already reading from, so the model context projection picks
	// them up on the next turn for free. F-17: per-job logs land
	// under the active frame's JobsDir.
	shellOpts := usershell.Options{Log: log}
	if activeFrame.Name != "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			shellOpts.OutputDir = frame.PathsFor(home, activeFrame.Name).JobsDir
		}
	}
	shellMgr := usershell.New(shellOpts)
	defer shellMgr.Close()
	// Phase U S7: separate ~/.carlos/shell-history file walked via
	// ↑/↓ in shell mode. Created lazily on first Add; reads on
	// startup so previous-session entries are available.
	shellHistory := usershell.NewHistory("")

	for {
		opts := []chat.Option{
			chat.WithTUIApprover(approver),
			chat.WithUserName(cfg.UserName),
			chat.WithSummarizer(summarizer),
			chat.WithUserShell(shellMgr),
			chat.WithShellHistory(shellHistory),
		}
		if frameUI.Active != "" {
			opts = append(opts, chat.WithFrame(frameUI))
		}
		if trustPolicy != nil {
			opts = append(opts, chat.WithWorkspacePolicy(trustPolicy))
		}
		if researchEngine != nil {
			opts = append(opts, chat.WithResearchEngine(researchEngine))
			// Phase 11 slice 11e: when the research engine is wired,
			// also wire the sub-agent spawner so /research takes the
			// async path. The spawner closes over the same log + engine
			// the chat already references; SpawnResearch handles the
			// projection-row insert + state-change emission itself, so
			// the chat goroutine only needs to drain the done channel.
			engine := researchEngine // capture for the closure
			opts = append(opts, chat.WithResearchSpawner(chat.SpawnFunc(
				func(ctx context.Context, q string) (string, <-chan research.ResearchResult, error) {
					return research.SpawnResearch(ctx, log, engine, q)
				},
			)))
		}
		m := chat.New(log, defaultAgentID, src, opts...)
		if _, err := m.Run(); err != nil {
			return fmt.Errorf("chat: %w", err)
		}
		if !m.OpenManageRequested() {
			return nil
		}
		// Swap to manage TUI on the same DB; on quit, loop back to
		// chat. The user gets a single-binary chat ⇄ manage flow
		// until the unified single-program TUI lands.
		mng := manage.New(manage.NewSQLiteSnapshotSource(log), log, sup)
		if _, err := mng.Run(); err != nil {
			return fmt.Errorf("manage: %w", err)
		}
	}
}

// ensureDefaultAgent seeds the agent row + state-change event for the
// stable default-mode agent id (first run), OR refreshes the row's
// state + heartbeat (subsequent runs). The refresh path matters
// because Recover orphans any non-terminal agent whose heartbeat is
// stale, and the chat-default agent is always stale at startup —
// there's no per-process supervisor heartbeat ticker for it. Without
// the refresh, every restart shows the chat header stuck on
// `[orphaned]`.
//
// The projection-only refresh (no state-change event in the log) is
// deliberate for the default chat agent: orphaned is terminal in the
// state machine, so there's no legal Transition out of it. The chat
// agent isn't a sub-agent with a meaningful lifecycle — it's a
// conversation handle. Direct projection edits are the right hammer.
func ensureDefaultAgent(ctx context.Context, log *agent.SQLiteEventLog, id, provider, model, userName string) error {
	now := time.Now().UTC()
	existing, err := log.Read(ctx, id, 0)
	if err != nil {
		return fmt.Errorf("read existing: %w", err)
	}
	if len(existing) > 0 {
		// Resume path: chat's in-memory projection replays the FULL
		// event log on backfill, so a previous-run heartbeat-lost →
		// orphaned event sticks unless we append a follow-up
		// transition event the projection respects. We bypass the
		// state machine's terminal-state guard (orphaned would
		// refuse Transition()) by writing the event directly — the
		// projection's Apply() is permissive about transition source
		// state, which is exactly what we want here. The chat-default
		// agent isn't a sub-agent with a meaningful lifecycle; it's
		// a conversation handle the user resumes.
		payload, err := agent.NewStateChangeTransition(agent.StateRunning)
		if err != nil {
			return fmt.Errorf("marshal resume transition: %w", err)
		}
		if _, err := log.Append(ctx, agent.Event{
			AgentID: id, TS: now, Type: agent.EvtStateChange, Payload: payload,
		}); err != nil {
			return fmt.Errorf("append resume transition: %w", err)
		}
		// Keep the projection cache (agents table) in sync so
		// manage's roster shows the right state too.
		if err := log.UpdateAgentState(ctx, id, agent.StateRunning, now); err != nil {
			return fmt.Errorf("refresh agent state: %w", err)
		}
		if err := log.UpdateHeartbeat(ctx, id, now); err != nil {
			return fmt.Errorf("refresh agent heartbeat: %w", err)
		}
		return nil
	}
	title := "chat with " + userName + " (" + provider + ")"
	payload, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: id, RootID: id, Title: title, Model: model,
	})
	if err != nil {
		return err
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: id, TS: now, Type: agent.EvtStateChange, Payload: payload,
	}); err != nil {
		return err
	}
	// Created leaves the projection in StateSpawning. Append the
	// transition to Running immediately so the chat header reflects
	// "active" instead of staying stuck at "◐ spawning" forever.
	// Same recipe the resume branch above uses — without this, every
	// fresh Phase R session showed the wrong badge.
	trans, err := agent.NewStateChangeTransition(agent.StateRunning)
	if err != nil {
		return fmt.Errorf("marshal initial transition: %w", err)
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: id, TS: now, Type: agent.EvtStateChange, Payload: trans,
	}); err != nil {
		return fmt.Errorf("append initial transition: %w", err)
	}
	return log.InsertAgent(ctx, agent.AgentRow{
		ID: id, RootID: id, State: agent.StateRunning, Attempt: 1,
		Title: title, Model: model, CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	})
}

// $TMPDIR/carlos-chat-devaid/state.db, seeds a sample agent if the log
// is empty, and drops into the chat TUI so the read-only viewer + input
// + slash dispatch can all be exercised by hand.
//
// Reasons this is dev-only:
//
//   - The default-mode TUI (carlos with no args) is owned by a later
//     slice and will compose chat + manage + plan into one Program.
//   - No provider dispatch happens here; submitted user messages just
//     land in the log. There's nobody listening on the other side.
//   - Slice 1f wires the real TextSource and the agent-loop seam; until
//     then, the assistant pane is empty.
//
// We mark the surface with a banner on stderr so anyone running it
// remembers it's a smoke harness, not the product.
func runChatDevAid() error {
	// Dev-aid: no cfg on disk; use env-only autodetect.
	applyTheme(nil)
	dir := filepath.Join(os.TempDir(), "carlos-chat-devaid")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("dev-aid mkdir: %w", err)
	}
	dbPath := filepath.Join(dir, "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		return fmt.Errorf("dev-aid open log: %w", err)
	}
	defer log.Close()

	const agentID = "01HVDEVDEVDEVDEVDEVDEVDEV1"
	// Seed once: if the log already has events for this id, skip.
	existing, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		return fmt.Errorf("dev-aid read: %w", err)
	}
	if len(existing) == 0 {
		if err := seedChatDevAid(log, agentID); err != nil {
			return fmt.Errorf("dev-aid seed: %w", err)
		}
	}

	fmt.Fprintln(os.Stderr, "carlos: chat dev-aid — Slice 1e smoke harness, not a public surface.")
	fmt.Fprintf(os.Stderr, "carlos:   db = %s\n", dbPath)
	fmt.Fprintf(os.Stderr, "carlos:   agent_id = %s\n", agentID)
	fmt.Fprintln(os.Stderr, "carlos:   ctrl-c to quit; /help inside the TUI for commands.")

	src := chat.NewMemTextSource()
	src.Append(agentID, "(no real provider wired yet — Slice 1f will plug in the stream.)")
	m := chat.New(log, agentID, src)
	if _, err := m.Run(); err != nil {
		return fmt.Errorf("dev-aid run: %w", err)
	}
	// Slice 7g: /agents inside chat sets OpenManageRequested(); honor
	// it by relaunching into the manage TUI. Dev-aid limitation: chat
	// and manage use different smoke-harness DBs, so the chat agent
	// won't appear in manage's roster — the swap mechanism still
	// exercises end-to-end; the shared-DB version lands with the
	// unified default-mode TUI.
	if m.OpenManageRequested() {
		fmt.Fprintln(os.Stderr, "carlos: /agents — switching to manage TUI.")
		return runManageDevAid()
	}
	return nil
}

// runManageDevAid is the Slice-4 development aid — NOT a stable
// surface. Opens a temp SQLite event log under
// $TMPDIR/carlos-manage-devaid/state.db, seeds ~8 sample agents
// covering most SPEC states plus a 3-level lineage, and drops into
// the manage TUI so the roster / focus pane / verbs can be exercised
// by hand.
//
// Pass a Supervisor=nil to the TUI so the verbs surface a "no
// supervisor wired" line in the status bar rather than fanning out
// to a real one. This is the smoke harness — not the product.
func runManageDevAid() error {
	// Dev-aid: no cfg on disk; use env-only autodetect.
	applyTheme(nil)
	dir := filepath.Join(os.TempDir(), "carlos-manage-devaid")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("dev-aid mkdir: %w", err)
	}
	dbPath := filepath.Join(dir, "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		return fmt.Errorf("dev-aid open log: %w", err)
	}
	defer log.Close()

	if err := seedManageDevAid(log); err != nil {
		return fmt.Errorf("dev-aid seed: %w", err)
	}

	fmt.Fprintln(os.Stderr, "carlos: manage dev-aid — Slice 4 smoke harness, not a public surface.")
	fmt.Fprintf(os.Stderr, "carlos:   db = %s\n", dbPath)
	fmt.Fprintln(os.Stderr, "carlos:   ctrl-c to quit; verbs (s/i/x) surface 'not implemented' until Slice 3 verbs land.")

	src := manage.NewSQLiteSnapshotSource(log)
	m := manage.New(src, log, nil)
	if _, err := m.Run(); err != nil {
		return fmt.Errorf("dev-aid run: %w", err)
	}
	return nil
}

// seedManageDevAid writes a representative roster: top-level "review
// branch" parent with two children (one awaiting input, one running),
// plus a handful of siblings in distinct states so the badge palette
// and sort behavior are visible at first paint.
//
// Idempotent: if the agent table already has rows we skip the
// re-seed. State.db is durable across runs of the dev-aid; the
// developer can `rm -rf $TMPDIR/carlos-manage-devaid` to reset.
func seedManageDevAid(log *agent.SQLiteEventLog) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	existing, err := log.NonTerminalAgents(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}

	type seed struct {
		ID, ParentID, Title, Model string
		State                      agent.State
		TokensIn, TokensOut        int64
		CostCents                  int64
	}
	root := "01HVDEV0DEV0DEV0DEV0DEV001"
	childA := "01HVDEV0DEV0DEV0DEV0DEV002"
	childB := "01HVDEV0DEV0DEV0DEV0DEV003"
	grand := "01HVDEV0DEV0DEV0DEV0DEV004"
	seeds := []seed{
		{ID: root, Title: "review the open PR branch", Model: "claude-4.7-sonnet", State: agent.StateRunning, TokensIn: 12_400, TokensOut: 3_120, CostCents: 47},
		{ID: childA, ParentID: root, Title: "summarize commits since divergence", Model: "claude-4.7-haiku", State: agent.StateAwaitingInput, TokensIn: 4_120, TokensOut: 880, CostCents: 8},
		{ID: childB, ParentID: root, Title: "diff vendored deps", Model: "claude-4.7-haiku", State: agent.StateRunning, TokensIn: 6_200, TokensOut: 1_400, CostCents: 12},
		{ID: grand, ParentID: childB, Title: "fetch tarball + sha256 each", Model: "claude-4.7-haiku", State: agent.StateBlocked, TokensIn: 220, TokensOut: 40, CostCents: 1},
		{ID: "01HVDEV0DEV0DEV0DEV0DEV005", Title: "sweep stale skill proposals", Model: "claude-4.7-haiku", State: agent.StatePausedByUser, TokensIn: 1_900, TokensOut: 320, CostCents: 4},
		{ID: "01HVDEV0DEV0DEV0DEV0DEV006", Title: "draft the weekly digest", Model: "gpt-4o", State: agent.StateCompacting, TokensIn: 28_400, TokensOut: 9_200, CostCents: 142},
		{ID: "01HVDEV0DEV0DEV0DEV0DEV007", Title: "rebuild the embedding index", Model: "llama3.1:8b", State: agent.StateDone, TokensIn: 800, TokensOut: 200, CostCents: 0},
		{ID: "01HVDEV0DEV0DEV0DEV0DEV008", Title: "verify the changelog against tags", Model: "claude-4.7-haiku", State: agent.StateOrphaned, TokensIn: 320, TokensOut: 80, CostCents: 1},
	}

	for _, s := range seeds {
		rootID := s.ParentID
		if rootID == "" {
			rootID = s.ID
		}
		payload, err := agent.NewStateChangeCreated(agent.AgentCreated{
			ID: s.ID, ParentID: s.ParentID, RootID: rootID, Title: s.Title, Model: s.Model,
		})
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if _, err := log.Append(ctx, agent.Event{
			AgentID: s.ID, TS: now, Type: agent.EvtStateChange, Payload: payload,
		}); err != nil {
			return err
		}
		row := agent.AgentRow{
			ID: s.ID, ParentID: s.ParentID, RootID: rootID,
			State: s.State, Attempt: 1, Title: s.Title, Model: s.Model,
			TokensIn: s.TokensIn, TokensOut: s.TokensOut, CostCents: s.CostCents,
			CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
		}
		if err := log.InsertAgent(ctx, row); err != nil {
			return err
		}
		if s.State != agent.StateSpawning {
			if err := log.UpdateAgentState(ctx, s.ID, s.State, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// seedChatDevAid writes a `created` event so the projection has a row
// and the header badge has something to display. Side-loads a user
// message so the transcript isn't empty on first paint.
func seedChatDevAid(log *agent.SQLiteEventLog, agentID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	created, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID:     agentID,
		RootID: agentID,
		Title:  "chat dev-aid",
		Model:  "fake:smoke",
	})
	if err != nil {
		return err
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtStateChange,
		Payload: created,
	}); err != nil {
		return err
	}
	return nil
}

// confirmOverwrite asks once before clobbering an existing config. We use
// a plain stdin scanner (not bubbletea) because this gate runs before the
// flow starts and we want a dead-simple "y/N" with no terminal state.
func confirmOverwrite(path string) bool {
	fmt.Printf("Overwrite existing %s? [y/N] ", path)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

func printUsage() {
	fmt.Println(`carlos — a general-purpose TUI agent

Usage:
  carlos                                   launch the TUI (runs onboarding first if needed)
  carlos please [flags] <prompt>           headless tool-use loop
    -y, --yes                                auto-approve tool calls (for scripts)
    -p, --provider <name>                    override default; one of:
                                             anthropic | openai | openrouter | ollama
    -m, --model <id>                         override the provider's default model
  carlos research <question>               deep-research a question end-to-end; saves the cited report to ~/.carlos/research/
  carlos approvals list                    list pending approvals (Phase 4h)
  carlos approvals accept <id> [note...]   accept a pending approval by artifact id
  carlos approvals reject <id> <reason...> reject a pending approval with a reason
  carlos daemon run                        run the background daemon (called by launchd/systemd)
  carlos daemon enable                     install + start the per-user daemon unit
  carlos daemon disable                    stop + uninstall the per-user daemon unit
  carlos daemon status                     show the running daemon's status + schedules
  carlos schedule list                     list configured scheduled runs
  carlos schedule add "<when>" <prompt>    add a schedule (e.g. "every weekday at 9am")
  carlos schedule rm <name>                remove a scheduled run by name
  carlos chat                              [dev-aid, Slice 1e] chat TUI against a temp log
  carlos manage                            [dev-aid, Slice 4]  manage TUI with seeded sample roster
  carlos onboard                           re-run the first-run setup flow
  carlos version                           print the build version
  carlos help                              this message

Examples:
  carlos please list the 5 largest files in my home dir
  carlos please -y "run the test suite and tell me which tests are slow"
  carlos please --provider openai --model gpt-4o "explain this diff"
  carlos approvals list
  carlos approvals accept 01HQ... "looks good"`)
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, "carlos:", err)
	os.Exit(1)
}

// runMemory dispatches `carlos memory search <query>` and friends.
// Memory persistence lives in ~/.carlos/state.db (same DB the event
// log uses); the memory package opens it shared.
func runMemory(args []string) error {
	if len(args) == 0 {
		return errors.New("memory: subcommand required (search)")
	}
	switch args[0] {
	case "search":
		query := strings.Join(args[1:], " ")
		if strings.TrimSpace(query) == "" {
			return errors.New("memory search: query required")
		}
		return memory.RunSearch(query, 10)
	default:
		return fmt.Errorf("memory: unknown subcommand %q (expected: search)", args[0])
	}
}

// runApprovals dispatches `carlos approvals <list|accept|reject> ...`.
// Reads/writes the user's actual state.db at ~/.carlos/state.db so the
// CLI surface composes naturally with the TUI: a script that scrubs
// approvals via the CLI, and a user reviewing the same queue in the
// manage view, both see the same source of truth.
func runApprovals(args []string) error {
	if len(args) == 0 {
		return errors.New("approvals: subcommand required (list | accept | reject)")
	}
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".carlos", "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		return fmt.Errorf("open state.db: %w", err)
	}
	defer log.Close()

	ctx := context.Background()
	switch args[0] {
	case "list":
		pending, err := agent.ListPendingApprovals(ctx, log)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			fmt.Println("no pending approvals")
			return nil
		}
		for _, p := range pending {
			fmt.Printf("%s  %s  [%s]  %s\n",
				p.Ref.ID,
				p.ProposedAt.Local().Format("2006-01-02 15:04"),
				p.Ref.Kind,
				p.Title,
			)
			fmt.Printf("         producer=%s  artifact=%s  size=%d\n",
				p.AgentID, p.Ref.Path, p.Ref.Size)
		}
		return nil
	case "accept":
		if len(args) < 2 {
			return errors.New("approvals accept: artifact ID required")
		}
		id := args[1]
		note := strings.Join(args[2:], " ")
		if _, err := agent.AcceptApproval(ctx, log, id, note); err != nil {
			return err
		}
		fmt.Printf("accepted: %s\n", id)
		return nil
	case "reject":
		if len(args) < 3 {
			return errors.New("approvals reject: artifact ID and reason required (reject <id> <reason words...>)")
		}
		id := args[1]
		reason := strings.Join(args[2:], " ")
		if _, err := agent.RejectApproval(ctx, log, id, reason); err != nil {
			return err
		}
		fmt.Printf("rejected: %s  (reason: %s)\n", id, reason)
		return nil
	default:
		return fmt.Errorf("approvals: unknown subcommand %q (expected list | accept | reject)", args[0])
	}
}
