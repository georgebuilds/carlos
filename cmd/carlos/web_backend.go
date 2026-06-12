// web_backend.go - the runtime-backed implementation of web.Backend
// (slice W-2). It wires the same pieces runDefault assembles (provider
// dispatch, tool registries, supervisor, layered approver, frame-resolved
// system prompt) and, per attached thread, runs a chatglue.Loop with a
// per-thread WebApprover bridged to the browser.
//
// This deliberately rebuilds the per-thread loop recipe here rather than
// relocating runDefault's assembly into a shared buildRuntime/OpenThread
// (the spec's W-0). W-0 is the riskiest refactor in the plan and ships on
// its own; duplicating the recipe here keeps the TUI byte-identical while
// the web feature lands. The W-0 dedup is tracked in the vault plan.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/projectctx"
	"github.com/georgebuilds/carlos/internal/skills"
	"github.com/georgebuilds/carlos/internal/tools"
	"github.com/georgebuilds/carlos/internal/tui/chatglue"
	"github.com/georgebuilds/carlos/internal/web"
	"github.com/georgebuilds/carlos/internal/workspace"
)

// carlosBackend is the single v1 web.Backend. One per `carlos web` process.
type carlosBackend struct {
	cfg      *config.Config
	log      *agent.SQLiteEventLog
	sup      *agent.Supervisor
	parent   *tools.Registry
	src      *web.WebTextSource
	hub      *web.Server // for the approver's publish hook (srv.Hub())
	dispatch *dispatch

	// Frame context resolved once (no per-thread frame in v1, spec §11.4).
	system    string
	frameName string
	frameRoot map[string]string // frame name -> on-disk root, for cross-frame writes
	activeFrm string
	cwd       string

	// lifeCtx is the server-lifetime context (the signal ctx runWeb owns).
	// Every attached thread's loop + heartbeat ticker derive from it - NOT
	// from the ctx Attach receives, which in production is the attach/create
	// HTTP request's r.Context(). net/http cancels that the moment the
	// handler returns; deriving the thread context from it killed the loop
	// and the heartbeat instantly, and the process's own orphan sweeper then
	// flipped the freshly attached thread to `orphaned` within ~10-20s (the
	// web GUI's "state: orphaned" right after sending a message).
	lifeCtx context.Context

	mu       sync.Mutex
	attached map[string]*webThread
}

type webThread struct {
	loop     *chatglue.Loop
	approver *web.WebApprover
	cancel   context.CancelFunc
}

// newCarlosBackend assembles the shared runtime. ctx is the server's
// lifetime context; the supervisor and per-thread loops derive from it.
func newCarlosBackend(ctx context.Context, cfg *config.Config, log *agent.SQLiteEventLog, srv *web.Server) (*carlosBackend, error) {
	d, err := buildDispatchForFrame(cfg, pleaseOptions{}, activeFrameForDispatch(cfg, ""))
	if err != nil {
		return nil, err
	}

	skillsLib, _ := skills.LoadFromConfig(cfg, "")
	baseReg := tools.NewDefaultRegistryWithIdentity("", cfg.Vault, cfg.Frames, cfg.Frames.Active,
		tools.ProviderSummariesFromConfig(cfg.Providers), cfg.UserName)
	baseReg.Register(tools.NewSkillUseTool(skillsLib, cfg.Frames.Active))

	sup := agent.NewSupervisor(log, d.provider, baseReg)
	sup.Run(ctx)

	// parentReg = baseReg + the Agent delegation tool, exactly as runDefault
	// builds it, so a thread in orchestrator mode can spawn sub-agents.
	parentReg := tools.NewRegistry()
	for _, t := range baseReg.All() {
		parentReg.Register(t)
	}
	parentReg.Register(agent.NewAgentTool(sup))

	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	projCtx := ""
	if cwd != "" {
		if pc, err := projectctx.LoadFromCwd(cwd); err == nil && pc != nil {
			projCtx = pc.Combined
		}
	}

	// Resolve the frame once (env -> cwd-hint -> persisted -> default) and
	// build the system prompt + frame info shared by every attached thread.
	b := &carlosBackend{
		cfg: cfg, log: log, sup: sup, parent: parentReg,
		src:       web.NewWebTextSource(srv.Hub()),
		hub:       srv,
		dispatch:  d,
		cwd:       cwd,
		lifeCtx:   ctx,
		attached:  map[string]*webThread{},
		frameRoot: map[string]string{},
	}

	var frameInfo agent.FrameInfo
	if res, ok := frame.ResolveActive(&cfg.Frames, frame.Input{Env: os.Getenv("CARLOS_FRAME"), Cwd: cwd}); ok {
		if f := cfg.Frames.Find(res.Frame); f != nil {
			b.frameName = f.Name
			b.activeFrm = f.Name
			frameInfo = agent.FrameInfo{
				Name:         f.Name,
				Append:       f.SystemPromptAppend,
				Mode:         frame.EffectiveMode(*f),
				VaultPath:    cfg.Vault.Path,
				VaultSubtree: f.VaultSubtree,
				CwdHints:     f.CwdHints,
				Capabilities: extractCapabilityBackends(*f),
				Skills:       summariseSkills(skillsLib, f.Name),
			}
			sup.SetMode(frame.EffectiveMode(*f))
			sup.SetDefaultModel(d.model)
		}
	}
	if home != "" && len(cfg.Frames.List) > 0 {
		for _, f := range cfg.Frames.List {
			b.frameRoot[f.Name] = frame.PathsFor(home, f.Name).Root
		}
	}
	b.system = agent.SystemPromptWithFrame(cfg.UserName, cwd, projCtx, frameInfo)

	// Live crew updates: every child lifecycle edge publishes a fresh
	// `children` snapshot for the parent thread to the SSE hub, so the
	// crew column appears the moment the first sub-agent spawns and its
	// rows flip to done/failed as they finish.
	sup.SetChildNotifier(b.publishChildren)
	return b, nil
}

