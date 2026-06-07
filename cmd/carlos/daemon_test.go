package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/daemon"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/providers/anthropic"
	"github.com/georgebuilds/carlos/internal/providers/gemini"
	"github.com/georgebuilds/carlos/internal/providers/ollama"
	"github.com/georgebuilds/carlos/internal/providers/openai"
	"github.com/georgebuilds/carlos/internal/providers/openrouter"
	"github.com/georgebuilds/carlos/internal/schedule"
)

// shortSockDaemon mirrors gateway_test.go's shortSock: returns a UDS
// path short enough for macOS's UNIX_PATH_MAX. We declare a distinct
// helper so daemon_test.go and gateway_test.go don't depend on each
// other's symbols.
func shortSockDaemon(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "csockd*.s")
	if err != nil {
		t.Fatalf("shortSockDaemon: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	_ = os.Remove(path)
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

// fakeDaemonD spins a UDS listener at sock and dispatches each accepted
// connection through dispatch. Mirrors gateway_test.go's fakeDaemon.
func fakeDaemonD(t *testing.T, sock string, dispatch func(daemon.Request) daemon.Response) func() {
	t.Helper()
	l, err := daemon.Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				select {
				case <-done:
					return
				default:
				}
				return
			}
			go daemon.HandleConn(conn, dispatch)
		}
	}()
	return func() {
		close(done)
		_ = l.Close()
		wg.Wait()
	}
}

// captureStdoutD runs fn with os.Stdout redirected and returns whatever
// was written. Mirrors gateway_test.go's captureStdout.
func captureStdoutD(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	errCh := make(chan error, 1)
	go func() { errCh <- fn() }()

	doneCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		doneCh <- buf.String()
	}()

	fnErr := <-errCh
	_ = w.Close()
	out := <-doneCh
	_ = r.Close()
	return out, fnErr
}

// captureStderrD is the same as captureStdoutD but for os.Stderr. Used
// for verifying warnGatewayOrphaned's one-line banner.
func captureStderrD(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	doneCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		doneCh <- buf.String()
	}()

	fn()
	_ = w.Close()
	out := <-doneCh
	_ = r.Close()
	return out
}

// isolateHome points HOME + CARLOS_CONFIG + CARLOS_DAEMON_SOCKET at a
// brand-new tmpdir for the lifetime of the test. Every CLI verb that
// resolves config or socket paths via the standard helpers will see
// the isolated dir, so two tests can't trample each other and no test
// touches the real ~/.carlos. The socket env var points at a path
// under os.TempDir (not the test tmpdir) so we stay under macOS's
// UNIX_PATH_MAX even for deeply-nested test names.
func isolateHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CARLOS_CONFIG", filepath.Join(tmp, ".carlos", "config.yaml"))
	t.Setenv("CARLOS_DAEMON_SOCKET", shortSockDaemon(t))
	return tmp
}

// saveCfgD saves cfg to the test-isolated config path. Helper so the
// individual tests can stay focused on the verb-under-test.
func saveCfgD(t *testing.T, cfg *config.Config) {
	t.Helper()
	if err := config.Save(config.DefaultPath(), cfg); err != nil {
		t.Fatalf("saveCfgD: %v", err)
	}
}

// --- runDaemon dispatch ------------------------------------------------

func TestRunDaemon_NoArgs(t *testing.T) {
	err := runDaemon(nil)
	if err == nil {
		t.Fatal("expected error for missing subcommand")
	}
	if !strings.Contains(err.Error(), "subcommand required") {
		t.Errorf("error should mention required subcommand: %v", err)
	}
}

func TestRunDaemon_UnknownSubcommand(t *testing.T) {
	err := runDaemon([]string{"frobnicate"})
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	if !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("error should mention unknown subcommand: %v", err)
	}
}

func TestRunDaemon_DispatchStatusNoDaemon(t *testing.T) {
	// Status path is safe: no daemon running → prints + returns nil.
	isolateHome(t)
	out, err := captureStdoutD(t, func() error {
		return runDaemon([]string{"status"})
	})
	if err != nil {
		t.Fatalf("dispatch status: %v", err)
	}
	if !strings.Contains(out, "not running") {
		t.Errorf("status output should say not running: %q", out)
	}
}

