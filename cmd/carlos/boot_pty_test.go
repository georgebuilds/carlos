// boot_pty_test.go - slice 9f regression harness: launch-to-first-frame
// measured end-to-end through a real PTY.
//
// The test builds the carlos binary, seeds a minimal complete config in
// a throwaway HOME (so the real ~/.carlos is never touched and no
// onboarding flow triggers), runs it under a pty with CARLOS_BOOT_TRACE
// on, and parses the in-process first_frame figure out of the trace
// line the binary prints at its first bubbletea frame. This doubles as
// an end-to-end test of the boot trace itself.
//
// Flake resistance (CI boxes are noisy):
//
//   - The budget is generous: 500ms against ~50ms measured on an M3.
//     Override with CARLOS_BOOT_BUDGET_MS; set it to 0 to record the
//     numbers without enforcing (measure-only mode).
//   - Best-of-3 attempts: only the fastest run is judged, so a single
//     scheduler hiccup can't fail the suite.
//   - Skips cleanly when a pty can't be allocated (sandboxed CI) and
//     under -short (the go-build alone costs seconds).
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/georgebuilds/carlos/internal/config"
)

// bootTraceLineRE matches the trace line the binary prints on its first
// frame; capture group 1 is the cumulative first_frame milliseconds.
var bootTraceLineRE = regexp.MustCompile(`carlos boot trace:.* first_frame=([0-9]+\.[0-9])ms`)

func TestBootToFirstFrame_PTY(t *testing.T) {
	if testing.Short() {
		t.Skip("boot benchmark skipped in -short mode")
	}

	budgetMs := 500.0
	if v := os.Getenv("CARLOS_BOOT_BUDGET_MS"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("CARLOS_BOOT_BUDGET_MS=%q is not a number: %v", v, err)
		}
		budgetMs = f
	}

	// Build the real binary once.
	bin := filepath.Join(t.TempDir(), "carlos-bootbench")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// Throwaway HOME with a complete config: a user name plus one
	// provider with an API key satisfies config.IsComplete, so the
	// binary boots straight into the chat TUI. The anthropic client is
	// constructed without any network call; the fake key is only ever
	// used if a turn is dispatched, which this test never does.
	home := t.TempDir()
	cfgPath := filepath.Join(home, ".carlos", "config.yaml")
	if err := config.Save(cfgPath, &config.Config{
		UserName:        "bootbench",
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "bootbench-not-a-real-key", DefaultModel: "claude-bootbench"},
		},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// Judge the EXTERNAL wall time (spawn to trace line on the pty):
	// it covers exec + runtime/package init + the full boot path, so a
	// regression hiding before the trace's zero point (e.g. a terminal
	// query stalling in a package init) still trips it. The binary's
	// self-reported first_frame is logged alongside for triage.
	const attempts = 3
	bestWall := time.Duration(-1)
	bestSelf := -1.0
	for i := 0; i < attempts; i++ {
		ms, wall, err := bootOnceUnderPTY(t, bin, home, cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("attempt %d: wall=%.1fms self-reported first_frame=%.1fms",
			i+1, float64(wall.Microseconds())/1000.0, ms)
		if bestWall < 0 || wall < bestWall {
			bestWall, bestSelf = wall, ms
		}
	}

	bestWallMs := float64(bestWall.Microseconds()) / 1000.0
	t.Logf("best of %d: wall=%.1fms (self-reported first_frame=%.1fms), budget %.0fms",
		attempts, bestWallMs, bestSelf, budgetMs)
	if budgetMs > 0 && bestWallMs > budgetMs {
		t.Fatalf("launch-to-first-frame regression: best of %d attempts was %.1fms wall, budget %.0fms",
			attempts, bestWallMs, budgetMs)
	}
}

// bootOnceUnderPTY runs one boot, returning the binary's self-reported
// first_frame milliseconds plus the external wall time from spawn to
// the trace line appearing on the pty. Calls t.Skip when no pty can be
// allocated; other failures return an error.
func bootOnceUnderPTY(t *testing.T, bin, home, cfgPath string) (float64, time.Duration, error) {
	t.Helper()
	cmd := exec.Command(bin)
	// Hermetic env: only what the boot path needs. Notably no
	// CARLOS_FRAME (would change frame resolution) and no real HOME.
	cmd.Env = []string{
		"HOME=" + home,
		"CARLOS_CONFIG=" + cfgPath,
		"CARLOS_BOOT_TRACE=1",
		"TERM=xterm-256color",
		"PATH=" + os.Getenv("PATH"),
	}
	cmd.Dir = home

	start := time.Now()
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Skipf("pty unavailable on this host: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = f.Close()
	}()

	// Drain the pty on a goroutine (TUI frames + the stderr trace line
	// share the terminal); poll the accumulated bytes for the trace.
	//
	// The drain goroutine also ANSWERS terminal queries. During package
	// init (before main-package vars - i.e. before the trace's zero
	// point), lipgloss background detection fires termenv's OSC 11
	// background-color query plus a CPR (ESC[6n) and blocks up to
	// termenv's OSCTimeout (5s) for the reply. A real terminal answers
	// in <1ms; a silent pty would stall every boot at exactly ~5s and
	// measure termenv's timeout instead of carlos. Replying makes the
	// harness behave like a real terminal.
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	go func() {
		chunk := make([]byte, 4096)
		answeredBG, answeredCPR := false, false
		for {
			n, err := f.Read(chunk)
			if n > 0 {
				mu.Lock()
				buf.Write(chunk[:n])
				if !answeredBG && bytes.Contains(buf.Bytes(), []byte("\x1b]11;?")) {
					answeredBG = true
					_, _ = f.Write([]byte("\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\"))
				}
				if !answeredCPR && bytes.Contains(buf.Bytes(), []byte("\x1b[6n")) {
					answeredCPR = true
					_, _ = f.Write([]byte("\x1b[1;1R"))
				}
				mu.Unlock()
			}
			if err != nil {
				return // EIO on child exit is the normal pty EOF
			}
		}
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		m := bootTraceLineRE.FindSubmatch(buf.Bytes())
		mu.Unlock()
		if m != nil {
			wall := time.Since(start)
			ms, err := strconv.ParseFloat(string(m[1]), 64)
			if err != nil {
				return 0, 0, fmt.Errorf("unparsable first_frame value %q: %v", m[1], err)
			}
			return ms, wall, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	tail := buf.String()
	mu.Unlock()
	if len(tail) > 2000 {
		tail = tail[len(tail)-2000:]
	}
	return 0, 0, fmt.Errorf("boot trace line never appeared on the pty within 15s; last output:\n%s", tail)
}
