// runtime_headless.go - headless agent loop entry points (runHeadless,
// runResearch, stdinApprover). Split out of main.go so codecov can
// ignore the long-running, network-driven code paths that resist unit
// testing.

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	xterm "github.com/charmbracelet/x/term"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/projectctx"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/sandbox"
	"github.com/georgebuilds/carlos/internal/skills"
	"github.com/georgebuilds/carlos/internal/tools"
	"github.com/georgebuilds/carlos/internal/tui/chat"
	"github.com/georgebuilds/carlos/internal/workspace"
)

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
		fmt.Fprintln(os.Stderr, "carlos: run `carlos onboard` first - headless mode needs a configured provider.")
		os.Exit(1)
	}
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !config.IsComplete(cfg) {
		fmt.Fprintln(os.Stderr, "carlos: config incomplete - run `carlos onboard`.")
		os.Exit(1)
	}

	// Phase 9 slice 9a: pull cfg.Theme + env into every TUI surface
	// before anything renders. please-mode shows the chat-approver
	// prompt with these colors.
	applyTheme(cfg)
	warnGatewayOrphaned(cfg)

	d, err := buildDispatchForFrame(cfg, opts, activeFrameForDispatch(cfg, opts.frame))
	if err != nil {
		fmt.Fprintln(os.Stderr, "carlos:", scrubProviderName(err))
		os.Exit(1)
	}

	// State.db lifecycle (Phase 1h): open ~/.carlos/state.db (created
	// at 0700 if absent), recover any leftover non-terminal agents
	// from a prior process kill into `orphaned`. Logged but not auto-
	// retried - the user explicitly retries via the TUI.
	home, _ := os.UserHomeDir()
	migrateFrameLayout(home)
	// Phase F-19: inline TTY frame picker. When the user didn't pass
	// -f / --frame, didn't set CARLOS_FRAME, the run is interactive
	// (stdin is a tty), and the config has multiple frames, prompt
	// for one before the rest of the boot path resolves the frame.
	// Single-frame configs get a one-line dim notice and skip the
	// picker so non-TTY / cron runs keep their old quiet behavior.
	if opts.frame == "" && os.Getenv("CARLOS_FRAME") == "" && stdinIsTTY() {
		switch len(cfg.Frames.List) {
		case 0:
		case 1:
			fmt.Fprintf(os.Stderr, "carlos: running in %s frame\n", cfg.Frames.List[0].Name)
		default:
			name, perr := RunInlineFramePicker("carlos please", prompt, &cfg.Frames)
			if perr != nil {
				if errors.Is(perr, errFramePickerCancelled) {
					return perr
				}
				return fmt.Errorf("frame picker: %w", perr)
			}
			opts.frame = name
		}
	}
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

	// Base registry - what children inherit (filtered by their spawn
	// contract's tool_allowlist). Phase 7 adds the full coding-agent
	// tool set: bash + file ops + git read-only. The Agent tool
	// itself is added to the PARENT'S registry only (below), NOT the
	// base - children at depth 1 (the v0 cap) can't further spawn.
	baseReg := tools.NewDefaultRegistryWithIdentity(baseDir, cfg.Vault, cfg.Frames, cfg.Frames.Active, tools.ProviderSummariesFromConfig(cfg.Providers), cfg.UserName)
	// MCP v1: same registration as the TUI path. Children inherit
	// these tools because we register into baseReg before the
	// parentReg copy below is built.
	_, mcpClose, mcpCount := wireMCP(ctx, os.Stderr, cfg.MCP, cfg.Frames.Active, baseReg)
	defer mcpClose()
	if mcpCount > 0 {
		fmt.Fprintf(os.Stderr, "carlos: mcp: registered %d tool(s) from %d server(s)\n", mcpCount, len(cfg.MCP.Servers))
	}

	// Supervisor takes the resolved provider + base registry. Its
	// goroutine sweep + per-agent heartbeats run for the lifetime of
	// this process; Shutdown stops them cleanly on exit.
	sup := agent.NewSupervisor(log, d.provider, baseReg)
	sup.Run(ctx)
	defer sup.Shutdown()
	// Align the supervisor's spawn cap with the active frame's mode.
	// The headless dispatch has no /frame switch, so this is a one-shot
	// at session boot. Empty frame name falls back to the supervisor's
	// default (orchestrator) which preserves pre-modes behaviour.
	if activeFrameName != "" {
		if f := cfg.Frames.Find(activeFrameName); f != nil {
			sup.SetMode(frame.EffectiveMode(*f))
		}
	}
	// Hand d.model through as the supervisor's sub-agent fallback so a
	// `carlos please` invocation that spawns a child via the `agent`
	// tool inherits the parent's model. Without this, OpenAI-compatible
	// providers reject the child's first call with HTTP 400.
	sup.SetDefaultModel(d.model)

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
	// runDispatch path as in the chat path below - the model can't
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
	// Phase F-12: cross-frame write/edit detector. Same wiring as the
	// chat path so a `carlos please` run sees the same boundary as
	// the interactive surface.
	if home != "" && len(cfg.Frames.List) > 0 {
		subtrees := make(map[string]string, len(cfg.Frames.List))
		for _, f := range cfg.Frames.List {
			subtrees[f.Name] = frame.PathsFor(home, f.Name).Root
		}
		layered.SetFrameSubtrees(activeFrameName, subtrees)
	}
	approver = layered

	// Surface which provider/model we're using on stderr so scripts
	// and users both see it. Skipped when the live status panel is
	// going to render (TTY mode): the panel's footer row carries the
	// same provider/model label, and a duplicate line above the
	// rounded box reads as visual noise.
	caps := d.provider.Capabilities()
	pleasePanelOn := stdoutIsTTY()
	if !pleasePanelOn {
		fmt.Fprintf(os.Stderr, "carlos: provider=%s model=%s (parallel-tool=%t, caching=%t, vision=%t)\n",
			d.name, d.model, caps.ParallelToolUse, caps.PromptCaching, caps.Vision)
	}
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
	// active. Headless run, no TTY picker - the persisted active is
	// the canonical fallback for cron / pipe invocations.
	pleaseFrameInfo := agent.FrameInfo{}
	if res, ok := frame.ResolveActive(&cfg.Frames, frame.Input{
		Env:  os.Getenv("CARLOS_FRAME"),
		Flag: opts.frame,
		Cwd:  hcwd,
	}); ok {
		if f := cfg.Frames.Find(res.Frame); f != nil {
			pleaseLib, _ := skills.LoadFromConfig(cfg, "")
			pleaseFrameInfo = agent.FrameInfo{
				Name:         f.Name,
				Append:       f.SystemPromptAppend,
				Mode:         frame.EffectiveMode(*f),
				VaultPath:    cfg.Vault.Path,
				VaultSubtree: f.VaultSubtree,
				CwdHints:     f.CwdHints,
				Capabilities: extractCapabilityBackends(*f),
				Skills:       summariseSkills(pleaseLib, f.Name),
			}
			if opts.frame != "" || os.Getenv("CARLOS_FRAME") != "" {
				fmt.Fprintf(os.Stderr, "carlos: frame=%s (via %s)\n", res.Frame, res.Reason)
			}
		}
	}
	system := agent.SystemPromptWithFrame(cfg.UserName, hcwd, hctx, pleaseFrameInfo)

	initial := []providers.Message{
		{Role: "user", Content: []providers.Block{{Kind: "text", Text: prompt}}},
	}

	if pleasePanelOn {
		err = runPleaseWithPanel(ctx, prompt, d, parentReg, agent.LoopOptions{
			Model:    d.model,
			System:   system,
			Tools:    toolSpecs,
			Approver: approver,
		}, initial)
	} else {
		_, err = agent.Run(ctx, d.provider, parentReg, agent.LoopOptions{
			Model:    d.model,
			System:   system,
			Tools:    toolSpecs,
			Approver: approver,
			TextSink: os.Stdout,
		}, initial)
		// Newline keeps the terminal prompt clean regardless of how
		// the loop finished — the model's last text may not end
		// with one.
		fmt.Println()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// runPleaseWithPanel runs the agent loop with the live bubbletea
// status panel. The panel renders a 3-row rounded box showing the
// current tool, streaming-text preview, and a running counter; on
// completion the panel quits in place and the buffered assistant
// text prints below.
//
// Hooks are wired so the panel sees:
//   - OnToolCall    → pleaseToolStartMsg
//   - OnToolResult  → pleaseToolDoneMsg (with isError flag flagged)
//   - TextSink Write → pleaseTextDeltaMsg (for the writing-line preview)
//
// Errors from agent.Run flow back through the driver; ctrl-c is
// surfaced as context.Canceled and treated as a clean exit by the
// caller.
func runPleaseWithPanel(
	ctx context.Context,
	prompt string,
	d *dispatch,
	reg *tools.Registry,
	opts agent.LoopOptions,
	initial []providers.Message,
) error {
	sink := &pleaseTextSink{}

	err := runPleaseDriver(ctx, prompt, d.name, d.model, func(prog *tea.Program) error {
		sink.prog = prog
		opts.TextSink = sink
		opts.OnToolCall = func(use providers.Block) {
			input := string(use.ToolInput)
			prog.Send(pleaseToolStartMsg{
				name:      use.ToolName,
				inputJSON: input,
				t:         time.Now(),
			})
		}
		opts.OnToolResult = func(use providers.Block, result providers.Block) {
			errMsg := ""
			body := string(result.ToolResult)
			if strings.HasPrefix(body, "(rejected by user)") || strings.HasPrefix(body, "tool error:") {
				errMsg = firstLine(body)
			}
			prog.Send(pleaseToolDoneMsg{
				name:    use.ToolName,
				elapsed: 0, // panel computes its own elapsed from toolStarted
				errMsg:  errMsg,
			})
		}
		_, runErr := agent.Run(ctx, d.provider, reg, opts, initial)
		return runErr
	})

	// Panel has exited; flush the buffered assistant text to stdout
	// so the user sees the actual reply. A trailing newline keeps
	// the shell prompt clean.
	text := sink.String()
	if text != "" {
		fmt.Print(text)
		if !strings.HasSuffix(text, "\n") {
			fmt.Println()
		}
	}
	return err
}

// firstLine returns s up to the first newline; used to clip a
// multi-line tool error to a sensible one-line summary for the
// status panel.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// stdoutIsTTY reports whether stdout is attached to a terminal. Used
// to gate the live-panel branch in carlos please: piped or
// redirected stdout falls back to the plain streaming-to-stdout
// path that scripts already depend on.
func stdoutIsTTY() bool {
	return xterm.IsTerminal(os.Stdout.Fd())
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
		fmt.Fprintln(os.Stderr, `carlos: research-internal needs a question - e.g. carlos research-internal "what is the current state of WebGPU in Safari?"`)
		os.Exit(2)
	}
	path := config.DefaultPath()
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "carlos: run `carlos onboard` first - research-internal needs a configured provider.")
		os.Exit(1)
	}
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !config.IsComplete(cfg) {
		fmt.Fprintln(os.Stderr, "carlos: config incomplete - run `carlos onboard`.")
		os.Exit(1)
	}
	d, err := buildDispatchForFrame(cfg, pleaseOptions{}, activeFrameForDispatch(cfg, ""))
	if err != nil {
		fmt.Fprintln(os.Stderr, "carlos:", scrubProviderName(err))
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
		fmt.Fprintf(os.Stderr, "carlos: research run ended with: %s\n", scrubProviderName(runErr))
	}
	return nil
}

