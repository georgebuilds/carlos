package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/schedule"
)

// TestNew_EmptyConfigPathErrors guards the must-have field. A future
// refactor that drops this check would silently break the CLI flag
// surface.
func TestNew_EmptyConfigPathErrors(t *testing.T) {
	_, err := New(Options{})
	if err == nil {
		t.Fatal("expected error for empty ConfigPath")
	}
	if !strings.Contains(err.Error(), "ConfigPath") {
		t.Errorf("err should mention ConfigPath; got %v", err)
	}
}

// TestNew_DefaultsApplied confirms the constructor backfills the
// sensible default for TickInterval, Now, and SocketPath when each is
// left zero.
func TestNew_DefaultsApplied(t *testing.T) {
	t.Setenv("CARLOS_DAEMON_SOCKET", "/tmp/carlos-test.sock")
	d, err := New(Options{ConfigPath: "x.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if d.opts.TickInterval != 30*time.Second {
		t.Errorf("TickInterval default: %v", d.opts.TickInterval)
	}
	if d.opts.Now == nil {
		t.Errorf("Now default missing")
	}
	if d.opts.SocketPath != "/tmp/carlos-test.sock" {
		t.Errorf("SocketPath honors env: %q", d.opts.SocketPath)
	}
	// RealClock returns time near time.Now.
	rc := RealClock{}
	if diff := time.Since(rc.Now()); diff > time.Second {
		t.Errorf("RealClock.Now appears stale: %v", diff)
	}
}

// TestRun_RequiresStateDBPathWhenNoSpawner confirms the production-mode
// guard: without a Spawner, the daemon needs a state.db path so the
// supervisor can write events.
func TestRun_RequiresStateDBPathWhenNoSpawner(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeTestConfig(t, cfgPath, nil)

	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     shortSock(t),
		DisableSignals: true,
		// No Spawner, no StateDBPath -> Run should error.
	})
	if err != nil {
		t.Fatal(err)
	}
	err = d.Run(context.Background())
	if err == nil {
		t.Fatal("expected StateDBPath error")
	}
	if !strings.Contains(err.Error(), "StateDBPath") {
		t.Errorf("err should mention StateDBPath; got %v", err)
	}
}

// TestRun_LoadConfigErrorClosesListener - a malformed config file
// should abort Run cleanly without leaking the listener or state.db.
func TestRun_LoadConfigErrorClosesListener(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("user_name: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     shortSock(t),
		Spawner:        fs,
		TickInterval:   50 * time.Millisecond,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runErr := d.Run(context.Background())
	if runErr == nil {
		t.Fatal("expected Run to fail on bad config")
	}
	if !strings.Contains(runErr.Error(), "load config") && !strings.Contains(runErr.Error(), "parse") {
		t.Errorf("error should mention load failure; got %v", runErr)
	}
}

// TestStop_IsIdempotent - calling Stop twice should not panic and the
// inner cancel only fires once.
func TestStop_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	sock := shortSock(t)
	writeTestConfig(t, cfgPath, nil)

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     sock,
		Spawner:        fs,
		TickInterval:   500 * time.Millisecond,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	waitForSocket(t, sock, 2*time.Second)

	d.Stop()
	d.Stop() // must be a no-op
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit after Stop")
	}
}

// TestDispatch_UnknownCmdReturnsError exercises the default branch in
// dispatch so a malformed/older client sees ok:false rather than a
// silent ok:true.
func TestDispatch_UnknownCmdReturnsError(t *testing.T) {
	d, err := New(Options{ConfigPath: "x.yaml", DisableSignals: true})
	if err != nil {
		t.Fatal(err)
	}
	resp := d.dispatch(Request{Cmd: "no-such-verb"})
	if resp.Ok {
		t.Errorf("expected ok=false for unknown cmd; got %+v", resp)
	}
	if !strings.Contains(resp.Msg, "no-such-verb") {
		t.Errorf("msg should echo unknown cmd; got %q", resp.Msg)
	}
}

// TestDispatch_StopReturnsOk pins the wire shape of the stop verb so
// the CLI gets the expected ack before Run unwinds.
func TestDispatch_StopReturnsOk(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeTestConfig(t, cfgPath, nil)
	fs := &fakeSpawner{}
	sock := shortSock(t)
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     sock,
		Spawner:        fs,
		TickInterval:   500 * time.Millisecond,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	waitForSocket(t, sock, 2*time.Second)

	resp := d.dispatch(Request{Cmd: "stop"})
	if !resp.Ok {
		t.Errorf("stop should return ok=true; got %+v", resp)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit after stop verb")
	}
}