func TestRunDaemon_DispatchRunMissingConfig(t *testing.T) {
	// "run" dispatch path exercised via the missing-config error so we
	// don't accidentally fire up a real daemon main loop.
	isolateHome(t)
	err := runDaemon([]string{"run"})
	if err == nil {
		t.Fatal("expected load-config error")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Errorf("error should mention load config: %v", err)
	}
}

func TestRunDaemon_DispatchEnable(t *testing.T) {
	// "enable" dispatch path exercised via the safe early-error branch
	// (HOME is a regular file so MkdirAll(LaunchAgents) bails before
	// launchctl is invoked).
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "home-file")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	t.Setenv("HOME", blocker)
	err := runDaemon([]string{"enable"})
	if err == nil {
		t.Fatal("expected install error")
	}
	if !strings.Contains(err.Error(), "daemon enable") {
		t.Errorf("error should be wrapped under daemon enable: %v", err)
	}
}

func TestRunDaemon_DispatchDisable(t *testing.T) {
	// "disable" dispatch path exercised the same way as
	// TestRunDaemonDisable_NoLaunchctlInPath - safe PATH override so
	// the platform shell-outs no-op.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CARLOS_CONFIG", filepath.Join(tmp, ".carlos", "config.yaml"))
	t.Setenv("PATH", filepath.Join(tmp, "no-bin"))
	if err := runDaemon([]string{"disable"}); err != nil {
		t.Fatalf("runDaemon disable: %v", err)
	}
}

// --- runDaemonRun ------------------------------------------------------

func TestRunDaemonRun_MissingConfig(t *testing.T) {
	isolateHome(t)
	err := runDaemonRun()
	if err == nil {
		t.Fatal("expected error when config missing")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Errorf("error should mention load config: %v", err)
	}
}

func TestRunDaemonRun_IncompleteConfig(t *testing.T) {
	isolateHome(t)
	// Config exists but has no providers → IsComplete returns false.
	saveCfgD(t, &config.Config{UserName: "tester"})
	err := runDaemonRun()
	if err == nil {
		t.Fatal("expected error for incomplete config")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("error should mention incomplete config: %v", err)
	}
}

// TestRunDaemonRun_BuildDispatchFails covers the "config complete but
// the named provider is unknown" branch so buildDispatch errors before
// we touch the daemon's startup path. The provider switch is in
// main.go; we trip it by writing a config with an unknown name as the
// default + a single matching providers entry (so IsComplete passes).
func TestRunDaemonRun_BuildDispatchFails(t *testing.T) {
	isolateHome(t)
	saveCfgD(t, &config.Config{
		UserName:        "tester",
		DefaultProvider: "imaginary",
		Providers: map[string]config.ProviderConfig{
			"imaginary": {APIKey: "x"},
		},
	})
	err := runDaemonRun()
	if err == nil {
		t.Fatal("expected build-dispatch error for unknown provider")
	}
	if !strings.Contains(err.Error(), "build dispatch") {
		t.Errorf("error should be wrapped under build dispatch: %v", err)
	}
}

// TestRunDaemonRun_EnsureCarlosDirFails covers the post-buildDispatch
// failure branch: a complete config with a valid provider passes
// IsComplete + buildDispatch, then EnsureCarlosDir tries to mkdir
// ~/.carlos and fails because HOME points at a regular file. The
// wrapped error confirms the path was traversed without ever entering
// the daemon's blocking Run loop.
func TestRunDaemonRun_EnsureCarlosDirFails(t *testing.T) {
	tmp := t.TempDir()
	// First write a valid config to a real path.
	cfgPath := filepath.Join(tmp, "config.yaml")
	t.Setenv("CARLOS_CONFIG", cfgPath)
	if err := config.Save(cfgPath, &config.Config{
		UserName:        "tester",
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "sk-test"},
		},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	// Now point HOME at a regular file so EnsureCarlosDir's
	// MkdirAll(<HOME>/.carlos) bails before daemon.New / Run.
	blocker := filepath.Join(tmp, "home-file")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	t.Setenv("HOME", blocker)

	err := runDaemonRun()
	if err == nil {
		t.Fatal("expected EnsureCarlosDir error when HOME is a regular file")
	}
}