// runResearch is the Phase 11 slice 11f user-facing headless command:
// `carlos research <question>`. Mirrors runResearchInternal's setup -
// load config, build dispatch, construct engine - but with friendlier
// stderr framing, and saves the rendered markdown report to
// ~/.carlos/research/<slug>-<unix-ts>.md so the user can reference it
// later or pipe to other tooling.
//
// Like research-internal, this is synchronous: the chat-side `/research`
// slash command shares the same engine but runs in a goroutine so the
// TUI stays interactive. The headless variant blocks because there's
// nothing else competing for the terminal - the user explicitly asked
// for a research arc.
func runResearch(args []string) error {
	frameOverride, rest, ferr := parseLeadingFrameFlag(args)
	if ferr != nil {
		fmt.Fprintln(os.Stderr, "carlos:", ferr)
		os.Exit(2)
	}
	question := strings.TrimSpace(strings.Join(rest, " "))
	if question == "" {
		fmt.Fprintln(os.Stderr, `carlos: research needs a question - e.g. carlos research "what is the current state of WebGPU in Safari?"`)
		os.Exit(2)
	}
	path := config.DefaultPath()
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "carlos: run `carlos onboard` first - research needs a configured provider.")
		os.Exit(1)
	}
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !config.IsComplete(cfg) {
		fmt.Fprintln(os.Stderr, "carlos: config incomplete - run `carlos onboard`.")
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
	// Phase F-19: inline TTY frame picker. Same gate as runHeadless:
	// no flag, no env override, interactive tty, more than one
	// configured frame.
	if frameOverride == "" && os.Getenv("CARLOS_FRAME") == "" && stdinIsTTY() {
		switch len(cfg.Frames.List) {
		case 0:
		case 1:
			fmt.Fprintf(os.Stderr, "carlos: running in %s frame\n", cfg.Frames.List[0].Name)
		default:
			name, perr := RunInlineFramePicker("carlos research", question, &cfg.Frames)
			if perr != nil {
				if errors.Is(perr, errFramePickerCancelled) {
					return perr
				}
				return fmt.Errorf("frame picker: %w", perr)
			}
			frameOverride = name
		}
	}
	researchFrameName := ""
	if res, ok := frame.ResolveActive(&cfg.Frames, frame.Input{
		Env:  os.Getenv("CARLOS_FRAME"),
		Flag: frameOverride,
		Cwd:  rcwd,
	}); ok {
		researchFrameName = res.Frame
	}
	d, err := buildDispatchForFrame(cfg, pleaseOptions{}, cfg.Frames.Find(researchFrameName))
	if err != nil {
		fmt.Fprintln(os.Stderr, "carlos:", scrubProviderName(err))
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
	//    anything advertising as a bot - the model's tool calls
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

	// Live status panel via bubbletea inline (no AltScreen) -
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
			fmt.Fprintf(os.Stderr, "carlos: research failed: %s\n", scrubProviderName(runErr))
			return runErr
		}
		return errors.New("carlos: research produced no report (unexpected)")
	}
	rendered := chat.RenderReportMarkdown(report)

	// Persist the markdown under the active frame's research dir so the
	// user has a stable artifact to share or revisit. The CLI prints
	// only the saved-path link; the full rendered report lives on disk.
	// Errors saving are surfaced but don't fail the whole command.
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
		fmt.Fprintf(os.Stderr, "carlos: research run ended with: %s\n", scrubProviderName(runErr))
	}
	return nil
}

// seedHeadlessParentAgent inserts a minimal agent row + state-change
// event for the synthetic headless parent so the artifacts FK
// constraint is satisfied when PlanTool writes its diff + metadata
// artifacts. Returns the generated agent id.
//
// Slice 7e: PlanTool needs AgentID to be a real row. The headless `please`
// command otherwise has no persistent agent identity - agent.Run runs
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
// "always" answer is remembered for the rest of the session - common
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
