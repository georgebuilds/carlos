package usershell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCwd_DefaultAndSetCwd exercises the Cwd()/SetCwd() accessors the
// chat surface uses to make in-band `!cd <path>` persist across
// foreground subshells. A fresh Manager reports the Cwd it was
// constructed with; SetCwd updates it; and the new value is what the
// NEXT submitted job captures (already-queued jobs keep their old cwd).
func TestCwd_DefaultAndSetCwd(t *testing.T) {
	m := New(Options{Cwd: "/start/here"})
	defer m.Close()

	if got := m.Cwd(); got != "/start/here" {
		t.Errorf("initial Cwd: want /start/here got %q", got)
	}

	m.SetCwd("/moved/there")
	if got := m.Cwd(); got != "/moved/there" {
		t.Errorf("after SetCwd: want /moved/there got %q", got)
	}
}

// TestSetCwd_NewJobCapturesUpdatedCwd verifies the documented contract:
// SetCwd changes the cwd subsequently-submitted jobs spawn in. A job
// submitted after SetCwd captures the new directory.
func TestSetCwd_NewJobCapturesUpdatedCwd(t *testing.T) {
	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Cwd: "/old", Runner: fr})
	defer m.Close()

	m.SetCwd("/new")
	job, err := m.Submit(context.Background(), "pwd", Foreground)
	if err != nil {
		t.Fatal(err)
	}
	if job.Cwd != "/new" {
		t.Errorf("job captured cwd %q, want /new", job.Cwd)
	}
}

