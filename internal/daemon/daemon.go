package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
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
	// completion (slice 8d). Optional — nil means notifications are
	// disabled. Production typically wires &SystemNotifier{}; tests
	// pass a recording fake to assert content.
	Notifier Notifier
}

// Daemon is one running carlos daemon process: it owns the UDS listener,
// the per-process supervisor, the schedule list, and the main loop.
//
// Lifecycle (Run):
//
//	1. Open state.db (single-writer invariant — see eventlog_sqlite.go).
//	2. Open UDS listener at ~/.carlos/daemon.sock (fail-fast on EADDRINUSE).
//	3. Load config; build the schedule list.
//	4. Install SIGHUP (reload) and SIGTERM/SIGINT (graceful shutdown) handlers.
//	5. Start the supervisor + IPC accept goroutine.
//	6. Tick loop: every TickInterval, for each Due() schedule, spawn a
//	   sub-agent and wait for completion before considering it for the
//	   next tick. One-shot schedules (Once=true) are deleted from
//	   config after a successful fire.
//	7. On shutdown: cancel in-flight children, close the listener,
//	   close state.db, return.
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

	listener net.Listener

	mu         sync.Mutex
	schedules  []schedule.Schedule
	gatewayCfg config.GatewayConfig
	gw         *gatewayRuntime
	startedAt  time.Time
	reloadAt   time.Time

	// activeCount tracks in-flight scheduled spawns so the status
	// response can surface "n schedules running right now".
	activeCount int

	// stopOnce + stopFn coordinate graceful shutdown across the signal
	// handler, the IPC stop command, and ctx cancellation.
	stopOnce sync.Once
	stopFn   context.CancelFunc
}

// New constructs a Daemon from Options. Does NOT start anything — call
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
	return &Daemon{opts: opts}, nil
}

// Run drives the daemon until ctx is cancelled (or a stop request
// arrives over IPC). Returns nil on clean shutdown; an error if any of
// the startup steps fail.
//
// Run is single-call — invoking it twice on the same Daemon is
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

	// 4. Signal handlers — derive a cancellable ctx so SIGTERM and
	//    the IPC stop verb both unwind through the same path.
	runCtx, cancel := context.WithCancel(ctx)
	d.stopFn = cancel

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
						// the previous schedule list in place; we just log
						// to stderr so the user sees what happened.
						fmt.Fprintf(os.Stderr, "carlos daemon: reload failed: %v\n", err)
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
		gw, err := startGateway(runCtx, d.log, gwCfg)
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
		if d.stopFn != nil {
			d.stopFn()
		}
	})
}

// Reload re-reads the config from disk and replaces the schedule list.
// Schedules with the same Name and Spec preserve their LastRunAt /
// LastRunOK fields (otherwise a SIGHUP would cause an immediate
// re-fire of any schedule that already ran today).
func (d *Daemon) Reload() error {
	d.mu.Lock()
	prev := make(map[string]schedule.Schedule, len(d.schedules))
	for _, s := range d.schedules {
		prev[s.Name+"\x00"+s.Spec] = s
	}
	d.mu.Unlock()
	if err := d.loadConfig(); err != nil {
		return err
	}
	d.mu.Lock()
	for i, s := range d.schedules {
		if old, ok := prev[s.Name+"\x00"+s.Spec]; ok {
			d.schedules[i].LastRunAt = old.LastRunAt
			d.schedules[i].LastRunOK = old.LastRunOK
		}
	}
	d.reloadAt = d.opts.Now.Now().UTC()
	d.mu.Unlock()
	return nil
}

// loadConfig reads ConfigPath, validates each schedule, and replaces
// d.schedules. A single malformed schedule is non-fatal — we log it to
// stderr and skip it so one bad entry doesn't disable the whole daemon.
//
// The gateway block is cached on d so Run() can construct the broker
// after this returns. Reload (SIGHUP) refreshes the cache but does
// NOT rebuild the gateway — see internal/daemon/gateway.go for why.
func (d *Daemon) loadConfig() error {
	cfg, err := config.Load(d.opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("daemon: load config: %w", err)
	}
	good := make([]schedule.Schedule, 0, len(cfg.Schedules))
	for _, s := range cfg.Schedules {
		if err := s.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "carlos daemon: skipping schedule %q: %v\n", s.Name, err)
			continue
		}
		good = append(good, s)
	}
	d.mu.Lock()
	d.schedules = good
	d.gatewayCfg = cfg.Gateway
	d.mu.Unlock()
	return nil
}

// tick walks the schedule list, spawning the due ones. Per-schedule
// fires happen sequentially within one tick — a daemon that just woke
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
	d.mu.Unlock()

	for _, s := range snapshot {
		if !s.Due(now) {
			continue
		}
		ok := d.fire(ctx, s)
		// Persist the update — both in-memory and on-disk.
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
			fmt.Fprintf(os.Stderr, "carlos daemon: persist schedules: %v\n", err)
		}
		// One-shot: remove on successful fire.
		if s.Once && ok {
			d.removeSchedule(s.Name)
			_ = d.persistSchedules()
		}
	}
}

// fire dispatches one scheduled run through the supervisor and returns
// true iff the SpawnResult reported state=done (no Err).
//
// In test mode (Spawner returns immediately) we still wait on the
// returned channel so the loop's "don't double-fire" invariant holds.
func (d *Daemon) fire(ctx context.Context, s schedule.Schedule) bool {
	contract := agent.SpawnContract{
		Objective:    s.Prompt,
		MaxTokens:    s.BudgetTokens,
		MaxWallClock: 0, // honored separately if the user sets it
		// Tool allowlist is intentionally empty; child gets the parent's
		// base registry the supervisor wires through buildChildRegistry.
	}
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
		fmt.Fprintf(os.Stderr, "carlos daemon: spawn %q: %v\n", s.Name, err)
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
		fmt.Fprintf(os.Stderr, "carlos daemon: notify %q: %v\n", s.Name, err)
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
			fmt.Fprintf(os.Stderr, "carlos daemon: accept: %v\n", err)
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
	default:
		return Response{Ok: false, Msg: fmt.Sprintf("unknown cmd %q", req.Cmd)}
	}
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
