package usershell

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

func newPersistenceTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// TestPersistence_StartAndEndEventsLanded covers the load-bearing
// path: a foreground job submitted with a wired log produces both a
// start row + an end row, in that order, with the right shapes.
func TestPersistence_StartAndEndEventsLanded(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmp := t.TempDir()
	fr := &fakeRunner{output: "hi\n", exit: 0}
	m := New(Options{Runner: fr, Log: log, OutputDir: tmp})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "echo hi", Foreground)
	if err := waitForState(t, m, job.ID, StateDone, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitForEvents(t, log, EventAgentID, 2, time.Second); err != nil {
		t.Fatal(err)
	}

	events, err := log.Read(context.Background(), EventAgentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events (start + end); got %d", len(events))
	}
	if events[0].Type != agent.EvtUserShellStart {
		t.Errorf("event 0: want start, got %v", events[0].Type)
	}
	if events[1].Type != agent.EvtUserShellEnd {
		t.Errorf("event 1: want end, got %v", events[1].Type)
	}

	start, err := DecodeStartPayload(events[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if start.JobID != job.ID || start.Command != "echo hi" || start.Mode != "foreground" {
		t.Errorf("start payload mismatch: %+v", start)
	}

	end, err := DecodeEndPayload(events[1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if end.JobID != job.ID || end.ExitCode != 0 {
		t.Errorf("end payload mismatch: %+v", end)
	}
	if end.OutputInline != "hi\n" {
		t.Errorf("inline output: %q", end.OutputInline)
	}
	if end.TruncatedBytes != 0 {
		t.Errorf("truncated bytes: %d", end.TruncatedBytes)
	}
	if end.OutputPath == "" {
		t.Error("OutputPath should be set when output was non-empty")
	}
}

// TestPersistence_OutputFilePersists confirms the per-job log file
// lands at <OutputDir>/<id>.log with the captured bytes.
func TestPersistence_OutputFilePersists(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmp := t.TempDir()
	fr := &fakeRunner{output: "line1\nline2\n", exit: 0}
	m := New(Options{Runner: fr, Log: log, OutputDir: tmp})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "x", Foreground)
	if err := waitForState(t, m, job.ID, StateDone, time.Second); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(tmp, job.ID+".log"))
	if err != nil {
		t.Fatalf("read output log: %v", err)
	}
	if string(got) != "line1\nline2\n" {
		t.Errorf("output file content: %q", string(got))
	}
}

// TestPersistence_OutputFileMode pins 0600 on the per-job log file —
// user-shell output may include secrets the user echoed, and a
// world-readable log is the textbook screw-up.
func TestPersistence_OutputFileMode(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmp := t.TempDir()
	fr := &fakeRunner{output: "secret-ish", exit: 0}
	m := New(Options{Runner: fr, Log: log, OutputDir: tmp})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "x", Foreground)
	_ = waitForState(t, m, job.ID, StateDone, time.Second)
	info, err := os.Stat(filepath.Join(tmp, job.ID+".log"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("output log mode: want 0600 got %o", mode)
	}
}

// TestPersistence_NoOutputFileWhenSilent — commands with no output
// should NOT leave an empty file on disk (avoids per-job spam).
func TestPersistence_NoOutputFileWhenSilent(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmp := t.TempDir()
	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fr, Log: log, OutputDir: tmp})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "true", Foreground)
	_ = waitForState(t, m, job.ID, StateDone, time.Second)
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Errorf("silent command should not create a log file; got %d entries", len(entries))
	}
}

// TestPersistence_OutputDirAutoCreated — the manager mkdirs the
// output dir lazily, so callers don't have to.
func TestPersistence_OutputDirAutoCreated(t *testing.T) {
	log := newPersistenceTestLog(t)
	nested := filepath.Join(t.TempDir(), "deep", "nested", "dir")
	fr := &fakeRunner{output: "x", exit: 0}
	m := New(Options{Runner: fr, Log: log, OutputDir: nested})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "x", Foreground)
	_ = waitForState(t, m, job.ID, StateDone, time.Second)
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("nested OutputDir should have been created: %v", err)
	}
}

// TestPersistence_FailedJobStillWritesEnd verifies a non-zero exit
// produces an end event with the right ExitCode and StateFailed.
func TestPersistence_FailedJobStillWritesEnd(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmp := t.TempDir()
	fr := &fakeRunner{output: "boom\n", exit: 137}
	m := New(Options{Runner: fr, Log: log, OutputDir: tmp})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "false", Foreground)
	_ = waitForState(t, m, job.ID, StateFailed, time.Second)
	_ = waitForEvents(t, log, EventAgentID, 2, time.Second)
	events, _ := log.Read(context.Background(), EventAgentID, 0)
	if len(events) != 2 {
		t.Fatalf("want 2 events; got %d", len(events))
	}
	end, _ := DecodeEndPayload(events[1].Payload)
	if end.ExitCode != 137 {
		t.Errorf("end exit code: %d", end.ExitCode)
	}
	if end.Cancelled {
		t.Error("end Cancelled should be false on natural failure")
	}
}