// --- runDaemonEnable / runDaemonDisable -------------------------------

// TODO: runDaemonEnable + runDaemonDisable shell out to launchctl
// (darwin) / systemctl (linux) against the user's real session.
// Letting the happy path run in tests would attempt to bootstrap a
// LaunchAgent / systemd unit pointing at the test binary, which the
// host launchd would then keep-alive-restart on every crash. That
// would corrupt the developer's machine. The platform-unit templates
// + plist path resolution are covered in unit_macos_test.go /
// unit_linux_test.go; here we only exercise the early
// InstallUnit-failure branch by pointing HOME at a regular file so
// MkdirAll(LaunchAgents) bails before any platform call happens.

func TestRunDaemonEnable_InstallUnitFailure(t *testing.T) {
	tmp := t.TempDir()
	// HOME is a regular file: MkdirAll(<HOME>/Library/LaunchAgents) on
	// darwin and MkdirAll(<HOME>/.config/systemd/user) on linux both
	// fail with "not a directory" before any shell-out happens.
	blocker := filepath.Join(tmp, "home-file")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	t.Setenv("HOME", blocker)
	err := runDaemonEnable()
	if err == nil {
		t.Fatal("expected install error when HOME is a regular file")
	}
	if !strings.Contains(err.Error(), "daemon enable") {
		t.Errorf("error should be wrapped under daemon enable: %v", err)
	}
}

// TestRunDaemonDisable_NoLaunchctlInPath exercises the disable verb
// without risking the developer's running LaunchAgent.
//
// Why this is safe: we override PATH to an empty tmpdir so the
// launchctl shell-out resolves to "command not found"; the
// UninstallLaunchAgent path swallows that error. With HOME pointing at
// a fresh tmpdir, the os.Remove call on the (non-existent) plist
// returns ErrNotExist, which the platform code also swallows.
// Net result: no host-side launchd / systemd traffic, just a clean
// walk of the carlos-side wrapper.
func TestRunDaemonDisable_NoLaunchctlInPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CARLOS_CONFIG", filepath.Join(tmp, ".carlos", "config.yaml"))
	// Empty PATH so launchctl / systemctl can't be resolved.
	t.Setenv("PATH", filepath.Join(tmp, "no-bin"))

	// No config on disk: cfg == nil branch runs, no save, returns nil.
	if err := runDaemonDisable(); err != nil {
		t.Fatalf("runDaemonDisable: %v", err)
	}
}

