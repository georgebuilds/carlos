package daemon

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestJobsIPC_RoundTrip drives the job verbs over a REAL UDS pair, so the
// new Request/Response job fields (Command, Cwd, Offset, Jobs, Job,
// Output, NextOffset, Done) are verified to survive JSON serialization -
// something the direct-dispatch handler tests don't exercise.
func TestJobsIPC_RoundTrip(t *testing.T) {
	d := newJobsTestDaemon(t)

	dir := t.TempDir()
	sock := filepath.Join(dir, "jobs.sock")
	l, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Server: accept until the listener closes, dispatching each conn
	// through the real daemon dispatch.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, aerr := l.Accept()
			if aerr != nil {
				return
			}
			go HandleConn(conn, d.dispatch)
		}
	}()
	// Close the listener BEFORE waiting on the accept goroutine - it only
	// unblocks from Accept once the listener is closed (defers are LIFO,
	// so this single closure orders close-then-wait correctly).
	defer func() {
		_ = l.Close()
		wg.Wait()
	}()

	roundtrip := func(t *testing.T, req Request) Response {
		t.Helper()
		conn, derr := Dial(sock)
		if derr != nil {
			t.Fatalf("Dial: %v", derr)
		}
		defer conn.Close()
		resp, rerr := SendRequest(conn, req)
		if rerr != nil {
			t.Fatalf("SendRequest(%s): %v", req.Cmd, rerr)
		}
		return resp
	}

	// spawn
	spawn := roundtrip(t, Request{Cmd: "jobs-spawn", Command: "echo wiretest"})
	if !spawn.Ok || spawn.Job == nil || spawn.Job.ID == "" {
		t.Fatalf("jobs-spawn over wire: %+v (msg=%q)", spawn.Job, spawn.Msg)
	}
	id := spawn.Job.ID

	// poll jobs-get over the wire until terminal.
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := roundtrip(t, Request{Cmd: "jobs-get", JobID: id})
		if got.Ok && got.Job != nil && got.Job.State == "done" {
			if got.Job.Command != "echo wiretest" {
				t.Errorf("jobs-get command round-trip: %q", got.Job.Command)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never reached done over the wire")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// list
	list := roundtrip(t, Request{Cmd: "jobs-list"})
	if !list.Ok || len(list.Jobs) != 1 || list.Jobs[0].ID != id {
		t.Fatalf("jobs-list over wire: %+v", list.Jobs)
	}

	// logs (offset 0) - output + Done must survive serialization.
	logs := roundtrip(t, Request{Cmd: "jobs-logs", JobID: id, Offset: 0})
	if !logs.Ok {
		t.Fatalf("jobs-logs over wire: %q", logs.Msg)
	}
	if !logs.Done {
		t.Error("jobs-logs Done should be true for a finished job")
	}
	if logs.NextOffset != len(logs.Output) {
		t.Errorf("NextOffset %d != output len %d", logs.NextOffset, len(logs.Output))
	}
	if want := "wiretest"; !strings.Contains(logs.Output, want) {
		t.Errorf("logs output %q missing %q", logs.Output, want)
	}

	// logs from the end - empty delta, stable offset.
	tail := roundtrip(t, Request{Cmd: "jobs-logs", JobID: id, Offset: logs.NextOffset})
	if tail.Output != "" {
		t.Errorf("tail delta should be empty, got %q", tail.Output)
	}

	// stop (already terminal) - idempotent ok.
	stop := roundtrip(t, Request{Cmd: "jobs-stop", JobID: id})
	if !stop.Ok {
		t.Errorf("jobs-stop over wire: %q", stop.Msg)
	}

	// unknown verb still routes to the default arm.
	if unk := roundtrip(t, Request{Cmd: "jobs-bogus"}); unk.Ok {
		t.Error("unknown job verb should be ok=false")
	}
}