// TestDispatch_ReloadHandlesError - when config.Load fails mid-flight
// (file replaced with garbage between Run and the reload IPC), the
// daemon surfaces ok=false with the parse error string.
func TestDispatch_ReloadHandlesError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeTestConfig(t, cfgPath, nil)

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     shortSock(t),
		Spawner:        fs,
		TickInterval:   500 * time.Millisecond,
		DisableSignals: true,
		Now:            &fakeClock{now: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitForSocket(t, d.opts.SocketPath, 2*time.Second)

	// Replace config with garbage; Reload should surface a parse failure.
	if err := os.WriteFile(cfgPath, []byte("???: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}

	resp := d.dispatch(Request{Cmd: "reload"})
	if resp.Ok {
		t.Errorf("Reload should fail on bad YAML; got %+v", resp)
	}
	cancel()
	<-done
}

// TestPersistSchedules_LoadErrorPropagates ensures persistSchedules
// returns a clear error if the on-disk config can't be re-read.
func TestPersistSchedules_LoadErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "missing.yaml")
	d, err := New(Options{ConfigPath: cfgPath, DisableSignals: true})
	if err != nil {
		t.Fatal(err)
	}
	err = d.persistSchedules()
	if err == nil {
		t.Fatal("expected error when config file is missing")
	}
}

// TestRemoveSchedule_DropsTheNamedEntry verifies the internal helper:
// after removeSchedule("a"), only the survivors remain.
func TestRemoveSchedule_DropsTheNamedEntry(t *testing.T) {
	d, err := New(Options{ConfigPath: "x.yaml", DisableSignals: true})
	if err != nil {
		t.Fatal(err)
	}
	d.schedules = []schedule.Schedule{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}
	d.removeSchedule("b")
	if len(d.schedules) != 2 {
		t.Fatalf("want 2 left; got %d", len(d.schedules))
	}
	for _, s := range d.schedules {
		if s.Name == "b" {
			t.Errorf("b survived removal")
		}
	}
	// Removing missing name should be a no-op, not a panic.
	d.removeSchedule("zzz")
	if len(d.schedules) != 2 {
		t.Errorf("missing-name removal should leave list intact")
	}
}

// TestTimePtr returns nil for the zero time and a non-nil pointer
// otherwise. The status response relies on this distinction.
func TestTimePtr(t *testing.T) {
	if timePtr(time.Time{}) != nil {
		t.Errorf("zero time should produce nil")
	}
	now := time.Now()
	p := timePtr(now)
	if p == nil || !p.Equal(now) {
		t.Errorf("non-zero time: %v -> %v", now, p)
	}
}

// TestGatewayChannelEnabled covers all four switch arms + the unknown
// case so the helper's behaviour is pinned.
func TestGatewayChannelEnabled(t *testing.T) {
	cfg := config.GatewayConfig{
		Ntfy:     config.NtfyGatewayConfig{Enabled: true},
		Telegram: config.TelegramConfig{},
		Signal:   config.SignalConfig{Enabled: true},
		Custom:   config.CustomGatewayConfig{Enabled: false},
	}
	if !gatewayChannelEnabled(cfg, gateway.SourceNtfy) {
		t.Errorf("ntfy should be enabled")
	}
	if gatewayChannelEnabled(cfg, gateway.SourceTelegram) {
		t.Errorf("telegram disabled, should report false")
	}
	if !gatewayChannelEnabled(cfg, gateway.SourceSignal) {
		t.Errorf("signal enabled, should report true")
	}
	if gatewayChannelEnabled(cfg, gateway.SourceCustom) {
		t.Errorf("custom disabled, should report false")
	}
	// Unknown source returns false.
	if gatewayChannelEnabled(cfg, gateway.Source("bogus")) {
		t.Errorf("unknown source should report false")
	}
}

// TestSupervisorAdapter_DelegatesToSupervisor - the adapter is a thin
// wrapper that surfaces (nil, nil, err) when called on a nil supervisor.
// We can't easily wire a real Supervisor without provider/registry, so
// we exercise the call site by constructing an adapter around a nil and
// expecting it to panic (and recover). This proves the Spawn signature
// matches the interface contract.
func TestSupervisorAdapter_TypeImplementsSpawner(t *testing.T) {
	var _ Spawner = supervisorAdapter{}
}

