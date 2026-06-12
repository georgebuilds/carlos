package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/schedule"
	"github.com/georgebuilds/carlos/internal/tools"
)

// Clock abstracts time.Now so the daemon's main loop is testable with a
// fake clock. Production code uses RealClock; tests use FakeClock to
// step time forward deterministically.
type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// Spawner is the subset of *agent.Supervisor the daemon's main loop
// needs. Defining it here (rather than depending on the concrete
// *Supervisor) lets tests stand up a fake without spinning the full
// pipeline.
type Spawner interface {
	Spawn(ctx context.Context, parentID string, contract agent.SpawnContract) (*agent.SubAgent, <-chan agent.SpawnResult, error)
}

// Options configures a Daemon. Production callers supply Provider +
// BaseTools so Spawn fires real sub-agents; tests pass Now + an
// in-memory Spawner so the loop's logic can be exercised without a
// live state.db or provider.
type Options struct {
	// ConfigPath is the on-disk config the daemon reads at startup and
	// re-reads on Reload(). Required; no default.
	ConfigPath string

	// StateDBPath is the SQLite event log. Required for production
	// runs (the daemon spawns sub-agents that write here). Tests may
	// leave it empty if they provide a custom Spawner.
	StateDBPath string

	// SocketPath is where the UDS listener binds. Empty → DefaultSocketPath.
	SocketPath string

	// Provider + BaseTools build the per-spawn supervisor when the
	// daemon owns the supervisor lifecycle. Optional if Spawner is set.
	Provider  providers.Provider
	BaseTools *tools.Registry

	// Spawner, if non-nil, is used directly instead of constructing a
	// supervisor from Provider + BaseTools. Test seam.
	Spawner Spawner

	// TickInterval is the main loop's poll cadence. Default 30s.
	TickInterval time.Duration

	// Now is the clock the loop reads. Default RealClock.
	Now Clock

	// DisableSignals, when true, skips installing SIGTERM/SIGINT/SIGHUP
	// handlers. Tests set this to avoid stealing signals from the test
	// runner when many daemons spin up in one process.
	DisableSignals bool

	// Notifier dispatches a desktop banner per scheduled-run
	// completion (slice 8d). Optional - nil means notifications are
	// disabled. Production typically wires &SystemNotifier{}; tests
	// pass a recording fake to assert content.
	Notifier Notifier

	// Home is the user's home directory. Used by Phase F-14 to build
	// per-frame paths via frame.PathsFor. Empty means the daemon won't
	// thread frame-scoped paths into the fire-time tool registry.
	Home string

	// ProviderBuilder, when non-nil, lets the daemon construct a fresh
	// provider client per scheduled fire so a frame's provider_override
	// is honoured. Phase F-14. Receiving a nil/zero ResolvedProvider
	// means the caller refused to construct (caller decides whether to
	// fall back to opts.Provider). When ProviderBuilder is itself nil,
	// every fire uses opts.Provider unconditionally - the legacy
	// behaviour.
	ProviderBuilder func(frame.ResolvedProvider) (providers.Provider, error)

	// Logger, when non-nil, is used for every package-internal log line
	// (lifecycle events, schedule fires, gateway errors). Defaults to a
	// text handler writing to os.Stderr at Info level so production
	// launchd / systemd units get structured output without any extra
	// wiring. Tests pass a *bytes.Buffer-backed handler to assert on
	// emitted attributes.
	Logger *slog.Logger
}

// fireLogger is the subset of *schedule.FireLog the tick path consumes.
// Exists as an interface (rather than holding the concrete pointer
// directly) so tests can stub Append failures without touching the file
// system. Production wires the *schedule.FireLog returned by
// schedule.OpenFireLog; the tick path treats a nil fireLogger as "no
// suppression journal" and falls back to plain Due() behaviour.
type fireLogger interface {
	Has(name string, slot time.Time) bool
	Append(name string, slot time.Time) error
	Close() error
	Path() string
}

