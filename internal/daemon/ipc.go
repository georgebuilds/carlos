// Package daemon owns carlos's background scheduler process.
//
// The daemon is a Go binary, not a separate executable — `carlos daemon
// run` is the entry point the launchd / systemd unit calls. The other
// `carlos daemon …` subcommands (enable / disable / status) are
// user-facing CLIs that manage the platform unit and talk to the
// running daemon over a Unix domain socket at ~/.carlos/daemon.sock.
//
// IPC protocol
//
// The CLI dials the UDS and writes one newline-delimited JSON
// request; the daemon writes one newline-delimited JSON reply and
// closes the connection. Keeping it newline-delimited makes the
// protocol trivially debuggable (`echo '{"cmd":"status"}' | nc -U
// ~/.carlos/daemon.sock`) and avoids any framing complexity.
//
// Commands:
//
//	status   → returns active schedule count, next-fire time, last-run summary
//	reload   → re-reads config from disk; equivalent to SIGHUP
//	stop     → graceful shutdown (cancels the ctx the daemon is running under)
//
// Reply envelope is always {"ok": bool, "msg": string, ...} so a CLI
// failure mode is simple to surface: ok=false + msg is the human text
// the user sees on stderr.
package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// SocketName is the filename of the daemon's UDS rendezvous under
// ~/.carlos/. Exported so the CLI side can dial the same path without
// repeating the literal.
const SocketName = "daemon.sock"

// DefaultSocketPath returns ~/.carlos/daemon.sock. Test code can override
// the path by passing an explicit value to Listen / Dial.
func DefaultSocketPath() string {
	if env := os.Getenv("CARLOS_DAEMON_SOCKET"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".carlos", SocketName)
	}
	return filepath.Join(home, ".carlos", SocketName)
}

// Request is the JSON shape the CLI sends:
//
//	{"cmd":"status"}
//	{"cmd":"reload"}
//	{"cmd":"stop"}
type Request struct {
	Cmd string `json:"cmd"`
}

// Response is the JSON shape the daemon returns:
//
//	{"ok":true, "msg":"3 schedules active", "schedules":[...], "next_fire_at":"..."}
//
// Fields beyond `ok` + `msg` are command-specific and optional; older
// CLI builds reading newer daemon output ignore unknown fields.
type Response struct {
	Ok  bool   `json:"ok"`
	Msg string `json:"msg,omitempty"`

	// Status-specific fields.
	Schedules    []ScheduleStatus `json:"schedules,omitempty"`
	NextFireAt   *time.Time       `json:"next_fire_at,omitempty"`
	StartedAt    *time.Time       `json:"started_at,omitempty"`
	LastReloadAt *time.Time       `json:"last_reload_at,omitempty"`
	ActiveCount  int              `json:"active_count,omitempty"`
}

// ScheduleStatus is one row in Response.Schedules. Mirrors a subset of
// schedule.Schedule so the CLI surface stays decoupled from the
// schedule package's full struct (which has cost fields irrelevant to
// the status view).
type ScheduleStatus struct {
	Name       string     `json:"name"`
	Spec       string     `json:"spec"`
	NextFireAt time.Time  `json:"next_fire_at"`
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	LastRunOK  bool       `json:"last_run_ok,omitempty"`
	Once       bool       `json:"once,omitempty"`
}

// Listen opens a Unix domain socket at path. Returns the listener plus
// an error if the path is already bound (i.e. another daemon is
// running). The listener removes the socket file on Close().
//
// We deliberately don't blindly unlink stale sockets — if a previous
// daemon crashed without removing its socket, the dial-first probe in
// the CLI (or the daemon's own startup check) will detect the orphan
// and prompt the user / clean it up.
func Listen(path string) (net.Listener, error) {
	if path == "" {
		return nil, errors.New("daemon: empty socket path")
	}
	// Probe: if a live daemon is already bound, dial succeeds quickly.
	if conn, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("daemon: socket %s is already in use by another carlos daemon", path)
	}
	// Stale socket cleanup — only after the dial probe failed.
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("daemon: remove stale socket %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: mkdir socket parent: %w", err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("daemon: listen %s: %w", path, err)
	}
	// 0600: only the owner of ~/.carlos may speak to the daemon.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("daemon: chmod socket: %w", err)
	}
	return l, nil
}

// Dial connects to a running daemon's UDS and returns the connection.
// Caller is responsible for Close().
func Dial(path string) (net.Conn, error) {
	if path == "" {
		path = DefaultSocketPath()
	}
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("daemon: dial %s: %w (is the daemon running?)", path, err)
	}
	return conn, nil
}

// SendRequest writes one Request as JSON+newline and reads one Response
// as JSON+newline. Returns an error if the read or write fails or the
// reply is unparseable.
func SendRequest(conn net.Conn, req Request) (Response, error) {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return Response{}, fmt.Errorf("daemon: write request: %w", err)
	}
	dec := json.NewDecoder(bufio.NewReader(conn))
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("daemon: read response: %w", err)
	}
	return resp, nil
}

// HandleConn reads exactly one Request from conn and dispatches it
// through `dispatch`, then writes the returned Response and closes the
// connection. The dispatcher is supplied by the Daemon (it owns the
// state the commands inspect).
//
// Exported so the Daemon implementation in daemon.go can route accepted
// connections to it; also testable in isolation by passing a synthetic
// dispatcher.
func HandleConn(conn net.Conn, dispatch func(Request) Response) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	dec := json.NewDecoder(bufio.NewReader(conn))
	var req Request
	if err := dec.Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(Response{Ok: false, Msg: "bad request: " + err.Error()})
		return
	}
	resp := dispatch(req)
	_ = json.NewEncoder(conn).Encode(resp)
}