// TestRunDaemonDisable_WithConfigSaves verifies the "config exists
// → set Enabled=false + clear UnitPath + save" branch. Same PATH
// scrub as above keeps the platform shell-outs harmless.
func TestRunDaemonDisable_WithConfigSaves(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CARLOS_CONFIG", filepath.Join(tmp, ".carlos", "config.yaml"))
	t.Setenv("PATH", filepath.Join(tmp, "no-bin"))

	saveCfgD(t, &config.Config{
		UserName: "tester",
		Daemon:   config.DaemonConfig{Enabled: true, UnitPath: "/some/plist"},
	})
	out, err := captureStdoutD(t, func() error {
		return runDaemonDisable()
	})
	if err != nil {
		t.Fatalf("runDaemonDisable: %v", err)
	}
	if !strings.Contains(out, "daemon disabled") {
		t.Errorf("expected confirmation line, got %q", out)
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Daemon.Enabled {
		t.Error("Daemon.Enabled should be false after disable")
	}
	if cfg.Daemon.UnitPath != "" {
		t.Errorf("Daemon.UnitPath should be cleared, got %q", cfg.Daemon.UnitPath)
	}
}

// --- warnGatewayOrphaned ----------------------------------------------

func TestWarnGatewayOrphaned_NilConfigIsNoop(t *testing.T) {
	got := captureStderrD(t, func() { warnGatewayOrphaned(nil) })
	if got != "" {
		t.Errorf("nil cfg should be silent, got %q", got)
	}
}

func TestWarnGatewayOrphaned_DisabledGatewayIsNoop(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.Enabled = false
	got := captureStderrD(t, func() { warnGatewayOrphaned(cfg) })
	if got != "" {
		t.Errorf("disabled gateway should be silent, got %q", got)
	}
}

func TestWarnGatewayOrphaned_EnabledNoDaemonWarns(t *testing.T) {
	isolateHome(t)
	cfg := &config.Config{}
	cfg.Gateway.Enabled = true
	got := captureStderrD(t, func() { warnGatewayOrphaned(cfg) })
	if !strings.Contains(got, "gateway is configured") {
		t.Errorf("expected banner, got %q", got)
	}
	if !strings.Contains(got, "carlos daemon enable") {
		t.Errorf("banner should hint at the fix, got %q", got)
	}
}

func TestWarnGatewayOrphaned_EnabledDaemonRunningSilent(t *testing.T) {
	sock := shortSockDaemon(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)
	stop := fakeDaemonD(t, sock, func(req daemon.Request) daemon.Response {
		return daemon.Response{Ok: true}
	})
	defer stop()

	cfg := &config.Config{}
	cfg.Gateway.Enabled = true
	got := captureStderrD(t, func() { warnGatewayOrphaned(cfg) })
	if got != "" {
		t.Errorf("daemon up should silence banner, got %q", got)
	}
}

// --- runDaemonStatus ---------------------------------------------------

func TestRunDaemonStatus_NoDaemonRunning(t *testing.T) {
	isolateHome(t)
	out, err := captureStdoutD(t, runDaemonStatus)
	if err != nil {
		t.Fatalf("runDaemonStatus: %v", err)
	}
	if !strings.Contains(out, "not running") {
		t.Errorf("expected not running line, got %q", out)
	}
}

func TestRunDaemonStatus_OkResponse(t *testing.T) {
	sock := shortSockDaemon(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)

	started := time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)
	next := time.Date(2026, 6, 6, 9, 5, 0, 0, time.UTC)
	last := time.Date(2026, 6, 6, 8, 55, 0, 0, time.UTC)
	stop := fakeDaemonD(t, sock, func(_ daemon.Request) daemon.Response {
		return daemon.Response{
			Ok:         true,
			Msg:        "carlos: 2 schedules active",
			StartedAt:  &started,
			NextFireAt: &next,
			Schedules: []daemon.ScheduleStatus{
				{Name: "morning", Spec: "0 9 * * *", NextFireAt: next, Once: false},
				{Name: "remind", Spec: "*/30 * * * *", NextFireAt: next, LastRunAt: &last, LastRunOK: true},
			},
		}
	})
	defer stop()

	out, err := captureStdoutD(t, runDaemonStatus)
	if err != nil {
		t.Fatalf("runDaemonStatus: %v", err)
	}
	for _, want := range []string{"2 schedules active", "started:", "next fire:", "morning", "remind", "last="} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q: %s", want, out)
		}
	}
}

func TestRunDaemonStatus_DaemonReportsFailure(t *testing.T) {
	sock := shortSockDaemon(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)
	stop := fakeDaemonD(t, sock, func(_ daemon.Request) daemon.Response {
		return daemon.Response{Ok: false, Msg: "stale lockfile"}
	})
	defer stop()
	err := runDaemonStatus()
	if err == nil {
		t.Fatal("expected error when daemon reports failure")
	}
	if !strings.Contains(err.Error(), "stale lockfile") {
		t.Errorf("error should surface daemon msg: %v", err)
	}
}

// --- runSchedule dispatch ---------------------------------------------

func TestRunSchedule_NoArgs(t *testing.T) {
	err := runSchedule(nil)
	if err == nil {
		t.Fatal("expected error for missing subcommand")
	}
	if !strings.Contains(err.Error(), "subcommand required") {
		t.Errorf("expected subcommand-required text: %v", err)
	}
}

func TestRunSchedule_UnknownSubcommand(t *testing.T) {
	err := runSchedule([]string{"vacate"})
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	if !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("expected unknown-subcommand text: %v", err)
	}
}