// Daemon is one running carlos daemon process: it owns the UDS listener,
// the per-process supervisor, the schedule list, and the main loop.
//
// Lifecycle (Run):
//
//  1. Open state.db (single-writer invariant - see eventlog_sqlite.go).
//  2. Open UDS listener at ~/.carlos/daemon.sock (fail-fast on EADDRINUSE).
//  3. Load config; build the schedule list.
//  4. Install SIGHUP (reload) and SIGTERM/SIGINT (graceful shutdown) handlers.
//  5. Start the supervisor + IPC accept goroutine.
//  6. Tick loop: every TickInterval, for each Due() schedule, spawn a
//     sub-agent and wait for completion before considering it for the
//     next tick. One-shot schedules (Once=true) are deleted from
//     config after a successful fire.
//  7. On shutdown: cancel in-flight children, close the listener,
//     close state.db, return.
type Daemon struct {
	opts Options

	// State.db handle; nil in test mode when the caller supplied a
	// Spawner directly.
	log *agent.SQLiteEventLog
	// supervisor is the per-process supervisor the daemon constructs
	// when Options.Spawner is nil. nil in test mode.
	supervisor *agent.Supervisor

	// spawner is what the tick loop calls. Either supervisor (cast to
	// Spawner) in production or opts.Spawner in tests.
	spawner Spawner

	// fireLog is the crash-window double-fire suppression journal. The
	// tick path writes (schedule name, slot) to it BEFORE invoking the
	// scheduled action so a process restart that replays the log skips
	// the recorded slot rather than re-running it. nil when the journal
	// could not be opened at boot; the tick path falls back to plain
	// Due() in that case (loud-warn at boot, never refuses to start).
	fireLog fireLogger

	listener net.Listener

	mu               sync.Mutex
	schedules        []schedule.Schedule
	gatewayCfg       config.GatewayConfig
	gw               *gatewayRuntime
	startedAt        time.Time
	reloadAt         time.Time
	lastReloadStatus *ReloadStatus

	// Phase F-14: cached cfg fields the per-fire frame resolver needs.
	// Snapshotted by loadConfig (and refreshed on reload) so fire() does
	// not have to re-read the YAML from disk on every tick.
	userName        string
	defaultProvider string
	frameCfg        frame.Config
	providersCfg    map[string]config.ProviderConfig
	vaultCfg        config.VaultConfig

	// activeCount tracks in-flight scheduled spawns so the status
	// response can surface "n schedules running right now".
	activeCount int

	// stopOnce + stopFn coordinate graceful shutdown across the signal
	// handler, the IPC stop command, and ctx cancellation.
	stopOnce sync.Once
	stopFn   context.CancelFunc

	// logger is the package-internal slog.Logger. Always non-nil after
	// New (defaulted to a stderr text handler so call sites never need a
	// nil check). Scoped with a "component" attr so multi-binary
	// journals can filter on it.
	//
	// Use d.slogger() instead of reading this field directly: a handful
	// of tests construct Daemon{} literals without going through New,
	// and slogger backfills slog.Default() so those keep working
	// without a panic.
	logger *slog.Logger
}

// slogger returns a never-nil *slog.Logger. Always-on backstop for
// callers that bypass New (a few existing in-package tests do this) so
// migrated lifecycle logging never panics on the zero Daemon.
func (d *Daemon) slogger() *slog.Logger {
	if d.logger != nil {
		return d.logger
	}
	return slog.Default()
}

