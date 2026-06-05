package daemon

import (
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestIPC_RoundTrip exercises the full Listen → Dial → SendRequest →
// HandleConn → Response roundtrip over a real UDS pair in a tmpdir, so
// the protocol is end-to-end verified without touching ~/.carlos.
func TestIPC_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	l, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	// Server side: accept exactly 3 connections, dispatch each through
	// a synthetic handler that returns the cmd echoed in Msg.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		for i := 0; i < 3; i++ {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer wg.Done()
				HandleConn(conn, func(req Request) Response {
					switch req.Cmd {
					case "status":
						return Response{Ok: true, Msg: "ok-status", ActiveCount: 7}
					case "reload":
						return Response{Ok: true, Msg: "ok-reload"}
					case "stop":
						return Response{Ok: true, Msg: "ok-stop"}
					default:
						return Response{Ok: false, Msg: "unknown"}
					}
				})
			}()
		}
	}()

	cases := []struct {
		cmd     string
		wantMsg string
		wantOk  bool
	}{
		{"status", "ok-status", true},
		{"reload", "ok-reload", true},
		{"stop", "ok-stop", true},
	}
	for _, c := range cases {
		conn, err := Dial(sock)
		if err != nil {
			t.Fatalf("Dial(%s): %v", c.cmd, err)
		}
		resp, err := SendRequest(conn, Request{Cmd: c.cmd})
		_ = conn.Close()
		if err != nil {
			t.Fatalf("SendRequest(%s): %v", c.cmd, err)
		}
		if resp.Ok != c.wantOk || resp.Msg != c.wantMsg {
			t.Errorf("%s: got {ok=%v msg=%q} want {ok=%v msg=%q}", c.cmd, resp.Ok, resp.Msg, c.wantOk, c.wantMsg)
		}
	}
	wg.Wait()
}

// TestIPC_DoubleListenFails confirms a second Listen on the same path
// surfaces a clean error instead of silently shadowing the first.
func TestIPC_DoubleListenFails(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	l1, err := Listen(sock)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer l1.Close()

	_, err = Listen(sock)
	if err == nil {
		t.Fatal("expected second Listen to fail with 'already in use'")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestIPC_StaleSocketCleanup verifies that a leftover socket file from
// a crashed daemon doesn't block a fresh Listen.
func TestIPC_StaleSocketCleanup(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "stale.sock")

	// Create + immediately close a listener to leave a stale path.
	l1, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	// Close the listener but leave the socket file behind so the file
	// exists yet nobody is bound. This is what a crashed-daemon state
	// looks like on disk.
	if uc, ok := l1.(*net.UnixListener); ok {
		// SetUnlinkOnClose(false) so the file persists after Close.
		uc.SetUnlinkOnClose(false)
	}
	_ = l1.Close()

	l2, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	defer l2.Close()
}

// TestIPC_BadRequest verifies HandleConn surfaces a parseable error
// response (rather than panicking) when the client sends garbage.
func TestIPC_BadRequest(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	l, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	done := make(chan struct{})
	go func() {
		conn, err := l.Accept()
		if err != nil {
			close(done)
			return
		}
		HandleConn(conn, func(Request) Response { return Response{Ok: true} })
		close(done)
	}()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	// HandleConn should write a {"ok":false,...} reply and close.
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, `"ok":false`) {
		t.Fatalf("expected ok:false in reply, got %q", got)
	}
	<-done
}
