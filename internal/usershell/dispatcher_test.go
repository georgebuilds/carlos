package usershell

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
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

	// Dispatch carries a spawn-parent so the wake can be routed back to the
	// dispatching agent.
	ctx := agent.WithSpawnParent(context.Background(), "agent-77")
	id, err := d.StartBackground(ctx, "echo done", "")
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	if id == "" {
		t.Fatal("want non-empty job id")
	}
	if !d.IsAgentJob(id) {
		t.Error("started job should be marked agent-owned")
	}
	if owner, ok := d.OwnerOf(id); !ok || owner != "agent-77" {
		t.Errorf("OwnerOf = %q,%v; want agent-77,true", owner, ok)
	}
	if _, ok := d.OwnerOf("some-other-id"); ok {
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

// TestManager_SubscribeCompletions fires exactly once per job, on the
// terminal transition, carrying the final snapshot - the low-volume feed the
// wake path relies on instead of the chatty output Subscribe channel.
func TestManager_SubscribeCompletions(t *testing.T) {
	fr := &fakeRunner{output: "hi\n", exit: 0}
	m := New(Options{Runner: fr, OutputDir: t.TempDir()})
	defer m.Close()

	comps, unsub := m.SubscribeCompletions()
	defer unsub()

	job, err := m.Submit(context.Background(), "echo hi", Background)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case snap := <-comps:
		if snap.ID != job.ID {
			t.Errorf("completion for wrong job: got %s want %s", snap.ID, job.ID)
		}
		if !snap.State.IsTerminal() {
			t.Errorf("completion snapshot not terminal: %s", snap.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no completion published within deadline")
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
