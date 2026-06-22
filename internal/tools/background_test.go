package tools

import (
	"context"
	"strings"
	"testing"
)

// fakeShell is a deterministic BackgroundShell for tool-level tests.
type fakeShell struct {
	started  []startedJob
	out      []byte
	state    string
	exit     int
	done     bool
	outErr   error
	killErr  error
	killed   []string
	startErr error
	nextID   string
}

type startedJob struct{ cmd, cwd string }

func (f *fakeShell) StartBackground(_ context.Context, cmd, cwd string) (string, error) {
	if f.startErr != nil {
		return "", f.startErr
	}
	f.started = append(f.started, startedJob{cmd, cwd})
	if f.nextID == "" {
		return "job-1", nil
	}
	return f.nextID, nil
}

func (f *fakeShell) Output(string) ([]byte, string, int, bool, error) {
	return f.out, f.state, f.exit, f.done, f.outErr
}

func (f *fakeShell) Kill(id string) error {
	f.killed = append(f.killed, id)
	return f.killErr
}

// TestBash_RunInBackground_Dispatches: with a runner wired, run_in_background
// hands off to StartBackground and returns the id without blocking.
func TestBash_RunInBackground_Dispatches(t *testing.T) {
	fs := &fakeShell{nextID: "abc123"}
	bt := &BashTool{Background: fs, WorkingDir: "/tmp/work"}
	out, err := bt.Execute(context.Background(), []byte(`{"cmd":"sleep 100","run_in_background":true}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(fs.started) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(fs.started))
	}
	if fs.started[0].cmd != "sleep 100" || fs.started[0].cwd != "/tmp/work" {
		t.Errorf("dispatched job wrong: %+v", fs.started[0])
	}
	s := string(out)
	for _, want := range []string{"abc123", "BashOutput", "KillShell"} {
		if !strings.Contains(s, want) {
			t.Errorf("result missing %q: %s", want, s)
		}
	}
}

// TestBash_RunInBackground_NoRunner_FallsBackSync: without a runner the
// command runs synchronously (and actually executes), so the model still
// gets a result instead of a hard failure.
func TestBash_RunInBackground_NoRunner_FallsBackSync(t *testing.T) {
	bt := NewBashTool() // Background is nil
	out, err := bt.Execute(context.Background(), []byte(`{"cmd":"echo hi","run_in_background":true}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "hi") || !strings.Contains(s, "[exit 0]") {
		t.Errorf("expected synchronous output, got: %s", s)
	}
}

// TestBash_StartError surfaces a dispatch failure as an infra error.
func TestBash_StartError(t *testing.T) {
	fs := &fakeShell{startErr: context.Canceled}
	bt := &BashTool{Background: fs}
	if _, err := bt.Execute(context.Background(), []byte(`{"cmd":"x","run_in_background":true}`)); err == nil {
		t.Error("want error when StartBackground fails")
	}
}

func TestBashOutput_RunningAndDone(t *testing.T) {
	fs := &fakeShell{out: []byte("partial output"), state: "running", done: false}
	tool := &BashOutputTool{Shell: fs}
	out, err := tool.Execute(context.Background(), []byte(`{"bash_id":"j1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "running") || !strings.Contains(string(out), "partial output") {
		t.Errorf("running output wrong: %s", out)
	}
	if strings.Contains(string(out), "exit") {
		t.Errorf("running job should not report an exit code: %s", out)
	}

	fs.state, fs.done, fs.exit, fs.out = "failed", true, 2, []byte("boom")
	out, _ = tool.Execute(context.Background(), []byte(`{"bash_id":"j1"}`))
	if !strings.Contains(string(out), "exit 2") || !strings.Contains(string(out), "boom") {
		t.Errorf("done output should carry exit code + output: %s", out)
	}
}

func TestBashOutput_UnknownJob(t *testing.T) {
	fs := &fakeShell{outErr: ErrUnknownBackgroundJob}
	tool := &BashOutputTool{Shell: fs}
	out, err := tool.Execute(context.Background(), []byte(`{"bash_id":"ghost"}`))
	if err != nil {
		t.Fatalf("unknown job should be a value result, not an error: %v", err)
	}
	if !strings.Contains(string(out), "ghost") {
		t.Errorf("want a readable not-found note, got: %s", out)
	}
}

func TestBashOutput_NoRunner(t *testing.T) {
	tool := &BashOutputTool{} // nil Shell
	if _, err := tool.Execute(context.Background(), []byte(`{"bash_id":"x"}`)); err == nil {
		t.Error("want error when no runner wired")
	}
}

func TestKillShell(t *testing.T) {
	fs := &fakeShell{}
	tool := &KillShellTool{Shell: fs}
	if _, err := tool.Execute(context.Background(), []byte(`{"shell_id":"j9"}`)); err != nil {
		t.Fatal(err)
	}
	if len(fs.killed) != 1 || fs.killed[0] != "j9" {
		t.Errorf("kill not forwarded: %+v", fs.killed)
	}
}

func TestKillShell_UnknownJob(t *testing.T) {
	fs := &fakeShell{killErr: ErrUnknownBackgroundJob}
	tool := &KillShellTool{Shell: fs}
	out, err := tool.Execute(context.Background(), []byte(`{"shell_id":"ghost"}`))
	if err != nil {
		t.Fatalf("unknown job should be a value result: %v", err)
	}
	if !strings.Contains(string(out), "ghost") {
		t.Errorf("want readable not-found note: %s", out)
	}
}