// New constructs a Daemon from Options. Does NOT start anything - call
// Run to actually open the listener + state.db.
func New(opts Options) (*Daemon, error) {
	if opts.ConfigPath == "" {
		return nil, errors.New("daemon: ConfigPath required")
	}
	if opts.TickInterval <= 0 {
		opts.TickInterval = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = RealClock{}
	}
	if opts.SocketPath == "" {
		opts.SocketPath = DefaultSocketPath()
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	logger = logger.With("component", "carlos-daemon")
	return &Daemon{opts: opts, logger: logger}, nil
}

// Run drives the daemon until ctx is cancelled (or a stop request
// arrives over IPC). Returns nil on clean shutdown; an error if any of
// the startup steps fail.
//
// Run is single-call - invoking it twice on the same Daemon is
// undefined behavior.
func (d *Daemon) Run(ctx context.Context) error {
	// 1. State.db (skipped if test mode supplied a Spawner).
	if d.opts.Spawner == nil {
		if d.opts.StateDBPath == "" {
			return errors.New("daemon: StateDBPath required when Spawner not provided")
		}
		log, err := agent.OpenStateDB(d.opts.StateDBPath)
		if err != nil {
			return fmt.Errorf("daemon: open state.db: %w", err)
		}
		d.log = log
		// Boot-time prune: a daemon restart is a natural moment to
		// sweep empty orphan rows (top-level chats the user never
		// typed in plus sub-agents that never made a tool call)
		// older than the grace window. Failure is logged, never
		// blocks startup - a janitor pass should never stop the
		// daemon from coming up.
		if pruned, err := d.log.DeleteEmptyOrphanedAgents(ctx, agent.DefaultOrphanPruneAge); err != nil {
			d.slogger().Warn("prune empty orphans failed", "err", err)
		} else if len(pruned) > 0 {
			d.slogger().Info("pruned empty orphaned agents", "count", len(pruned))
		}
	}

	// 2. UDS listener.
	l, err := Listen(d.opts.SocketPath)
	if err != nil {
		if d.log != nil {
			_ = d.log.Close()
		}
		return err
	}
	d.listener = l

	// 3. Load config + schedules.
	if err := d.loadConfig(); err != nil {
		_ = l.Close()
		if d.log != nil {
			_ = d.log.Close()
		}
		return err
	}

	// 3a. Open the fire-log journal. Sits next to state.db in production
	// and next to the test config in Spawner-mode tests. A failure here
	// is non-fatal: the daemon falls back to nil-fireLog (plain Due()
	// behaviour) and loud-warns so the operator can see the journal is
	// off. We refuse to make the journal a startup gate; better to run
	// without crash-window suppression than to refuse to schedule.
	if path := d.fireLogPath(); path != "" {
		fl, err := schedule.OpenFireLog(path)
		if err != nil {
			d.slogger().Warn("firelog open failed, falling back to plain Due() (crash-window suppression off)", "path", path, "err", err)
		} else {
			d.mu.Lock()
			d.fireLog = fl
			d.mu.Unlock()
		}
	}

	// 4. Signal handlers - derive a cancellable ctx so SIGTERM and
	//    the IPC stop verb both unwind through the same path.
	runCtx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	d.stopFn = cancel
	d.mu.Unlock()

	var sigCh chan os.Signal
	if !d.opts.DisableSignals {
		sigCh = make(chan os.Signal, 4)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		go func() {
			for sig := range sigCh {
				switch sig {
				case syscall.SIGHUP:
					if err := d.Reload(); err != nil {
						// Best-effort: a malformed config on reload leaves
						// the previous schedule list in place. The error
						// is also captured in lastReloadStatus so the IPC
						// status surface picks it up.
						d.slogger().Error("reload failed", "err", err)
					}
				case syscall.SIGTERM, syscall.SIGINT:
					d.Stop()
					return
				}
			}
		}()
		defer func() {
			signal.Stop(sigCh)
			close(sigCh)
		}()
	}

	// 5. Supervisor (production mode) or test-supplied spawner.
	if d.opts.Spawner != nil {
		d.spawner = d.opts.Spawner
	} else {
		d.supervisor = agent.NewSupervisor(d.log, d.opts.Provider, d.opts.BaseTools)
		d.supervisor.Run(runCtx)
		d.spawner = supervisorAdapter{d.supervisor}
		defer d.supervisor.Shutdown()
	}

	// 5.5 Gateway (broker + adapters + approvals router). Skipped when
	// the config block is disabled OR when no event log is available
	// (test mode). A construction failure aborts startup so a
	// misconfigured gateway surfaces loudly at boot rather than
	// silently fanning out into nothing.
	d.mu.Lock()
	gwCfg := d.gatewayCfg
	d.mu.Unlock()
	if d.log != nil && gwCfg.Enabled {
		gw, err := startGateway(runCtx, d.log, gwCfg, d.slogger())
		if err != nil {
			_ = d.listener.Close()
			_ = d.log.Close()
			return fmt.Errorf("daemon: gateway: %w", err)
		}
		d.mu.Lock()
		d.gw = gw
		d.mu.Unlock()
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = gw.Stop(stopCtx)
		}()
	}

	// 6. IPC accept goroutine.
	go d.acceptLoop(runCtx)

	d.mu.Lock()
	d.startedAt = d.opts.Now.Now().UTC()
	d.mu.Unlock()

	// 7. Tick loop.
	ticker := time.NewTicker(d.opts.TickInterval)
	defer ticker.Stop()
	// First tick immediate so a schedule that was due at startup fires
	// without waiting a full TickInterval.
	d.tick(runCtx)
	for {
		select {
		case <-runCtx.Done():
			_ = d.listener.Close()
			d.mu.Lock()
			fl := d.fireLog
			d.mu.Unlock()
			if fl != nil {
				_ = fl.Close()
			}
			if d.log != nil {
				_ = d.log.Close()
			}
			return nil
		case <-ticker.C:
			d.tick(runCtx)
		}
	}
}

