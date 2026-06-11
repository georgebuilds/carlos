// carlos - entrypoint.
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
//	carlos gateway test <channel>
//	                             → Sends a fixed test notification through
//	                               one configured gateway channel
//	                               (ntfy | telegram | signal | custom) so
//	                               the user can verify their adapter wiring
//	                               without waiting for a scheduled run.
//	carlos version               → print the build version string.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/farewell"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/memory"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/anthropic"
	"github.com/georgebuilds/carlos/internal/providers/gemini"
	"github.com/georgebuilds/carlos/internal/providers/ollama"
	"github.com/georgebuilds/carlos/internal/providers/openai"
	"github.com/georgebuilds/carlos/internal/providers/openrouter"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/skills"
	"github.com/georgebuilds/carlos/internal/theme"
	"github.com/georgebuilds/carlos/internal/tools"
	"github.com/georgebuilds/carlos/internal/tui/chat"
	"github.com/georgebuilds/carlos/internal/tui/manage"
	"github.com/georgebuilds/carlos/internal/tui/onboarding"
)

// fallbackVersion is what we print when runtime/debug.ReadBuildInfo
// returns nothing useful - typically when carlos was built via
// `go run` or `go build` from a working tree rather than installed
// from a tag (`go install` / goreleaser). Production builds always
// have BuildInfo.Main.Version populated; this string is for dev.
const fallbackVersion = "dev"

// versionString resolves the build version. Order:
//
//  1. BuildInfo.Main.Version when set + not "(devel)" → real semver
//     (e.g. "v0.3.1" from `go install` or a goreleaser-stamped build).
//  2. The VCS commit hash from BuildInfo.Settings when available -
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

// stripResumeMode pulls a leading -c/--continue or -r/--resume flag off
// the argument list and reports which resume mode it selected.
//
// Phase R - session resume flags. Stripped before the verb switch so
// `carlos -c` / `carlos -r` land in the default chat path instead of
// falling through to the help case. The flag is only meaningful in the
// leading position; `carlos -c onboard` strips the -c and then runs
// onboarding (the mode is ignored by every non-default verb). Returns
// "" and the args untouched when no resume flag is present.
func stripResumeMode(args []string) (mode string, rest []string) {
	if len(args) > 0 {
		switch args[0] {
		case "-c", "--continue":
			return "continue", args[1:]
		case "-r", "--resume":
			return "resume", args[1:]
		}
	}
	return "", args
}

