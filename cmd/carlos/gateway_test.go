package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/georgebuilds/carlos/internal/daemon"
)

// shortSock returns a UDS path short enough for macOS's UNIX_PATH_MAX.
// Mirrors the helper in internal/daemon/daemon_test.go because the
// constraint is the same regardless of which package owns the socket.
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

// fakeDaemon spins a UDS listener at sock and dispatches each accepted
// connection through dispatch. Returns a stop func the caller defers.
// One goroutine per connection so the listener can serve many in a row.
func fakeDaemon(t *testing.T, sock string, dispatch func(daemon.Request) daemon.Response) func() {
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

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// whatever was written. Used to assert the human-readable CLI output of
// runGatewayTest on success without coupling to a logging shim.
func captureStdout(t *testing.T, fn func() error) (string, error) {
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

func TestRunGatewayTest_SuccessRoundTrip(t *testing.T) {
	sock := shortSock(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)

	var got daemon.Request
	stop := fakeDaemon(t, sock, func(req daemon.Request) daemon.Response {
		got = req
		return daemon.Response{Ok: true, Msg: "gateway-test: sent via telegram"}
	})
	defer stop()

	out, err := captureStdout(t, func() error {
		return runGatewayTest([]string{"telegram"})
	})
	if err != nil {
		t.Fatalf("runGatewayTest: %v", err)
	}
	if got.Cmd != "gateway-test" {
		t.Errorf("daemon received cmd %q, want gateway-test", got.Cmd)
	}
	if got.Channel != "telegram" {
		t.Errorf("daemon received channel %q, want telegram", got.Channel)
	}
	if !strings.Contains(out, "sent via telegram") {
		t.Errorf("stdout missing success message: %q", out)
	}
}

func TestRunGatewayTest_UnknownChannelRejectedClientSide(t *testing.T) {
	// No daemon needed — the CLI rejects the bad channel before dialing.
	err := runGatewayTest([]string{"slack"})
	if err == nil {
		t.Fatal("expected error for unknown channel")
	}
	if !strings.Contains(err.Error(), "unknown channel") {
		t.Errorf("error should mention unknown channel: %v", err)
	}
}

func TestRunGatewayTest_MissingChannel(t *testing.T) {
	err := runGatewayTest(nil)
	if err == nil {
		t.Fatal("expected error for missing channel arg")
	}
	if !strings.Contains(err.Error(), "channel required") {
		t.Errorf("error should mention required channel: %v", err)
	}
}

func TestRunGatewayTest_DaemonNotRunning(t *testing.T) {
	sock := shortSock(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)
	// Do NOT start a fake daemon — the dial must fail clean.
	err := runGatewayTest([]string{"telegram"})
	if err == nil {
		t.Fatal("expected error when daemon not running")
	}
	if !strings.Contains(err.Error(), "daemon not running") {
		t.Errorf("error should mention daemon not running: %v", err)
	}
}

func TestRunGatewayTest_DaemonReturnsFailure(t *testing.T) {
	sock := shortSock(t)
	t.Setenv("CARLOS_DAEMON_SOCKET", sock)

	stop := fakeDaemon(t, sock, func(_ daemon.Request) daemon.Response {
		return daemon.Response{Ok: false, Msg: "gateway-test: channel \"telegram\" is not enabled in config"}
	})
	defer stop()

	err := runGatewayTest([]string{"telegram"})
	if err == nil {
		t.Fatal("expected error when daemon reports failure")
	}
	if !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("error should surface the daemon's reason: %v", err)
	}
}

func TestRunGateway_UnknownSubcommand(t *testing.T) {
	err := runGateway([]string{"reload"})
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	if !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("error should mention unknown subcommand: %v", err)
	}
}

func TestRunGateway_NoArgs(t *testing.T) {
	err := runGateway(nil)
	if err == nil {
		t.Fatal("expected error for missing subcommand")
	}
}

func TestGatewayTestRequest_JSONShape(t *testing.T) {
	// Pins the wire shape so a future field rename doesn't silently
	// break the daemon ↔ CLI handshake.
	req := daemon.Request{Cmd: "gateway-test", Channel: "ntfy"}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	want := `{"cmd":"gateway-test","channel":"ntfy"}`
	if got != want {
		t.Errorf("json shape: got %s want %s", got, want)
	}
}