// supervisorAdapter narrows *agent.Supervisor to the Spawner interface
// so the daemon can hold a single field.
type supervisorAdapter struct{ s *agent.Supervisor }

func (a supervisorAdapter) Spawn(ctx context.Context, parentID string, contract agent.SpawnContract) (*agent.SubAgent, <-chan agent.SpawnResult, error) {
	return a.s.Spawn(ctx, parentID, contract)
}

// Stop initiates graceful shutdown. Idempotent.
func (d *Daemon) Stop() {
	d.stopOnce.Do(func() {
		d.mu.Lock()
		fn := d.stopFn
		d.mu.Unlock()
		if fn != nil {
			fn()
		}
	})
}

// Reload re-reads the config from disk and replaces the schedule list.
// Schedules with the same Name and Spec preserve their LastRunAt /
// LastRunOK fields (otherwise a SIGHUP would cause an immediate
// re-fire of any schedule that already ran today).
//
// On both success and failure the outcome is captured in
// d.lastReloadStatus so `carlos daemon status` can surface a bad
// reload to operators (without that, the only signal is a stderr line
// in the launchd / systemd journal).
func (d *Daemon) Reload() error {
	d.mu.Lock()
	prev := make(map[string]schedule.Schedule, len(d.schedules))
	for _, s := range d.schedules {
		prev[s.Name+"\x00"+s.Spec] = s
	}
	d.mu.Unlock()
	if err := d.loadConfig(); err != nil {
		d.mu.Lock()
		d.lastReloadStatus = &ReloadStatus{
			At:  d.opts.Now.Now().UTC(),
			OK:  false,
			Msg: err.Error(),
		}
		d.mu.Unlock()
		return err
	}
	d.mu.Lock()
	for i, s := range d.schedules {
		if old, ok := prev[s.Name+"\x00"+s.Spec]; ok {
			d.schedules[i].LastRunAt = old.LastRunAt
			d.schedules[i].LastRunOK = old.LastRunOK
		}
	}
	now := d.opts.Now.Now().UTC()
	d.reloadAt = now
	d.lastReloadStatus = &ReloadStatus{
		At:  now,
		OK:  true,
		Msg: "config reloaded",
	}
	d.mu.Unlock()
	return nil
}

// loadConfig reads ConfigPath, validates each schedule, and replaces
// d.schedules. A single malformed schedule is non-fatal - we log it to
// stderr and skip it so one bad entry doesn't disable the whole daemon.
//
// The gateway block is cached on d so Run() can construct the broker
// after this returns. Reload (SIGHUP) refreshes the cache but does
// NOT rebuild the gateway - see internal/daemon/gateway.go for why.
func (d *Daemon) loadConfig() error {
	cfg, err := config.Load(d.opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("daemon: load config: %w", err)
	}
	known := make(map[string]bool, len(cfg.Frames.List))
	for _, f := range cfg.Frames.List {
		known[f.Name] = true
	}
	good := make([]schedule.Schedule, 0, len(cfg.Schedules))
	for _, s := range cfg.Schedules {
		if err := s.Validate(known); err != nil {
			d.slogger().Warn("skipping invalid schedule", "name", s.Name, "err", err)
			continue
		}
		good = append(good, s)
	}
	d.mu.Lock()
	d.schedules = good
	d.gatewayCfg = cfg.Gateway
	// Phase F-14: snapshot the cfg fields the fire path needs to resolve
	// a schedule's frame at run time. Cheap to copy; saves us a re-read
	// on every tick.
	d.userName = cfg.UserName
	d.defaultProvider = cfg.DefaultProvider
	d.frameCfg = cfg.Frames
	d.providersCfg = cfg.Providers
	d.vaultCfg = cfg.Vault
	d.mu.Unlock()
	return nil
}