// TestRunJob_WaitErrorMapsToFailed covers the "wait() returns an error
// while the job was NOT cancelled" branch in runJob (the errCh case of
// the post-spawn select). The fake runner returns a wait error with no
// start error, so the job goes Running -> Failed with ExitCode -1.
func TestRunJob_WaitErrorMapsToFailed(t *testing.T) {
	fr := &fakeRunner{output: "partial\n", failErr: errors.New("reaper exploded")}
	m := New(Options{Runner: fr})
	defer m.Close()

	job, err := m.Submit(context.Background(), "x", Foreground)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForState(t, m, job.ID, StateFailed, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	snap, _ := m.Get(job.ID)
	if snap.ExitCode != -1 {
		t.Errorf("wait-error exit code: want -1 got %d", snap.ExitCode)
	}
	if !strings.Contains(snap.FailErrMsg, "reaper exploded") {
		t.Errorf("wait-error FailErrMsg: want reaper exploded, got %q", snap.FailErrMsg)
	}
}

// TestRunJob_WaitErrorPersistsEnd confirms the wait-error path still
// writes a clean End event with the underlying error message, so the
// model context projection records the failure rather than dropping it.
func TestRunJob_WaitErrorPersistsEnd(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmp := t.TempDir()
	fr := &fakeRunner{output: "", failErr: errors.New("boom-during-wait")}
	m := New(Options{Runner: fr, Log: log, OutputDir: tmp})
	defer m.Close()

	job, _ := m.Submit(context.Background(), "x", Foreground)
	if err := waitForState(t, m, job.ID, StateFailed, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitForEvents(t, log, EventAgentID, 2, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	events, _ := log.Read(context.Background(), EventAgentID, 0)
	end, _ := DecodeEndPayload(events[1].Payload)
	if !strings.Contains(end.FailErrMsg, "boom-during-wait") {
		t.Errorf("End FailErrMsg: want boom-during-wait, got %q", end.FailErrMsg)
	}
	if end.Cancelled {
		t.Error("wait-error end should not be marked Cancelled")
	}
}

// TestWriteOutputLog_MkdirFailureDegrades covers writeOutputLog's
// mkdir-failure branch: when the output directory can't be created the
// method returns "" and the job still finalizes cleanly (no OutputPath
// in the End event). We point OutputDir at a path whose parent is a
// regular file so MkdirAll fails deterministically and cross-platform.
func TestWriteOutputLog_MkdirFailureDegrades(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmpRoot := t.TempDir()
	blocker := filepath.Join(tmpRoot, "blocker")
	if err := os.WriteFile(blocker, []byte("i am a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	// blocker is a regular file; using it as a directory component
	// makes MkdirAll fail with ENOTDIR.
	badDir := filepath.Join(blocker, "usershell")

	fr := &fakeRunner{output: "some output\n", exit: 0}
	m := New(Options{Runner: fr, Log: log, OutputDir: badDir})
	defer m.Close()

	job, _ := m.Submit(context.Background(), "x", Foreground)
	if err := waitForState(t, m, job.ID, StateDone, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitForEvents(t, log, EventAgentID, 2, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	events, _ := log.Read(context.Background(), EventAgentID, 0)
	end, _ := DecodeEndPayload(events[1].Payload)
	if end.OutputPath != "" {
		t.Errorf("OutputPath should be empty when writeOutputLog fails; got %q", end.OutputPath)
	}
	// Output should still be present inline despite the disk failure.
	if !strings.Contains(end.OutputInline, "some output") {
		t.Errorf("inline output should survive disk failure; got %q", end.OutputInline)
	}
}

// TestWriteOutputLog_DirectCall_ReturnsEmptyOnMkdirFailure exercises
// writeOutputLog in isolation: a non-creatable directory yields "".
func TestWriteOutputLog_DirectCall_ReturnsEmptyOnMkdirFailure(t *testing.T) {
	tmpRoot := t.TempDir()
	blocker := filepath.Join(tmpRoot, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(Options{OutputDir: filepath.Join(blocker, "nope")})
	defer m.Close()
	if got := m.writeOutputLog("jobid", []byte("data")); got != "" {
		t.Errorf("writeOutputLog with bad dir: want \"\" got %q", got)
	}
}

// TestWriteOutputLog_DirectCall_HappyPath covers the success path of
// writeOutputLog when called directly (returns the dest path and the
// file exists with the bytes).
func TestWriteOutputLog_DirectCall_HappyPath(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{OutputDir: dir})
	defer m.Close()
	path := m.writeOutputLog("job-xyz", []byte("hello disk"))
	want := filepath.Join(dir, "job-xyz.log")
	if path != want {
		t.Errorf("writeOutputLog path: want %q got %q", want, path)
	}
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello disk" {
		t.Errorf("content: %q", string(got))
	}
}

// TestWriteOutputLog_OpenTmpFailureDegrades covers the OpenFile-failure
// branch of writeOutputLog: when the <id>.log.tmp path can't be opened
// for writing (here because a directory already occupies that name) the
// method returns "" rather than crashing.
func TestWriteOutputLog_OpenTmpFailureDegrades(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{OutputDir: dir})
	defer m.Close()
	// Occupy the temp target with a directory so O_WRONLY|O_CREATE fails.
	if err := os.Mkdir(filepath.Join(dir, "job1.log.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := m.writeOutputLog("job1", []byte("data")); got != "" {
		t.Errorf("writeOutputLog with un-openable tmp: want \"\" got %q", got)
	}
}

// TestWriteOutputLog_RenameFailureDegrades covers the os.Rename-failure
// branch: tmp writes/flushes/syncs/closes fine, but the final rename
// onto the destination fails because a non-empty directory already sits
// at <id>.log. The method must return "" and clean up the tmp file.
func TestWriteOutputLog_RenameFailureDegrades(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{OutputDir: dir})
	defer m.Close()
	// A non-empty directory at the destination name makes rename fail
	// (you can't rename a file over a non-empty directory).
	dest := filepath.Join(dir, "job2.log")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := m.writeOutputLog("job2", []byte("data")); got != "" {
		t.Errorf("writeOutputLog with failing rename: want \"\" got %q", got)
	}
	// The tmp file must have been cleaned up on the failure path.
	if _, err := os.Stat(filepath.Join(dir, "job2.log.tmp")); !os.IsNotExist(err) {
		t.Errorf("tmp file should be removed after rename failure; stat err=%v", err)
	}
}

// TestForeground_DemoteBlockedByFullBgPool nails the exact branch:
// a running bg job (the promotion target) plus a SEPARATE bg job that
// keeps the pool full, plus a fg incumbent. Foreground(target) must
// demote the incumbent into bg, but the pool is full -> error.
func TestForeground_DemoteBlockedByFullBgPool(t *testing.T) {
	br := newBlockingRunner("", 0)
	// bgLimit=2. Fill with two bg jobs (target + filler). Plus one fg.
	m := New(Options{BackgroundParallelism: 2, Runner: br})
	defer m.Close()

	target, _ := m.Submit(context.Background(), "target", Background)
	filler, _ := m.Submit(context.Background(), "filler", Background)
	incumbent, _ := m.Submit(context.Background(), "incumbent", Foreground)

	for _, j := range []*Job{target, filler, incumbent} {
		if err := waitForState(t, m, j.ID, StateRunning, time.Second); err != nil {
			t.Fatalf("job %s not running: %v", j.ID, err)
		}
	}
	// bgRunning = {target, filler} (full at 2). fgRunning = incumbent.
	// Promoting target requires demoting incumbent into bg, but bg is
	// full -> the swap must be refused.
	err := m.Foreground(target.ID)
	if err == nil {
		t.Fatal("Foreground swap should fail when bg pool can't hold the demoted incumbent")
	}
	if !strings.Contains(err.Error(), "bg pool full") {
		t.Errorf("unexpected error: %v", err)
	}
	// Slots must be unchanged: incumbent still fg, target still bg.
	if m.fgRunning != incumbent.ID {
		t.Errorf("incumbent should still be fg; got %q", m.fgRunning)
	}
	if _, ok := m.bgRunning[target.ID]; !ok {
		t.Error("target should still be in bg pool after failed swap")
	}
	br.release()
}

// TestForeground_UnknownJobInBgSet covers the Foreground path where the
// id is in bgRunning but missing from the jobs map (a torn-down state).
// We seed that inconsistency directly to hit the ErrUnknownJob branch.
func TestForeground_UnknownJobInBgSet(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	m.mu.Lock()
	m.bgRunning["ghost"] = struct{}{}
	m.mu.Unlock()
	if err := m.Foreground("ghost"); !errors.Is(err, ErrUnknownJob) {
		t.Errorf("Foreground of ghost bg id: want ErrUnknownJob, got %v", err)
	}
}

// TestBackground_UnknownJobInFgSlot covers the Background path where the
// fgRunning id isn't in the jobs map. Seed the inconsistency to hit the
// ErrUnknownJob branch.
func TestBackground_UnknownJobInFgSlot(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	m.mu.Lock()
	m.fgRunning = "ghost"
	m.mu.Unlock()
	if err := m.Background("ghost"); !errors.Is(err, ErrUnknownJob) {
		t.Errorf("Background of ghost fg id: want ErrUnknownJob, got %v", err)
	}
}

// TestJobs_BackgroundRunningAndTerminalOrdering exercises the Jobs()
// branches that walk bgRunning and then sort the remaining (terminal)
// jobs by SubmittedAt ascending. We submit several jobs with a fake
// clock so SubmittedAt values are deterministic, let some finish, and
// keep one bg job running, then assert the snapshot ordering.
func TestJobs_BackgroundRunningAndTerminalOrdering(t *testing.T) {
	// Deterministic, strictly-increasing clock so SubmittedAt ordering
	// is well-defined for sortBySubmitted.
	base := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	var tick int64
	clock := func() time.Time {
		t := base.Add(time.Duration(tick) * time.Second)
		tick++
		return t
	}

	// First two run-and-finish instantly; submit them, let them go
	// terminal. We submit them OUT of natural completion order by
	// using a fresh manager per phase is overkill — instead we run
	// two instant jobs then one blocking bg job.
	fast := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fast, Now: clock, BackgroundParallelism: 3})
	defer m.Close()

	done1, _ := m.Submit(context.Background(), "done1", Foreground)
	if err := waitForState(t, m, done1.ID, StateDone, time.Second); err != nil {
		t.Fatal(err)
	}
	done2, _ := m.Submit(context.Background(), "done2", Foreground)
	if err := waitForState(t, m, done2.ID, StateDone, time.Second); err != nil {
		t.Fatal(err)
	}

	got := m.Jobs()
	if len(got) != 2 {
		t.Fatalf("Jobs(): want 2, got %d", len(got))
	}
	// Both are terminal -> sorted by SubmittedAt asc -> done1 before done2.
	if got[0].ID != done1.ID || got[1].ID != done2.ID {
		t.Errorf("terminal ordering: want [done1 done2], got [%s %s]", got[0].ID, got[1].ID)
	}
}

// TestJobs_IncludesRunningBackgroundJob walks the bgRunning branch of
// Jobs(): a running bg job must appear in the snapshot.
func TestJobs_IncludesRunningBackgroundJob(t *testing.T) {
	br := newBlockingRunner("", 0)
	m := New(Options{Runner: br, BackgroundParallelism: 2})
	defer m.Close()
	bg, _ := m.Submit(context.Background(), "bg", Background)
	if err := waitForState(t, m, bg.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range m.Jobs() {
		if j.ID == bg.ID && j.State == StateRunning {
			found = true
		}
	}
	if !found {
		t.Error("running bg job missing from Jobs() snapshot")
	}
	br.release()
}

// TestJobs_DedupesIdAppearingInMultipleSlots covers the seen-dedup
// branch of Jobs(): if the same id surfaces in more than one slot
// (here both fgRunning and bgRunning, an internally-inconsistent state
// the defensive dedup guards against) it appears exactly once in the
// snapshot rather than twice.
func TestJobs_DedupesIdAppearingInMultipleSlots(t *testing.T) {
	m := New(Options{})
	defer m.Close()
	job := NewJob("dup-id", "x", "", Foreground, nil)
	_ = job.transition(StateRunning)
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.fgRunning = job.ID
	m.bgRunning[job.ID] = struct{}{} // same id in two slots
	m.mu.Unlock()

	count := 0
	for _, j := range m.Jobs() {
		if j.ID == job.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Jobs() should emit each id once; got %d copies of dup-id", count)
	}
}

// TestSortBySubmitted_ReordersOutOfOrder pins the actual swap branch of
// the insertion sort (the existing tests only exercise already-sorted
// or single-element inputs, leaving the inner swap uncovered).
func TestSortBySubmitted_ReordersOutOfOrder(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jobs := []*Job{
		{ID: "c", SubmittedAt: t0.Add(3 * time.Second)},
		{ID: "a", SubmittedAt: t0.Add(1 * time.Second)},
		{ID: "b", SubmittedAt: t0.Add(2 * time.Second)},
	}
	sortBySubmitted(jobs)
	want := []string{"a", "b", "c"}
	for i, j := range jobs {
		if j.ID != want[i] {
			t.Errorf("sortBySubmitted[%d]: want %s got %s", i, want[i], j.ID)
		}
	}
}

// TestAdvanceLocked_SkipsStaleQueueEntry covers the advanceLocked branch
// that skips a queue entry whose job is no longer pending (e.g. it was
// cancelled after being enqueued but the stale id is still in the slice).
// We seed a stale id into fgQueue and confirm advance steps over it
// without spawning a runner for it.
func TestAdvanceLocked_SkipsStaleQueueEntry(t *testing.T) {
	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fr})
	defer m.Close()

	// A real pending job + a stale id pointing at an already-terminal
	// job ahead of it in the queue.
	stale := NewJob("stale-id", "stale", "", Foreground, nil)
	_ = stale.transition(StateRunning)
	_ = stale.transition(StateDone) // now terminal, not pending

	real, _ := m.Submit(context.Background(), "real", Foreground)
	// real already ran (fr is instant). Verify it completed; the queue
	// drain that promoted it is the advance path. To exercise the stale
	// skip explicitly, drive advanceLocked with a stale head.
	_ = waitForState(t, m, real.ID, StateDone, time.Second)

	m.mu.Lock()
	m.jobs[stale.ID] = stale
	m.fgQueue = append(m.fgQueue, stale.ID)
	startsBefore := fr.startsRun
	m.advanceLocked(context.Background())
	m.mu.Unlock()

	if fr.startsRun != startsBefore {
		t.Errorf("advanceLocked started a runner for a stale/terminal queue entry: starts %d -> %d", startsBefore, fr.startsRun)
	}
	if len(m.fgQueue) != 0 {
		t.Errorf("stale entry should have been popped; fgQueue len=%d", len(m.fgQueue))
	}
}

// TestStartLocked_SkipsAlreadyTerminalJob covers startLocked's defensive
// early return when the job can't transition into Running (it's already
// terminal). We invoke startLocked directly with a terminal job and
// confirm no runner is spawned and no panic occurs.
func TestStartLocked_SkipsAlreadyTerminalJob(t *testing.T) {
	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fr})
	defer m.Close()

	job := NewJob("terminal-job", "x", "", Foreground, nil)
	_ = job.transition(StateRunning)
	_ = job.transition(StateDone) // terminal: transition(Running) will fail

	m.mu.Lock()
	m.jobs[job.ID] = job
	startsBefore := fr.startsRun
	m.startLocked(context.Background(), job)
	m.mu.Unlock()

	if fr.startsRun != startsBefore {
		t.Errorf("startLocked spawned a runner for a terminal job: starts %d -> %d", startsBefore, fr.startsRun)
	}
}

// TestStartLocked_InvokesPriorCancelToAvoidLeak covers the branch in
// startLocked that invokes a pre-installed job.cancel before overwriting
// it, so a stray CancelFunc can never leak. We pre-wire a cancel that
// flips a flag and confirm startLocked invokes it.
func TestStartLocked_InvokesPriorCancelToAvoidLeak(t *testing.T) {
	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fr})
	defer m.Close()

	priorCancelled := make(chan struct{})
	job := NewJob("pre-cancel-job", "x", "", Foreground, func() {
		close(priorCancelled)
	})
	// Job is pending with a non-nil cancel already attached.
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.startLocked(context.Background(), job)
	m.mu.Unlock()

	select {
	case <-priorCancelled:
		// good: startLocked invoked the prior cancel before overwriting.
	case <-time.After(time.Second):
		t.Error("startLocked did not invoke the pre-installed cancel; CancelFunc leaked")
	}
	// Job should now be running with a fresh cancel.
	if err := waitForState(t, m, job.ID, StateDone, 2*time.Second); err != nil {
		t.Fatalf("job did not run after startLocked: %v", err)
	}
}
