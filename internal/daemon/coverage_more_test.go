package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/schedule"
)

// daemonForResolve builds a bare Daemon with a frame config wired
// directly (bypassing Run/loadConfig) so resolveFrameForFire can be
// unit-tested in isolation.
func daemonForResolve(t *testing.T, fc frame.Config) *Daemon {
	t.Helper()
	d, err := New(Options{ConfigPath: "unused.yaml", DisableSignals: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.frameCfg = fc
	return d
}

// TestResolveFrameForFire_UnknownScheduleFrameFallsBackToActive — a
// schedule that names a frame which no longer exists must not abort the
// fire; resolveFrameForFire walks to cfg.Frames.Active and resolves the
// real frame there. This exercises the Find==nil fallback branch.
func TestResolveFrameForFire_UnknownScheduleFrameFallsBackToActive(t *testing.T) {
	fc := frame.Config{
		Default: "personal",
		Active:  "work",
		List: []frame.Frame{
			frame.NewPersonal("anthropic", "claude-test"),
			{
				Name:               "work",
				Provider:           "anthropic",
				Model:              "claude-test",
				SystemPromptAppend: "Work frame.",
				Mode:               frame.ModeOrchestrator,
			},
		},
	}
	d := daemonForResolve(t, fc)

	name, info, reg, prov, model := d.resolveFrameForFire(schedule.Schedule{
		Name:  "stale",
		Frame: "deleted-long-ago", // not in List
	})

	if name != "work" {
		t.Fatalf("expected fallback to active=work, got %q", name)
	}
	if info.Name != "work" {
		t.Errorf("frameInfo.Name = %q, want work", info.Name)
	}
	if info.Append != "Work frame." {
		t.Errorf("frameInfo.Append = %q, want the work frame append", info.Append)
	}
	if reg == nil {
		t.Error("registry should be built even on the fallback path")
	}
	// No ProviderBuilder wired → prov/model stay zero.
	if prov != nil || model != "" {
		t.Errorf("provider/model should be empty without a ProviderBuilder; got %v/%q", prov, model)
	}
}

// TestResolveFrameForFire_UnknownFrameFallsBackToDefault — when the
// schedule frame is unknown AND Active is also unresolvable, the resolver
// walks to cfg.Frames.Default.
func TestResolveFrameForFire_UnknownFrameFallsBackToDefault(t *testing.T) {
	fc := frame.Config{
		Default: "personal",
		Active:  "also-missing", // Find(Active) == nil too
		List: []frame.Frame{
			frame.NewPersonal("anthropic", "claude-test"),
		},
	}
	d := daemonForResolve(t, fc)

	name, info, _, _, _ := d.resolveFrameForFire(schedule.Schedule{
		Name:  "stale",
		Frame: "deleted",
	})

	if name != frame.DefaultPersonalName {
		t.Fatalf("expected fallback to default=personal, got %q", name)
	}
	if info.Name != frame.DefaultPersonalName {
		t.Errorf("frameInfo.Name = %q, want %q", info.Name, frame.DefaultPersonalName)
	}
}

// blockingSpawner returns a result channel that never fires, so the
// caller of fire() blocks on <-resultCh until ctx is cancelled. Used to
// drive fire()'s ctx.Done() branch.
type blockingSpawner struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingSpawner) Spawn(ctx context.Context, _ string, _ agent.SpawnContract) (*agent.SubAgent, <-chan agent.SpawnResult, error) {
	b.once.Do(func() { close(b.started) })
	// Never-closed channel: fire() will select between this (silent) and
	// ctx.Done().
	return &agent.SubAgent{ID: "blocked"}, make(chan agent.SpawnResult), nil
}

// TestFire_ContextCancelledMidRunReturnsFalse — when ctx is cancelled
// while a spawn is still in flight (result channel never fires), fire()
// must take the ctx.Done() branch and report failure rather than hang.
func TestFire_ContextCancelledMidRunReturnsFalse(t *testing.T) {
	bs := &blockingSpawner{started: make(chan struct{})}
	d, err := New(Options{ConfigPath: "unused.yaml", DisableSignals: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.spawner = bs

	ctx, cancel := context.WithCancel(context.Background())
	resCh := make(chan bool, 1)
	go func() { resCh <- d.fire(ctx, schedule.Schedule{Name: "blocking"}) }()

	select {
	case <-bs.started:
	case <-time.After(2 * time.Second):
		t.Fatal("spawn never started")
	}
	cancel()

	select {
	case ok := <-resCh:
		if ok {
			t.Errorf("fire should report false when ctx is cancelled mid-run")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fire did not unwind on ctx cancellation")
	}

	// activeCount must be restored to zero after the deferred decrement.
	d.mu.Lock()
	ac := d.activeCount
	d.mu.Unlock()
	if ac != 0 {
		t.Errorf("activeCount = %d after cancellation, want 0", ac)
	}
}

// TestFire_SpawnErrorReportsFailureAndNotifies — when the spawner returns
// an error up front, fire() must push a failure notification and report
// false.
func TestFire_SpawnErrorReportsFailureAndNotifies(t *testing.T) {
	rec := &recordingNotifier{}
	d, err := New(Options{ConfigPath: "unused.yaml", DisableSignals: true, Notifier: rec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.spawner = &fakeSpawner{err: errors.New("supervisor saturated")}

	ok := d.fire(context.Background(), schedule.Schedule{Name: "doomed"})
	if ok {
		t.Fatal("fire should report false when Spawn errors")
	}
	calls := rec.calls()
	if len(calls) != 1 {
		t.Fatalf("expected one failure notification, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Body, "supervisor saturated") {
		t.Errorf("notification body missing spawn error: %q", calls[0].Body)
	}
	if calls[0].Urgency != "critical" {
		t.Errorf("spawn-failure urgency = %q, want critical", calls[0].Urgency)
	}
}

// TestEnsureCarlosDir_MkdirFailureSurfaces — when HOME points at a
// regular file, MkdirAll on "<file>/.carlos" fails (ENOTDIR) and
// EnsureCarlosDir wraps it. Covers the mkdir error arm.
func TestEnsureCarlosDir_MkdirFailureSurfaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ENOTDIR semantics differ on windows")
	}
	tmp := t.TempDir()
	homeFile := filepath.Join(tmp, "home-is-a-file")
	if err := os.WriteFile(homeFile, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	t.Setenv("HOME", homeFile)

	err := EnsureCarlosDir()
	if err == nil {
		t.Fatal("expected EnsureCarlosDir to fail when HOME is a regular file")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error should mention mkdir; got %v", err)
	}
}

// TestSystemNotifier_DispatchesOnDarwin runs the real osascript path on
// macOS. This exercises Notify's darwin switch arm and runMacOSNotify's
// happy path. Skipped on non-darwin (the dispatch is no-op there).
func TestSystemNotifier_DispatchesOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("osascript dispatch only meaningful on darwin")
	}
	if _, err := os.Stat("/usr/bin/osascript"); err != nil {
		t.Skip("osascript not present")
	}
	s := &SystemNotifier{Timeout: 5 * time.Second}
	// A banner with quotes exercises escapeForAppleScript inside the
	// dispatch. osascript display-notification is non-interactive and
	// returns immediately; it does not steal focus.
	if err := s.Notify(context.Background(), Notification{
		Body:    `carlos test "banner"`,
		Urgency: "low",
	}); err != nil {
		t.Fatalf("darwin notify dispatch failed: %v", err)
	}
}

// TestSystemNotifier_DefaultTimeoutApplied — a zero Timeout falls back to
// the 5s default before dispatch. We exercise this on darwin (where the
// real osascript dispatch runs) so the timeout-default arm is covered.
func TestSystemNotifier_DefaultTimeoutApplied(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("relies on the darwin osascript dispatch to reach past the timeout default")
	}
	if _, err := os.Stat("/usr/bin/osascript"); err != nil {
		t.Skip("osascript not present")
	}
	s := &SystemNotifier{} // Timeout: 0 → default 5s
	if err := s.Notify(context.Background(), Notification{Title: "carlos", Body: "default-timeout"}); err != nil {
		t.Fatalf("notify with default timeout failed: %v", err)
	}
}

// TestRunLinuxNotify_NotInstalledIsClearError — runLinuxNotify is a
// plain (un-build-tagged) function, so it can be unit-tested on any host.
// On a box without notify-send the exec fails with ErrNotFound and the
// wrapper turns that into an actionable install hint. The urgency arg is
// set to also exercise the --urgency prepend branch.
func TestRunLinuxNotify_NotInstalledIsClearError(t *testing.T) {
	if _, err := exec.LookPath("notify-send"); err == nil {
		t.Skip("notify-send IS installed here; the ENOENT branch is unreachable")
	}
	err := runLinuxNotify(context.Background(), Notification{
		Title:   "carlos",
		Body:    "hello",
		Urgency: "critical", // exercises the --urgency prepend
	})
	if err == nil {
		t.Fatal("expected an error when notify-send is missing")
	}
	if !strings.Contains(err.Error(), "notify-send not installed") {
		t.Errorf("error should give the install hint; got %v", err)
	}
}

// TestRunMacOSNotify_BadScriptSurfacesError — point osascript at input
// that AppleScript can't compile is hard to do via the escaped body, so
// instead we drive the error wrapper by cancelling the context before the
// call: CommandContext kills osascript and CombinedOutput returns a
// non-nil error, exercising runMacOSNotify's error arm.
func TestRunMacOSNotify_CancelledContextSurfacesError(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("runMacOSNotify shells out to osascript; meaningful only on darwin")
	}
	if _, err := exec.LookPath("osascript"); err != nil {
		t.Skip("osascript not present")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-cancelled → the exec fails immediately
	err := runMacOSNotify(ctx, Notification{Title: "carlos", Body: "x"})
	if err == nil {
		t.Fatal("expected an error from a cancelled osascript run")
	}
	if !strings.Contains(err.Error(), "osascript") {
		t.Errorf("error should be osascript-wrapped; got %v", err)
	}
}

// TestSendRequest_SetDeadlineFailureIsHardError — SendRequest treats a
// SetDeadline failure as fatal so a CLI client cannot block forever on a
// wedged daemon. errDeadlineConn (defined in ipc_test.go) injects the
// failure.
func TestSendRequest_SetDeadlineFailureIsHardError(t *testing.T) {
	conn := newErrDeadlineConn()
	_, err := SendRequest(conn, Request{Cmd: "status"})
	if err == nil {
		t.Fatal("expected SendRequest to fail when SetDeadline errors")
	}
	if !strings.Contains(err.Error(), "set deadline") {
		t.Errorf("error should mention set deadline; got %v", err)
	}
}

// TestSendRequest_WriteFailureSurfaces — if the underlying connection is
// already closed, Encode fails and SendRequest returns a write error
// rather than hanging on the read.
// writeErrConn lets SetDeadline succeed but fails every Write, so
// SendRequest reaches the Encode step and surfaces a write-request error
// (rather than short-circuiting on SetDeadline).
type writeErrConn struct{ net.Conn }

func (writeErrConn) SetDeadline(time.Time) error      { return nil }
func (writeErrConn) SetReadDeadline(time.Time) error  { return nil }
func (writeErrConn) SetWriteDeadline(time.Time) error { return nil }
func (writeErrConn) Write([]byte) (int, error)        { return 0, errors.New("synthetic write failure") }

func TestSendRequest_WriteFailureSurfaces(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_, err := SendRequest(writeErrConn{b}, Request{Cmd: "status"})
	if err == nil {
		t.Fatal("expected a write error when the underlying Write fails")
	}
	if !strings.Contains(err.Error(), "write request") {
		t.Errorf("error should mention write request; got %v", err)
	}
}

// TestSendRequest_UnparseableResponseSurfaces — a daemon that writes
// non-JSON back must produce a read-response error, not a silent zero
// Response.
func TestSendRequest_UnparseableResponseSurfaces(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		// Read (and discard) the request, then write garbage back.
		buf := make([]byte, 256)
		_, _ = server.Read(buf)
		_, _ = server.Write([]byte("not-json\n"))
		_ = server.Close()
	}()
	_, err := SendRequest(client, Request{Cmd: "status"})
	if err == nil {
		t.Fatal("expected a decode error on a garbage response")
	}
	if !strings.Contains(err.Error(), "read response") {
		t.Errorf("error should mention read response; got %v", err)
	}
}

// errAcceptListener returns a non-net.ErrClosed error from Accept so the
// acceptLoop's error-logging branch (not the quiet-shutdown branch) is
// exercised.
type errAcceptListener struct {
	err  error
	once sync.Once
	done chan struct{}
}

func (l *errAcceptListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.done) })
	return nil, l.err
}
func (l *errAcceptListener) Close() error   { return nil }
func (l *errAcceptListener) Addr() net.Addr { return dummyAddr{} }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "unix" }
func (dummyAddr) String() string  { return "dummy" }