func (b *carlosBackend) Caps() map[string]bool {
	return map[string]bool{
		"create": true, "send": true, "approve": true,
		"observe": true, "children": true,
	}
}

func (b *carlosBackend) Attached(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.attached[id]
	return ok
}

func (b *carlosBackend) Frame(id string) string {
	if b.Attached(id) {
		return b.frameName
	}
	return ""
}

// newLayered builds a fresh layered approver for one thread: the per-thread
// WebApprover is the interactive fallback; the builtin read-only allowlist,
// workspace trust, and cross-frame write detection layer on top, exactly as
// runDefault wires them.
func (b *carlosBackend) newLayered(fallback agent.Approver) *agent.LayeredApprover {
	layered := agent.NewLayeredApprover(fallback, agent.DefaultBuiltinAllow, nil)
	if b.cwd != "" {
		layered.SetWorkspacePolicy(workspace.NewPolicy(workspace.NewStore(workspace.DefaultPath()), b.cwd))
	}
	if len(b.frameRoot) > 0 {
		layered.SetFrameSubtrees(b.activeFrm, b.frameRoot)
	}
	return layered
}

// Attach starts the interactive loop + heartbeat for a thread. Idempotent.
// Refuses with ErrThreadOwned when another live process holds the thread
// (fresh heartbeat), the single-owner guard (spec §11.1).
func (b *carlosBackend) Attach(ctx context.Context, id string) error {
	b.mu.Lock()
	if _, ok := b.attached[id]; ok {
		b.mu.Unlock()
		return nil // already ours, no-op
	}
	b.mu.Unlock()

	// Cross-process guard: a fresh heartbeat from some other process means
	// it owns the interactive loop (a TUI session, most likely).
	if row, ok, err := b.log.GetAgent(ctx, id); err == nil && ok {
		if !row.LastHeartbeatAt.IsZero() && time.Since(row.LastHeartbeatAt) < agent.StalenessTolerance {
			return fmt.Errorf("%w (heartbeat %s fresh)", web.ErrThreadOwned, time.Since(row.LastHeartbeatAt).Round(time.Second))
		}
	}

	// Resume/refresh the row (un-orphans it) so the projection + heartbeat
	// are current before we drive it. Slice 9f split the janitor prune out
	// of ensureDefaultAgent; run it inline on the brand-new branch to keep
	// this path's behaviour identical (diagnostics were io.Discard before
	// the split too).
	created, err := ensureDefaultAgent(ctx, b.log, id, b.dispatch.name, b.dispatch.model, b.cfg.UserName)
	if err != nil {
		return err
	}
	if created {
		pruneEmptyOrphans(ctx, b.log, io.Discard)
	}

	approver := web.NewWebApprover(id, b.hub.Hub().Publish)
	layered := b.newLayered(approver)
	// Sub-agents of this thread share its layered approver (last attach wins
	// for the single shared supervisor - an accepted v1 quirk, spec §11.2).
	b.sup.SetSubAgentApprover(layered)

	// The thread's loop + heartbeat must outlive THIS call's ctx: in
	// production ctx is the attach/create request's r.Context(), which
	// net/http cancels when the handler returns. Tie the thread to the
	// server's lifetime instead; Detach/Shutdown still cancel it
	// explicitly. (Regression: web_backend_attach_ctx_test.go - the
	// "attach old chat -> send -> 'state: orphaned'" bug.)
	life := b.lifeCtx
	if life == nil {
		life = context.Background()
	}
	threadCtx, cancel := context.WithCancel(life)
	loop := chatglue.NewLoop(chatglue.Config{
		Provider: b.dispatch.provider,
		Model:    b.dispatch.model,
		Tools:    b.parent,
		Approver: layered,
		System:   b.system,
	}, b.log, b.src, id)
	if err := loop.Start(threadCtx); err != nil {
		cancel()
		approver.Close()
		return err
	}
	b.sup.StartHeartbeat(threadCtx, id)

	b.mu.Lock()
	b.attached[id] = &webThread{loop: loop, approver: approver, cancel: cancel}
	b.mu.Unlock()
	return nil
}

