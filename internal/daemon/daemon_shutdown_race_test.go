package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
)

// TestRun_ShutdownJoinsBackgroundGoroutines starts the daemon with the
// gateway enabled and a real state.db, so acceptLoop, the away-watcher, and
// the reminder-watcher are all live and reading d.log. Cancelling the
// context must shut everything down cleanly: Run joins those goroutines
// (d.wg.Wait) before it closes d.log, so under `go test -race` there is no
// data race between a watcher's log.Read() and the log handle being closed.
//
// Before the WaitGroup join this test fails the race detector; it is the
// regression guard for that shutdown-ordering fix.
func TestRun_ShutdownJoinsBackgroundGoroutines(t *testing.T) {
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
		TickInterval:   25 * time.Millisecond,
		DisableSignals: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitForSocket(t, d.opts.SocketPath, 2*time.Second)
	if !waitForGatewayWired(t, d, 2*time.Second) {
		t.Fatal("gateway runtime should be wired (watchers must be live for this test)")
	}
	// Let the watchers run at least one loop iteration so a close-vs-read
	// race would have a window to manifest.
	time.Sleep(60 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit within 5s - shutdown join may be deadlocked")
	}
}