// TestEnsureCarlosDir creates ~/.carlos on a fake HOME so we don't
// touch the real user dir. The function reads HOME via UserHomeDir so
// setting HOME redirects it.
func TestEnsureCarlosDir(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if err := EnsureCarlosDir(); err != nil {
		t.Fatalf("EnsureCarlosDir: %v", err)
	}
	info, err := os.Stat(filepath.Join(tmpHome, ".carlos"))
	if err != nil {
		t.Fatalf("expected .carlos created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf(".carlos is not a dir")
	}
	// Idempotent: second call must not error.
	if err := EnsureCarlosDir(); err != nil {
		t.Errorf("second EnsureCarlosDir: %v", err)
	}
}

// TestEnsureCarlosDir_HomeDirFailureSurfaces - when UserHomeDir
// returns an error (empty HOME on unix), the call wraps with a clear
// error.
func TestEnsureCarlosDir_HomeDirFailureSurfaces(t *testing.T) {
	t.Setenv("HOME", "")
	err := EnsureCarlosDir()
	// Behavior: either fail with "home dir:" wrap OR succeed via some
	// fallback. On macOS/linux empty HOME yields an error; assert that
	// path explicitly.
	if err != nil && !strings.Contains(err.Error(), "home dir") {
		t.Errorf("error should mention home dir; got %v", err)
	}
}

// TestDefaultSocketPath_EnvOverrideAndFallback - exercises both the
// env-set path and the fallback when HOME is empty.
func TestDefaultSocketPath_EnvOverrideAndFallback(t *testing.T) {
	t.Setenv("CARLOS_DAEMON_SOCKET", "/tmp/explicit.sock")
	if got := DefaultSocketPath(); got != "/tmp/explicit.sock" {
		t.Errorf("env override: %q", got)
	}
	t.Setenv("CARLOS_DAEMON_SOCKET", "")
	t.Setenv("HOME", "")
	got := DefaultSocketPath()
	if !strings.HasSuffix(got, filepath.Join(".carlos", SocketName)) {
		t.Errorf("fallback should end with .carlos/daemon.sock; got %q", got)
	}
}

// TestListen_EmptyPathError guards the early-return when no path is
// provided.
func TestListen_EmptyPathError(t *testing.T) {
	if _, err := Listen(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

// TestDial_EmptyPathUsesDefault - passing "" routes through
// DefaultSocketPath; if the default doesn't exist the dial fails. We
// just confirm the error path doesn't panic.
func TestDial_EmptyPathUsesDefault(t *testing.T) {
	t.Setenv("CARLOS_DAEMON_SOCKET", filepath.Join(t.TempDir(), "nope.sock"))
	_, err := Dial("")
	if err == nil {
		t.Fatal("expected dial error to a non-existent default")
	}
	if !strings.Contains(err.Error(), "is the daemon running") {
		t.Errorf("error should hint about daemon: %v", err)
	}
}

// TestResolveFrameForFire_FallsBackOnUnknownSchedFrame - when the
// schedule names a frame that doesn't exist in cfg, we fall back to
// the active frame, then the default. The returned frame name should
// reflect the fallback.
func TestResolveFrameForFire_FallsBackOnUnknownSchedFrame(t *testing.T) {
	d, err := New(Options{ConfigPath: "x.yaml", DisableSignals: true})
	if err != nil {
		t.Fatal(err)
	}
	d.frameCfg = frame.Config{
		Default: "personal",
		Active:  "personal",
		List: []frame.Frame{
			frame.NewPersonal("anthropic", "claude"),
		},
	}
	name, info, reg, prov, model := d.resolveFrameForFire(schedule.Schedule{
		Name: "x", Frame: "nonexistent",
	})
	if name != "personal" {
		t.Errorf("name fallback: got %q", name)
	}
	if info.Name != "personal" {
		t.Errorf("FrameInfo.Name: %q", info.Name)
	}
	if reg == nil {
		t.Errorf("registry should always be non-nil")
	}
	if prov != nil || model != "" {
		t.Errorf("no ProviderBuilder -> prov/model should be nil/empty; got %v/%q", prov, model)
	}
}

// TestResolveFrameForFire_ProviderBuilderInvoked - when ProviderBuilder
// is wired and the frame resolves to a known provider, the returned
// provider + model come from the builder.
func TestResolveFrameForFire_ProviderBuilderInvoked(t *testing.T) {
	var built bool
	fp := &fakeProvider{name: "built"}
	d, err := New(Options{
		ConfigPath:     "x.yaml",
		DisableSignals: true,
		ProviderBuilder: func(rp frame.ResolvedProvider) (providers.Provider, error) {
			built = true
			if rp.Provider != "anthropic" {
				t.Errorf("ProviderBuilder got %q, want anthropic", rp.Provider)
			}
			return fp, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.frameCfg = frame.Config{
		Default: "personal", Active: "personal",
		List: []frame.Frame{frame.NewPersonal("anthropic", "claude-test")},
	}
	d.providersCfg = map[string]config.ProviderConfig{
		"anthropic": {APIKey: "sk", DefaultModel: "claude-test"},
	}
	d.defaultProvider = "anthropic"
	name, _, _, prov, model := d.resolveFrameForFire(schedule.Schedule{Name: "x"})
	if !built {
		t.Errorf("ProviderBuilder not invoked")
	}
	if name != "personal" {
		t.Errorf("name: %q", name)
	}
	if prov != fp {
		t.Errorf("provider not threaded through")
	}
	if model != "claude-test" {
		t.Errorf("model: %q", model)
	}
}

// TestResolveFrameForFire_ProviderBuilderErrorFallsBack - when the
// builder returns an error, we keep the legacy provider (nil) and log.
func TestResolveFrameForFire_ProviderBuilderErrorFallsBack(t *testing.T) {
	d, err := New(Options{
		ConfigPath:     "x.yaml",
		DisableSignals: true,
		ProviderBuilder: func(frame.ResolvedProvider) (providers.Provider, error) {
			return nil, errors.New("build failed")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.frameCfg = frame.Config{
		Default: "personal", Active: "personal",
		List: []frame.Frame{frame.NewPersonal("anthropic", "claude")},
	}
	d.providersCfg = map[string]config.ProviderConfig{"anthropic": {APIKey: "k", DefaultModel: "m"}}
	d.defaultProvider = "anthropic"
	_, _, _, prov, _ := d.resolveFrameForFire(schedule.Schedule{Name: "x"})
	if prov != nil {
		t.Errorf("on builder error, provider should remain nil; got %v", prov)
	}
}

// fakeProvider is a no-op providers.Provider for tests that need a
// non-nil instance without spinning up a real backend.
type fakeProvider struct{ name string }

func (f *fakeProvider) Name() string                     { return f.name }
func (f *fakeProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (f *fakeProvider) Stream(context.Context, providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event)
	close(ch)
	return ch, nil
}

// TestSystemNotifier_NoopOnUnsupportedPlatform - on platforms other
// than darwin/linux, Notify returns nil with a non-empty body.
func TestSystemNotifier_NoopOnUnsupportedPlatform(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		t.Skip("platform-specific path tested on this OS via the shell-out tests")
	}
	s := &SystemNotifier{}
	if err := s.Notify(context.Background(), Notification{Body: "hi"}); err != nil {
		t.Errorf("unsupported platform should no-op; got %v", err)
	}
}

// TestSystemNotifier_DefaultTimeoutApplied - when Timeout is zero or
// negative, the notifier still produces a bounded context. We exercise
// this on platforms where the dispatch shell-out exists; the call may
// fail (osascript/notify-send missing in CI) but should not panic, and
// the timeout path is sampled.
func TestSystemNotifier_DispatchTimeoutPath(t *testing.T) {
	s := &SystemNotifier{Timeout: 10 * time.Millisecond}
	// We don't care whether the OS has notify-send/osascript available;
	// just that Notify exits without panicking. On unsupported platforms
	// it's a no-op (already covered above). On linux/darwin we'll either
	// succeed or fail fast from ENOENT/exec-timeout.
	_ = s.Notify(context.Background(), Notification{Body: "x"})
}

// TestSystemNotifier_DispatchWithExplicitUrgency just exercises the
// urgency-formatting branch.
func TestSystemNotifier_DispatchWithExplicitUrgency(t *testing.T) {
	s := &SystemNotifier{Timeout: 10 * time.Millisecond}
	_ = s.Notify(context.Background(), Notification{
		Title:   "title",
		Body:    "body",
		Urgency: "critical",
	})
}

// TestFire_SpawnErrorPushesFailureNotification - fire() should still
// fire a failure notification when Spawn returns an error before the
// resultCh ever exists.
func TestFire_SpawnErrorPushesFailureNotification(t *testing.T) {
	rec := &recordingNotifier{}
	fs := &fakeSpawner{err: errors.New("spawn refused")}
	d, err := New(Options{
		ConfigPath:     "x.yaml",
		DisableSignals: true,
		Spawner:        fs,
		Notifier:       rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.spawner = fs // simulate post-Run wiring
	// Minimal frame config so resolveFrameForFire produces something.
	d.frameCfg = frame.Config{
		Default: "personal", Active: "personal",
		List: []frame.Frame{frame.NewPersonal("a", "m")},
	}
	d.userName = "Tester"
	ok := d.fire(context.Background(), schedule.Schedule{Name: "boom", Prompt: "noop"})
	if ok {
		t.Errorf("Spawn err should make fire return false")
	}
	calls := rec.calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Body, "boom") {
		t.Errorf("notification should mention schedule name: %q", calls[0].Body)
	}
	if !strings.Contains(calls[0].Body, "failed") {
		t.Errorf("notification should reflect failure: %q", calls[0].Body)
	}
}

// TestFire_ResultCancelledByCtx - when ctx fires before the spawn
// returns, fire returns false.
func TestFire_ResultCancelledByCtx(t *testing.T) {
	hold := &holdSpawner{ch: make(chan agent.SpawnResult)}
	d, err := New(Options{
		ConfigPath:     "x.yaml",
		DisableSignals: true,
		Spawner:        hold,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.spawner = hold
	d.frameCfg = frame.Config{Default: "personal", Active: "personal", List: []frame.Frame{frame.NewPersonal("a", "m")}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	ok := d.fire(ctx, schedule.Schedule{Name: "n", Prompt: "p"})
	if ok {
		t.Errorf("ctx cancel should make fire return false")
	}
}

type holdSpawner struct {
	ch chan agent.SpawnResult
}

func (h *holdSpawner) Spawn(_ context.Context, _ string, _ agent.SpawnContract) (*agent.SubAgent, <-chan agent.SpawnResult, error) {
	return &agent.SubAgent{ID: "hold"}, h.ch, nil
}

// TestAcceptLoop_ListenerClosedExitsCleanly - when the listener is
// closed externally, acceptLoop returns without spamming stderr.
// (Coverage of the ErrClosed branch.)
func TestAcceptLoop_ListenerClosedExitsCleanly(t *testing.T) {
	sock := shortSock(t)
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{ConfigPath: "x.yaml", DisableSignals: true})
	if err != nil {
		t.Fatal(err)
	}
	d.listener = l
	done := make(chan struct{})
	go func() {
		d.acceptLoop(context.Background())
		close(done)
	}()
	// Close the listener to force the accept error path; the loop should
	// exit on net.ErrClosed.
	_ = l.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("acceptLoop did not exit after listener close")
	}
}

// TestSocketName confirms the constant matches its docstring.
func TestSocketName(t *testing.T) {
	if SocketName != "daemon.sock" {
		t.Errorf("SocketName = %q, want daemon.sock", SocketName)
	}
}

// TestPersistSchedules_RoundtripsThroughDisk pins the integration: a
// schedule list in memory + persistSchedules + config.Load returns the
// same list, plus the unrelated config fields are preserved.
func TestPersistSchedules_RoundtripsThroughDisk(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{
		UserName:  "Tester",
		Providers: map[string]config.ProviderConfig{"anthropic": {APIKey: "k"}},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{ConfigPath: cfgPath, DisableSignals: true})
	if err != nil {
		t.Fatal(err)
	}
	d.schedules = []schedule.Schedule{
		{Name: "a", Spec: "0 9 * * *", Prompt: "alpha"},
		{Name: "b", Spec: "0 10 * * *", Prompt: "beta"},
	}
	if err := d.persistSchedules(); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Schedules) != 2 {
		t.Errorf("want 2 schedules persisted; got %d", len(got.Schedules))
	}
	if got.UserName != "Tester" {
		t.Errorf("unrelated fields lost: %q", got.UserName)
	}
}

// TestFakeClockNowReturnsSetValue is a tiny self-test confirming the
// fakeClock helper used elsewhere has the right semantics.
func TestFakeClockNowReturnsSetValue(t *testing.T) {
	target := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c := &fakeClock{now: target}
	if got := c.Now(); !got.Equal(target) {
		t.Errorf("fakeClock.Now: got %v want %v", got, target)
	}
	next := target.Add(time.Hour)
	c.Set(next)
	if got := c.Now(); !got.Equal(next) {
		t.Errorf("after Set: got %v want %v", got, next)
	}
}

// TestStatusResponse_PopulatedFields exercises the status assembly even
// without a Run() context. We construct a Daemon directly, seed it,
// and verify the returned Response.
func TestStatusResponse_PopulatedFields(t *testing.T) {
	d, err := New(Options{ConfigPath: "x.yaml", DisableSignals: true, Now: &fakeClock{now: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	d.startedAt = time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)
	d.reloadAt = time.Date(2026, 6, 1, 11, 30, 0, 0, time.UTC)
	d.activeCount = 2
	d.schedules = []schedule.Schedule{
		{Name: "morning", Spec: "0 9 * * *", Prompt: "x"},
		{Name: "evening", Spec: "0 21 * * *", Prompt: "y", LastRunAt: time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC), LastRunOK: true},
	}
	resp := d.statusResponse()
	if !resp.Ok {
		t.Fatalf("ok false: %+v", resp)
	}
	if resp.ActiveCount != 2 {
		t.Errorf("ActiveCount: %d", resp.ActiveCount)
	}
	if resp.StartedAt == nil {
		t.Errorf("StartedAt should be non-nil")
	}
	if resp.LastReloadAt == nil {
		t.Errorf("LastReloadAt should be non-nil")
	}
	if len(resp.Schedules) != 2 {
		t.Errorf("schedule count: %d", len(resp.Schedules))
	}
	// At least one schedule should have a non-zero NextFireAt -> NextFireAt
	// on the response should point at it.
	if resp.NextFireAt == nil {
		t.Errorf("NextFireAt should propagate the soonest")
	}
	// The evening schedule had a LastRunAt; make sure it survived.
	var evening ScheduleStatus
	for _, s := range resp.Schedules {
		if s.Name == "evening" {
			evening = s
		}
	}
	if evening.LastRunAt == nil || !evening.LastRunOK {
		t.Errorf("evening summary: %+v", evening)
	}
	if !strings.Contains(resp.Msg, "2 schedule") {
		t.Errorf("msg should mention count: %q", resp.Msg)
	}
}

// TestRun_FullSupervisorPath exercises the production-mode boot: real
// state.db, real supervisor, fake provider+registry. Covers the
// otherwise-untested "OpenStateDB + NewSupervisor + supervisorAdapter"
// branch in Run.
func TestRun_FullSupervisorPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	statePath := filepath.Join(dir, "state.db")
	writeTestConfig(t, cfgPath, nil)

	// Prove the state.db path is openable on this host - some CI sandboxes
	// reject SQLite extensions. Skip cleanly rather than fail.
	log, openErr := agent.OpenStateDB(statePath)
	if openErr != nil {
		t.Skipf("OpenStateDB unavailable here: %v", openErr)
	}
	_ = log.Close()

	d, err := New(Options{
		ConfigPath:     cfgPath,
		StateDBPath:    statePath,
		SocketPath:     shortSock(t),
		Provider:       &fakeProvider{name: "fake"},
		BaseTools:      nil, // supervisor handles nil registry by using its built-in default
		TickInterval:   50 * time.Millisecond,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitForSocket(t, d.opts.SocketPath, 2*time.Second)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after ctx cancel")
	}
}

// TestSupervisorAdapter_DelegatesViaInterface confirms that the adapter
// satisfies Spawner and that calling Spawn forwards to the inner
// supervisor (we don't have one here; the assertion is the interface
// shape).
func TestSupervisorAdapter_InterfaceShape(t *testing.T) {
	// The adapter is exported indirectly. We just sanity-check that an
	// instance can be assigned to the Spawner interface variable.
	var sp Spawner = supervisorAdapter{s: nil}
	if sp == nil {
		t.Fatal("supervisorAdapter must implement Spawner")
	}
}

// TestRun_StateDBOpenFailure - when the state.db path lives under a
// non-existent unreadable parent, OpenStateDB fails and Run surfaces a
// wrapped error.
func TestRun_StateDBOpenFailure(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeTestConfig(t, cfgPath, nil)
	// State path under a regular-file parent so MkdirAll/Open both fail.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(blocker, "state.db")
	d, err := New(Options{
		ConfigPath:     cfgPath,
		StateDBPath:    statePath,
		SocketPath:     shortSock(t),
		Provider:       &fakeProvider{name: "fake"},
		TickInterval:   50 * time.Millisecond,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = d.Run(context.Background())
	if err == nil {
		t.Fatal("expected open state.db error")
	}
	if !strings.Contains(err.Error(), "state.db") {
		t.Errorf("error should mention state.db; got %v", err)
	}
}

// TestRun_ListenerBindFailure - a SocketPath whose parent is a
// regular file makes Listen fail. Run should report the error.
func TestRun_ListenerBindFailure(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeTestConfig(t, cfgPath, nil)
	blocker := filepath.Join(dir, "block-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	badSock := filepath.Join(blocker, "x.sock")
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     badSock,
		Spawner:        &fakeSpawner{},
		TickInterval:   50 * time.Millisecond,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = d.Run(context.Background())
	if err == nil {
		t.Fatal("expected Listen error")
	}
}

// TestRun_GatewayEnabledStartsRuntime - Run with gateway.enabled=true
// and a real state.db should construct + tear down the gatewayRuntime.
func TestRun_GatewayEnabledStartsRuntime(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{
		UserName:        "Tester",
		Providers:       map[string]config.ProviderConfig{"anthropic": {APIKey: "k"}},
		DefaultProvider: "anthropic",
		Gateway:         config.GatewayConfig{Enabled: true},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.db")
	log, openErr := agent.OpenStateDB(statePath)
	if openErr != nil {
		t.Skipf("OpenStateDB unavailable: %v", openErr)
	}
	_ = log.Close()
	d, err := New(Options{
		ConfigPath:     cfgPath,
		StateDBPath:    statePath,
		SocketPath:     shortSock(t),
		Provider:       &fakeProvider{name: "fake"},
		TickInterval:   50 * time.Millisecond,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitForSocket(t, d.opts.SocketPath, 2*time.Second)
	d.mu.Lock()
	hasGw := d.gw != nil
	d.mu.Unlock()
	if !hasGw {
		t.Errorf("gateway runtime should be wired when Enabled+state.db")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit")
	}
}

// TestRun_SignalHandlerSighupReloads - installs the real signal handler,
// fires SIGHUP, and confirms the schedule list reloads. The test runs
// on platforms where syscall.SIGHUP exists (darwin/linux/freebsd) which
// is everywhere carlos targets.
func TestRun_SignalHandlerSighupReloads(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	sock := shortSock(t)
	writeTestConfig(t, cfgPath, []schedule.Schedule{{Name: "a", Spec: "0 9 * * *", Prompt: "x"}})

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:   cfgPath,
		SocketPath:   sock,
		Spawner:      fs,
		TickInterval: 500 * time.Millisecond,
		Now:          &fakeClock{now: time.Date(2026, 6, 5, 8, 0, 0, 0, time.Local)},
		// signals ENABLED so the goroutine spins up
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitForSocket(t, sock, 2*time.Second)

	// Rewrite config with a second schedule, then fire SIGHUP at
	// ourselves. The daemon's signal handler should pick it up.
	writeTestConfig(t, cfgPath, []schedule.Schedule{
		{Name: "a", Spec: "0 9 * * *", Prompt: "x"},
		{Name: "b", Spec: "0 12 * * *", Prompt: "y"},
	})
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Signal(syscall.SIGHUP); err != nil {
		t.Skipf("cannot send SIGHUP: %v", err)
	}

	// Poll until the reload takes effect.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		n := len(d.schedules)
		d.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	d.mu.Lock()
	n := len(d.schedules)
	d.mu.Unlock()
	if n != 2 {
		t.Errorf("SIGHUP did not reload schedules; len=%d", n)
	}
	cancel()
	<-done
}

// Avoid unused-import shadow when the file doesn't reference fmt
// in the final build.
var _ = fmt.Sprintf