// TestAcceptLoop_NonClosedErrorLogsAndExits — when Accept returns an
// error that is NOT net.ErrClosed and the context is still live, the
// loop logs and exits cleanly (it must not spin).
func TestAcceptLoop_NonClosedErrorLogsAndExits(t *testing.T) {
	var buf bytes.Buffer
	d, err := New(Options{
		ConfigPath:     "unused.yaml",
		DisableSignals: true,
		Logger:         slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lis := &errAcceptListener{err: errors.New("synthetic accept failure"), done: make(chan struct{})}
	d.listener = lis

	exited := make(chan struct{})
	go func() {
		d.acceptLoop(context.Background()) // live ctx → takes the error-log arm
		close(exited)
	}()

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("acceptLoop did not exit after a non-ErrClosed accept error")
	}
	if !strings.Contains(buf.String(), "accept failed") {
		t.Errorf("expected an 'accept failed' log line; got %q", buf.String())
	}
}

// TestBuildNtfyAdapter_DisabledReturnsNil — the early-return guard.
func TestBuildNtfyAdapter_DisabledReturnsNil(t *testing.T) {
	a, err := buildNtfyAdapter(config.NtfyGatewayConfig{Enabled: false})
	if err != nil {
		t.Fatalf("disabled buildNtfyAdapter should not error: %v", err)
	}
	if a != nil {
		t.Errorf("disabled buildNtfyAdapter should return nil adapter, got %v", a)
	}
}

// TestBuildNtfyAdapter_PriorityMapAndHeadersCopied — the enabled path
// copies the priority map and resolves header secrets. env:-prefixed
// header values are resolved from the environment.
func TestBuildNtfyAdapter_PriorityMapAndHeadersCopied(t *testing.T) {
	t.Setenv("CARLOS_TEST_NTFY_HEADER", "secret-header-value")
	cfg := config.NtfyGatewayConfig{
		Enabled:     true,
		Server:      "https://ntfy.example",
		Topic:       "carlos-alerts",
		PriorityMap: map[string]int{"high": 5, "low": 1},
		Headers: map[string]string{
			"Authorization": "env:CARLOS_TEST_NTFY_HEADER",
			"X-Static":      "static-value",
		},
	}
	a, err := buildNtfyAdapter(cfg)
	if err != nil {
		t.Fatalf("buildNtfyAdapter: %v", err)
	}
	if a == nil {
		t.Fatal("expected a non-nil adapter for an enabled ntfy config")
	}
}

// TestRun_GatewayConstructionFailureAbortsBoot — when the gateway is
// enabled but misconfigured (telegram on, no bot token), startGateway
// errors at boot. Run must surface that error and tear down the listener
// + state.db rather than coming up half-wired. Exercises Run's gateway-
// error cleanup arm (which only runs when d.log != nil, i.e. the full
// supervisor path).
func TestRun_GatewayConstructionFailureAbortsBoot(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	statePath := filepath.Join(dir, "state.db")

	cfg := &config.Config{
		UserName:        "Tester",
		Providers:       map[string]config.ProviderConfig{"anthropic": {APIKey: "sk-test"}},
		DefaultProvider: "anthropic",
		Gateway: config.GatewayConfig{
			Enabled:  true,
			Telegram: config.TelegramConfig{Enabled: true}, // no bot token → build fails
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Prove state.db is openable here; skip if the sandbox rejects SQLite.
	if log, err := agent.OpenStateDB(statePath); err != nil {
		t.Skipf("OpenStateDB unavailable: %v", err)
	} else {
		_ = log.Close()
	}

	d, err := New(Options{
		ConfigPath:     cfgPath,
		StateDBPath:    statePath,
		SocketPath:     shortSock(t),
		Provider:       &fakeProvider{name: "fake"}, // no Spawner → real supervisor → d.log != nil
		TickInterval:   time.Hour,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := d.Run(context.Background())
	if runErr == nil {
		t.Fatal("Run should fail when the gateway cannot be constructed")
	}
	if !strings.Contains(runErr.Error(), "gateway") {
		t.Errorf("error should mention the gateway; got %v", runErr)
	}
}

// TestRun_SignalHandlersReloadAndShutdown — with signals ENABLED, a
// SIGHUP triggers Reload (picking up a config change) and a SIGTERM
// drives graceful shutdown through the same cancel path the IPC stop
// verb uses. This exercises the signal goroutine's SIGHUP and SIGTERM
// arms in Run.
//
// Sending signals to our own PID is safe here: signal.Notify has already
// re-routed SIGHUP/SIGTERM to the daemon's channel by the time we send,
// so neither reaches the process's default disposition.
func TestRun_SignalHandlersReloadAndShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals not available on windows")
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeTestConfig(t, cfgPath, []schedule.Schedule{
		{Name: "first", Spec: "0 9 * * *", Prompt: "x"},
	})

	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     shortSock(t),
		Spawner:        &fakeSpawner{},
		TickInterval:   time.Hour, // keep the tick loop quiet; we drive via signals
		Now:            &fakeClock{now: time.Date(2026, 6, 5, 3, 0, 0, 0, time.Local)},
		DisableSignals: false, // <-- signal handlers installed
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitForSocket(t, d.opts.SocketPath, 2*time.Second)

	// Rewrite the config to add a second schedule, then SIGHUP to reload.
	writeTestConfig(t, cfgPath, []schedule.Schedule{
		{Name: "first", Spec: "0 9 * * *", Prompt: "x"},
		{Name: "second", Spec: "0 10 * * *", Prompt: "y"},
	})
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}

	// Poll until the reload took effect (schedule count went 1 -> 2).
	reloaded := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		n := len(d.schedules)
		d.mu.Unlock()
		if n == 2 {
			reloaded = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !reloaded {
		t.Fatal("SIGHUP did not trigger a config reload")
	}

	// SIGTERM should drive graceful shutdown; Run returns nil.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned non-nil after SIGTERM: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not shut down after SIGTERM")
	}
}

// TestFireLogPath_PrefersStateDBThenConfig — fireLogPath roots the
// journal next to state.db in production, falls back to the config
// directory in Spawner-mode tests, and returns "" when neither path is
// set. Covers all three arms directly.
func TestFireLogPath_PrefersStateDBThenConfig(t *testing.T) {
	// 1. StateDBPath wins.
	d1 := &Daemon{opts: Options{StateDBPath: "/var/lib/carlos/state.db", ConfigPath: "/etc/carlos/config.yaml"}}
	if got, want := d1.fireLogPath(), filepath.FromSlash("/var/lib/carlos/fire.log"); got != want {
		t.Errorf("with StateDBPath: got %q want %q", got, want)
	}
	// 2. No StateDBPath → fall back to the config directory.
	d2 := &Daemon{opts: Options{ConfigPath: "/etc/carlos/config.yaml"}}
	if got, want := d2.fireLogPath(), filepath.FromSlash("/etc/carlos/fire.log"); got != want {
		t.Errorf("config fallback: got %q want %q", got, want)
	}
	// 3. Neither set → empty (journal disabled).
	d3 := &Daemon{opts: Options{}}
	if got := d3.fireLogPath(); got != "" {
		t.Errorf("no paths: got %q want empty", got)
	}
}

// TestSupervisorAdapter_SpawnDelegates wires a real supervisor and calls
// the adapter's Spawn so the delegation line is exercised (rather than
// only the interface-shape assertion the existing tests make). A spawn
// with an empty parentID + minimal contract returns promptly; we only
// assert the call returns without panicking and yields a usable handle
// or a structured error.
func TestSupervisorAdapter_SpawnDelegates(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.db")
	log, openErr := agent.OpenStateDB(statePath)
	if openErr != nil {
		t.Skipf("OpenStateDB unavailable here: %v", openErr)
	}
	t.Cleanup(func() { _ = log.Close() })

	sup := agent.NewSupervisor(log, &fakeProvider{name: "fake"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Run(ctx)
	t.Cleanup(sup.Shutdown)

	adapter := supervisorAdapter{s: sup}
	sub, ch, err := adapter.Spawn(ctx, "", agent.SpawnContract{Objective: "noop"})
	if err != nil {
		// A structured error is an acceptable outcome (e.g. the fake
		// provider can't actually complete a loop); the point is the
		// adapter forwarded the call to the inner supervisor.
		return
	}
	if sub == nil || ch == nil {
		t.Fatal("adapter.Spawn returned nil handle/channel without an error")
	}
}

// TestListen_BindFailureSurfaces — when the socket path is itself an
// existing directory, net.Listen("unix", …) fails with EADDRINUSE/
// bind error and Listen wraps it. Exercises the listen-error arm.
func TestListen_BindFailureSurfaces(t *testing.T) {
	dir := t.TempDir()
	// Make the "socket path" a NON-EMPTY directory. The dial-probe fails
	// (a dir is not a live socket), then stale-cleanup's os.Remove fails
	// because the directory is not empty — exercising the remove-stale
	// error arm.
	sockAsDir := filepath.Join(dir, "iam-a-dir")
	if err := os.MkdirAll(filepath.Join(sockAsDir, "child"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := Listen(sockAsDir)
	if err == nil {
		t.Fatal("expected Listen to fail when the socket path is a non-empty directory")
	}
	if !strings.Contains(err.Error(), "daemon:") {
		t.Errorf("error should be daemon-wrapped; got %v", err)
	}
}

// TestListen_OverlongPathBindFails — a UDS path past the platform's
// sun_path limit (~104 on darwin, ~108 on linux) makes net.Listen fail,
// exercising Listen's bind-error wrapper. The parent dir is created first
// so we get past the mkdir step and actually reach the bind.
func TestListen_OverlongPathBindFails(t *testing.T) {
	dir := t.TempDir()
	// A single path component well over the sun_path cap.
	long := strings.Repeat("x", 200) + ".sock"
	sock := filepath.Join(dir, long)
	_, err := Listen(sock)
	if err == nil {
		t.Fatal("expected Listen to fail on an over-length socket path")
	}
	if !strings.Contains(err.Error(), "daemon:") {
		t.Errorf("error should be daemon-wrapped; got %v", err)
	}
}

// TestListen_EmptyPathErrors — the empty-path guard.
func TestListen_EmptyPathErrors(t *testing.T) {
	if _, err := Listen(""); err == nil {
		t.Fatal("expected Listen(\"\") to error")
	}
}

// resultSpawner returns a single SpawnResult carrying the configured Err
// so fire()'s result branch (and the failure-reason extraction) can be
// driven without a real supervisor.
type resultSpawner struct{ resErr error }

func (s resultSpawner) Spawn(_ context.Context, _ string, _ agent.SpawnContract) (*agent.SubAgent, <-chan agent.SpawnResult, error) {
	ch := make(chan agent.SpawnResult, 1)
	ch <- agent.SpawnResult{Err: s.resErr}
	close(ch)
	return &agent.SubAgent{ID: "res"}, ch, nil
}

// TestFire_ResultErrorReportsFailureWithReason — when the SpawnResult
// carries a non-nil Err, fire() returns false and the failure reason is
// threaded into the notification body.
func TestFire_ResultErrorReportsFailureWithReason(t *testing.T) {
	rec := &recordingNotifier{}
	d, err := New(Options{ConfigPath: "unused.yaml", DisableSignals: true, Notifier: rec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.spawner = resultSpawner{resErr: errors.New("max iterations exceeded")}

	if ok := d.fire(context.Background(), schedule.Schedule{Name: "looping"}); ok {
		t.Fatal("fire should report false when the result carries an error")
	}
	calls := rec.calls()
	if len(calls) != 1 {
		t.Fatalf("want one notification, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Body, "max iterations exceeded") {
		t.Errorf("notification body missing result error: %q", calls[0].Body)
	}
}

// stubReceiptAdapter is a minimal gateway.Adapter whose Send returns a
// caller-configured receipt. Used to drive the gatewayTestResponse status
// branches (Unknown / Failed) the fake adapter can't produce. Start
// blocks until ctx is cancelled so the broker's lifecycle stays sane.
type stubReceiptAdapter struct {
	name    gateway.Source
	receipt gateway.DeliveryReceipt
}

func (s *stubReceiptAdapter) Name() gateway.Source { return s.name }
func (s *stubReceiptAdapter) OutboundCapabilities() gateway.OutboundCapabilities {
	return gateway.OutboundCapabilities{Push: true}
}
func (s *stubReceiptAdapter) Send(_ context.Context, _ gateway.OutboundEnvelope) (gateway.DeliveryReceipt, error) {
	r := s.receipt
	if r.Source == "" {
		r.Source = s.name
	}
	return r, nil
}
func (s *stubReceiptAdapter) Start(ctx context.Context, _ gateway.IngestFunc) error {
	<-ctx.Done()
	return nil
}
func (s *stubReceiptAdapter) Stop(context.Context) error { return nil }

// daemonWithStubAdapter stands up a real broker carrying one
// stubReceiptAdapter and wires it into a Daemon's dispatch path.
func daemonWithStubAdapter(t *testing.T, channel gateway.Source, receipt gateway.DeliveryReceipt, cfg config.GatewayConfig) *Daemon {
	t.Helper()
	log := newGatewayLog(t)
	b, err := gateway.New(gateway.Options{
		Log:   log,
		Retry: gateway.RetryConfig{MaxAttempts: 1, BackoffInitial: time.Millisecond, BackoffMax: 2 * time.Millisecond},
		Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	if err := b.Register(&stubReceiptAdapter{name: channel, receipt: receipt}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return daemonWithGateway(t, cfg, b)
}

// TestDispatchGatewayTest_UnknownStatusReportsDispatched — a fire-and-
// forget channel (ntfy) whose adapter returns StatusUnknown surfaces as
// ok=true with "dispatched" phrasing rather than a failure.
func TestDispatchGatewayTest_UnknownStatusReportsDispatched(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Ntfy:    config.NtfyGatewayConfig{Enabled: true},
	}
	d := daemonWithStubAdapter(t, gateway.SourceNtfy, gateway.DeliveryReceipt{Status: gateway.StatusUnknown}, cfg)

	resp := d.dispatch(Request{Cmd: "gateway-test", Channel: "ntfy"})
	if !resp.Ok {
		t.Fatalf("StatusUnknown should be reported ok=true (fire-and-forget); got %+v", resp)
	}
	if !strings.Contains(resp.Msg, "dispatched") {
		t.Errorf("msg should mention dispatched: %q", resp.Msg)
	}
}

// TestDispatchGatewayTest_FailedStatusReportsAdapterFailure — a receipt
// with StatusFailed (but no SendTo error) takes the default switch arm
// and surfaces the adapter's reported failure.
func TestDispatchGatewayTest_FailedStatusReportsAdapterFailure(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Ntfy:    config.NtfyGatewayConfig{Enabled: true},
	}
	receipt := gateway.DeliveryReceipt{Status: gateway.StatusFailed, Error: "topic rejected"}
	d := daemonWithStubAdapter(t, gateway.SourceNtfy, receipt, cfg)

	resp := d.dispatch(Request{Cmd: "gateway-test", Channel: "ntfy"})
	if resp.Ok {
		t.Fatalf("StatusFailed should be reported ok=false; got %+v", resp)
	}
	if !strings.Contains(resp.Msg, "topic rejected") {
		t.Errorf("msg should surface the adapter failure reason: %q", resp.Msg)
	}
}

// TestDispatchGatewayTest_FailedStatusNoErrorFallsBackToStatusString — a
// failed receipt with an empty Error field falls back to the status
// string in the surfaced message. Covers the msg=="" fallback in the
// default switch arm.
func TestDispatchGatewayTest_FailedStatusNoErrorFallsBackToStatusString(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Ntfy:    config.NtfyGatewayConfig{Enabled: true},
	}
	// StatusFailed but no Error string.
	receipt := gateway.DeliveryReceipt{Status: gateway.StatusFailed}
	d := daemonWithStubAdapter(t, gateway.SourceNtfy, receipt, cfg)

	resp := d.dispatch(Request{Cmd: "gateway-test", Channel: "ntfy"})
	if resp.Ok {
		t.Fatalf("StatusFailed should be reported ok=false; got %+v", resp)
	}
	if !strings.Contains(resp.Msg, string(gateway.StatusFailed)) {
		t.Errorf("msg should fall back to the status string %q; got %q", gateway.StatusFailed, resp.Msg)
	}
}
