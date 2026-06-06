package usershell

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNew_Defaults(t *testing.T) {
	m := New(Options{})
	if m.bgLimit != DefaultBackgroundParallelism {
		t.Errorf("bgLimit default: want %d got %d", DefaultBackgroundParallelism, m.bgLimit)
	}
	if m.now == nil {
		t.Error("now should default to time.Now")
	}
}

func TestNew_BackgroundParallelismOverride(t *testing.T) {
	m := New(Options{BackgroundParallelism: 7})
	if m.bgLimit != 7 {
		t.Errorf("bgLimit override: want 7 got %d", m.bgLimit)
	}
	m = New(Options{BackgroundParallelism: -1})
	if m.bgLimit != DefaultBackgroundParallelism {
		t.Errorf("negative parallelism should fall back to default; got %d", m.bgLimit)
	}
}

func TestNew_ClockOverride(t *testing.T) {
	fixed := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	m := New(Options{Now: func() time.Time { return fixed }})
	id, err := m.newJobID()
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Error("newJobID returned empty")
	}
}

// TestSubmit_ReturnsJobAndErrNotImplemented exercises the S0
// contract: Submit lands the Job in the registry but the spawn
// path is intentionally stubbed.
func TestSubmit_ReturnsJobAndErrNotImplemented(t *testing.T) {
	m := New(Options{Cwd: "/tmp"})
	defer m.Close()
	job, err := m.Submit(context.Background(), "echo hi", Foreground)
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Submit should return ErrNotImplemented in S0; got %v", err)
	}
	if job == nil {
		t.Fatal("Submit should still return the Job")
	}
	if job.Command != "echo hi" {
		t.Errorf("Job command: %q", job.Command)
	}
	if job.Cwd != "/tmp" {
		t.Errorf("Job cwd: %q", job.Cwd)
	}
	if job.State() != StatePending {
		t.Errorf("Initial state: %v", job.State())
	}
}

func TestSubmit_EmptyCommandErrors(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	if _, err := m.Submit(context.Background(), "   ", Foreground); err == nil {
		t.Error("expected error on empty command")
	}
}

func TestSubmit_TrimsWhitespace(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "  ls -la  \n", Foreground)
	if job.Command != "ls -la" {
		t.Errorf("trim failed: %q", job.Command)
	}
}

func TestSubmit_AfterCloseErrors(t *testing.T) {
	m := New(Options{})
	_ = m.Close()
	if _, err := m.Submit(context.Background(), "echo", Foreground); !errors.Is(err, ErrClosed) {
		t.Errorf("submit-after-close: want ErrClosed, got %v", err)
	}
}

func TestSubmit_QueuesForegroundJobs(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	for range 3 {
		_, _ = m.Submit(context.Background(), "echo", Foreground)
	}
	if len(m.fgQueue) != 3 {
		t.Errorf("fgQueue should hold 3, got %d", len(m.fgQueue))
	}
}

func TestCancel_UnknownJob(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	if err := m.Cancel("nope"); !errors.Is(err, ErrUnknownJob) {
		t.Errorf("want ErrUnknownJob, got %v", err)
	}
}

func TestCancel_PendingJob_TransitionsToCancelled(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "sleep 60", Foreground)
	if err := m.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	if job.State() != StateCancelled {
		t.Errorf("pending cancel: want Cancelled, got %v", job.State())
	}
	// Job removed from queue.
	for _, qid := range m.fgQueue {
		if qid == job.ID {
			t.Error("cancelled job still in queue")
		}
	}
}

func TestCancel_Idempotent(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "echo", Foreground)
	if err := m.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(job.ID); err != nil {
		t.Errorf("second cancel should be a no-op; got %v", err)
	}
}

func TestBackground_NotRunning(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "echo", Foreground)
	if err := m.Background(job.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Background of pending job: want ErrInvalidTransition, got %v", err)
	}
}

func TestBackground_MovesRunningJob(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "echo", Foreground)
	// Simulate the manager promoting it to the fgRunning slot — S2
	// will do this automatically; for S0 we drive it manually.
	m.fgRunning = job.ID
	_ = job.transition(StateRunning)
	if err := m.Background(job.ID); err != nil {
		t.Fatal(err)
	}
	if m.fgRunning != "" {
		t.Errorf("fg slot should be empty after bg; got %q", m.fgRunning)
	}
	if _, ok := m.bgRunning[job.ID]; !ok {
		t.Error("job missing from bg pool")
	}
	if !job.Backgrounded {
		t.Error("Backgrounded flag not set")
	}
}

