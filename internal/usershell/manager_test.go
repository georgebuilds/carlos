package usershell

import (
	"context"
	"errors"
	"sync"
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

// TestSubmit_RunsViaInjectedRunner verifies the happy-path: a
// fake runner produces deterministic output + exit code, the
// Manager promotes the job to running, captures output into the
// ring buffer, and transitions to Done.
func TestSubmit_RunsViaInjectedRunner(t *testing.T) {
	fr := &fakeRunner{output: "hello\n", exit: 0}
	m := New(Options{Cwd: "/tmp", Runner: fr})
	defer m.Close()
	job, err := m.Submit(context.Background(), "echo hi", Foreground)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		t.Fatal("Submit returned nil job")
	}
	if job.Command != "echo hi" || job.Cwd != "/tmp" {
		t.Errorf("Job fields: %+v", job)
	}
	if err := waitForState(t, m, job.ID, StateDone, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := string(m.Output(job.ID)); got != "hello\n" {
		t.Errorf("captured output: want %q got %q", "hello\n", got)
	}
	if snap, _ := m.Get(job.ID); snap.ExitCode != 0 {
		t.Errorf("exit code: %d", snap.ExitCode)
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
	br := newBlockingRunner("", 0)
	m := New(Options{Runner: br})
	defer m.Close()
	// Submit 3 fg jobs. First runs (blocked); other two queue.
	a, _ := m.Submit(context.Background(), "a", Foreground)
	b, _ := m.Submit(context.Background(), "b", Foreground)
	c, _ := m.Submit(context.Background(), "c", Foreground)
	if err := waitForState(t, m, a.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(m.fgQueue) != 2 {
		t.Errorf("fgQueue should hold the 2 waiting jobs, got %d", len(m.fgQueue))
	}
	// b and c should still be pending.
	if b.State() != StatePending || c.State() != StatePending {
		t.Errorf("b=%v c=%v; want both pending", b.State(), c.State())
	}
	br.release()
}

func TestCancel_UnknownJob(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	if err := m.Cancel("nope"); !errors.Is(err, ErrUnknownJob) {
		t.Errorf("want ErrUnknownJob, got %v", err)
	}
}

func TestCancel_PendingJob_TransitionsToCancelled(t *testing.T) {
	br := newBlockingRunner("", 0)
	m := New(Options{Runner: br})
	defer m.Close()
	// Job 1 runs and blocks; job 2 is the pending one we'll cancel.
	a, _ := m.Submit(context.Background(), "head", Foreground)
	pending, _ := m.Submit(context.Background(), "sleep 60", Foreground)
	if err := waitForState(t, m, a.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	if pending.State() != StatePending {
		t.Fatalf("pending job state: %v", pending.State())
	}
	if err := m.Cancel(pending.ID); err != nil {
		t.Fatal(err)
	}
	if pending.State() != StateCancelled {
		t.Errorf("pending cancel: want Cancelled, got %v", pending.State())
	}
	for _, qid := range m.fgQueue {
		if qid == pending.ID {
			t.Error("cancelled job still in queue")
		}
	}
	br.release()
}

func TestCancel_Idempotent(t *testing.T) {
	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fr})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "echo", Foreground)
	// Wait for natural completion, then double-cancel is a no-op.
	_ = waitForState(t, m, job.ID, StateDone, time.Second)
	if err := m.Cancel(job.ID); err != nil {
		t.Errorf("cancel of done: %v", err)
	}
	if err := m.Cancel(job.ID); err != nil {
		t.Errorf("second cancel: %v", err)
	}
}

func TestBackground_NotRunning(t *testing.T) {
	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fr})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "echo", Foreground)
	_ = waitForState(t, m, job.ID, StateDone, time.Second)
	// Job has completed; not the fg runner anymore.
	if err := m.Background(job.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Background of completed job: want ErrInvalidTransition, got %v", err)
	}
}

func TestBackground_MovesRunningJob(t *testing.T) {
	br := newBlockingRunner("", 0)
	m := New(Options{Runner: br})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "echo", Foreground)
	if err := waitForState(t, m, job.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
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
	br.release()
}

