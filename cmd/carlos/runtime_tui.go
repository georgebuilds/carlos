// runtime_tui.go - TUI bootstrap entry points (runDefault, runOnboard,
// dev-aid commands). Split out of main.go so codecov can ignore the
// bubbletea program boundaries that resist unit testing.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/farewell"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/memory"
	"github.com/georgebuilds/carlos/internal/projectctx"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/skills"
	"github.com/georgebuilds/carlos/internal/tools"
	"github.com/georgebuilds/carlos/internal/tui/chat"
	"github.com/georgebuilds/carlos/internal/tui/chatglue"
	"github.com/georgebuilds/carlos/internal/tui/manage"
	"github.com/georgebuilds/carlos/internal/tui/onboarding"
	"github.com/georgebuilds/carlos/internal/usershell"
	"github.com/georgebuilds/carlos/internal/workspace"
)

// runOnboard runs the onboarding flow and persists the resulting config.
// If force is true (carlos onboard), prompts before overwriting an existing
// config. If force is false (auto-trigger), runs unconditionally.
//
// only, when non-empty, restricts the flow to a single screen (see
// onboardScreenByName). The existing config is loaded and merged so the
// sub-flow only edits its slice - the rest of the config rounds-trips
// untouched.
//
// A ctrl-c during the flow returns onboarding.ErrAborted, which we treat as
// a clean exit (code 0, no message) - the user opted out, no half-written
// state should remain.
func runOnboard(force bool, only string) error {
	// Onboarding runs BEFORE the config exists, so apply with nil
	// (env-only autodetect: NO_COLOR + COLORFGBG). The post-onboarding
	// chat/manage paths re-apply with the user's saved Theme settings.
	applyTheme(nil)
	path := config.DefaultPath()
	if only == "" && force && config.Exists(path) {
		if !confirmOverwrite(path) {
			fmt.Println("Aborted; existing config left in place.")
			return nil
		}
	}
	var flow *onboarding.Flow
	if only != "" {
		f, err := buildOnboardOnlyFlow(only, path)
		if err != nil {
			return err
		}
		flow = f
	} else {
		flow = onboarding.New()
	}
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
			"note: daemon preference saved - run `carlos daemon enable` to install the autostart unit.")
	}
	if only != "" {
		// Partial re-onboard: don't drop into the chat TUI - the user
		// was editing a single screen and likely wants to verify by
		// hand. Print the config path and return.
		fmt.Fprintln(os.Stderr, "config updated:", path)
		return nil
	}
	// Drop straight into the TUI so the user doesn't have to re-launch
	// to start working. This mirrors what the default-mode path does
	// when config already exists. Fresh-session - onboarding just
	// finished, the user's first launch deserves a clean slate.
	return runDefault(cfg, "")
}