func main() {
	resumeMode, args := stripResumeMode(os.Args[1:])
	if len(args) > 0 {
		switch args[0] {
		case "version", "-v", "--version":
			fmt.Println("carlos " + versionString())
			return
		case "onboard":
			only, oerr := parseOnboardOnly(args[1:])
			if oerr != nil {
				fmt.Fprintln(os.Stderr, "carlos:", oerr)
				os.Exit(2)
			}
			if err := runOnboard(true, only); err != nil {
				exit(err)
			}
			return
		case "please":
			// carlos please [-y|--yes] [-p|--provider <name>] [-m|--model <id>] <prompt>
			//
			// Flag parsing is intentionally hand-rolled (no flag package)
			// so it stays bounded to the recognized flags above. The
			// prompt is exactly ONE positional argument after the
			// flags; multi-word prompts must be quoted by the shell.
			pleaseOpts, prompt, perr := parsePleaseArgs(args[1:])
			if perr != nil {
				fmt.Fprintln(os.Stderr, "carlos:", perr)
				os.Exit(2)
			}
			if strings.TrimSpace(prompt) == "" {
				fmt.Fprintln(os.Stderr, `carlos: "please" needs something to do - e.g. carlos please "summarize ~/notes/today.md"`)
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
			// `carlos approvals list|accept|reject ...` - Phase 4h v0
			// CLI surface for the pending-approval queue. The TUI
			// pane that consumes the same agent.ListPendingApprovals
			// API lands in a Phase-4h-follow-up slice; this gives
			// scripts + the user a working accept/reject path today.
			if err := runApprovals(args[1:]); err != nil {
				exit(err)
			}
			return
		case "memory":
			// `carlos memory search <query>` - Phase 7h CLI surface
			// for the FTS5 summary index. Other memory subcommands
			// (list-facts, etc.) follow when needed.
			if err := runMemory(args[1:]); err != nil {
				exit(err)
			}
			return
		case "research-internal":
			// Phase 11 slice 11c - DEV-AID smoke harness for the
			// research orchestrator engine. The user-facing
			// `/research` slash command + `carlos research <q>`
			// headless variant land in slice 11f (see below).
			if err := runResearchInternal(args[1:]); err != nil {
				exit(err)
			}
			return
		case "research":
			// Phase 11 slice 11f - user-facing headless variant.
			// Mirrors runResearchInternal but with friendlier output,
			// a markdown report saved to ~/.carlos/research/<slug>-<ts>.md,
			// and a banner line that surfaces both on stderr and at
			// the end of stdout.
			if err := runResearch(args[1:]); err != nil {
				exit(err)
			}
			return
		case "daemon":
			// `carlos daemon run|enable|disable|status` - Phase 8a
			// CLI surface. `run` is what the launchd plist / systemd
			// unit calls; enable/disable manage the platform unit;
			// status dials the running daemon over UDS.
			if err := runDaemon(args[1:]); err != nil {
				exit(err)
			}
			return
		case "schedule":
			// `carlos schedule list|add|rm` - Phase 8b CLI surface.
			// Edits ~/.carlos/config.yaml directly so the change is
			// picked up by the daemon on next SIGHUP / reload.
			if err := runSchedule(args[1:]); err != nil {
				exit(err)
			}
			return
		case "gateway":
			// `carlos gateway test <channel>` - round-trip a fixed test
			// envelope through one adapter so the user can verify their
			// notification wiring without waiting for a scheduled run.
			if err := runGateway(args[1:]); err != nil {
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
		// First run - no message, just launch onboarding.
		if err := runOnboard(false, ""); err != nil {
			exit(err)
		}
		return
	case err != nil:
		fmt.Fprintf(os.Stderr, "carlos: cannot read %s: %v\n", path, err)
		os.Exit(1)
	}

	if !config.IsComplete(cfg) {
		fmt.Fprintln(os.Stderr, "carlos: config incomplete, re-running onboarding")
		if err := runOnboard(false, ""); err != nil {
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
			// User backed out of the picker - exit 0, no message.
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
			fmt.Fprintln(os.Stderr, "carlos: no past sessions - starting a fresh one")
			return "", nil
		}
		log, err := agent.OpenStateDB(dbPath)
		if err != nil {
			return "", err
		}
		defer log.Close()
		sess, err := agent.MostRecentUserSession(ctx, log)
		if errors.Is(err, agent.ErrNoSessions) {
			fmt.Fprintln(os.Stderr, "carlos: no past sessions - starting a fresh one")
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return sess.ID, nil
	case "resume":
		id, err := runSessionPicker(ctx)
		if errors.Is(err, agent.ErrNoSessions) {
			fmt.Fprintln(os.Stderr, "carlos: no past sessions - starting a fresh one")
			return "", nil
		}
		return id, err
	}
	return "", nil
}

// parseOnboardOnly strips an optional --only <screen> flag from args.
// Returns the screen name (or "") and an error if the flag was used
// without a value.
func parseOnboardOnly(args []string) (string, error) {
	for len(args) > 0 {
		switch args[0] {
		case "--only", "-only":
			if len(args) < 2 {
				return "", errors.New("--only requires a screen name (name|providers|models|skills|vault|daemon|gateway)")
			}
			return args[1], nil
		default:
			return "", fmt.Errorf("unknown argument %q (expected --only <screen>)", args[0])
		}
	}
	return "", nil
}

// onboardScreenByName maps the --only flag value to the onboarding
// Screen enum. Names are the lowercase short form so a user typing
// `--only providers` lands on the provider screen without surprises.
func onboardScreenByName(name string) (onboarding.Screen, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "name":
		return onboarding.ScreenName, true
	case "providers", "provider":
		return onboarding.ScreenProvider, true
	case "models", "model":
		return onboarding.ScreenModel, true
	case "skills":
		return onboarding.ScreenSkills, true
	case "vault":
		return onboarding.ScreenVault, true
	case "daemon":
		return onboarding.ScreenDaemon, true
	case "gateway":
		return onboarding.ScreenGateway, true
	}
	return 0, false
}

// runGateway is the standalone `carlos gateway add` wizard. Runs the
// same gateway sub-flow the onboarding presents, but bypasses the
// "later or now" gate so the user lands directly on the enable / channel
// pickers. Result merges back into the existing config; other fields
// round-trip untouched.
func runGatewayAdd(args []string) error {
	_ = args
	applyTheme(nil)
	path := config.DefaultPath()
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("no config yet. Run `carlos onboard` first")
	}
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Daemon must be enabled for the gateway to do anything useful.
	// Warn the user instead of failing - they may be staging config
	// for a separate `carlos daemon enable` run.
	if !cfg.Daemon.Enabled {
		fmt.Fprintln(os.Stderr, "note: daemon is disabled. Run `carlos daemon enable` once the gateway is configured.")
	}
	flow := buildGatewayAddFlow(cfg)
	out, err := flow.Run()
	if err != nil {
		if errors.Is(err, onboarding.ErrAborted) {
			return nil
		}
		return err
	}
	if err := config.Save(path, out); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintln(os.Stderr, "gateway config updated:", path)
	return nil
}

// buildGatewayAddFlow constructs the onboarding Flow that the `carlos
// gateway add` wizard runs. Exposed as a helper so tests can assert on
// the constructed Flow's state without spinning up a bubbletea program.
// NewWithOptions auto-Primes the gateway past gwStageDecide when the
// caller passes Only + ScreenGateway + ExistingConfig, so the explicit
// PrimeGatewayStandalone call below is a defensive no-op covering the
// nil-cfg path (which auto-Prime does not).
func buildGatewayAddFlow(cfg *config.Config) *onboarding.Flow {
	flow := onboarding.NewWithOptions(onboarding.Options{
		StartingScreen: onboarding.ScreenGateway,
		Only:           true,
		ExistingConfig: cfg,
	})
	flow.PrimeGatewayStandalone()
	return flow
}

// buildOnboardOnlyFlow is the testable seam for `carlos onboard --only
// <screen>`. Loads the existing config (treating ENOENT as nil so a
// first-time --only run still works) and constructs the right Flow.
// Returns an error when the screen name is unknown.
func buildOnboardOnlyFlow(only, path string) (*onboarding.Flow, error) {
	screen, ok := onboardScreenByName(only)
	if !ok {
		return nil, fmt.Errorf("unknown screen %q (valid: name, providers, models, skills, vault, daemon, gateway)", only)
	}
	existing, err := config.Load(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return onboarding.NewWithOptions(onboarding.Options{
		StartingScreen: screen,
		Only:           true,
		ExistingConfig: existing,
	}), nil
}

// pleaseOptions collects parsed flags from `carlos please [flags] <prompt>`.
type pleaseOptions struct {
	autoApprove bool
	provider    string // "" means use cfg.DefaultProvider
	model       string // "" means use the provider's default
	// worktree opens a sandbox.Worktree at session start and registers
	// PlanTool. Off by default in v0 - opt-in until field experience
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

// parseLeadingFrameFilter is the FrameFilter-typed cousin of
// parseLeadingFrameFlag, used by `memory search` where all three
// query intents (any, named-with-legacy, unframed-only) are valid.
// Recognized forms (any leading position; the function consumes a
// single flag and returns):
//
//   - no flag: memory.AnyFrames()
//   - "-f <name>" / "--frame <name>" (name non-empty):
//     memory.InFrame(name)
//   - "--unframed": memory.Unframed()
//
// Error conditions:
//
//   - "-f" / "--frame" missing its value
//   - "-f \"\"" / "--frame \"\"" (empty name) - explicit error pointing
//     the user at --unframed
//   - both "-f" and "--unframed" together - mutually exclusive
//
// Returns the filter, the remaining args (with the recognized flag
// stripped), and any parse error.
func parseLeadingFrameFilter(args []string) (memory.FrameFilter, []string, error) {
	if len(args) == 0 {
		return memory.AnyFrames(), args, nil
	}
	// We accept one frame-related flag at the head. Two passes are
	// not needed because callers compose flags in a fixed leading
	// position for `memory search`.
	switch args[0] {
	case "-f", "--frame":
		if len(args) < 2 {
			return memory.FrameFilter{}, nil, errors.New("--frame requires a name (e.g. personal, work)")
		}
		name := args[1]
		if name == "" {
			return memory.FrameFilter{}, nil, errors.New("memory search: -f requires a non-empty frame name; pass --unframed for legacy/unframed rows")
		}
		// Reject the combo on the trailing args too: --unframed after
		// -f is the mutually-exclusive case.
		for _, a := range args[2:] {
			if a == "--unframed" {
				return memory.FrameFilter{}, nil, errors.New("memory search: -f and --unframed are mutually exclusive")
			}
		}
		return memory.InFrame(name), args[2:], nil
	case "--unframed":
		for _, a := range args[1:] {
			if a == "-f" || a == "--frame" {
				return memory.FrameFilter{}, nil, errors.New("memory search: -f and --unframed are mutually exclusive")
			}
		}
		return memory.Unframed(), args[1:], nil
	}
	return memory.AnyFrames(), args, nil
}

// parsePleaseArgs strips recognized leading flags from args and
// returns the options plus the prompt. The prompt is a SINGLE
// positional argument; multi-word prompts must be quoted by the
// shell.
//
// Rationale: previously the parser joined all trailing tokens with
// spaces, which is convenient for one-shot trivia but interacts
// poorly with -y/--yes (and with anything else that looks flag-ish
// in the middle of a prompt). The single-positional rule keeps the
// CLI consistent with every other carlos verb (`carlos research
// "..."`, `carlos memory search "..."`) and makes a forgotten quote
// loudly fail at the boundary instead of silently joining tokens.
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
			// Flags are done; the prompt is whatever's left. One
			// token = prompt. Two or more tokens = the user forgot
			// to quote a multi-word prompt; refuse with a clear hint
			// rather than concatenating in a way they didn't ask for.
			if len(args) > 1 {
				return opts, "", fmt.Errorf(
					"`carlos please` takes a single prompt; quote it: carlos please %q",
					strings.Join(args, " "),
				)
			}
			return opts, args[0], nil
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
	return buildDispatchForFrame(cfg, opts, nil)
}

// buildDispatchForFrame is the Phase F-9 variant. When activeFrame is
// non-nil, frame.ResolveProvider applies the frame's provider_override
// against the shared providers pantry so per-frame billing keys + base
// URLs win over the legacy cfg.Providers entry. nil activeFrame keeps
// the legacy pantry-only behaviour.
func buildDispatchForFrame(cfg *config.Config, opts pleaseOptions, activeFrame *frame.Frame) (*dispatch, error) {
	name := opts.provider
	if name == "" && activeFrame != nil {
		name = activeFrame.Provider
	}
	if name == "" {
		name = cfg.DefaultProvider
	}
	if name == "" {
		// First configured wins. Iteration order is the YAML map order,
		// which Go randomizes - but with a single configured provider this
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
		return nil, errors.New("no provider configured - run `carlos onboard`")
	}

	apiKey, baseURL, frameDefaultModel := resolveProviderCreds(cfg, name, activeFrame)
	if apiKey == "" && baseURL == "" {
		return nil, fmt.Errorf("provider %q not configured - run `carlos onboard`", name)
	}

	var p providers.Provider
	switch name {
	case "anthropic":
		p = anthropic.New(apiKey)
	case "openai":
		p = openai.New(apiKey)
	case "gemini":
		p = gemini.New(apiKey)
	case "openrouter":
		p = openrouter.New(apiKey)
	case "ollama":
		p = ollama.New(baseURL)
	default:
		return nil, fmt.Errorf("unknown provider %q (expected anthropic | openai | gemini | openrouter | ollama)", name)
	}

	model := opts.model
	if model == "" && activeFrame != nil {
		model = activeFrame.Model
	}
	if model == "" {
		model = frameDefaultModel
	}
	if model == "" {
		model = providerDefaultModel(name)
	}
	return &dispatch{provider: p, name: name, model: model}, nil
}

// activeFrameForDispatch returns a pointer to the active frame's record
// in cfg.Frames.List, honouring CARLOS_FRAME env + cwd + persisted-active
// resolution. Returns nil when frames aren't wired or the resolved name
// can't be found. nil is the legacy single-shelf signal - buildDispatch
// then uses the shared pantry without applying provider_override.
func activeFrameForDispatch(cfg *config.Config, flag string) *frame.Frame {
	if len(cfg.Frames.List) == 0 {
		return nil
	}
	cwd, _ := os.Getwd()
	res, ok := frame.ResolveActive(&cfg.Frames, frame.Input{
		Env:  os.Getenv("CARLOS_FRAME"),
		Flag: flag,
		Cwd:  cwd,
	})
	if !ok {
		return nil
	}
	return cfg.Frames.Find(res.Frame)
}

// resolveProviderCreds returns the resolved (api_key, base_url, default_model)
// for a provider, with the active frame's provider_override (Phase F-9)
// applied on top of the shared pantry. Returns zero strings when neither
// source has the provider configured; the caller treats that as "not
// configured" and prompts re-onboarding.
func resolveProviderCreds(cfg *config.Config, providerName string, activeFrame *frame.Frame) (apiKey, baseURL, defaultModel string) {
	pc, ok := cfg.Providers[providerName]
	if ok {
		apiKey = pc.APIKey
		baseURL = pc.BaseURL
		defaultModel = pc.DefaultModel
	}
	if activeFrame == nil {
		return apiKey, baseURL, defaultModel
	}
	if ov, ok := activeFrame.ProviderOverride[providerName]; ok {
		if ov.APIKey != "" {
			apiKey = ov.APIKey
		}
		if ov.BaseURL != "" {
			baseURL = ov.BaseURL
		}
		if ov.DefaultModel != "" {
			defaultModel = ov.DefaultModel
		}
	}
	return apiKey, baseURL, defaultModel
}

// modelCompletionsFor powers the /model slash autocomplete. The
// input is whatever the user has typed past "/model "; the output is
// a tab-completion list rendered in the suggest band. Branching:
//
//   - empty / no ":": list configured provider names suffixed with ":"
//     so a single Tab gets the user from "/model " to "/model <prov>:"
//     and the second character cycles to the model side.
//   - "<prov>:<frag>": list models for that provider whose ids
//     contain (or prefix) <frag>. We surface the provider's configured
//     default model plus the OpenRouter live catalog when available.
//
// Returns nil for the "no completions" cases so the suggest layer
// renders nothing.
func modelCompletionsFor(cfg *config.Config, partial string) []string {
	if cfg == nil {
		return nil
	}
	partial = strings.TrimSpace(partial)
	idx := strings.IndexByte(partial, ':')
	if idx < 0 {
		out := make([]string, 0, len(cfg.Providers))
		for _, name := range sortedProviderNamesForCompletion(cfg.Providers) {
			if partial == "" || strings.HasPrefix(name, strings.ToLower(partial)) {
				out = append(out, name+":")
			}
		}
		sort.Strings(out)
		return out
	}
	provider := strings.ToLower(strings.TrimSpace(partial[:idx]))
	frag := strings.TrimSpace(partial[idx+1:])
	models := knownModelsFor(cfg, provider)
	if len(models) == 0 {
		return nil
	}
	out := make([]string, 0, len(models))
	for _, m := range models {
		if frag == "" || strings.Contains(strings.ToLower(m), strings.ToLower(frag)) {
			out = append(out, provider+":"+m)
		}
	}
	sort.Strings(out)
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

// sortedProviderNamesForCompletion is the cmd-side mirror of
// chat.sortedProviderNames. Pulled out here to keep the chat package
// free of cfg.Providers map iteration order surprises in tests.
func sortedProviderNamesForCompletion(provs map[string]config.ProviderConfig) []string {
	out := make([]string, 0, len(provs))
	for n := range provs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// knownModelsFor returns the candidate model ids the autocomplete
// surfaces for a provider. For each provider we layer:
//
//  1. The configured DefaultModel (the model the user is on right now).
//  2. The curated onboarding list (a small hand-picked spread per
//     provider). Survives offline / first-run / missing-cache cases
//     where the on-disk catalog would yield nothing — field report:
//     "/model openrouter:<tab> only suggests the model I'm already
//     using" was exactly this gap, because the cache only populates
//     after a successful live fetch.
//  3. For OpenRouter only, the cached live catalog at
//     ~/.carlos/openrouter-models.json when available. Layered LAST
//     so curated suggestions still surface even when the live fetch
//     hasn't run.
//
// Failures here are silent — the user still gets at least the default
// model and the curated spread, so an unreadable cache file degrades
// to "no extra completions beyond the curated list" rather than
// blocking the swap.
func knownModelsFor(cfg *config.Config, provider string) []string {
	var out []string
	if pc, ok := cfg.Providers[provider]; ok && pc.DefaultModel != "" {
		out = append(out, pc.DefaultModel)
	}
	out = append(out, onboarding.CuratedModelSlugs(provider)...)
	if provider == "openrouter" {
		out = append(out, loadOpenRouterCatalog()...)
	}
	return dedupStrings(out)
}

// loadOpenRouterCatalog reads the cached OpenRouter model catalog at
// ~/.carlos/openrouter-models.json and returns the id list. The cache
// is populated by the onboarding "models" step + the openrouter
// provider's startup probe; we only READ here so a stale cache still
// powers the autocomplete (catalogs change rarely; the user can
// always type a model id directly). Returns nil on any read or parse
// failure — autocomplete just shows the default model in that case.
func loadOpenRouterCatalog() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".carlos", "openrouter-models.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// The on-disk shape carries more fields (pricing, context window);
	// we only need the id. A minimal struct keeps decode cheap and
	// future-proof.
	var doc struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	out := make([]string, 0, len(doc.Models))
	for _, m := range doc.Models {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out
}

// dedupStrings preserves first-occurrence order while dropping
// duplicates. Tiny helper for the autocomplete path so the configured
// default model + the cached catalog can both contribute without the
// user seeing the same id twice.
func dedupStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// summariseSkills projects the frame-applicable slice of a *skills.Library
// into the lightweight []agent.SkillSummary the sysprompt builder needs.
// Pulled out so the runtime + daemon share the same projection rule:
// "use ForFrame() so frames:-restricted skills stay scoped, then keep
// only the Name + Description so we don't ship the full body via the
// prompt". Returns nil for a nil library so callers can pass straight
// through to FrameInfo without a nil-check.
func summariseSkills(lib *skills.Library, frameName string) []agent.SkillSummary {
	if lib == nil {
		return nil
	}
	applicable := lib.ForFrame(frameName)
	if len(applicable) == 0 {
		return nil
	}
	out := make([]agent.SkillSummary, 0, len(applicable))
	for _, s := range applicable {
		if s == nil {
			continue
		}
		out = append(out, agent.SkillSummary{
			Name:        s.Name,
			Description: s.Description,
		})
	}
	return out
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

// buildResearchEngine constructs a *research.Engine from the tools the
// chat surface already has registered. Returns nil if either web tool
// is missing - the chat surface still works fine without /research, so
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
// same question distinguishable without requiring a per-run UUID -
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
// are surfaced but never fatal - every file we can't move stays where
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

// queueFrameMigration runs the same Phase F-17 migration but pushes
// its summary into a farewell.Panel for the end-of-session bordered
// box instead of emitting bare stderr lines at startup. Errors still
// go to stderr immediately because they want visibility BEFORE the
// TUI runs, not after.
func queueFrameMigration(home string, panel *farewell.Panel) {
	if home == "" {
		return
	}
	report, err := frame.Migrate(home, frame.DefaultPersonalName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "carlos: frame migration error: %v\n", err)
		return
	}
	if report.HasMovement() && panel != nil {
		panel.Add("📦", farewellMigrationSummary(report.ResearchMoved, report.JobsMoved, report.WorktreesMoved))
	}
	for _, e := range report.Errors {
		fmt.Fprintf(os.Stderr, "carlos: %v\n", e)
	}
}

// farewellMigrationSummary writes the human-readable per-frame
// migration line. Pulled out so it can be unit-tested without standing
// up a real migration on disk.
func farewellMigrationSummary(research, jobs, worktrees int) string {
	parts := make([]string, 0, 3)
	if research > 0 {
		parts = append(parts, fmt.Sprintf("%d research note%s", research, pluralS(research)))
	}
	if jobs > 0 {
		parts = append(parts, fmt.Sprintf("%d shell job%s", jobs, pluralS(jobs)))
	}
	if worktrees > 0 {
		parts = append(parts, fmt.Sprintf("%d worktree%s", worktrees, pluralS(worktrees)))
	}
	if len(parts) == 0 {
		return "migrated to per-frame layout"
	}
	return "migrated " + joinAnd(parts) + " to per-frame layout"
}

// joinAnd reads a list as English: "a", "a and b", "a, b, and c".
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}

// slugifyQuestion turns a free-form question into a filesystem-safe
// slug: lowercase, [a-z0-9] runs joined by '-', collapsed dashes, max
// 60 chars (so the full filename including the -<unix-ts>.md suffix
// stays well under common 255-byte filename limits). Empty input falls
// back to "research" - the caller still gets a usable name even when
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
	fmt.Println(`carlos - a general-purpose TUI agent

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
  carlos gateway add                       interactive wizard to configure ntfy / Telegram / Signal channels
  carlos gateway test <channel>            send a test notification through one gateway channel
                                             (ntfy | telegram | signal | custom)
  carlos chat                              [dev-aid, Slice 1e] chat TUI against a temp log
  carlos manage                            [dev-aid, Slice 4]  manage TUI with seeded sample roster
  carlos onboard                           re-run the first-run setup flow
  carlos version                           print the build version
  carlos help                              this message

Examples:
  carlos please "list the 5 largest files in my home dir"
  carlos please -y "run the test suite and tell me which tests are slow"
  carlos please --provider openai --model gpt-4o "explain this diff"
  carlos approvals list
  carlos approvals accept 01HQ... "looks good"`)
}

func exit(err error) {
	if errors.Is(err, errFramePickerCancelled) {
		os.Exit(130)
	}
	fmt.Fprintln(os.Stderr, "carlos:", scrubProviderName(err))
	os.Exit(1)
}

// scrubProviderName runs the model-name scrub over an error's display
// string before it reaches stderr. The existing structural lines
// ("carlos: provider=X model=Y") are deliberate and stay - the user
// configured them. The hardening here is about errors bubbled up from
// a provider client: when those carry a "I am Gemini" / "Claude:"
// reveal in their wire payload, we rewrite the visible string to
// "carlos" so the framing is consistent with the chat surface.
//
// Returns "" for a nil error so the caller can use it inline with
// Fprintln/Fprintf without a separate guard.
func scrubProviderName(err error) string {
	if err == nil {
		return ""
	}
	return providers.ScrubModelNameString(err.Error())
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
		rest := args[1:]
		// -f|--frame scopes the query to one frame's summaries (plus
		// legacy unframed rows that fall through). --unframed scopes
		// to legacy/unframed-only. No flag returns the full corpus
		// across frames, which is the most useful default for
		// `memory search` invoked from a script.
		filter, rest, ferr := parseLeadingFrameFilter(rest)
		if ferr != nil {
			return ferr
		}
		query := strings.Join(rest, " ")
		if strings.TrimSpace(query) == "" {
			return errors.New("memory search: query required")
		}
		return memory.RunSearchInFrame(query, filter, 10)
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
