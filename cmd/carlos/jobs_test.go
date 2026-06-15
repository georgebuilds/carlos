package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJobVerbs_ArgValidation pins the usage errors each verb returns
// before it ever tries to dial the daemon. Arg-count checks come first
// so a typo'd invocation fails fast with a helpful message.
func TestJobVerbs_ArgValidation(t *testing.T) {
	cases := []struct {
		name string
		fn   func([]string) error
		args []string
		want string
	}{
		{"run no args", runJobRun, nil, "usage"},
		{"run too many", runJobRun, []string{"a", "b"}, "usage"},
		{"jobs with args", runJobsList, []string{"x"}, "usage"},
		{"attach no args", runJobAttach, nil, "usage"},
		{"attach too many", runJobAttach, []string{"a", "b"}, "usage"},
		{"logs no args", runJobLogs, nil, "usage"},
		{"stop no args", runJobStop, nil, "usage"},
		{"stop too many", runJobStop, []string{"a", "b"}, "usage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.fn(c.args)
			if err == nil {
				t.Fatalf("%s: expected an error", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: error %q should contain %q", c.name, err, c.want)
			}
		})
	}
}

// TestJobVerbs_NoDaemon checks the verbs that strictly require a live
// daemon surface the "daemon not running" hint when the UDS dial fails.
// isolateHome points CARLOS_DAEMON_SOCKET at a dead path.
func TestJobVerbs_NoDaemon(t *testing.T) {
	isolateHome(t)
	cases := []struct {
		name string
		err  error
	}{
		{"run", runJobRun([]string{"echo hi"})},
		{"jobs", runJobsList(nil)},
		{"attach", runJobAttach([]string{"01ABC"})},
		{"stop", runJobStop([]string{"01ABC"})},
	}
	for _, c := range cases {
		if c.err == nil {
			t.Errorf("%s: expected an error with no daemon running", c.name)
			continue
		}
		if !strings.Contains(c.err.Error(), "daemon not running") {
			t.Errorf("%s: error %q should mention the daemon is not running", c.name, c.err)
		}
	}
}

// TestRunJobLogs_DiskFallback covers the `carlos logs <id>` path when the
// daemon is unreachable: it reads ~/.carlos/jobs/<id>.log directly.
func TestRunJobLogs_DiskFallback(t *testing.T) {
	home := isolateHome(t)
	jobsDir := filepath.Join(home, ".carlos", "jobs")
	if err := os.MkdirAll(jobsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const id = "01HXYZJOBLOGFALLBACK"
	const body = "build output line 1\nbuild output line 2\n"
	if err := os.WriteFile(filepath.Join(jobsDir, id+".log"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := captureStdoutD(t, func() error { return runJobLogs([]string{id}) })
	if err != nil {
		t.Fatalf("runJobLogs: %v", err)
	}
	if out != body {
		t.Errorf("logs output: got %q want %q", out, body)
	}
}

// TestRunJobLogs_MissingOnDisk surfaces a clean error when neither the
// daemon nor the disk has the job.
func TestRunJobLogs_MissingOnDisk(t *testing.T) {
	isolateHome(t)
	err := runJobLogs([]string{"01NOSUCHJOB"})
	if err == nil {
		t.Fatal("expected an error for a job with no on-disk log")
	}
	if !strings.Contains(err.Error(), "no output on disk") {
		t.Errorf("error %q should mention no output on disk", err)
	}
}

// TestPrintJobLogFromDisk_ReadError exercises the non-ENOENT read error
// branch (a directory where a file is expected).
func TestPrintJobLogFromDisk_ReadError(t *testing.T) {
	home := isolateHome(t)
	jobsDir := filepath.Join(home, ".carlos", "jobs")
	const id = "01DIRNOTFILE"
	// Create a directory at the .log path so os.ReadFile returns a
	// non-ErrNotExist error.
	if err := os.MkdirAll(filepath.Join(jobsDir, id+".log"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := printJobLogFromDisk(id)
	if err == nil {
		t.Fatal("expected a read error for a directory-shaped log path")
	}
	if strings.Contains(err.Error(), "no output on disk") {
		t.Errorf("a read error should not be reported as no-output: %v", err)
	}
}
