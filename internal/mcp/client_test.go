package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseArgs_AcceptsValidObject(t *testing.T) {
	cases := map[string]struct {
		in   string
		want map[string]any
	}{
		"empty input":            {"", map[string]any{}},
		"whitespace only":        {"   \n\t", map[string]any{}},
		"explicit null":          {"null", map[string]any{}},
		"empty object":           {"{}", map[string]any{}},
		"object with whitespace": {"  { \"k\": 1 } ", map[string]any{"k": float64(1)}},
		"object with strings":    {`{"a":"b","c":"d"}`, map[string]any{"a": "b", "c": "d"}},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			got, err := parseArgs([]byte(tc.in))
			if err != nil {
				t.Fatalf("parseArgs(%q) returned error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseArgs(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseArgs_RejectsNonObject(t *testing.T) {
	cases := map[string]struct {
		in       string
		wantKind string
	}{
		"array":         {`["a","b"]`, "array"},
		"empty array":   {`[]`, "array"},
		"number":        {`42`, "number"},
		"negative":      {`-1.5`, "number"},
		"string":        {`"hello"`, "string"},
		"bool true":     {`true`, "bool"},
		"bool false":    {`false`, "bool"},
		"padded array":  {"  [1,2]  ", "array"},
		"padded string": {"  \"x\"  ", "string"},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			got, err := parseArgs([]byte(tc.in))
			if err == nil {
				t.Fatalf("parseArgs(%q) = %#v, want error", tc.in, got)
			}
			if got != nil {
				t.Fatalf("parseArgs(%q) returned map %#v on error; want nil", tc.in, got)
			}
			msg := err.Error()
			if !strings.Contains(msg, "expected JSON object") {
				t.Fatalf("parseArgs(%q) error %q missing 'expected JSON object'", tc.in, msg)
			}
			if !strings.Contains(msg, tc.wantKind) {
				t.Fatalf("parseArgs(%q) error %q missing kind label %q", tc.in, msg, tc.wantKind)
			}
		})
	}
}

func TestParseArgs_PropagatesMalformedJSON(t *testing.T) {
	// Shape-valid (starts with '{') but otherwise malformed input should
	// still surface a JSON parse error rather than be silently dropped.
	_, err := parseArgs([]byte(`{not json`))
	if err == nil {
		t.Fatal("parseArgs of malformed object returned nil error")
	}
}

func TestJoinContent_SkipsNilBlock(t *testing.T) {
	valid := func(s string) *sdk.TextContent { return &sdk.TextContent{Text: s} }

	t.Run("interleaved nils match clean slice", func(t *testing.T) {
		withNils := []sdk.Content{nil, valid("hello"), nil, valid("world"), nil}
		clean := []sdk.Content{valid("hello"), valid("world")}
		gotNils := joinContent(withNils)
		gotClean := joinContent(clean)
		if gotNils != gotClean {
			t.Fatalf("nil-interleaved=%q, clean=%q; want equal", gotNils, gotClean)
		}
		if gotNils != "hello\nworld" {
			t.Fatalf("joinContent = %q, want %q", gotNils, "hello\nworld")
		}
	})

	t.Run("leading nil does not emit leading newline", func(t *testing.T) {
		got := joinContent([]sdk.Content{nil, valid("a")})
		if got != "a" {
			t.Fatalf("joinContent leading-nil = %q, want %q", got, "a")
		}
	})

	t.Run("trailing nil does not emit trailing newline", func(t *testing.T) {
		got := joinContent([]sdk.Content{valid("a"), nil})
		if got != "a" {
			t.Fatalf("joinContent trailing-nil = %q, want %q", got, "a")
		}
	})

	t.Run("all nils returns empty string without panic", func(t *testing.T) {
		// Run inside a closure so a panic here fails the subtest rather
		// than aborting the whole binary.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("joinContent panicked on all-nil slice: %v", r)
			}
		}()
		got := joinContent([]sdk.Content{nil, nil, nil})
		if got != "" {
			t.Fatalf("joinContent all-nil = %q, want empty", got)
		}
	})

	t.Run("nil-only does not panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("joinContent panicked on single nil: %v", r)
			}
		}()
		_ = joinContent([]sdk.Content{nil})
	})
}