// Detach stops the loop + heartbeat and unblocks any pending approval as a
// deny. Idempotent.
func (b *carlosBackend) Detach(id string) error {
	b.mu.Lock()
	wt, ok := b.attached[id]
	delete(b.attached, id)
	b.mu.Unlock()
	if !ok {
		return nil
	}
	wt.loop.Stop()
	wt.approver.Close()
	wt.cancel()
	return nil
}

// Send appends an EvtUserMessage; the attached loop reacts and streams the
// turn. Does not wait for the turn (spec §9.2). Requires attachment.
func (b *carlosBackend) Send(ctx context.Context, id, text string) (int64, error) {
	if !b.Attached(id) {
		return 0, fmt.Errorf("thread not attached")
	}
	payload, err := json.Marshal(agent.MessagePayload{Text: text})
	if err != nil {
		return 0, err
	}
	return b.log.Append(ctx, agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtUserMessage, Payload: payload,
	})
}

func (b *carlosBackend) Resolve(id, requestID, decision string) error {
	b.mu.Lock()
	wt, ok := b.attached[id]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("thread not attached")
	}
	return wt.approver.Resolve(requestID, decision)
}

// CreateThread mints a fresh thread, seeds the agent row, and auto-attaches
// so it is immediately interactive.
func (b *carlosBackend) CreateThread(ctx context.Context, title string) (agent.Session, error) {
	id, err := mintSessionID(time.Now().UTC())
	if err != nil {
		return agent.Session{}, err
	}
	// Slice 9f: prune split out of ensureDefaultAgent; CreateThread always
	// takes the brand-new branch, so fire it inline exactly as before.
	created, err := ensureDefaultAgent(ctx, b.log, id, b.dispatch.name, b.dispatch.model, b.cfg.UserName)
	if err != nil {
		return agent.Session{}, err
	}
	if created {
		pruneEmptyOrphans(ctx, b.log, io.Discard)
	}
	if err := b.Attach(ctx, id); err != nil {
		return agent.Session{}, err
	}
	t := title
	if t == "" {
		t = "chat with " + b.cfg.UserName + " (" + b.dispatch.name + ")"
	}
	return agent.Session{ID: id, Title: t, Model: b.dispatch.model, State: agent.StateRunning}, nil
}

// Delete detaches the thread if this process is driving it, then hard-
// deletes it and its sub-agent lineage. force is set when we owned the
// thread (we just stopped its loop + heartbeat, so the fresh heartbeat is
// ours, not a foreign owner's), bypassing the live guard; an unowned
// thread keeps the guard, so a thread a TUI session is driving is refused.
func (b *carlosBackend) Delete(id string) (int, error) {
	owned := b.Attached(id)
	if owned {
		_ = b.Detach(id)
	}
	return agent.DeleteSession(context.Background(), b.log, id, owned)
}

// Children returns the thread's full sub-agent roster - live AND
// finished - from the agents projection table (parent_id == id). The
// previous implementation read the supervisor's in-memory in-flight map
// (SnapshotChildrenOf), which is empty for any child that has terminated
// and, worse, was keyed by the parent id the Agent tool passed to Spawn -
// historically "" - so the web crew column never saw anything at all.
// Reading the DB makes the crew column durable: a finished thread's
// children stay inspectable with their final state + spend.
func (b *carlosBackend) Children(ctx context.Context, id string) []web.ChildSnap {
	snaps, err := agent.ListChildSnapshots(ctx, b.log, id)
	if err != nil || len(snaps) == 0 {
		return nil
	}
	out := make([]web.ChildSnap, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, web.ChildSnap{
			ID:        s.AgentID,
			State:     s.State.String(),
			Title:     s.Title,
			LastTool:  s.LastTool,
			Tokens:    s.Tokens,
			CostCents: s.CostCents,
			StartedAt: s.StartedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// publishChildren pushes a fresh children snapshot for the thread onto
// the SSE hub. Installed as the supervisor's child notifier, so every
// child lifecycle edge (spawned / running / done / failed) makes the
// browser's crew column update live instead of waiting for a reconnect
// or a re-select.
func (b *carlosBackend) publishChildren(parentID string) {
	ctx := b.lifeCtx
	if ctx == nil {
		ctx = context.Background()
	}
	kids := b.Children(ctx, parentID)
	b.hub.Hub().Publish(web.ChildrenEvent(parentID, kids, time.Now().UTC()))
}

func (b *carlosBackend) LiveText(id string) string { return b.src.Get(id) }

func (b *carlosBackend) PendingApprovals(id string) []web.WireEvent {
	b.mu.Lock()
	wt, ok := b.attached[id]
	b.mu.Unlock()
	if !ok {
		return nil
	}
	return wt.approver.Pending()
}

// Shutdown detaches every thread (stops loops + heartbeats). Called on
// server teardown.
func (b *carlosBackend) Shutdown() {
	b.mu.Lock()
	ids := make([]string, 0, len(b.attached))
	for id := range b.attached {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	for _, id := range ids {
		_ = b.Detach(id)
	}
}