func TestRunSchedule_DispatchList(t *testing.T) {
	isolateHome(t)
	saveCfgD(t, &config.Config{UserName: "tester"})
	out, err := captureStdoutD(t, func() error {
		return runSchedule([]string{"list"})
	})
	if err != nil {
		t.Fatalf("dispatch list: %v", err)
	}
	if !strings.Contains(out, "no schedules") {
		t.Errorf("empty list should print no-schedules hint: %q", out)
	}
}

func TestRunSchedule_DispatchAdd(t *testing.T) {
	// Cover the "add" arm of runSchedule's switch by hitting the
	// usage-error path inside runScheduleAdd.
	err := runSchedule([]string{"add"})
	if err == nil {
		t.Fatal("expected usage error")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("error should bubble usage text: %v", err)
	}
}

func TestRunSchedule_DispatchRm(t *testing.T) {
	// Cover the "rm" arm of runSchedule's switch via the usage-error
	// path inside runScheduleRm.
	err := runSchedule([]string{"rm"})
	if err == nil {
		t.Fatal("expected usage error")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("error should bubble usage text: %v", err)
	}
}

// --- runScheduleList ---------------------------------------------------

func TestRunScheduleList_LoadFailure(t *testing.T) {
	isolateHome(t)
	// No config written → load fails with ErrNotExist (wrapped).
	err := runScheduleList()
	if err == nil {
		t.Fatal("expected error when config missing")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Errorf("error should mention load config: %v", err)
	}
}

func TestRunScheduleList_Empty(t *testing.T) {
	isolateHome(t)
	saveCfgD(t, &config.Config{UserName: "tester"})
	out, err := captureStdoutD(t, runScheduleList)
	if err != nil {
		t.Fatalf("runScheduleList: %v", err)
	}
	if !strings.Contains(out, "no schedules configured") {
		t.Errorf("expected no-schedules hint, got %q", out)
	}
}

func TestRunScheduleList_PrintsRows(t *testing.T) {
	isolateHome(t)
	cfg := &config.Config{
		UserName: "tester",
		Schedules: []schedule.Schedule{
			{
				Name:         "morning-slack",
				Spec:         "0 9 * * 1-5",
				Prompt:       "summarize my unread Slack DMs",
				BudgetTokens: 8000,
				BudgetCents:  50,
			},
			{
				Name:   "tonight",
				Spec:   "0 22 * * *",
				Prompt: "wind-down recap",
				Once:   true,
			},
		},
	}
	saveCfgD(t, cfg)

	out, err := captureStdoutD(t, runScheduleList)
	if err != nil {
		t.Fatalf("runScheduleList: %v", err)
	}
	for _, want := range []string{
		"morning-slack",
		"0 9 * * 1-5",
		"summarize my unread Slack DMs",
		"tokens=8000",
		"cents=50",
		"tonight",
		"wind-down recap",
		"once=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

// --- runScheduleAdd ----------------------------------------------------

func TestRunScheduleAdd_UsageError(t *testing.T) {
	err := runScheduleAdd(nil)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("error should mention usage: %v", err)
	}

	err = runScheduleAdd([]string{"every weekday at 9am"})
	if err == nil {
		t.Fatal("expected usage error with only one arg")
	}
}

func TestRunScheduleAdd_BadWhen(t *testing.T) {
	isolateHome(t)
	saveCfgD(t, &config.Config{UserName: "tester"})
	err := runScheduleAdd([]string{"never never never", "do", "thing"})
	if err == nil {
		t.Fatal("expected parse error for unparseable when clause")
	}
	if !strings.Contains(err.Error(), "schedule add") {
		t.Errorf("error should be wrapped under schedule add: %v", err)
	}
}

func TestRunScheduleAdd_LoadFailure(t *testing.T) {
	isolateHome(t)
	// No config → Load returns ErrNotExist. Add bails before save.
	err := runScheduleAdd([]string{"every weekday at 9am", "summarize", "slack"})
	if err == nil {
		t.Fatal("expected load failure")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Errorf("error should mention load config: %v", err)
	}
}

func TestRunScheduleAdd_PersistsAndPrints(t *testing.T) {
	isolateHome(t)
	saveCfgD(t, &config.Config{UserName: "tester"})
	out, err := captureStdoutD(t, func() error {
		return runScheduleAdd([]string{"every weekday at 9am", "summarize", "my", "slack"})
	})
	if err != nil {
		t.Fatalf("runScheduleAdd: %v", err)
	}
	if !strings.Contains(out, "added schedule") {
		t.Errorf("expected confirmation line, got %q", out)
	}
	// Config now has exactly one schedule with the correct spec + prompt.
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(cfg.Schedules))
	}
	s := cfg.Schedules[0]
	if s.Spec != "0 9 * * 1-5" {
		t.Errorf("spec = %q, want 0 9 * * 1-5", s.Spec)
	}
	if s.Prompt != "summarize my slack" {
		t.Errorf("prompt = %q", s.Prompt)
	}
	if s.Name == "" {
		t.Error("name should be auto-generated")
	}
	// The "daemon not running" suffix appears because we haven't started one.
	if !strings.Contains(out, "daemon not running") {
		t.Errorf("expected daemon-not-running suffix: %q", out)
	}
}

