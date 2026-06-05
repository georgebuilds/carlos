package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/schedule"
)

// fakeClock implements Clock with an injectable now value. Used by the
// tick-loop tests to advance time deterministically.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// fakeSpawner records every Spawn call and returns an immediately-done
// SpawnResult. The Spawn count is the test's primary observable.
type fakeSpawner struct {
	count int32
	calls []string // captured objectives, in order
	mu    sync.Mutex
	err   error
}

func (f *fakeSpawner) Spawn(ctx context.Context, parentID string, c agent.SpawnContract) (*agent.SubAgent, <-chan agent.SpawnResult, error) {
	atomic.AddInt32(&f.count, 1)
	f.mu.Lock()
	f.calls = append(f.calls, c.Objective)
	f.mu.Unlock()
	if f.err != nil {
		return nil, nil, f.err
	}
	ch := make(chan agent.SpawnResult, 1)
	ch <- agent.SpawnResult{}
	close(ch)
	return &agent.SubAgent{ID: "fake"}, ch, nil
}

func (f *fakeSpawner) Count() int32 { return atomic.LoadInt32(&f.count) }

func (f *fakeSpawner) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// waitForSocket polls until a fast unix-dial succeeds (daemon is
// listening) or the timeout expires. Used by tests that need to send
// an IPC request immediately after spinning up the daemon. We bypass
// Dial here to keep each probe's timeout short (Dial's 2s would
// dominate the poll interval).
func waitForSocket(t *testing.T, sock string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", sock, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon never bound %s within %v", sock, timeout)
}

// shortSock returns a UDS path short enough to satisfy macOS's
// UNIX_PATH_MAX (104 chars including the terminating null). t.TempDir
// paths under /var/folders/.../TestNameXXX/001/ routinely exceed 104,
// so we anchor sockets in os.TempDir() directly with a short random
// suffix per test. The path is cleaned up at test exit.
func shortSock(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "csock*.s")
	if err != nil {
		t.Fatalf("shortSock: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	_ = os.Remove(path) // listener will recreate
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

// writeTestConfig drops a minimal carlos config + the given schedules
// at path/config.yaml. Returns the absolute path.
func writeTestConfig(t *testing.T, path string, schedules []schedule.Schedule) string {
	t.Helper()
	cfg := &config.Config{
		UserName: "Tester",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "sk-test"},
		},
		DefaultProvider: "anthropic",
		Schedules:       schedules,
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return path
}

// TestDaemon_TickFiresDueSchedule writes one config with a schedule
// whose Spec matches the fake clock's current minute, runs Daemon.Run
// in a goroutine, and asserts the spawner observed exactly one Spawn.
func TestDaemon_TickFiresDueSchedule(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Anchor the clock to 09:00 local; schedule "0 9 * * *" should be
	// immediately due on first tick.
	clock := &fakeClock{now: time.Date(2026, 6, 5, 9, 0, 0, 0, time.Local)}

	writeTestConfig(t, cfgPath, []schedule.Schedule{{
		Name:   "morning",
		Spec:   "0 9 * * *",
		Prompt: "summarize my unread Slack DMs",
	}})

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:   cfgPath,
		SocketPath:   shortSock(t),
		Spawner:        fs,
		TickInterval:   50 * time.Millisecond,
		Now:            clock,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Poll for the first-tick fire (faster + more reliable than a hard
	// sleep across CI variance).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fs.Count() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned: %v", err)
	}

	if fs.Count() < 1 {
		t.Fatalf("expected at least one Spawn, got %d", fs.Count())
	}
	calls := fs.Calls()
	if calls[0] != "summarize my unread Slack DMs" {
		t.Fatalf("first Spawn objective: %q", calls[0])
	}
}

// TestDaemon_NotDueSkipsSpawn — a schedule that fires at 09:00 should
// NOT fire when the clock is anchored at 08:00.
func TestDaemon_NotDueSkipsSpawn(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	clock := &fakeClock{now: time.Date(2026, 6, 5, 8, 0, 0, 0, time.Local)}

	writeTestConfig(t, cfgPath, []schedule.Schedule{{
		Name:   "morning",
		Spec:   "0 9 * * *",
		Prompt: "x",
	}})

	fs := &fakeSpawner{}
	sock := shortSock(t)
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     sock,
		Spawner:        fs,
		TickInterval:   50 * time.Millisecond,
		Now:            clock,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Wait long enough that the first immediate tick + at least two
	// ticker fires have had a chance to NOT fire. There's no way to
	// poll for "nothing happens"; a generous sleep is the right
	// instrument here.
	waitForSocket(t, sock, 2*time.Second)
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done

	if fs.Count() != 0 {
		t.Fatalf("expected 0 Spawn calls before 9am, got %d", fs.Count())
	}
}

// TestDaemon_OneShotRemovedAfterFire — a schedule with Once=true is
// removed from the on-disk config after a successful fire.
func TestDaemon_OneShotRemovedAfterFire(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	clock := &fakeClock{now: time.Date(2026, 6, 5, 15, 0, 0, 0, time.Local)}

	writeTestConfig(t, cfgPath, []schedule.Schedule{{
		Name:   "one-off",
		Spec:   "0 15 5 6 *", // 15:00 on June 5
		Prompt: "do the thing",
		Once:   true,
	}})

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:   cfgPath,
		SocketPath:   shortSock(t),
		Spawner:        fs,
		TickInterval:   50 * time.Millisecond,
		Now:            clock,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	// Poll: the first tick is immediate; on a loaded laptop the full
	// fire + persistSchedules round-trip is still well under 2s.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fs.Count() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if fs.Count() == 0 {
		t.Fatalf("expected at least 1 Spawn for one-off, got %d", fs.Count())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, s := range cfg.Schedules {
		if s.Name == "one-off" {
			t.Fatalf("expected one-off to be removed after fire; still present: %+v", s)
		}
	}
}

// TestDaemon_StatusOverIPC walks the full path: start the daemon →
// dial the UDS → send {"cmd":"status"} → assert the response shape.
func TestDaemon_StatusOverIPC(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	sock := shortSock(t)
	clock := &fakeClock{now: time.Date(2026, 6, 5, 8, 0, 0, 0, time.Local)}

	writeTestConfig(t, cfgPath, []schedule.Schedule{
		{Name: "a", Spec: "0 9 * * *", Prompt: "A"},
		{Name: "b", Spec: "0 18 * * *", Prompt: "B"},
	})

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:   cfgPath,
		SocketPath:   sock,
		Spawner:        fs,
		TickInterval:   1 * time.Second,
		Now:            clock,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitForSocket(t, sock, 2*time.Second)

	conn, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	resp, err := SendRequest(conn, Request{Cmd: "status"})
	_ = conn.Close()
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}

	if !resp.Ok {
		t.Fatalf("status not ok: %+v", resp)
	}
	if len(resp.Schedules) != 2 {
		t.Fatalf("expected 2 schedules in status, got %d", len(resp.Schedules))
	}
	if !strings.Contains(resp.Msg, "2 schedule") {
		t.Fatalf("msg should mention count: %q", resp.Msg)
	}

	cancel()
	<-done
}