// tick walks the schedule list, spawning the due ones. Per-schedule
// fires happen sequentially within one tick - a daemon that just woke
// up and finds 10 due schedules will fire them in declaration order.
//
// For each fire:
//  1. Compose a SpawnContract from the schedule (prompt, token cap, time cap).
//  2. Spawn via d.spawner; AutoApprover wired by the supervisor itself.
//  3. Wait for the SpawnResult so the daemon doesn't fire the same
//     schedule again before the previous run finished.
//  4. Update LastRunAt + LastRunOK in-memory; persist back to config
//     so the next process restart honors the timestamps.
func (d *Daemon) tick(ctx context.Context) {
	now := d.opts.Now.Now()
	d.mu.Lock()
	snapshot := make([]schedule.Schedule, len(d.schedules))
	copy(snapshot, d.schedules)
	fireLog := d.fireLog
	d.mu.Unlock()

	for _, s := range snapshot {
		if !s.Due(now) {
			continue
		}
		// Crash-window double-fire suppression: consult the fire-log
		// journal before invoking the action, and durably record the
		// (name, slot) pair BEFORE invoking it. A restart mid-action
		// then replays the log and skips re-firing the same slot.
		//
		// When fireLog is nil (open failed at boot) the journal is
		// disabled and we run with plain Due() semantics. The slot
		// computation still happens but is not persisted, so a crash
		// during the action will double-fire on restart - that's the
		// legacy behaviour and matches what the boot-time warning
		// already told the operator.
		slot := s.SlotFor(now)
		if fireLog != nil {
			if !slot.IsZero() && fireLog.Has(s.Name, slot) {
				// Already fired this slot in a previous process
				// instance (likely the one that crashed). Stay quiet.
				continue
			}
			if !slot.IsZero() {
				if err := fireLog.Append(s.Name, slot); err != nil {
					// Append must be durable before the action runs.
					// If we can't get a durable record, skip the fire
					// rather than risk a double-fire on restart.
					d.slogger().Warn("firelog append failed, skipping fire to preserve at-most-once", "name", s.Name, "err", err)
					continue
				}
			}
		}
		ok := d.fire(ctx, s)
		// Persist the update - both in-memory and on-disk.
		d.mu.Lock()
		// Re-find by name (the list may have been reloaded mid-tick).
		for j := range d.schedules {
			if d.schedules[j].Name == s.Name {
				d.schedules[j].LastRunAt = now.UTC()
				d.schedules[j].LastRunOK = ok
				break
			}
		}
		d.mu.Unlock()
		if err := d.persistSchedules(); err != nil {
			d.slogger().Error("persist schedules", "err", err)
		}
		// One-shot: remove on successful fire.
		if s.Once && ok {
			d.removeSchedule(s.Name)
			// The in-memory state is already advanced. If this persist
			// fails, the on-disk config still carries the schedule and
			// the daemon will re-fire it on restart. We log loudly but
			// don't restore the in-memory entry — doing so could race
			// with newly-arriving fires and re-introduce a removed
			// schedule. Operator sees the error in the daemon log and
			// can fix the underlying disk/config issue.
			if err := d.persistSchedules(); err != nil {
				d.slogger().Error("persist schedules after one-shot removal", "schedule", s.Name, "err", err)
			}
		}
	}
}

// fire dispatches one scheduled run through the supervisor and returns
// true iff the SpawnResult reported state=done (no Err).
//
// In test mode (Spawner returns immediately) we still wait on the
// returned channel so the loop's "don't double-fire" invariant holds.
func (d *Daemon) fire(ctx context.Context, s schedule.Schedule) bool {
	// Phase F-14: resolve the schedule's frame and build the per-fire
	// sysprompt, tool registry, and (when ProviderBuilder is set)
	// provider. Empty Schedule.Frame falls back to cfg.Frames.Active,
	// then cfg.Frames.Default, then frame.DefaultPersonalName.
	frameName, frameInfo, frameReg, frameProvider, frameModel := d.resolveFrameForFire(s)

	d.mu.Lock()
	userName := d.userName
	d.mu.Unlock()

	contract := agent.SpawnContract{
		Objective:    s.Prompt,
		MaxTokens:    s.BudgetTokens,
		MaxWallClock: 0, // honored separately if the user sets it
		System:       agent.SystemPromptWithFrame(userName, "", "", frameInfo),
		Model:        frameModel,
		// Tool allowlist is intentionally empty; the frame-scoped registry
		// is handed through verbatim via OverrideRegistry.
		OverrideRegistry: frameReg,
		OverrideProvider: frameProvider,
	}

	d.slogger().Info("firing schedule", "name", s.Name, "frame", frameName)

	d.mu.Lock()
	d.activeCount++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.activeCount--
		d.mu.Unlock()
	}()

	_, resultCh, err := d.spawner.Spawn(ctx, "", contract)
	if err != nil {
		d.slogger().Error("spawn failed", "name", s.Name, "err", err)
		d.pushNotification(ctx, s, false, err.Error())
		return false
	}
	select {
	case res := <-resultCh:
		ok := res.Err == nil
		var reason string
		if res.Err != nil {
			reason = res.Err.Error()
		}
		d.pushNotification(ctx, s, ok, reason)
		return ok
	case <-ctx.Done():
		return false
	}
}

