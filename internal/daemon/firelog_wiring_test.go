package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/schedule"
)

// TestDaemon_FireLog_OpensInPersistenceDir confirms the boot path
// creates a fire-log journal next to state.db (or, when StateDBPath is
// empty under Spawner-mode tests, next to the config file). The journal
// is the crash-window double-fire guard wired in atop schedule.FireLog;
// without this assertion the wire-up could silently regress to plain
// Due() and the v0.7.25 fix would be a no-op at the daemon layer.
func TestDaemon_FireLog_OpensInPersistenceDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeTestConfig(t, cfgPath, nil)

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     shortSock(t),
		Spawner:        fs,
		TickInterval:   1 * time.Second,
		Now:            &fakeClock{now: time.Date(2026, 6, 10, 8, 0, 0, 0, time.Local)},
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitForSocket(t, shortSockOK(t, d), 2*time.Second)

	// The daemon should have opened a journal at <dir>/fire.log by now.
	// Poll for the file to appear since the boot ordering is "listen
	// then open journal" and waitForSocket only proves the listener is
	// up; tens of microseconds later the journal lands.
	wantPath := filepath.Join(dir, "fire.log")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(wantPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected fire.log at %s, got stat err: %v", wantPath, err)
	}
	// Read d.fireLog under the daemon's mutex to avoid racing the boot
	// write in Run.
	if got := d.firelogPathLocked(); got != wantPath {
		t.Errorf("d.fireLog.Path() under lock = %q, want %q", got, wantPath)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	// After clean shutdown the underlying file should still exist (the
	// log is a persistent record, not a tmpfile).
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("fire.log disappeared after Close: %v", err)
	}
}

// TestDaemon_FireLog_SuppressesPreFiredSlot is the crash-window guard's
// core assertion: when the journal already records the slot a schedule
// would fire for, the daemon must skip the fire. We seed the file
// directly (mirroring what a crashed previous instance would have left
// on disk), boot the daemon, and assert the spawner was never called.
func TestDaemon_FireLog_SuppressesPreFiredSlot(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Anchor the clock to 09:00 local; schedule "0 9 * * *" would
	// normally fire on first tick.
	clock := &fakeClock{now: time.Date(2026, 6, 10, 9, 0, 0, 0, time.Local)}

	writeTestConfig(t, cfgPath, []schedule.Schedule{{
		Name:   "morning",
		Spec:   "0 9 * * *",
		Prompt: "summarize my unread Slack DMs",
	}})

	// Pre-populate the fire-log with the 09:00 slot for "morning". The
	// slot timestamp matches schedule.SlotFor's cron resolution: the
	// matching minute, truncated to whole minutes, in local time.
	slot := time.Date(2026, 6, 10, 9, 0, 0, 0, time.Local)
	seedFireLog(t, filepath.Join(dir, "fire.log"), "morning", slot)

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     shortSock(t),
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

	// Give the immediate first-tick + a couple of ticker passes a chance
	// to NOT fire. There's no way to poll for "nothing happens"; a
	// generous sleep is the right instrument.
	waitForSocket(t, shortSockOK(t, d), 2*time.Second)
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done

	if got := fs.Count(); got != 0 {
		t.Fatalf("expected 0 spawns (slot pre-fired), got %d", got)
	}
}

// TestDaemon_FireLog_HappyPathRecordsSlot is the dual assertion: a
// fresh tick that finds a due schedule must (1) fire the action AND (2)
// leave a journal entry for the slot so a subsequent restart sees the
// record. Without (2) the crash-window fix would be only a partial fix.
func TestDaemon_FireLog_HappyPathRecordsSlot(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	clock := &fakeClock{now: time.Date(2026, 6, 10, 9, 0, 0, 0, time.Local)}

	writeTestConfig(t, cfgPath, []schedule.Schedule{{
		Name:   "morning",
		Spec:   "0 9 * * *",
		Prompt: "summarize my unread Slack DMs",
	}})

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     shortSock(t),
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
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := fs.Count(); got < 1 {
		t.Fatalf("expected at least one spawn, got %d", got)
	}

	// Reopen the journal from scratch (mirrors a restart) and confirm
	// the slot is now recorded.
	logPath := filepath.Join(dir, "fire.log")
	replayed, err := schedule.OpenFireLog(logPath)
	if err != nil {
		t.Fatalf("reopen fire.log: %v", err)
	}
	defer replayed.Close()
	slot := time.Date(2026, 6, 10, 9, 0, 0, 0, time.Local)
	if !replayed.Has("morning", slot) {
		t.Fatalf("replayed log missing 'morning' entry for slot %v", slot)
	}
}