// TestRunScheduleAdd_DuplicateName forces the duplicate-detection
// branch by pre-seeding all 10000 possible 4-digit suffixes for the
// prompt's slug. autoScheduleName picks one of them at random, so the
// collision is guaranteed regardless of when the test runs.
func TestRunScheduleAdd_DuplicateName(t *testing.T) {
	isolateHome(t)
	seeded := make([]schedule.Schedule, 0, 10000)
	for i := 0; i < 10000; i++ {
		seeded = append(seeded, schedule.Schedule{
			Name:   fmt.Sprintf("dup-%04d", i),
			Spec:   "0 9 * * 1-5",
			Prompt: "filler",
		})
	}
	saveCfgD(t, &config.Config{UserName: "tester", Schedules: seeded})

	err := runScheduleAdd([]string{"every weekday at 9am", "dup"})
	if err == nil {
		t.Fatal("expected duplicate-name error")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("error should mention already in use: %v", err)
	}
}

func TestRunScheduleAdd_NotifiesDaemonWhenRunning(t *testing.T) {
	isolateHome(t)
	saveCfgD(t, &config.Config{UserName: "tester"})

	// Override the daemon socket to a short path (tmpdir-based paths
	// blow past UNIX_PATH_MAX on macOS).
	sock := shortSockDaemon(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)
	var gotReload bool
	stop := fakeDaemonD(t, sock, func(req daemon.Request) daemon.Response {
		if req.Cmd == "reload" {
			gotReload = true
		}
		return daemon.Response{Ok: true}
	})
	defer stop()

	out, err := captureStdoutD(t, func() error {
		return runScheduleAdd([]string{"every weekday at 9am", "summarize"})
	})
	if err != nil {
		t.Fatalf("runScheduleAdd: %v", err)
	}
	if !gotReload {
		t.Error("daemon did not receive reload cmd")
	}
	// When daemon is reachable, we don't print the not-running suffix.
	if strings.Contains(out, "daemon not running") {
		t.Errorf("daemon reachable: should not print not-running suffix: %q", out)
	}
}

// --- runScheduleRm ----------------------------------------------------

func TestRunScheduleRm_UsageError(t *testing.T) {
	err := runScheduleRm(nil)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("error should mention usage: %v", err)
	}
	err = runScheduleRm([]string{"a", "b"})
	if err == nil {
		t.Fatal("expected usage error with too many args")
	}
}

func TestRunScheduleRm_LoadFailure(t *testing.T) {
	isolateHome(t)
	err := runScheduleRm([]string{"morning"})
	if err == nil {
		t.Fatal("expected load failure")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Errorf("error should mention load config: %v", err)
	}
}

func TestRunScheduleRm_NotFound(t *testing.T) {
	isolateHome(t)
	saveCfgD(t, &config.Config{
		UserName: "tester",
		Schedules: []schedule.Schedule{
			{Name: "morning", Spec: "0 9 * * *", Prompt: "x"},
		},
	})
	err := runScheduleRm([]string{"nope"})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), `no schedule named "nope"`) {
		t.Errorf("error should name the missing schedule: %v", err)
	}
}