// TestDaemon_ReloadPicksUpNewSchedule — write config A, start daemon,
// rewrite config B with a new schedule, send IPC "reload", assert
// status shows the new entry.
func TestDaemon_ReloadPicksUpNewSchedule(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	sock := shortSock(t)
	clock := &fakeClock{now: time.Date(2026, 6, 5, 8, 0, 0, 0, time.Local)}

	writeTestConfig(t, cfgPath, []schedule.Schedule{
		{Name: "a", Spec: "0 9 * * *", Prompt: "A"},
	})

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:   cfgPath,
		SocketPath:   sock,
		Spawner:        fs,
		TickInterval:   1 * time.Second,
		Now:            clock,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitForSocket(t, sock, 2*time.Second)

	// Rewrite config with a second schedule.
	writeTestConfig(t, cfgPath, []schedule.Schedule{
		{Name: "a", Spec: "0 9 * * *", Prompt: "A"},
		{Name: "c", Spec: "0 12 * * *", Prompt: "C"},
	})

	conn, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	resp, err := SendRequest(conn, Request{Cmd: "reload"})
	_ = conn.Close()
	if err != nil || !resp.Ok {
		t.Fatalf("reload: ok=%v err=%v msg=%q", resp.Ok, err, resp.Msg)
	}

	conn2, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial (status): %v", err)
	}
	resp2, err := SendRequest(conn2, Request{Cmd: "status"})
	_ = conn2.Close()
	if err != nil {
		t.Fatalf("status after reload: %v", err)
	}
	if len(resp2.Schedules) != 2 {
		t.Fatalf("expected 2 schedules after reload, got %d", len(resp2.Schedules))
	}

	cancel()
	<-done
}

// TestDaemon_StopViaIPC — send {"cmd":"stop"} and confirm Run returns
// without needing an external ctx cancel.
func TestDaemon_StopViaIPC(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	sock := shortSock(t)
	writeTestConfig(t, cfgPath, nil)

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:   cfgPath,
		SocketPath:   sock,
		Spawner:        fs,
		TickInterval:   1 * time.Second,
		Now:            &fakeClock{now: time.Date(2026, 6, 5, 8, 0, 0, 0, time.Local)},
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	waitForSocket(t, sock, 2*time.Second)

	conn, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	resp, _ := SendRequest(conn, Request{Cmd: "stop"})
	_ = conn.Close()
	if !resp.Ok {
		t.Fatalf("stop response: %+v", resp)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit within 2s of IPC stop")
	}
}

// TestDaemon_InvalidScheduleIsSkipped — a malformed schedule logged
// to stderr should not break loadConfig.
func TestDaemon_InvalidScheduleIsSkipped(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	clock := &fakeClock{now: time.Date(2026, 6, 5, 9, 0, 0, 0, time.Local)}

	writeTestConfig(t, cfgPath, []schedule.Schedule{
		{Name: "good", Spec: "0 9 * * *", Prompt: "ok"},
		{Name: "bad", Spec: "this is not cron", Prompt: "x"},
	})

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:   cfgPath,
		SocketPath:   shortSock(t),
		Spawner:        fs,
		TickInterval:   50 * time.Millisecond,
		Now:            clock,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fs.Count() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	// Bad schedule didn't fire; good one did.
	if fs.Count() == 0 {
		t.Fatal("good schedule should have fired despite bad sibling")
	}
}
