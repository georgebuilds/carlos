package sandbox

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLocalExec_BasicEcho proves the Local backend actually runs commands
// and captures their stdout. The exit code must be 0 and the body must
// contain what we echoed — anything else means the runCommand plumbing
// is broken.
func TestLocalExec_BasicEcho(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("echo not on PATH")
	}
	l := &Local{}
	stdout, _, exit, err := l.Exec(context.Background(), []string{"echo", "hello-local"}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit=%d, want 0", exit)
	}
	if !strings.Contains(string(stdout), "hello-local") {
		t.Fatalf("stdout=%q, want it to contain hello-local", stdout)
	}
}

// TestLocalExec_NonZeroExit confirms non-zero exits surface in the exit
// return value and are NOT promoted to err. `false` always exits 1 — if
// we get err != nil here the policy boundary between "infrastructure
// error" and "command failure" is wrong.
func TestLocalExec_NonZeroExit(t *testing.T) {
	if _, err := exec.LookPath("false"); err != nil {
		t.Skip("false not on PATH")
	}
	l := &Local{}
	_, _, exit, err := l.Exec(context.Background(), []string{"false"}, nil)
	if err != nil {
		t.Fatalf("Exec: unexpected err: %v", err)
	}
	if exit == 0 {
		t.Fatalf("exit=0, want non-zero from `false`")
	}
}

// TestLocalExec_CancelKillsChild starts a long-running `sleep 10`, cancels
// the context within 50ms, and asserts that Exec returns (with the ctx
// error) within ~2s. If the kill-the-group plumbing is broken, this hangs
// for the full ten seconds.
func TestLocalExec_CancelKillsChild(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not on PATH")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, _, _, err := (&Local{}).Exec(ctx, []string{"sleep", "10"}, nil)
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("Exec returned after %s, want < 2s — process tree probably not killed", elapsed)
	}
	if err == nil {
		t.Fatalf("Exec returned nil err after cancel; expected ctx error")
	}
}

// TestLocalExec_OutputCap proves the 8 KiB cap is enforced. We ask `yes`
// for ~64 KiB of output via `head -c`. The captured stdout must be
// bounded at maxOutputBytes — if not, a runaway sub-agent could blow
// past the supervisor's context budget.
func TestLocalExec_OutputCap(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	l := &Local{}
	// `yes | head -c 65536` reliably produces ~64 KiB on every platform
	// that ships POSIX coreutils.
	stdout, _, _, err := l.Exec(context.Background(), []string{"sh", "-c", "yes | head -c 65536"}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(stdout) > maxOutputBytes {
		t.Fatalf("stdout len=%d > cap=%d", len(stdout), maxOutputBytes)
	}
}

// TestLocalClose_Idempotent proves Close is safe to call twice. The
// Backend interface contract requires this so supervisor defer chains
// don't have to think about it.
func TestLocalClose_Idempotent(t *testing.T) {
	l := &Local{}
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestNewLocalFactory exercises the New("local") path so future refactors
// that change the factory don't silently break this entry point.
func TestNewLocalFactory(t *testing.T) {
	b, err := New("local")
	if err != nil {
		t.Fatalf("New(local): %v", err)
	}
	if b.Name() != "local" {
		t.Fatalf("Name=%q, want local", b.Name())
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestNewUnknownKind confirms the factory rejects unknown kinds with a
// wrapped error rather than panicking.
func TestNewUnknownKind(t *testing.T) {
	_, err := New("chroot")
	if err == nil {
		t.Fatalf("New(chroot): expected error")
	}
}