// runChatDevAid is a Slice-1e development aid - NOT a stable public
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
	// Farewell panel collects end-of-session notes (daemon-orphan,
	// frame migration, brew update available) and renders one
	// bordered box on stderr after the TUI tears down. Pre-TUI bare
	// stderr lines would otherwise be hidden under the alt-screen
	// and only surface as visual noise next to the post-exit shell
	// prompt; routing them through the panel turns them into a
	// clean sign-off instead.
	panel := farewell.New()
	defer printFarewell(panel, cfg.UserName)
	// Defers run LIFO: the brew check fires before printFarewell so
	// the ⬆️ row, when present, lands in the same rendered box as
	// the rest of the end-of-session notes. We probe at EXIT (not
	// startup) so a slow `brew outdated` only delays the goodbye
	// box, not the boot — the user is already on their way out and
	// the timeout is the same 2s ceiling.
	defer checkBrewAtExit(panel)
	queueGatewayOrphaned(cfg, panel)
	home, _ := os.UserHomeDir()
	queueFrameMigration(home, panel)
	dbPath := filepath.Join(home, ".carlos", "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		return fmt.Errorf("open state.db: %w", err)
	}
	defer log.Close()

	d, err := buildDispatchForFrame(cfg, pleaseOptions{}, activeFrameForDispatch(cfg, ""))
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

	// Load the skill library before registry construction so the
	// skill_use tool can ship with the bundled + user-installed
	// catalog. Without this the model has no execution path for the
	// "Available skills" sysprompt block — it would know the names
	// but couldn't fetch the body of instructions. LoadFromConfig
	// walks the five canonical disk paths and overlays the binary's
	// embedded starter pack last, so a fresh install still sees the
	// shipped calendar skill without manual copy. Failures aren't
	// fatal; the chat boots either way.
	skillsLib, _ := skills.LoadFromConfig(cfg, "")

	// Phase F-11: thread the frame list + active frame through so the
	// notes_* / obsidian_* tools can default to the active frame's
	// vault_subtree and fan out across every configured frame on
	// cross-frame queries.
	baseReg := tools.NewDefaultRegistryWithIdentity("", cfg.Vault, cfg.Frames, cfg.Frames.Active, tools.ProviderSummariesFromConfig(cfg.Providers), cfg.UserName)
	baseReg.Register(tools.NewSkillUseTool(skillsLib, cfg.Frames.Active))
	// MCP v1: connect every configured MCP server enabled for the
	// active frame and register each discovered tool under the
	// "<server>__<tool>" namespace. Failures don't block boot - the
	// user sees the warning on stderr and the rest of the catalog
	// keeps wiring up. Sessions are closed on session end via defer.
	_, mcpClose, mcpCount := wireMCP(ctx, os.Stderr, cfg.MCP, cfg.Frames.Active, baseReg)
	defer mcpClose()
	if mcpCount > 0 {
		fmt.Fprintf(os.Stderr, "carlos: mcp: registered %d tool(s) from %d server(s)\n", mcpCount, len(cfg.MCP.Servers))
	}
	sup := agent.NewSupervisor(log, d.provider, baseReg)
	sup.Run(ctx)
	defer sup.Shutdown()

	// Keep the chat-default agent alive so Recover doesn't orphan
	// it on the next carlos invocation. The supervisor's heartbeat
	// ticker (5s emit, 10s stale tolerance) ticks for as long as
	// this process is up.
	sup.StartHeartbeat(ctx, defaultAgentID)

	// Parent's registry = baseReg + the Agent delegation tool. This
	// is the same shape carlos please uses (runtime_headless.go);
	// without it the interactive TUI model literally cannot reach
	// Supervisor.Spawn and the /agents view stays empty even when
	// the active frame is in orchestrator mode. Sub-agents inherit
	// the BASE registry (no Agent tool) so they can't further
	// delegate; that's enforced by Supervisor.Spawn's depth cap
	// independently, but keeping the child registry tool-list
	// honest avoids a confusing tool spec the child can't use.
	parentReg := tools.NewRegistry()
	for _, t := range baseReg.All() {
		parentReg.Register(t)
	}
	parentReg.Register(agent.NewAgentTool(sup))

	// Phase 11 slice 11f: wire the research engine off the same
	// provider + web tools the chat already uses. nil-safe - the
	// chat-side /research handler echoes "not wired" when this is
	// missing, so a degenerate registry (e.g. without web tools)
	// just disables the feature rather than crashing.
	researchEngine := buildResearchEngine(d.provider, d.model, baseReg)

	src := chat.NewMemTextSource()
	approver := chat.NewTUIApprover()
	defer approver.Close()
	// Phase T-1/T-2: the loop sees a LayeredApprover that auto-
	// approves the hardcoded read-only allowlist (notes_*,
	// read/grep/glob/ls, git_status, …) AND - when the cwd is
	// trusted via the workspace store - a small set of read-only
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
	// Phase F-12: plug the cross-frame detector. Active frame name +
	// every frame's on-disk root. write/edit calls landing in a
	// non-active frame's subtree get forced through the prompt path
	// with the cross-frame audit reason.
	if home != "" && len(cfg.Frames.List) > 0 {
		subtrees := make(map[string]string, len(cfg.Frames.List))
		for _, f := range cfg.Frames.List {
			subtrees[f.Name] = frame.PathsFor(home, f.Name).Root
		}
		activeName := cfg.Frames.Active
		if activeName == "" {
			activeName = cfg.Frames.Default
		}
		layered.SetFrameSubtrees(activeName, subtrees)
	}
	// Phase F-12 (Fix 4): subagents inherit the parent's layered
	// approver so a child writing into a non-active frame's subtree
	// trips the same cross-frame WRITE prompt the parent would have
	// seen. Without this the child runs under AutoApprover and a parent
	// in frame `work` could delegate a write into `personal` with no
	// audit hit. The supervisor stores the approver by reference, so the
	// SetFrameSubtrees call on /frame switch propagates to in-flight
	// children automatically.
	sup.SetSubAgentApprover(layered)
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
	// Surface any "you typed something we couldn't honor" warning at
	// startup so the user knows their CARLOS_FRAME / -f was ignored
	// instead of silently booting under the wrong frame.
	if frameOK && resolution.Warning != "" {
		fmt.Fprintf(os.Stderr, "carlos: %s; using %s (%s)\n", resolution.Warning, resolution.Frame, resolution.Reason)
	}
	// Phase F live-swap state. liveLoop holds the currently-running
	// chatglue.Loop; liveDispatch mirrors d so /whoami reflects the
	// currently-active provider + model after a swap. swapLoop is
	// filled in once the initial Loop is constructed further down;
	// SwitchActive captures the var so it can call the eventual
	// closure even though the Loop needs the systemPrompt which needs
	// frameInfo which needs frameOK to be resolved first.
	var (
		loopMu       sync.Mutex
		liveLoop     *chatglue.Loop
		liveDispatch = d
		swapLoop     func(name string) error
		swapModel    func(provider, model string) (string, string, error)
	)
	var activeFrame frame.Frame
	frameInfo := agent.FrameInfo{}
	frameUI := chat.FrameUI{}
	if frameOK {
		if f := cfg.Frames.Find(resolution.Frame); f != nil {
			activeFrame = *f
			frameInfo = agent.FrameInfo{
				Name:         activeFrame.Name,
				Append:       activeFrame.SystemPromptAppend,
				Mode:         frame.EffectiveMode(activeFrame),
				VaultPath:    cfg.Vault.Path,
				VaultSubtree: activeFrame.VaultSubtree,
				CwdHints:     activeFrame.CwdHints,
				Capabilities: extractCapabilityBackends(activeFrame),
				Skills:       summariseSkills(skillsLib, activeFrame.Name),
			}
			// Wire the supervisor's spawn cap to the active frame's
			// mode at session boot. Without this the sysprompt would
			// say "delegate aggressively" while the supervisor still
			// enforced the legacy hard ceiling (or refused everything
			// because the default flipped to solo).
			sup.SetMode(frame.EffectiveMode(activeFrame))
			// Also hand the active model through as the supervisor's
			// fallback for sub-agents. The chat-side `agent`
			// delegation tool builds SpawnContracts without a Model
			// field; without this the child's first provider call
			// went out with an empty model id and OpenRouter rejected
			// with HTTP 400.
			sup.SetDefaultModel(d.model)
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
					if err := config.Save(config.DefaultPath(), cfg); err != nil {
						return err
					}
					// Keep the supervisor's spawn cap aligned with the
					// new frame's mode. Without this update the cap
					// would still reflect the previous frame until the
					// next session restart.
					if nf := cfg.Frames.Find(name); nf != nil {
						sup.SetMode(frame.EffectiveMode(*nf))
					}
					if swapLoop != nil {
						return swapLoop(name)
					}
					return nil
				},
				SwitchMode: func(mode string) error {
					f := cfg.Frames.Find(activeFrame.Name)
					if f == nil {
						return fmt.Errorf("active frame %q vanished", activeFrame.Name)
					}
					f.Mode = mode
					sup.SetMode(mode)
					return config.Save(config.DefaultPath(), cfg)
				},
				Capabilities: extractCapabilityBackends(activeFrame),
				MatchCwd: func(cwd string) string {
					res, ok := frame.ResolveActive(&cfg.Frames, frame.Input{Cwd: cwd})
					if !ok {
						return ""
					}
					if res.Reason != frame.ReasonCwdHintExact && res.Reason != frame.ReasonCwdHintMultiple {
						return ""
					}
					if res.Frame == activeFrame.Name {
						return ""
					}
					return res.Frame
				},
				AddFrame: func(f frame.Frame) error {
					if cfg.Frames.Find(f.Name) != nil {
						return fmt.Errorf("frame %q already exists", f.Name)
					}
					cfg.Frames.List = append(cfg.Frames.List, f)
					return config.Save(config.DefaultPath(), cfg)
				},
				RefreshAvailable: func() []string {
					// Source-of-truth refresh: every Ctrl+F open re-
					// reads cfg.Frames.Names() so a frame added via the
					// wizard, /frame new <name>, OR an out-of-band edit
					// of ~/.carlos/config.yaml shows up in the switcher
					// without an app restart. cfg is the same struct the
					// AddFrame closure above mutates, so the two stay
					// trivially in sync.
					return cfg.Frames.Names()
				},
				PersonalTemplate: func() frame.Frame {
					if p := cfg.Frames.Find(frame.DefaultPersonalName); p != nil {
						return *p
					}
					return frame.Frame{}
				},
				Identity: func() (string, string) {
					loopMu.Lock()
					defer loopMu.Unlock()
					return liveDispatch.name, liveDispatch.model
				},
				LookupFrame: func(name string) (chat.FrameUIUpdate, bool) {
					f := cfg.Frames.Find(name)
					if f == nil {
						return chat.FrameUIUpdate{}, false
					}
					return chat.FrameUIUpdate{
						Glyph:        f.Glyph,
						Accent:       f.Accent,
						Mode:         frame.EffectiveMode(*f),
						Capabilities: extractCapabilityBackends(*f),
					}, true
				},
				SwitchModel: func(provider, model string) (string, string, error) {
					if swapModel == nil {
						return "", "", fmt.Errorf("model swap not wired")
					}
					return swapModel(provider, model)
				},
				ModelCompletions: func(partial string) []string {
					return modelCompletionsFor(cfg, partial)
				},
				SkillsCatalog: func() []chat.SkillCatalogEntry {
					if skillsLib == nil {
						return nil
					}
					out := make([]chat.SkillCatalogEntry, 0)
					for _, s := range skillsLib.ForFrame(activeFrame.Name) {
						if s == nil {
							continue
						}
						out = append(out, chat.SkillCatalogEntry{
							Name:        s.Name,
							Description: s.Description,
							Backend:     s.Backend,
						})
					}
					return out
				},
			}
		}
	}
	_ = activeFrame // referenced by Phase F-9 provider re-resolution slice

	systemPrompt := agent.SystemPromptWithFrame(cfg.UserName, chatCwd, chatProjectCtx, frameInfo)

	loop := chatglue.NewLoop(chatglue.Config{
		Provider: d.provider,
		Model:    d.model,
		Tools:    parentReg,
		Approver: layered,
		System:   systemPrompt,
	}, log, src, defaultAgentID)
	if err := loop.Start(ctx); err != nil {
		return err
	}
	liveLoop = loop
	defer func() {
		loopMu.Lock()
		l := liveLoop
		loopMu.Unlock()
		l.Stop()
	}()
	swapLoop = func(newFrameName string) error {
		f := cfg.Frames.Find(newFrameName)
		if f == nil {
			return fmt.Errorf("unknown frame: %s", newFrameName)
		}
		newDispatch, err := buildDispatchForFrame(cfg, pleaseOptions{provider: f.Provider, model: f.Model}, f)
		if err != nil {
			return fmt.Errorf("rebuild dispatch: %w", err)
		}
		newInfo := agent.FrameInfo{
			Name:         f.Name,
			Append:       f.SystemPromptAppend,
			Mode:         frame.EffectiveMode(*f),
			VaultPath:    cfg.Vault.Path,
			VaultSubtree: f.VaultSubtree,
			CwdHints:     f.CwdHints,
			Capabilities: extractCapabilityBackends(*f),
			Skills:       summariseSkills(skillsLib, f.Name),
		}
		newSys := agent.SystemPromptWithFrame(cfg.UserName, chatCwd, chatProjectCtx, newInfo)
		newLoop := chatglue.NewLoop(chatglue.Config{
			Provider: newDispatch.provider,
			Model:    newDispatch.model,
			Tools:    parentReg,
			Approver: layered,
			System:   newSys,
		}, log, src, defaultAgentID)
		if err := newLoop.Start(ctx); err != nil {
			return fmt.Errorf("start new loop: %w", err)
		}
		loopMu.Lock()
		old := liveLoop
		liveLoop = newLoop
		liveDispatch = newDispatch
		loopMu.Unlock()
		old.Stop()
		// Refresh the cross-frame approver's notion of which frame is
		// active so write/edit prompts label the new frame correctly.
		if len(cfg.Frames.List) > 0 && home != "" {
			subtrees := make(map[string]string, len(cfg.Frames.List))
			for _, fr := range cfg.Frames.List {
				subtrees[fr.Name] = frame.PathsFor(home, fr.Name).Root
			}
			layered.SetFrameSubtrees(newFrameName, subtrees)
		}
		return nil
	}
	// swapModel mirrors swapLoop's atomic chatglue.Loop rebuild but
	// pivots on (provider, model) instead of a frame name. Empty
	// provider means "keep the active provider, swap only the model"
	// — the most common case for OpenRouter users hopping between
	// catalog entries. Returns the resolved (provider, model) so the
	// slash echo can confirm exactly what landed (useful when the
	// caller passed a bare model and the provider was inferred).
	swapModel = func(reqProvider, reqModel string) (string, string, error) {
		if strings.TrimSpace(reqModel) == "" {
			return "", "", fmt.Errorf("model is required (got empty)")
		}
		loopMu.Lock()
		curName := liveDispatch.name
		loopMu.Unlock()
		provName := strings.TrimSpace(reqProvider)
		if provName == "" {
			provName = curName
		}
		newDispatch, err := buildDispatchForFrame(cfg, pleaseOptions{
			provider: provName,
			model:    strings.TrimSpace(reqModel),
		}, activeFrameForDispatch(cfg, ""))
		if err != nil {
			return "", "", fmt.Errorf("rebuild dispatch: %w", err)
		}
		// Re-derive sysprompt + skill summaries against the CURRENT
		// active frame so the new turn sees the same frame context as
		// the prior one (model swaps preserve frame; frame swaps go
		// through swapLoop).
		var newInfo agent.FrameInfo
		curFrameName := ""
		if af := cfg.Frames.Find(cfg.Frames.Active); af != nil {
			curFrameName = af.Name
			newInfo = agent.FrameInfo{
				Name:         af.Name,
				Append:       af.SystemPromptAppend,
				Mode:         frame.EffectiveMode(*af),
				VaultPath:    cfg.Vault.Path,
				VaultSubtree: af.VaultSubtree,
				CwdHints:     af.CwdHints,
				Capabilities: extractCapabilityBackends(*af),
				Skills:       summariseSkills(skillsLib, af.Name),
			}
		}
		_ = curFrameName
		newSys := agent.SystemPromptWithFrame(cfg.UserName, chatCwd, chatProjectCtx, newInfo)
		newLoop := chatglue.NewLoop(chatglue.Config{
			Provider: newDispatch.provider,
			Model:    newDispatch.model,
			Tools:    parentReg,
			Approver: layered,
			System:   newSys,
		}, log, src, defaultAgentID)
		if err := newLoop.Start(ctx); err != nil {
			return "", "", fmt.Errorf("start new loop: %w", err)
		}
		loopMu.Lock()
		old := liveLoop
		liveLoop = newLoop
		liveDispatch = newDispatch
		loopMu.Unlock()
		old.Stop()
		// Keep the supervisor's default model in sync so sub-agents
		// spawned after the swap inherit the freshly-chosen model.
		sup.SetDefaultModel(newDispatch.model)
		// Update the persisted agents table row so a future session
		// resume + projection rebuild lands on the freshly-chosen
		// model. The in-memory header read goes through Identity()
		// (see headerState in internal/tui/chat/view.go), so the user
		// sees the swap immediately without a state_change event.
		_ = log.UpdateAgentModel(ctx, defaultAgentID, newDispatch.model)
		return newDispatch.name, newDispatch.model, nil
	}

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
	// them up on the next turn for free.
	//
	// F-17 + v0.7.3 bug fix: per-job logs ALWAYS land under a frame's
	// JobsDir. Previously this was conditional on activeFrame.Name
	// being non-empty AND $HOME resolving; either branch failing
	// silently dropped jobs into the legacy ~/.carlos/usershell
	// directory, which the next session's farewell migration then
	// shoveled into the per-frame tree. The cycle repeated every
	// boot ("migrated N shell jobs to per-frame layout"). Now we
	// resolve a frame name (active → "personal" fallback) and a
	// home (resolved → "" fallback handled inside Manager) so the
	// OutputDir is always set; usershell.defaultOutputDir() also
	// targets the per-frame personal layout as a belt-and-braces
	// guard for any caller that doesn't pass an explicit option.
	usershellFrame := activeFrame.Name
	if usershellFrame == "" {
		usershellFrame = frame.DefaultPersonalName
	}
	shellOpts := usershell.Options{Log: log}
	if home, herr := os.UserHomeDir(); herr == nil {
		shellOpts.OutputDir = frame.PathsFor(home, usershellFrame).JobsDir
	}
	shellMgr := usershell.New(shellOpts)
	defer shellMgr.Close()
	// Phase U S7: separate ~/.carlos/shell-history file walked via
	// ↑/↓ in shell mode. Created lazily on first Add; reads on
	// startup so previous-session entries are available.
	shellHistory := usershell.NewHistory("")

	// Inline sub-agent panel: bridge the supervisor's per-parent
	// children map into the small ChildrenView the chat polls. The
	// adapter closes over sup + the chat-default agent id so the
	// chat doesn't need to know either; if the chat ever runs
	// against a non-default parent id the adapter can be rebuilt.
	childrenView := chat.ChildrenViewFunc(func() []chat.ChildSnapshot {
		snaps := sup.SnapshotChildrenOf(ctx, defaultAgentID)
		if len(snaps) == 0 {
			return nil
		}
		out := make([]chat.ChildSnapshot, 0, len(snaps))
		for _, s := range snaps {
			out = append(out, chat.ChildSnapshot{
				AgentID:   s.AgentID,
				State:     s.State,
				LastEvent: s.Title,
				Spend:     chat.ChildSpend{Tokens: s.Tokens, Cents: s.CostCents},
				StartedAt: s.StartedAt,
			})
		}
		return out
	})

	for {
		// Refresh the frame list each iteration so a new frame
		// created in the previous chat session (wizard or `/frame
		// new`) is visible in this iteration's switcher even on the
		// chat ⇄ manage ⇄ chat round-trip. The wizard already
		// appends to the live chat Model's mirror; this line keeps
		// the outer-loop snapshot honest too.
		if frameUI.Active != "" {
			frameUI.Available = cfg.Frames.Names()
		}
		opts := []chat.Option{
			chat.WithTUIApprover(approver),
			chat.WithUserName(cfg.UserName),
			chat.WithSummarizer(summarizer),
			chat.WithUserShell(shellMgr),
			chat.WithShellHistory(shellHistory),
			chat.WithChildrenView(childrenView),
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
		// /resume picker: when the user committed a session pick, swap
		// the current chatglue.Loop for one bound to the chosen agent
		// id and re-enter the chat loop on the same iteration so the
		// new transcript backfills inline.
		if picked := m.ResumeRequested(); picked != "" {
			if err := ensureDefaultAgent(ctx, log, picked, d.name, d.model, cfg.UserName); err != nil {
				return fmt.Errorf("resume %s: %w", picked, err)
			}
			loopMu.Lock()
			old := liveLoop
			loopMu.Unlock()
			if old != nil {
				old.Stop()
			}
			defaultAgentID = picked
			newLoop := chatglue.NewLoop(chatglue.Config{
				Provider: liveDispatch.provider,
				Model:    liveDispatch.model,
				Tools:    parentReg,
				Approver: layered,
				System:   systemPrompt,
			}, log, src, defaultAgentID)
			if err := newLoop.Start(ctx); err != nil {
				return fmt.Errorf("resume %s: start loop: %w", picked, err)
			}
			loopMu.Lock()
			liveLoop = newLoop
			loopMu.Unlock()
			continue
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
// stale, and the chat-default agent is always stale at startup -
// there's no per-process supervisor heartbeat ticker for it. Without
// the refresh, every restart shows the chat header stuck on
// `[orphaned]`.
//
// The projection-only refresh (no state-change event in the log) is
// deliberate for the default chat agent: orphaned is terminal in the
// state machine, so there's no legal Transition out of it. The chat
// agent isn't a sub-agent with a meaningful lifecycle - it's a
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
		// refuse Transition()) by writing the event directly - the
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
	// Same recipe the resume branch above uses - without this, every
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
	if err := log.InsertAgent(ctx, agent.AgentRow{
		ID: id, RootID: id, State: agent.StateRunning, Attempt: 1,
		Title: title, Model: model, CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		return err
	}
	// Brand-new thread: prune top-level orphans the user never typed
	// in. These accumulate on every abrupt exit and bury the /resume
	// picker under "(no messages yet)" rows. Failure here is logged,
	// never blocks startup — a janitor pass should never stop the
	// user from getting a working chat.
	if pruned, err := log.DeleteEmptyOrphanedAgents(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "carlos: prune empty orphans: %v\n", err)
	} else if len(pruned) > 0 {
		fmt.Fprintf(os.Stderr, "carlos: pruned %d empty orphaned session(s)\n", len(pruned))
	}
	return nil
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

	fmt.Fprintln(os.Stderr, "carlos: chat dev-aid - Slice 1e smoke harness, not a public surface.")
	fmt.Fprintf(os.Stderr, "carlos:   db = %s\n", dbPath)
	fmt.Fprintf(os.Stderr, "carlos:   agent_id = %s\n", agentID)
	fmt.Fprintln(os.Stderr, "carlos:   ctrl-c to quit; /help inside the TUI for commands.")

	src := chat.NewMemTextSource()
	src.Append(agentID, "(no real provider wired yet - Slice 1f will plug in the stream.)")
	m := chat.New(log, agentID, src)
	if _, err := m.Run(); err != nil {
		return fmt.Errorf("dev-aid run: %w", err)
	}
	// Slice 7g: /agents inside chat sets OpenManageRequested(); honor
	// it by relaunching into the manage TUI. Dev-aid limitation: chat
	// and manage use different smoke-harness DBs, so the chat agent
	// won't appear in manage's roster - the swap mechanism still
	// exercises end-to-end; the shared-DB version lands with the
	// unified default-mode TUI.
	if m.OpenManageRequested() {
		fmt.Fprintln(os.Stderr, "carlos: /agents - switching to manage TUI.")
		return runManageDevAid()
	}
	return nil
}

// runManageDevAid is the Slice-4 development aid - NOT a stable
// surface. Opens a temp SQLite event log under
// $TMPDIR/carlos-manage-devaid/state.db, seeds ~8 sample agents
// covering most SPEC states plus a 3-level lineage, and drops into
// the manage TUI so the roster / focus pane / verbs can be exercised
// by hand.
//
// Pass a Supervisor=nil to the TUI so the verbs surface a "no
// supervisor wired" line in the status bar rather than fanning out
// to a real one. This is the smoke harness - not the product.
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

	fmt.Fprintln(os.Stderr, "carlos: manage dev-aid - Slice 4 smoke harness, not a public surface.")
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