func TestRunScheduleRm_Removes(t *testing.T) {
	isolateHome(t)
	saveCfgD(t, &config.Config{
		UserName: "tester",
		Schedules: []schedule.Schedule{
			{Name: "morning", Spec: "0 9 * * *", Prompt: "x"},
			{Name: "evening", Spec: "0 18 * * *", Prompt: "y"},
		},
	})
	out, err := captureStdoutD(t, func() error {
		return runScheduleRm([]string{"morning"})
	})
	if err != nil {
		t.Fatalf("runScheduleRm: %v", err)
	}
	if !strings.Contains(out, `removed schedule "morning"`) {
		t.Errorf("expected removed line, got %q", out)
	}
	reload, err := config.Load(config.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(reload.Schedules) != 1 || reload.Schedules[0].Name != "evening" {
		t.Errorf("expected only evening remaining, got %+v", reload.Schedules)
	}
}

func TestRunScheduleRm_NotifiesDaemonWhenRunning(t *testing.T) {
	isolateHome(t)
	saveCfgD(t, &config.Config{
		UserName: "tester",
		Schedules: []schedule.Schedule{
			{Name: "morning", Spec: "0 9 * * *", Prompt: "x"},
		},
	})
	sock := shortSockDaemon(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)
	var gotReload bool
	stop := fakeDaemonD(t, sock, func(req daemon.Request) daemon.Response {
		if req.Cmd == "reload" {
			gotReload = true
		}
		return daemon.Response{Ok: true}
	})
	defer stop()
	out, err := captureStdoutD(t, func() error {
		return runScheduleRm([]string{"morning"})
	})
	if err != nil {
		t.Fatalf("runScheduleRm: %v", err)
	}
	if !gotReload {
		t.Error("daemon never saw reload")
	}
	if strings.Contains(out, "daemon not running") {
		t.Errorf("should not print not-running suffix: %q", out)
	}
}

// --- autoScheduleName --------------------------------------------------

func TestAutoScheduleName_Slugging(t *testing.T) {
	cases := []struct {
		name, in, wantPrefix string
	}{
		{"basic-lowercase", "Summarize Slack DMs", "summarize-slack-dms-"},
		{"long-input-trimmed-to-20-chars", "this prompt is way more than twenty characters long", ""},
		{"empty-fallback", "", "sched-"},
		{"only-punctuation-fallback", "??? !!! ...", "sched-"},
		{"collapses-runs-of-punctuation", "hello,,,world", "hello-world-"},
		{"digits-survive", "alert 5xx errors", "alert-5xx-errors-"},
		{"trims-trailing-dash", "foo-----", "foo-"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := autoScheduleName(c.in)
			// Suffix is always 4 digits.
			if len(got) < 5 {
				t.Fatalf("output too short: %q", got)
			}
			suffix := got[len(got)-5:]
			if suffix[0] != '-' {
				t.Errorf("missing dash before suffix: %q", got)
			}
			for i := 1; i < 5; i++ {
				if suffix[i] < '0' || suffix[i] > '9' {
					t.Errorf("suffix not 4 digits: %q", got)
					break
				}
			}
			slugPart := got[:len(got)-5]
			if c.wantPrefix != "" {
				if !strings.HasPrefix(got, c.wantPrefix) {
					t.Errorf("got %q, want prefix %q", got, c.wantPrefix)
				}
			}
			// The slug portion never exceeds 20 chars and never ends in '-'.
			if len(slugPart) > 20 {
				t.Errorf("slug portion %q > 20 chars", slugPart)
			}
			if strings.HasSuffix(slugPart, "-") {
				t.Errorf("slug portion %q ends in dash", slugPart)
			}
			// No uppercase letters anywhere.
			if slugPart != strings.ToLower(slugPart) {
				t.Errorf("slug portion %q is not lowercase", slugPart)
			}
		})
	}
}

