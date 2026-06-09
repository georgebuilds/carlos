package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// errDeadlineConn wraps a net.Conn and returns a synthetic error from
// SetDeadline so we can exercise HandleConn's deadline-failure path
// without relying on a real OS-level UDS failure (which is essentially
// impossible to provoke deterministically on macOS / Linux).
type errDeadlineConn struct {
	net.Conn
	// captures everything HandleConn writes to the wire so the test can
	// assert the ok=false response landed before close.
	written *bytes.Buffer
	mu      sync.Mutex
}

func newErrDeadlineConn() *errDeadlineConn {
	a, b := net.Pipe()
	// We don't need the peer end's reader for this test - drain it.
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := a.Read(buf); err != nil {
				return
			}
		}
	}()
	return &errDeadlineConn{Conn: b, written: &bytes.Buffer{}}
}

func (c *errDeadlineConn) SetDeadline(time.Time) error {
	return errors.New("synthetic deadline failure")
}

func (c *errDeadlineConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.written.Write(p)
	c.mu.Unlock()
	return c.Conn.Write(p)
}

func (c *errDeadlineConn) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, c.written.Len())
	copy(out, c.written.Bytes())
	return out
}

// TestHandleConn_SetDeadlineFailureClosesCleanly - if SetDeadline fails
// (synthetic; rare on real UDS but possible on a wedged kernel handle),
// HandleConn must write an ok=false response and close rather than
// proceeding without a deadline (which would let a slow peer pin the
// goroutine forever).
func TestHandleConn_SetDeadlineFailureClosesCleanly(t *testing.T) {
	conn := newErrDeadlineConn()
	dispatchCalled := false
	HandleConn(conn, func(Request) Response {
		dispatchCalled = true
		return Response{Ok: true}
	})

	if dispatchCalled {
		t.Errorf("dispatch should NOT be invoked when SetDeadline fails")
	}
	out := conn.bytes()
	if len(out) == 0 {
		t.Fatal("expected a best-effort response on the wire before close")
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		t.Fatalf("response should be valid JSON; got %q (%v)", out, err)
	}
	if resp.Ok {
		t.Errorf("response.Ok should be false on SetDeadline failure; got %+v", resp)
	}
	if !strings.Contains(resp.Msg, "set deadline") {
		t.Errorf("response.Msg should mention set-deadline failure; got %q", resp.Msg)
	}
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
