package usershell

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/tools"
)

// waitTerminal polls the dispatcher until the job reports done or the
// deadline passes, so the test doesn't race the background goroutine.
func waitTerminal(t *testing.T, d *BackgroundDispatcher, id string) (state string, exit int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, st, ex, done, err := d.Output(id)
		if err != nil {
			t.Fatalf("Output(%q): %v", id, err)
		}
		if done {
			return st, ex
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %q never reached a terminal state", id)
	return "", 0
}

// TestDispatcher_StartTracksAgentJob: a started job gets a non-empty id,
// is marked agent-owned, runs to completion, and surfaces its exit code.
func TestDispatcher_StartTracksAgentJob(t *testing.T) {
	fr := &fakeRunner{output: "done\n", exit: 0}
	m := New(Options{Runner: fr, OutputDir: t.TempDir()})
	defer m.Close()
	d := NewBackgroundDispatcher(m)

	id, err := d.StartBackground(context.Background(), "echo done", "")
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	if id == "" {
		t.Fatal("want non-empty job id")
	}
	if !d.IsAgentJob(id) {
		t.Error("started job should be marked agent-owned")
	}
	if d.IsAgentJob("some-other-id") {
		t.Error("unrelated id must not be agent-owned")
	}

	state, exit := waitTerminal(t, d, id)
	if state != "done" || exit != 0 {
		t.Errorf("want done/0, got %s/%d", state, exit)
	}
}

// TestDispatcher_NonZeroExit maps a failing command to a failed/exit-N
// status the BashOutput tool can render.
func TestDispatcher_NonZeroExit(t *testing.T) {
	fr := &fakeRunner{output: "boom\n", exit: 3}
	m := New(Options{Runner: fr, OutputDir: t.TempDir()})
	defer m.Close()
	d := NewBackgroundDispatcher(m)

	id, err := d.StartBackground(context.Background(), "false", "")
	if err != nil {
		t.Fatal(err)
	}
	state, exit := waitTerminal(t, d, id)
	if state != "failed" || exit != 3 {
		t.Errorf("want failed/3, got %s/%d", state, exit)
	}
}

// TestDispatcher_UnknownJobMapping: Output and Kill on an unknown id must
// surface tools.ErrUnknownBackgroundJob so the tools render a clean note
// rather than a generic failure.
func TestDispatcher_UnknownJobMapping(t *testing.T) {
	m := New(Options{Runner: &fakeRunner{}, OutputDir: t.TempDir()})
	defer m.Close()
	d := NewBackgroundDispatcher(m)

	if _, _, _, _, err := d.Output("ghost"); !errors.Is(err, tools.ErrUnknownBackgroundJob) {
		t.Errorf("Output unknown: want ErrUnknownBackgroundJob, got %v", err)
	}
	if err := d.Kill("ghost"); !errors.Is(err, tools.ErrUnknownBackgroundJob) {
		t.Errorf("Kill unknown: want ErrUnknownBackgroundJob, got %v", err)
	}
}