// TestPersistence_CancelledJobMarkedCancelled — Manager.Cancel
// during run produces an end event with Cancelled=true.
func TestPersistence_CancelledJobMarkedCancelled(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmp := t.TempDir()
	br := newBlockingRunner("partial\n", 0)
	m := New(Options{Runner: br, Log: log, OutputDir: tmp})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "sleep 60", Foreground)
	if err := waitForState(t, m, job.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	_ = waitForState(t, m, job.ID, StateCancelled, time.Second)
	_ = waitForEvents(t, log, EventAgentID, 2, time.Second)
	events, _ := log.Read(context.Background(), EventAgentID, 0)
	if len(events) != 2 {
		t.Fatalf("want 2 events; got %d", len(events))
	}
	end, _ := DecodeEndPayload(events[1].Payload)
	if !end.Cancelled {
		t.Error("Cancelled flag should be true")
	}
}

// TestPersistence_NilLogDoesNotCrash — Manager configured with no
// log still runs jobs, just doesn't persist anything. The chat
// surface needs this for ephemeral sessions.
func TestPersistence_NilLogDoesNotCrash(t *testing.T) {
	fr := &fakeRunner{output: "hi", exit: 0}
	m := New(Options{Runner: fr, Log: nil})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "echo", Foreground)
	if err := waitForState(t, m, job.ID, StateDone, time.Second); err != nil {
		t.Fatal(err)
	}
}

// TestPersistence_BackgroundJobsRecordedAsBg confirms the Mode flag
// in the start payload distinguishes fg from bg submissions.
func TestPersistence_BackgroundJobsRecordedAsBg(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmp := t.TempDir()
	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fr, Log: log, OutputDir: tmp})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "x", Background)
	_ = waitForState(t, m, job.ID, StateDone, time.Second)
	_ = waitForEvents(t, log, EventAgentID, 2, time.Second)
	events, _ := log.Read(context.Background(), EventAgentID, 0)
	start, _ := DecodeStartPayload(events[0].Payload)
	if start.Mode != "background" || !start.Background {
		t.Errorf("background flags: mode=%q bg=%v", start.Mode, start.Background)
	}
}

// TestPersistence_LongOutputTruncatedInline verifies that output
// larger than MaxInlineOutput is tail-truncated for the inline
// payload and reflected in TruncatedBytes.
func TestPersistence_LongOutputTruncatedInline(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmp := t.TempDir()
	// MaxInlineOutput + a marker tail; ring buffer cap pinned high
	// enough to capture everything.
	cap := MaxInlineOutput + 1024
	long := make([]byte, MaxInlineOutput+50)
	for i := range long {
		long[i] = 'a'
	}
	copy(long[len(long)-4:], []byte("TAIL"))
	fr := &fakeRunner{output: string(long), exit: 0}
	m := New(Options{Runner: fr, Log: log, OutputDir: tmp, RingBufferCap: cap})
	defer m.Close()
	job, _ := m.Submit(context.Background(), "spam", Foreground)
	if err := waitForState(t, m, job.ID, StateDone, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitForEvents(t, log, EventAgentID, 2, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	events, _ := log.Read(context.Background(), EventAgentID, 0)
	end, _ := DecodeEndPayload(events[1].Payload)
	if end.TruncatedBytes != 50 {
		t.Errorf("truncated bytes: want 50, got %d", end.TruncatedBytes)
	}
	if len(end.OutputInline) != MaxInlineOutput {
		t.Errorf("inline length: want %d got %d", MaxInlineOutput, len(end.OutputInline))
	}
	if end.OutputInline[len(end.OutputInline)-4:] != "TAIL" {
		t.Error("inline should preserve the tail of output")
	}
}

// TestPersistence_PostCrashRecovery — the start event landing
// BEFORE the spawn goroutine writes the end event means a daemon
// restart between the two leaves a recoverable "started, never
// finished" row. We verify by submitting a job whose end never
// fires (manager Close mid-run) and confirming only the start
// landed.
func TestPersistence_StartLandsBeforeEnd(t *testing.T) {
	log := newPersistenceTestLog(t)
	tmp := t.TempDir()
	br := newBlockingRunner("", 0)
	m := New(Options{Runner: br, Log: log, OutputDir: tmp})
	job, _ := m.Submit(context.Background(), "loop", Foreground)
	if err := waitForState(t, m, job.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	// At this exact moment, start should be persisted; end should not.
	events, _ := log.Read(context.Background(), EventAgentID, 0)
	if len(events) != 1 || events[0].Type != agent.EvtUserShellStart {
		t.Errorf("want exactly the start event; got %d: %+v", len(events), events)
	}
	br.release()
	_ = m.Close()
}