// TestConnect_ReapsSubprocessOnHandshakeFailure asserts the
// post-condition the leak fix exists to guarantee: after Connect
// returns an error, the spawned subprocess is gone within a short
// window - even when the parent ctx is process-lifetime (never
// cancelled).
//
// We deliberately use context.Background() (no deadline, no cancel) so
// exec.CommandContext's automatic kill-on-ctx-done cannot mask the
// leak. The subprocess writes garbage to stdout, which makes the SDK's
// JSON-RPC framer return a parse error, which makes client.Connect
// fail without our test cancelling anything.
//
// Strategy: /bin/sh -c 'echo $$ > pidfile; printf "garbage\n"; exec
// sleep 60'. PID stays stable because exec replaces the shell; the
// garbage line trips the handshake; the sleep 60 makes sure a leaked
// process is detectable for the full test window.
func TestConnect_ReapsSubprocessOnHandshakeFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX /bin/sh and Signal(0)")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh not available: %v", err)
	}

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")

	// No timeout - we want to prove the cleanup is not coming from
	// exec.CommandContext reacting to ctx cancellation.
	ctx := context.Background()

	cfg := ServerConfig{
		Name:    "garbage-emitter",
		Command: "/bin/sh",
		// Emit a junk line so the SDK's initialize read errors out
		// (JSON parse failure on the first frame). Then sleep so the
		// process would, absent cleanup, stay around for ages.
		Args: []string{"-c", "echo $$ > " + pidFile + "; printf 'not-json\\n'; exec sleep 60"},
	}
	srv, err := Connect(ctx, cfg)
	if err == nil {
		_ = srv.Close()
		t.Fatal("Connect against garbage-emitter unexpectedly succeeded")
	}

	// Poll for the PID file - the shell writes it before printf, so
	// by the time Connect returned (the SDK had to read the garbage
	// line) it's guaranteed to be on disk. Read defensively anyway.
	pid := waitForPID(t, pidFile, 2*time.Second)
	if pid == 0 {
		t.Fatal("subprocess never recorded its PID; cannot verify cleanup")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}
	// Signal(0) on a live process returns nil; on a dead one it
	// returns os.ErrProcessDone / ESRCH. Either way, a non-nil error
	// is the post-condition we want.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return // gone, as required
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Still alive after the window - best-effort kill so the test
	// host isn't left with a runaway sleeper, then fail loudly.
	_ = proc.Signal(syscall.SIGKILL)
	t.Fatalf("subprocess pid %d still alive after Connect failure - leak not patched", pid)
}

// TestKillAndReap_KillsLiveProcess proves the leak-fix helper actually
// terminates a running subprocess (the realistic scenario when the SDK
// returns from Connect without having closed the transport).
func TestKillAndReap_KillsLiveProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX /bin/sh and Signal(0)")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh not available: %v", err)
	}
	cmd := newSleepCmd(t)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := cmd.Process.Pid
	// Sanity: process is alive right now.
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("freshly-started process unexpectedly dead: %v", err)
	}
	killAndReap(cmd)
	// After kill+wait, the OS entry should be gone. Re-find by PID
	// (the cmd.Process handle is technically still valid but Wait
	// already returned, so a second Signal would race the reap).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("subprocess pid %d still alive after killAndReap", pid)
}

// TestKillAndReap_NoOpOnUnstartedCmd guards the branch that protects
// against killing a cmd whose transport never reached Start (e.g.
// StdoutPipe / StdinPipe failed before Start was attempted).
func TestKillAndReap_NoOpOnUnstartedCmd(t *testing.T) {
	cmd := newSleepCmd(t)
	// Do NOT Start. cmd.Process is nil; killAndReap must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("killAndReap panicked on unstarted cmd: %v", r)
		}
	}()
	killAndReap(cmd)
}

// TestKillAndReap_NoOpOnNilCmd documents that the helper accepts nil
// (defensive - exec.CommandContext can't actually produce a nil *Cmd
// today, but the contract is "safe in any post-Connect cleanup path").
func TestKillAndReap_NoOpOnNilCmd(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("killAndReap panicked on nil cmd: %v", r)
		}
	}()
	killAndReap(nil)
}

// newSleepCmd builds an exec.Cmd that, once Start()ed, sleeps for 60s
// - long enough to be observably alive when the test checks. Uses
// CommandContext bound to t.Context() so a panicking test still cleans
// up after itself.
func newSleepCmd(t *testing.T) *exec.Cmd {
	t.Helper()
	return exec.CommandContext(t.Context(), "/bin/sh", "-c", "exec sleep 60")
}

// waitForPID polls path for an integer PID, returning 0 if none
// appears before timeout. Used by the Connect-leak regression test to
// learn the subprocess's PID without exposing it through the public
// API (Connect returns nil on failure, so the caller has no handle).
func waitForPID(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			if s := strings.TrimSpace(string(raw)); s != "" {
				if p, convErr := strconv.Atoi(s); convErr == nil && p > 0 {
					return p
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0
}

func TestJoinContent_HappyPathUnchanged(t *testing.T) {
	// Guard against accidental regressions in the non-nil path while we
	// were patching the nil guard in.
	in := []sdk.Content{
		&sdk.TextContent{Text: "first"},
		&sdk.TextContent{Text: "second"},
	}
	got := joinContent(in)
	if got != "first\nsecond" {
		t.Fatalf("joinContent = %q, want %q", got, "first\nsecond")
	}
}
