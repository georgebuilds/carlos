package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newJobsTestDaemon builds a Daemon with its job runtime wired against
// tempdir paths and started. Returns the daemon; the caller defers
// stopJobsRuntime. ConfigPath is a dummy (New requires it non-empty but
// the job runtime never reads it).
func newJobsTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()
	d, err := New(Options{
		ConfigPath: filepath.Join(dir, "config.yaml"),
		JobsDir:    filepath.Join(dir, "jobs"),
		JobsDBPath: filepath.Join(dir, "jobs.db"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.startJobsRuntime()
	t.Cleanup(d.stopJobsRuntime)
	return d
}

// waitForJobState polls jobs-get until the job reaches the wanted state
// (or the deadline elapses). Background jobs run async, so handler tests
// can't assert terminal state synchronously off the spawn response.
func waitForJobState(t *testing.T, d *Daemon, jobID, want string) JobStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last := "<none>"
	for time.Now().Before(deadline) {
		resp := d.jobsGetResponse(jobID)
		if resp.Ok && resp.Job != nil {
			last = resp.Job.State
			if resp.Job.State == want {
				return *resp.Job
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	logs := d.jobsLogsResponse(jobID, 0)
	t.Fatalf("job %s never reached state %q (last=%q, output=%q)", jobID, want, last, logs.Output)
	return JobStatus{}
}

func TestJobsSpawnRunsToCompletion(t *testing.T) {
	d := newJobsTestDaemon(t)
	resp := d.jobsSpawnResponse(Request{Cmd: "jobs-spawn", Command: "echo hello-jobs"})
	if !resp.Ok {
		t.Fatalf("spawn: ok=false msg=%q", resp.Msg)
	}
	if resp.Job == nil || resp.Job.ID == "" {
		t.Fatalf("spawn: expected a Job with an id, got %+v", resp.Job)
	}
	if resp.Job.State != "running" && resp.Job.State != "pending" {
		t.Fatalf("spawn: fresh job should be pending/running, got %q", resp.Job.State)
	}
	done := waitForJobState(t, d, resp.Job.ID, "done")
	if done.ExitCode != 0 {
		t.Errorf("exit code: got %d want 0", done.ExitCode)
	}

	logs := d.jobsLogsResponse(resp.Job.ID, 0)
	if !logs.Ok {
		t.Fatalf("logs: ok=false msg=%q", logs.Msg)
	}
	if !strings.Contains(logs.Output, "hello-jobs") {
		t.Errorf("logs output %q does not contain command output", logs.Output)
	}
	if !logs.Done {
		t.Error("logs: Done should be true for a terminal job")
	}
	if logs.NextOffset != len(logs.Output) {
		t.Errorf("NextOffset %d != output len %d", logs.NextOffset, len(logs.Output))
	}
}

func TestJobsLogsOffsetReturnsDelta(t *testing.T) {
	d := newJobsTestDaemon(t)
	resp := d.jobsSpawnResponse(Request{Command: "echo abcdef"})
	if !resp.Ok {
		t.Fatalf("spawn: %q", resp.Msg)
	}
	waitForJobState(t, d, resp.Job.ID, "done")

	full := d.jobsLogsResponse(resp.Job.ID, 0)
	if len(full.Output) == 0 {
		t.Fatal("expected non-empty output")
	}
	// Polling from the end returns no new bytes but a stable offset.
	tail := d.jobsLogsResponse(resp.Job.ID, full.NextOffset)
	if tail.Output != "" {
		t.Errorf("tail poll should be empty, got %q", tail.Output)
	}
	if tail.NextOffset != full.NextOffset {
		t.Errorf("tail NextOffset %d != %d", tail.NextOffset, full.NextOffset)
	}
	// An over-large offset clamps rather than panicking.
	over := d.jobsLogsResponse(resp.Job.ID, full.NextOffset+9999)
	if over.Output != "" || over.NextOffset != full.NextOffset {
		t.Errorf("over-offset poll: got output=%q next=%d", over.Output, over.NextOffset)
	}
	// A negative offset is treated as 0 (full output).
	neg := d.jobsLogsResponse(resp.Job.ID, -5)
	if neg.Output != full.Output {
		t.Errorf("negative offset should return full output")
	}
}

func TestJobsStopCancelsRunningJob(t *testing.T) {
	d := newJobsTestDaemon(t)
	resp := d.jobsSpawnResponse(Request{Command: "sleep 30"})
	if !resp.Ok {
		t.Fatalf("spawn: %q", resp.Msg)
	}
	waitForJobState(t, d, resp.Job.ID, "running")

	stop := d.jobsStopResponse(resp.Job.ID)
	if !stop.Ok {
		t.Fatalf("stop: ok=false msg=%q", stop.Msg)
	}
	got := waitForJobState(t, d, resp.Job.ID, "cancelled")
	if got.State != "cancelled" {
		t.Errorf("state: got %q want cancelled", got.State)
	}
}

func TestJobsListIncludesSpawnedJobs(t *testing.T) {
	d := newJobsTestDaemon(t)
	a := d.jobsSpawnResponse(Request{Command: "echo one"})
	b := d.jobsSpawnResponse(Request{Command: "echo two"})
	if !a.Ok || !b.Ok {
		t.Fatalf("spawn failed: %q / %q", a.Msg, b.Msg)
	}
	list := d.jobsListResponse()
	if !list.Ok {
		t.Fatalf("list: %q", list.Msg)
	}
	if len(list.Jobs) != 2 {
		t.Fatalf("list: got %d jobs want 2", len(list.Jobs))
	}
	ids := map[string]bool{}
	for _, j := range list.Jobs {
		ids[j.ID] = true
	}
	if !ids[a.Job.ID] || !ids[b.Job.ID] {
		t.Errorf("list missing a spawned job: %+v", list.Jobs)
	}
}

func TestJobsHandlersRejectBadInput(t *testing.T) {
	d := newJobsTestDaemon(t)
	cases := []struct {
		name string
		resp Response
	}{
		{"spawn empty command", d.jobsSpawnResponse(Request{Command: "  "})},
		{"get empty id", d.jobsGetResponse("")},
		{"get unknown id", d.jobsGetResponse("nope")},
		{"logs empty id", d.jobsLogsResponse("", 0)},
		{"logs unknown id", d.jobsLogsResponse("nope", 0)},
		{"stop empty id", d.jobsStopResponse("")},
		{"stop unknown id", d.jobsStopResponse("nope")},
	}
	for _, c := range cases {
		if c.resp.Ok {
			t.Errorf("%s: expected ok=false, got ok=true (%q)", c.name, c.resp.Msg)
		}
		if c.resp.Msg == "" {
			t.Errorf("%s: expected a non-empty error message", c.name)
		}
	}
}

func TestJobsHandlersWithoutRuntime(t *testing.T) {
	// A daemon that never called startJobsRuntime (test-mode / pre-Run)
	// must refuse every job verb cleanly rather than nil-panic.
	d, err := New(Options{ConfigPath: "x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	responses := []Response{
		d.jobsSpawnResponse(Request{Command: "echo hi"}),
		d.jobsListResponse(),
		d.jobsGetResponse("id"),
		d.jobsLogsResponse("id", 0),
		d.jobsStopResponse("id"),
	}
	for i, r := range responses {
		if r.Ok {
			t.Errorf("response %d: expected ok=false when runtime is nil", i)
		}
		if !strings.Contains(r.Msg, "not available") {
			t.Errorf("response %d: msg %q should mention the runtime is unavailable", i, r.Msg)
		}
	}
}

func TestJobsSpawnUsesRequestCwd(t *testing.T) {
	d := newJobsTestDaemon(t)
	cwd := t.TempDir()
	resp := d.jobsSpawnResponse(Request{Command: "pwd", Cwd: cwd})
	if !resp.Ok {
		t.Fatalf("spawn: %q", resp.Msg)
	}
	waitForJobState(t, d, resp.Job.ID, "done")
	if resp.Job.Cwd != cwd {
		t.Errorf("job cwd: got %q want %q", resp.Job.Cwd, cwd)
	}
	logs := d.jobsLogsResponse(resp.Job.ID, 0)
	// macOS /var symlinks to /private/var; match on the trailing path.
	if !strings.Contains(logs.Output, filepath.Base(cwd)) {
		t.Errorf("pwd output %q should contain the cwd basename %q", logs.Output, filepath.Base(cwd))
	}
}

func TestDispatchRoutesJobVerbs(t *testing.T) {
	d := newJobsTestDaemon(t)
	// jobs-list through the real dispatch switch.
	resp := d.dispatch(Request{Cmd: "jobs-list"})
	if !resp.Ok {
		t.Fatalf("dispatch jobs-list: %q", resp.Msg)
	}
	spawn := d.dispatch(Request{Cmd: "jobs-spawn", Command: "echo viadispatch"})
	if !spawn.Ok || spawn.Job == nil {
		t.Fatalf("dispatch jobs-spawn: %q", spawn.Msg)
	}
	waitForJobState(t, d, spawn.Job.ID, "done")
	if got := d.dispatch(Request{Cmd: "jobs-get", JobID: spawn.Job.ID}); !got.Ok {
		t.Errorf("dispatch jobs-get: %q", got.Msg)
	}
	if got := d.dispatch(Request{Cmd: "jobs-logs", JobID: spawn.Job.ID}); !got.Ok {
		t.Errorf("dispatch jobs-logs: %q", got.Msg)
	}
	if got := d.dispatch(Request{Cmd: "jobs-stop", JobID: spawn.Job.ID}); !got.Ok {
		t.Errorf("dispatch jobs-stop: %q", got.Msg)
	}
}

func TestJobsPathResolution(t *testing.T) {
	// Explicit options win.
	d := &Daemon{opts: Options{JobsDir: "/tmp/x/jobs", JobsDBPath: "/tmp/x/jobs.db"}}
	if got := d.jobsDir(); got != "/tmp/x/jobs" {
		t.Errorf("jobsDir override: got %q", got)
	}
	if got := d.jobsDBPath(); got != "/tmp/x/jobs.db" {
		t.Errorf("jobsDBPath override: got %q", got)
	}
	// Home-derived defaults.
	d2 := &Daemon{opts: Options{Home: "/home/u"}}
	if got := d2.jobsDir(); got != filepath.Join("/home/u", ".carlos", "jobs") {
		t.Errorf("jobsDir from Home: got %q", got)
	}
	if got := d2.jobsDBPath(); got != filepath.Join("/home/u", ".carlos", "jobs.db") {
		t.Errorf("jobsDBPath from Home: got %q", got)
	}
}

func TestJobsPathResolutionFallsBackToUserHome(t *testing.T) {
	// Neither JobsDir nor Home set → both helpers fall through to
	// os.UserHomeDir (which resolves on any real test host).
	d := &Daemon{}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no resolvable home on this host")
	}
	if got, want := d.jobsDir(), filepath.Join(home, ".carlos", "jobs"); got != want {
		t.Errorf("jobsDir fallback: got %q want %q", got, want)
	}
	if got, want := d.jobsDBPath(), filepath.Join(home, ".carlos", "jobs.db"); got != want {
		t.Errorf("jobsDBPath fallback: got %q want %q", got, want)
	}
}

func TestStartJobsRuntimeDegradesWhenPersistenceUnavailable(t *testing.T) {
	// mkdir-parent failure: point jobs.db under a path whose parent is a
	// regular file, so MkdirAll(filepath.Dir(dbPath)) fails. The runtime
	// must still come up (in-memory), not abort.
	dir := t.TempDir()
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	d1, err := New(Options{
		ConfigPath: filepath.Join(dir, "c.yaml"),
		JobsDir:    filepath.Join(dir, "jobs"),
		JobsDBPath: filepath.Join(notADir, "sub", "jobs.db"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d1.startJobsRuntime()
	defer d1.stopJobsRuntime()
	if d1.jobsManager() == nil {
		t.Fatal("runtime should be up despite mkdir failure")
	}
	if d1.jobsLog != nil {
		t.Error("jobsLog should be nil when the parent mkdir failed")
	}
	// Jobs still run without persistence.
	if resp := d1.jobsSpawnResponse(Request{Command: "echo nopersist"}); !resp.Ok {
		t.Errorf("spawn without persistence: %q", resp.Msg)
	}

	// open failure: JobsDBPath is an existing directory, so OpenStateDB
	// fails opening it as a file. Runtime still degrades cleanly.
	d2dir := t.TempDir()
	dbAsDir := filepath.Join(d2dir, "jobs.db")
	if err := os.MkdirAll(dbAsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	d2, err := New(Options{
		ConfigPath: filepath.Join(d2dir, "c.yaml"),
		JobsDir:    filepath.Join(d2dir, "jobs"),
		JobsDBPath: dbAsDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d2.startJobsRuntime()
	defer d2.stopJobsRuntime()
	if d2.jobsManager() == nil {
		t.Fatal("runtime should be up despite jobs.db open failure")
	}
	if d2.jobsLog != nil {
		t.Error("jobsLog should be nil when OpenStateDB failed")
	}
}

func TestJobsPathDerivesFromStateDB(t *testing.T) {
	// With no explicit jobs paths, the runtime co-locates with state.db's
	// directory. Production points StateDBPath at ~/.carlos/state.db (so
	// this stays ~/.carlos), but a test pointing it at a tempdir gets an
	// isolated jobs store instead of the user's real ~/.carlos/jobs.db -
	// the isolation that keeps parallel daemon lifecycle tests off one
	// shared SQLite file (the CI busy-timeout flake this guards against).
	dir := t.TempDir()
	d := &Daemon{opts: Options{StateDBPath: filepath.Join(dir, "state.db")}}
	if got, want := d.jobsDir(), filepath.Join(dir, "jobs"); got != want {
		t.Errorf("jobsDir from StateDBPath: got %q want %q", got, want)
	}
	if got, want := d.jobsDBPath(), filepath.Join(dir, "jobs.db"); got != want {
		t.Errorf("jobsDBPath from StateDBPath: got %q want %q", got, want)
	}
	// An explicit JobsDBPath still wins over the StateDBPath derivation.
	d2 := &Daemon{opts: Options{StateDBPath: filepath.Join(dir, "state.db"), JobsDBPath: "/x/jobs.db"}}
	if got := d2.jobsDBPath(); got != "/x/jobs.db" {
		t.Errorf("explicit JobsDBPath should win: got %q", got)
	}
}

func TestJobStatusFromSnapshotZeroTimes(t *testing.T) {
	// A pending job has zero StartedAt/EndedAt → nil pointers, zero
	// duration → omitted.
	d := newJobsTestDaemon(t)
	resp := d.jobsSpawnResponse(Request{Command: "echo hi"})
	if !resp.Ok {
		t.Fatalf("spawn: %q", resp.Msg)
	}
	// Immediately after spawn the job may still be pending; assert the
	// projection never emits a non-nil pointer to a zero time.
	js := resp.Job
	if js.StartedAt != nil && js.StartedAt.IsZero() {
		t.Error("StartedAt pointer should be nil for a zero time, not a pointer-to-zero")
	}
	if js.EndedAt != nil && js.EndedAt.IsZero() {
		t.Error("EndedAt pointer should be nil for a zero time")
	}
	if js.SubmittedAt.IsZero() {
		t.Error("SubmittedAt should always be set")
	}
}