func TestAutoScheduleName_StableShape(t *testing.T) {
	// Same prompt → identical slug prefix; only the timestamp suffix
	// varies. The shape lets the duplicate check in runScheduleAdd
	// catch back-to-back same-prompt adds within the same nanosecond
	// window.
	a := autoScheduleName("daily standup")
	b := autoScheduleName("daily standup")
	prefA := a[:strings.LastIndex(a, "-")]
	prefB := b[:strings.LastIndex(b, "-")]
	if prefA != prefB {
		t.Errorf("prefixes differ for same prompt: %q vs %q", prefA, prefB)
	}
	if prefA != "daily-standup" {
		t.Errorf("unexpected slug: %q", prefA)
	}
}

// --- signalDaemonReload -----------------------------------------------

func TestSignalDaemonReload_DaemonAbsent(t *testing.T) {
	sock := shortSockDaemon(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)
	if signalDaemonReload() {
		t.Error("expected false when no daemon is listening")
	}
}

func TestSignalDaemonReload_DaemonOk(t *testing.T) {
	sock := shortSockDaemon(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)
	stop := fakeDaemonD(t, sock, func(req daemon.Request) daemon.Response {
		if req.Cmd != "reload" {
			return daemon.Response{Ok: false, Msg: "unexpected cmd"}
		}
		return daemon.Response{Ok: true, Msg: "reloaded"}
	})
	defer stop()
	if !signalDaemonReload() {
		t.Error("expected true when daemon responds ok")
	}
}

func TestSignalDaemonReload_DaemonReportsFailure(t *testing.T) {
	sock := shortSockDaemon(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)
	stop := fakeDaemonD(t, sock, func(_ daemon.Request) daemon.Response {
		return daemon.Response{Ok: false, Msg: "reload failed"}
	})
	defer stop()
	if signalDaemonReload() {
		t.Error("expected false when daemon returns ok=false")
	}
}

// --- buildProviderForFrame --------------------------------------------

func TestBuildProviderForFrame_KnownProviders(t *testing.T) {
	cases := []struct {
		name string
		in   frame.ResolvedProvider
		want any
	}{
		{"anthropic", frame.ResolvedProvider{Provider: "anthropic", APIKey: "sk-a"}, (*anthropic.Client)(nil)},
		{"openai", frame.ResolvedProvider{Provider: "openai", APIKey: "sk-o"}, (*openai.Client)(nil)},
		{"gemini", frame.ResolvedProvider{Provider: "gemini", APIKey: "sk-g"}, (*gemini.Client)(nil)},
		{"openrouter", frame.ResolvedProvider{Provider: "openrouter", APIKey: "sk-r"}, (*openrouter.Client)(nil)},
		{"ollama", frame.ResolvedProvider{Provider: "ollama", BaseURL: "http://localhost:11434"}, (*ollama.Client)(nil)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildProviderForFrame(c.in)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got == nil {
				t.Fatal("got nil provider")
			}
			switch c.name {
			case "anthropic":
				if _, ok := got.(*anthropic.Client); !ok {
					t.Errorf("expected *anthropic.Client, got %T", got)
				}
			case "openai":
				if _, ok := got.(*openai.Client); !ok {
					t.Errorf("expected *openai.Client, got %T", got)
				}
			case "gemini":
				if _, ok := got.(*gemini.Client); !ok {
					t.Errorf("expected *gemini.Client, got %T", got)
				}
			case "openrouter":
				if _, ok := got.(*openrouter.Client); !ok {
					t.Errorf("expected *openrouter.Client, got %T", got)
				}
			case "ollama":
				if _, ok := got.(*ollama.Client); !ok {
					t.Errorf("expected *ollama.Client, got %T", got)
				}
			}
		})
	}
}

func TestBuildProviderForFrame_UnknownProvider(t *testing.T) {
	_, err := buildProviderForFrame(frame.ResolvedProvider{Provider: "azure-openai", APIKey: "x"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error should mention unknown provider: %v", err)
	}
}

func TestBuildProviderForFrame_EmptyProviderName(t *testing.T) {
	_, err := buildProviderForFrame(frame.ResolvedProvider{})
	if err == nil {
		t.Fatal("expected error for empty provider name")
	}
}