// resolveFrameForFire walks Schedule.Frame → cfg.Frames.Active →
// cfg.Frames.Default → frame.DefaultPersonalName and builds the per-fire
// FrameInfo, registry, and (when ProviderBuilder is wired) provider + model.
func (d *Daemon) resolveFrameForFire(s schedule.Schedule) (string, agent.FrameInfo, *tools.Registry, providers.Provider, string) {
	d.mu.Lock()
	frameCfg := d.frameCfg
	defaultProvider := d.defaultProvider
	providersCfg := d.providersCfg
	vaultCfg := d.vaultCfg
	d.mu.Unlock()

	name := s.Frame
	if name == "" {
		name = frameCfg.Active
	}
	if name == "" {
		name = frameCfg.Default
	}
	if name == "" {
		name = frame.DefaultPersonalName
	}

	f := frameCfg.Find(name)
	if f == nil {
		// Unknown frame name on the schedule. Walk the fallbacks; we never
		// abort the fire over a stale schedule.Frame.
		if alt := frameCfg.Find(frameCfg.Active); alt != nil {
			f = alt
			name = alt.Name
		} else if alt := frameCfg.Find(frameCfg.Default); alt != nil {
			f = alt
			name = alt.Name
		}
	}

	frameInfo := agent.FrameInfo{Name: name}
	if f != nil {
		frameInfo.Append = f.SystemPromptAppend
		frameInfo.Mode = frame.EffectiveMode(*f)
	}

	// Per-fire registry. Empty baseDir matches the foreground daemon
	// boot path in cmd/carlos/daemon.go: scheduled runs share the home
	// dir as their sandbox root.
	reg := tools.NewDefaultRegistryWithBaseDirAndFrames("", vaultCfg, frameCfg, name)

	// Per-fire paths are reserved for the daemon daily-digest feature;
	// reading PathsFor here keeps the import live and signals intent
	// without changing behaviour for v1 schedules.
	_ = frame.PathsFor(d.opts.Home, name)

	// Per-fire provider. Skipped when no builder is wired (test mode) or
	// when the resolved provider is empty.
	var prov providers.Provider
	var model string
	if d.opts.ProviderBuilder != nil && f != nil {
		pantry := make(map[string]frame.SharedProvider, len(providersCfg))
		for n, pc := range providersCfg {
			pantry[n] = frame.SharedProvider{
				APIKey:       pc.APIKey,
				BaseURL:      pc.BaseURL,
				DefaultModel: pc.DefaultModel,
			}
		}
		if resolved, ok := frame.ResolveProvider(*f, defaultProvider, pantry); ok {
			built, err := d.opts.ProviderBuilder(resolved)
			if err != nil {
				d.slogger().Warn("provider build failed, falling back", "name", s.Name, "err", err)
			} else {
				prov = built
				model = resolved.Model
			}
		}
	}

	return name, frameInfo, reg, prov, model
}

// pushNotification dispatches a desktop banner for a finished
// schedule. Best-effort: a notification failure is logged but never
// crashes the tick loop. Suppressed when no Notifier is configured.
func (d *Daemon) pushNotification(ctx context.Context, s schedule.Schedule, ok bool, reason string) {
	if d.opts.Notifier == nil {
		return
	}
	var body string
	if ok {
		body = fmt.Sprintf("✓ %s ran successfully", s.Name)
	} else {
		body = fmt.Sprintf("✗ %s failed: %s", s.Name, truncate(reason, 80))
	}
	urgency := "normal"
	if !ok {
		urgency = "critical"
	}
	if err := d.opts.Notifier.Notify(ctx, Notification{
		Title:   "carlos",
		Body:    body,
		Urgency: urgency,
	}); err != nil {
		d.slogger().Warn("notify failed", "name", s.Name, "err", err)
	}
}