func TestBackground_PoolFull(t *testing.T) {
	m := New(Options{BackgroundParallelism: 1})
	defer m.Close()
	// Fill bg pool with a synthetic running job.
	other, _ := m.Submit(context.Background(), "tail -f /tmp/x", Background)
	_ = other.transition(StateRunning)
	_ = other.markBackgrounded(true)
	m.bgRunning[other.ID] = struct{}{}

	job, _ := m.Submit(context.Background(), "echo", Foreground)
	m.fgRunning = job.ID
	_ = job.transition(StateRunning)
	if err := m.Background(job.ID); err == nil {
		t.Error("expected error when bg pool full")
	}
}

func TestForeground_NotInBackground(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "echo", Foreground)
	if err := m.Foreground(job.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Foreground of non-bg job: want ErrInvalidTransition, got %v", err)
	}
}

func TestForeground_SwapsIncumbent(t *testing.T) {
	m := New(Options{BackgroundParallelism: 2})
	defer m.Close()

	bg, _ := m.Submit(context.Background(), "tail", Background)
	_ = bg.transition(StateRunning)
	_ = bg.markBackgrounded(true)
	m.bgRunning[bg.ID] = struct{}{}

	fg, _ := m.Submit(context.Background(), "vim", Foreground)
	_ = fg.transition(StateRunning)
	m.fgRunning = fg.ID

	if err := m.Foreground(bg.ID); err != nil {
		t.Fatal(err)
	}
	if m.fgRunning != bg.ID {
		t.Errorf("fg should now be bg; got %q", m.fgRunning)
	}
	if _, demoted := m.bgRunning[fg.ID]; !demoted {
		t.Error("incumbent should have been demoted to bg")
	}
	if _, stillBg := m.bgRunning[bg.ID]; stillBg {
		t.Error("promoted job should have left bg pool")
	}
	if bg.Backgrounded {
		t.Error("promoted job should have Backgrounded=false")
	}
}

func TestJobs_StableOrder(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	// Queue some foreground jobs.
	a, _ := m.Submit(context.Background(), "a", Foreground)
	b, _ := m.Submit(context.Background(), "b", Foreground)
	c, _ := m.Submit(context.Background(), "c", Foreground)
	got := m.Jobs()
	if len(got) != 3 {
		t.Fatalf("Jobs(): want 3, got %d", len(got))
	}
	wantOrder := []string{a.ID, b.ID, c.ID}
	for i, g := range got {
		if g.ID != wantOrder[i] {
			t.Errorf("Jobs()[%d]: want %q got %q", i, wantOrder[i], g.ID)
		}
	}
}

func TestGet_UnknownJob(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	if _, err := m.Get("nope"); !errors.Is(err, ErrUnknownJob) {
		t.Errorf("Get unknown: %v", err)
	}
}

func TestClose_Idempotent(t *testing.T) {
	m := New(Options{})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestClose_CancelsRunningJobs(t *testing.T) {
	m := New(Options{})
	job, _ := m.Submit(context.Background(), "echo", Foreground)
	// Promote to running so Close has something to cancel.
	_ = job.transition(StateRunning)
	cancelled := make(chan struct{})
	// Hand out a fresh cancel that signals when called.
	_, c := context.WithCancel(context.Background())
	job.cancel = func() {
		c()
		close(cancelled)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Error("Close did not cancel running job")
	}
}

func TestTrimCommand(t *testing.T) {
	cases := map[string]string{
		"echo":         "echo",
		"  echo  ":     "echo",
		"\n\t echo \r": "echo",
		"":             "",
		"   ":          "",
	}
	for in, want := range cases {
		if got := trimCommand(in); got != want {
			t.Errorf("trimCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewJobID_UniqueAndSortable(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	seen := map[string]bool{}
	prev := ""
	for range 100 {
		id, err := m.newJobID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate ULID: %s", id)
		}
		seen[id] = true
		if prev != "" && id <= prev {
			t.Errorf("ULIDs not monotonically increasing: %s ≤ %s", id, prev)
		}
		prev = id
	}
}