func TestBackground_PoolFull(t *testing.T) {
	br := newBlockingRunner("", 0)
	m := New(Options{BackgroundParallelism: 1, Runner: br})
	defer m.Close()
	// Fill bg pool: submit one bg job, wait until it's running.
	other, _ := m.Submit(context.Background(), "tail", Background)
	if err := waitForState(t, m, other.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	// Submit fg job; running blocked.
	fg, _ := m.Submit(context.Background(), "echo", Foreground)
	if err := waitForState(t, m, fg.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := m.Background(fg.ID); err == nil {
		t.Error("expected error when bg pool full")
	}
	br.release()
}

func TestForeground_NotInBackground(t *testing.T) {
	br := newBlockingRunner("", 0)
	m := New(Options{Runner: br})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "echo", Foreground)
	if err := waitForState(t, m, job.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	// Job is fg-running, not bg.
	if err := m.Foreground(job.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Foreground of non-bg job: want ErrInvalidTransition, got %v", err)
	}
	br.release()
}

func TestForeground_SwapsIncumbent(t *testing.T) {
	br := newBlockingRunner("", 0)
	m := New(Options{BackgroundParallelism: 2, Runner: br})
	defer m.Close()
	bg, _ := m.Submit(context.Background(), "tail", Background)
	fg, _ := m.Submit(context.Background(), "vim", Foreground)
	if err := waitForState(t, m, bg.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitForState(t, m, fg.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
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
	br.release()
}

func TestJobs_StableOrder(t *testing.T) {
	br := newBlockingRunner("", 0)
	m := New(Options{Runner: br})
	defer m.Close()
	a, _ := m.Submit(context.Background(), "a", Foreground)
	b, _ := m.Submit(context.Background(), "b", Foreground)
	c, _ := m.Submit(context.Background(), "c", Foreground)
	// a runs (blocked), b + c queue.
	_ = waitForState(t, m, a.ID, StateRunning, time.Second)
	got := m.Jobs()
	if len(got) != 3 {
		t.Fatalf("Jobs(): want 3, got %d", len(got))
	}
	wantOrder := []string{b.ID, c.ID, a.ID}
	// Jobs() emits queued first, then fgRunning. a is running, so
	// it appears AFTER the queued entries. Validate accordingly.
	for i, g := range got {
		if g.ID != wantOrder[i] {
			t.Errorf("Jobs()[%d]: want %q got %q", i, wantOrder[i], g.ID)
		}
	}
	br.release()
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
	br := newBlockingRunner("", 0)
	m := New(Options{Runner: br})
	job, _ := m.Submit(context.Background(), "sleep-forever", Foreground)
	if err := waitForState(t, m, job.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	// Close cancels the per-job ctx; the fake runner's wait sees
	// the kill signal and returns "killed", which we map to
	// StateCancelled.
	if err := waitForState(t, m, job.ID, StateCancelled, time.Second); err != nil {
		t.Errorf("Close did not cancel running job: %v", err)
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

func TestManager_FailedRunnerStart(t *testing.T) {
	fr := &fakeRunner{startErr: errors.New("shell missing")}
	m := New(Options{Runner: fr})
	defer m.Close()
	job, err := m.Submit(context.Background(), "echo", Foreground)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForState(t, m, job.ID, StateFailed, time.Second); err != nil {
		t.Fatal(err)
	}
	snap, _ := m.Get(job.ID)
	if snap.FailErrMsg == "" {
		t.Error("expected FailErrMsg set on spawn failure")
	}
}

func TestManager_NonZeroExitMapsToFailed(t *testing.T) {
	fr := &fakeRunner{exit: 1}
	m := New(Options{Runner: fr})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "false", Foreground)
	if err := waitForState(t, m, job.ID, StateFailed, time.Second); err != nil {
		t.Fatal(err)
	}
	snap, _ := m.Get(job.ID)
	if snap.ExitCode != 1 {
		t.Errorf("exit code: got %d", snap.ExitCode)
	}
}

func TestManager_QueueDrainsAfterEnd(t *testing.T) {
	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fr})
	defer m.Close()
	a, _ := m.Submit(context.Background(), "a", Foreground)
	b, _ := m.Submit(context.Background(), "b", Foreground)
	c, _ := m.Submit(context.Background(), "c", Foreground)
	for _, j := range []*Job{a, b, c} {
		if err := waitForState(t, m, j.ID, StateDone, 2*time.Second); err != nil {
			t.Fatalf("job %s: %v", j.ID, err)
		}
	}
	if fr.startsRun != 3 {
		t.Errorf("runner Start should have been called 3 times; got %d", fr.startsRun)
	}
}

func TestManager_BackgroundParallelism(t *testing.T) {
	br := newBlockingRunner("", 0)
	m := New(Options{BackgroundParallelism: 2, Runner: br})
	defer m.Close()
	jobs := make([]*Job, 4)
	for i := range jobs {
		j, _ := m.Submit(context.Background(), "bg", Background)
		jobs[i] = j
	}
	// First two should be running; last two queued.
	for i, j := range jobs[:2] {
		if err := waitForState(t, m, j.ID, StateRunning, time.Second); err != nil {
			t.Errorf("bg[%d] not running: %v", i, err)
		}
	}
	// Give the queued ones a beat — they should NOT have started.
	time.Sleep(20 * time.Millisecond)
	for i, j := range jobs[2:] {
		if j.State() != StatePending {
			t.Errorf("bg[%d] should still be pending; got %v", i+2, j.State())
		}
	}
	br.release()
}

func TestManager_Subscribe_DeliversStateUpdates(t *testing.T) {
	br := newBlockingRunner("", 0)
	m := New(Options{Runner: br})
	defer m.Close()
	ch, unsub := m.Subscribe()
	defer unsub()
	job, _ := m.Submit(context.Background(), "x", Foreground)
	gotRunning := false
	deadline := time.After(time.Second)
loop:
	for {
		select {
		case u := <-ch:
			if u.JobID == job.ID && u.State == StateRunning {
				gotRunning = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if !gotRunning {
		t.Error("never received running-state Update")
	}
	br.release()
}

func TestManager_Subscribe_DeliversOutputChunks(t *testing.T) {
	fr := &fakeRunner{output: "stream-me", exit: 0}
	m := New(Options{Runner: fr})
	defer m.Close()
	ch, unsub := m.Subscribe()
	defer unsub()
	job, _ := m.Submit(context.Background(), "x", Foreground)
	got := ""
	deadline := time.After(time.Second)
loop:
	for {
		select {
		case u := <-ch:
			if u.JobID == job.ID && len(u.Output) > 0 {
				got += string(u.Output)
				if got == "stream-me" {
					break loop
				}
			}
		case <-deadline:
			break loop
		}
	}
	if got != "stream-me" {
		t.Errorf("captured chunks: want stream-me got %q", got)
	}
}

func TestManager_Output_Empty(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	if got := m.Output("no-such"); got != nil {
		t.Errorf("unknown id should return nil; got %v", got)
	}
}

// TestSubmit_ConcurrentSubmitsRaceFree pounds Submit from many
// goroutines simultaneously, mixing queued and promoted jobs across
// both modes. The regression target is the data-race window in
// Submit: pre-fix, Submit took the lock to mint a job + store a
// CancelFunc, then dropped it; the goroutine reading job.cancel in
// Cancel() could race with startLocked overwriting it. We exercise
// the concurrent-Submit + concurrent-Cancel surface under `-race`
// so the detector flags the read/write conflict.
//
// We also assert Submit returns a usable Job for every call (no
// silent drops) and the Manager eventually finalizes them so the
// queue + runJob pathways drain cleanly.
func TestSubmit_ConcurrentSubmitsRaceFree(t *testing.T) {
	const submits = 200

	// Use a runner that completes instantly so jobs flow through
	// the full Submit → startLocked → runJob → finalize path
	// rather than piling up in StateRunning forever.
	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fr, BackgroundParallelism: 4})
	defer m.Close()

	var wg sync.WaitGroup
	idsMu := sync.Mutex{}
	ids := make([]string, 0, submits)
	for i := range submits {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mode := Foreground
			if i%2 == 0 {
				mode = Background
			}
			j, err := m.Submit(context.Background(), "noop", mode)
			if err != nil {
				t.Errorf("Submit[%d]: %v", i, err)
				return
			}
			idsMu.Lock()
			ids = append(ids, j.ID)
			idsMu.Unlock()
			// Half the submits race a Cancel against the
			// startLocked path — this is the surface that
			// pre-fix dropped the original CancelFunc on the
			// floor while installing a fresh one.
			if i%2 == 0 {
				_ = m.Cancel(j.ID)
			}
		}(i)
	}
	wg.Wait()

	if len(ids) != submits {
		t.Fatalf("submitted job count: want %d got %d", submits, len(ids))
	}

	// Wait for every submitted job to reach a terminal state so
	// the per-job runJob goroutines have drained. Either Done
	// (the natural path) or Cancelled (raced with Cancel) is
	// acceptable here — we're checking lifecycle drainage, not
	// the exact outcome.
	for _, id := range ids {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			snap, err := m.Get(id)
			if err == nil && snap.State.IsTerminal() {
				break
			}
			time.Sleep(time.Millisecond)
		}
		snap, _ := m.Get(id)
		if !snap.State.IsTerminal() {
			t.Errorf("job %s did not terminate: %v", id, snap.State)
		}
	}
}

// TestSubmit_QueuedJobNeverPromoted_NoLeak exercises the queued-
// then-cancelled-without-running path that pre-fix would orphan
// Submit's CancelFunc forever (since startLocked never runs and
// thus never overwrites job.cancel). Post-fix, Submit doesn't
// mint a cancel at all, so there's nothing to leak. We verify
// behaviorally: cancelling a pending job and then waiting for
// Done on the queue head doesn't trip the race detector.
func TestSubmit_QueuedJobNeverPromoted_NoLeak(t *testing.T) {
	br := newBlockingRunner("", 0)
	m := New(Options{Runner: br})
	defer m.Close()

	// First job occupies the fg slot (blocked).
	head, _ := m.Submit(context.Background(), "head", Foreground)
	if err := waitForState(t, m, head.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}

	// Queue a batch behind it, then cancel each before promotion.
	queued := make([]*Job, 0, 10)
	for range 10 {
		j, err := m.Submit(context.Background(), "queued", Foreground)
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, j)
	}
	for _, j := range queued {
		if err := m.Cancel(j.ID); err != nil {
			t.Errorf("cancel pending: %v", err)
		}
		if j.State() != StateCancelled {
			t.Errorf("queued job %s: want Cancelled, got %v", j.ID, j.State())
		}
	}

	// Release the head; it should complete cleanly.
	br.release()
	if err := waitForState(t, m, head.ID, StateDone, 2*time.Second); err != nil {
		t.Fatal(err)
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