// truncate caps a string at max runes with "…" suffix; used by the
// notification body to keep banners under ~120 chars on both
// macOS and most Linux notification daemons.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// removeSchedule drops the named schedule from the in-memory list.
func (d *Daemon) removeSchedule(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.schedules[:0]
	for _, s := range d.schedules {
		if s.Name != name {
			out = append(out, s)
		}
	}
	d.schedules = out
}

// persistSchedules writes the current in-memory schedule list back to
// the on-disk config. Preserves every other config field by re-reading
// + re-writing through config.Save (atomic temp+rename).
func (d *Daemon) persistSchedules() error {
	cfg, err := config.Load(d.opts.ConfigPath)
	if err != nil {
		return err
	}
	d.mu.Lock()
	cfg.Schedules = make([]schedule.Schedule, len(d.schedules))
	copy(cfg.Schedules, d.schedules)
	d.mu.Unlock()
	return config.Save(d.opts.ConfigPath, cfg)
}

// acceptLoop accepts UDS connections until ctx is cancelled. Each
// connection is handled in its own goroutine (HandleConn) so a slow
// client can't block another's status query.
func (d *Daemon) acceptLoop(ctx context.Context) {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			// listener closed → Run is returning → quietly exit.
			select {
			case <-ctx.Done():
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			d.slogger().Error("accept failed", "err", err)
			return
		}
		go HandleConn(conn, d.dispatch)
	}
}

// dispatch handles one IPC request and returns the response shape the
// CLI will format. Lives here (rather than in ipc.go) so it can read
// the daemon's state directly.
func (d *Daemon) dispatch(req Request) Response {
	switch req.Cmd {
	case "status":
		return d.statusResponse()
	case "reload":
		if err := d.Reload(); err != nil {
			return Response{Ok: false, Msg: err.Error()}
		}
		return Response{Ok: true, Msg: "config reloaded"}
	case "stop":
		go d.Stop()
		return Response{Ok: true, Msg: "shutting down"}
	case "gateway-test":
		return d.gatewayTestResponse(req.Channel)
	default:
		return Response{Ok: false, Msg: fmt.Sprintf("unknown cmd %q", req.Cmd)}
	}
}