// failingFireLog is a fireLogger stub whose Append always errors. Used
// to assert the "append-fails → skip" path: if the journal can't
// durably record the slot, the daemon must refuse to fire (better to
// skip than double-fire after a crash).
type failingFireLog struct {
	mu        sync.Mutex
	appendErr error
}

func (f *failingFireLog) Has(name string, slot time.Time) bool { return false }
func (f *failingFireLog) Append(name string, slot time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appendErr
}
func (f *failingFireLog) Close() error { return nil }
func (f *failingFireLog) Path() string { return "/dev/null/failing" }

// TestDaemon_FireLog_AppendFailureSkipsFire pins the at-most-once
// contract: when the journal Append fails (disk full, fs read-only,
// etc.) the tick path must skip the fire rather than run it without a
// durable record. A crash mid-action without a durable record would
// double-fire on restart, which is the exact failure mode the journal
// exists to prevent.
func TestDaemon_FireLog_AppendFailureSkipsFire(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	clock := &fakeClock{now: time.Date(2026, 6, 10, 9, 0, 0, 0, time.Local)}

	writeTestConfig(t, cfgPath, []schedule.Schedule{{
		Name:   "morning",
		Spec:   "0 9 * * *",
		Prompt: "x",
	}})

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     shortSock(t),
		Spawner:        fs,
		TickInterval:   50 * time.Millisecond,
		Now:            clock,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Swap in the failing stub before Run starts the tick loop. We do
	// this by intercepting Run inside a goroutine: New + a tiny delay
	// for loadConfig is too racy. Instead we drive tick() directly:
	// load config manually, install the stub, and call tick().
	if err := d.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	d.mu.Lock()
	d.fireLog = &failingFireLog{appendErr: errors.New("disk full (simulated)")}
	d.mu.Unlock()

	d.tick(context.Background())

	if got := fs.Count(); got != 0 {
		t.Fatalf("expected 0 spawns when Append fails, got %d", got)
	}
}

// TestDaemon_FireLog_OpenFailureFallsBackToDue covers the boot-time
// resilience contract: if the journal file cannot be opened (parent
// directory unwritable, etc.) the daemon must still come up and tick
// via plain Due(). Refusing to start over a missing journal would turn
// a recoverable filesystem hiccup into a hard outage of the user's
// scheduler.
func TestDaemon_FireLog_OpenFailureFallsBackToDue(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	clock := &fakeClock{now: time.Date(2026, 6, 10, 9, 0, 0, 0, time.Local)}

	writeTestConfig(t, cfgPath, []schedule.Schedule{{
		Name:   "morning",
		Spec:   "0 9 * * *",
		Prompt: "x",
	}})

	// Force OpenFireLog to fail by pointing StateDBPath into a path
	// whose parent cannot be created: /dev/null is a device, not a
	// directory, so MkdirAll inside OpenFireLog cannot create
	// /dev/null/carlos and the open call returns an error. The daemon
	// must absorb that and continue with a nil fireLog.
	bogusStateDB := "/dev/null/carlos/state.db"

	fs := &fakeSpawner{}
	d, err := New(Options{
		ConfigPath:     cfgPath,
		SocketPath:     shortSock(t),
		StateDBPath:    bogusStateDB,
		Spawner:        fs, // Spawner set → state.db itself isn't opened.
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
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Read under the daemon's mutex so we don't race the boot-time
	// write in Run.
	if got := d.firelogPathLocked(); got != "" {
		t.Errorf("d.fireLog should be nil after OpenFireLog failure; got non-empty path %q", got)
	}
	if got := fs.Count(); got < 1 {
		t.Fatalf("expected at least one spawn (Due() fallback), got %d", got)
	}
}

// shortSockOK returns the socket path the daemon was constructed with.
// Tiny helper so the suppression test can poll readiness via the same
// waitForSocket helper as the rest of the suite.
func shortSockOK(t *testing.T, d *Daemon) string {
	t.Helper()
	return d.opts.SocketPath
}

// seedFireLog writes a single (name, slot) entry to the journal file at
// path using the live OpenFireLog/Append API. We deliberately exercise
// the production code path rather than hand-rolling the wire format so
// a future format change keeps the test honest.
func seedFireLog(t *testing.T, path, name string, slot time.Time) {
	t.Helper()
	log, err := schedule.OpenFireLog(path)
	if err != nil {
		t.Fatalf("seedFireLog: open %s: %v", path, err)
	}
	if err := log.Append(name, slot); err != nil {
		_ = log.Close()
		t.Fatalf("seedFireLog: append: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("seedFireLog: close: %v", err)
	}
}