// gatewayTestResponse runs a fixed test envelope through one gateway
// channel and returns success/failure as a Response. Short bounded
// context so a wedged adapter cannot block the IPC connection past the
// HandleConn deadline.
func (d *Daemon) gatewayTestResponse(channel string) Response {
	if channel == "" {
		return Response{Ok: false, Msg: "gateway-test: channel required"}
	}
	d.mu.Lock()
	gw := d.gw
	gwCfg := d.gatewayCfg
	d.mu.Unlock()
	if gw == nil || gw.broker == nil {
		return Response{Ok: false, Msg: "gateway-test: gateway not running on this daemon (set gateway.enabled in config and restart)"}
	}
	src := gateway.Source(channel)
	if !src.Valid() {
		return Response{Ok: false, Msg: fmt.Sprintf("gateway-test: unknown channel %q (valid: ntfy, telegram, signal, custom)", channel)}
	}
	if !gatewayChannelEnabled(gwCfg, src) {
		return Response{Ok: false, Msg: fmt.Sprintf("gateway-test: channel %q is not enabled in config", channel)}
	}
	if src == gateway.SourceSignal {
		// signal ships as a stub; surface that cleanly rather than fanning
		// out a "not yet implemented" failure receipt.
		return Response{Ok: false, Msg: "gateway-test: signal adapter is stub-only"}
	}
	env := gateway.OutboundEnvelope{
		Kind:    gateway.OutboundNotification,
		Title:   "carlos: gateway test",
		Body:    fmt.Sprintf("This is a test notification from carlos via the %s channel. If you can read it, your gateway is wired correctly.", channel),
		Urgency: gateway.UrgencyDefault,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	receipt, err := gw.broker.SendTo(ctx, env, src)
	if err != nil {
		return Response{Ok: false, Msg: fmt.Sprintf("gateway-test: %v", err)}
	}
	switch receipt.Status {
	case gateway.StatusDelivered:
		return Response{Ok: true, Msg: fmt.Sprintf("gateway-test: sent via %s", channel)}
	case gateway.StatusUnknown:
		return Response{Ok: true, Msg: fmt.Sprintf("gateway-test: dispatched to %s (fire-and-forget; check the channel for the message)", channel)}
	default:
		msg := receipt.Error
		if msg == "" {
			msg = string(receipt.Status)
		}
		return Response{Ok: false, Msg: fmt.Sprintf("gateway-test: %s adapter reported failure: %s", channel, msg)}
	}
}

// gatewayChannelEnabled reports whether cfg has the named channel
// switched on. Mirrors the buildXAdapter checks in gateway.go so the
// CLI surfaces the same enabled/disabled gate the daemon uses at
// startup.
func gatewayChannelEnabled(cfg config.GatewayConfig, src gateway.Source) bool {
	switch src {
	case gateway.SourceNtfy:
		return cfg.Ntfy.Enabled
	case gateway.SourceTelegram:
		return cfg.Telegram.Enabled
	case gateway.SourceSignal:
		// Signal registers even when disabled (see buildSignalAdapter);
		// for the test verb we still gate on the config flag so the user
		// is told to enable it explicitly.
		return cfg.Signal.Enabled
	case gateway.SourceCustom:
		return cfg.Custom.Enabled
	}
	return false
}

// statusResponse builds the rich Response for the `status` verb: every
// schedule's next fire time, plus daemon-wide counters.
func (d *Daemon) statusResponse() Response {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.opts.Now.Now()
	out := Response{
		Ok:          true,
		Msg:         fmt.Sprintf("%d schedule(s) configured, %d active right now", len(d.schedules), d.activeCount),
		Schedules:   make([]ScheduleStatus, 0, len(d.schedules)),
		StartedAt:   timePtr(d.startedAt),
		ActiveCount: d.activeCount,
	}
	if !d.reloadAt.IsZero() {
		out.LastReloadAt = timePtr(d.reloadAt)
	}
	if d.lastReloadStatus != nil {
		// Defensive copy so the caller can't mutate the daemon's state.
		s := *d.lastReloadStatus
		out.LastReloadStatus = &s
	}
	var soonest time.Time
	for _, s := range d.schedules {
		nf := s.Next(now)
		st := ScheduleStatus{
			Name:       s.Name,
			Spec:       s.Spec,
			NextFireAt: nf,
			Once:       s.Once,
			LastRunOK:  s.LastRunOK,
		}
		if !s.LastRunAt.IsZero() {
			st.LastRunAt = timePtr(s.LastRunAt)
		}
		out.Schedules = append(out.Schedules, st)
		if !nf.IsZero() && (soonest.IsZero() || nf.Before(soonest)) {
			soonest = nf
		}
	}
	if !soonest.IsZero() {
		out.NextFireAt = timePtr(soonest)
	}
	return out
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// fireLogPath picks the on-disk path for the fire-log journal. In
// production this is next to state.db (both live under ~/.carlos by
// convention). In Spawner-mode tests StateDBPath is empty, so we fall
// back to the config directory so the journal still has somewhere
// stable to live. An empty return means we found no usable directory
// and the journal stays disabled.
func (d *Daemon) fireLogPath() string {
	if d.opts.StateDBPath != "" {
		return filepath.Join(filepath.Dir(d.opts.StateDBPath), "fire.log")
	}
	if d.opts.ConfigPath != "" {
		return filepath.Join(filepath.Dir(d.opts.ConfigPath), "fire.log")
	}
	return ""
}

// firelogPathLocked returns the open fire-log's path under d.mu. Used by
// tests to assert the journal landed where the boot path intended
// without racing the assignment in Run. Returns "" when no journal is
// open.
func (d *Daemon) firelogPathLocked() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fireLog == nil {
		return ""
	}
	return d.fireLog.Path()
}

// EnsureCarlosDir creates ~/.carlos with mode 0700 if it does not
// already exist. The daemon needs this BEFORE Listen because the UDS
// path lives there. Exported so the CLI side can do the same dance
// before dialing.
func EnsureCarlosDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("daemon: home dir: %w", err)
	}
	dir := filepath.Join(home, ".carlos")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: mkdir %s: %w", dir, err)
	}
	return nil
}
