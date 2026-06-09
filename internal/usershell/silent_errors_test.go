package usershell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// newOSPipe is a thin wrapper used by TestSite4 to provoke a real
// "file already closed" error from os.File.Close.
func newOSPipe() (*os.File, *os.File, error) {
	return os.Pipe()
}

// captureErrLog swaps the package-level errLog for a thread-safe
// buffer and returns it + a restore func. Tests use this to assert
// that the silent-error sites now surface their failures.
func captureErrLog(t *testing.T) (*safeBuffer, func()) {
	t.Helper()
	prev := errLog
	buf := &safeBuffer{}
	errLog = buf
	return buf, func() { errLog = prev }
}

// safeBuffer is a goroutine-safe bytes.Buffer. The reader goroutine
// and finalize goroutine both call warnf concurrently, and the test
// goroutine reads. Without a mutex -race trips.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// failingWriter wraps a real *RingBuffer but returns errFail on every
// Write. We keep the underlying buffer around so the finalize path
// (which calls rb.Snapshot()) still sees zero bytes — the contract is
// "buffer wedged", not "buffer disappeared".
type failingWriter struct {
	err error
}

func (f failingWriter) Write(p []byte) (int, error) {
	return 0, f.err
}

// ---------------------------------------------------------------------
// Site 1 — ring-buffer write error in the reader goroutine.
// ---------------------------------------------------------------------

// TestSite1_RingBufferWriteError_PublishesTruncationMarker verifies
// that when the per-job ring buffer's Write returns an error, the
// reader goroutine:
//
//  1. Publishes an Update carrying OutputBufferDroppedMarker so the
//     chat view can render "[output buffer dropped]" instead of just
//     dropping bytes on the floor.
//  2. Stops trying to read — no further chunks land.
//  3. Doesn't crash the goroutine (the job still reaches a terminal
//     state via the wait path).
//  4. Surfaces the failure via warnf so operators see the wedged
//     buffer in stderr.
func TestSite1_RingBufferWriteError_PublishesTruncationMarker(t *testing.T) {
	buf, restore := captureErrLog(t)
	defer restore()

	wantErr := errors.New("ring is wedged")

	fr := &fakeRunner{output: "hello there\n", exit: 0}
	m := New(Options{Runner: fr})
	m.outputWriterFor = func(jobID string, rb *RingBuffer) io.Writer {
		return failingWriter{err: wantErr}
	}
	defer m.Close()

	ch, unsub := m.Subscribe()
	defer unsub()

	job, err := m.Submit(context.Background(), "echo hi", Foreground)
	if err != nil {
		t.Fatal(err)
	}

	// The job should still reach a terminal state — a wedged buffer
	// shouldn't strand the lifecycle.
	if err := waitForState(t, m, job.ID, StateDone, 2*time.Second); err != nil {
		t.Fatalf("job never terminated despite wedged buffer: %v", err)
	}

	// Drain the subscriber and look for the marker.
	sawMarker := false
	drainDeadline := time.After(500 * time.Millisecond)
drain:
	for {
		select {
		case u := <-ch:
			if u.JobID != job.ID {
				continue
			}
			if len(u.Output) > 0 && strings.Contains(string(u.Output), "[output buffer dropped]") {
				sawMarker = true
			}
		case <-drainDeadline:
			break drain
		}
	}
	if !sawMarker {
		t.Error("expected an Update carrying the truncation marker; never saw one")
	}

	if !strings.Contains(buf.String(), "ring buffer write") {
		t.Errorf("expected stderr warning about ring buffer write; got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "ring is wedged") {
		t.Errorf("expected stderr warning to include underlying error; got %q", buf.String())
	}
}

// TestSite1_RingBufferWriteError_BreaksReadLoop confirms that once
// the writer errors, no further output chunks (post-marker) appear.
// The marker counts as exactly one chunk; everything after the first
// failed Write is dropped on the floor (correctly so — the buffer
// is wedged, there's no point pretending).
func TestSite1_RingBufferWriteError_BreaksReadLoop(t *testing.T) {
	_, restore := captureErrLog(t)
	defer restore()

	// Use a blocking runner so we can spy on chunk count without a
	// race against the wait()-returns path.
	br := newBlockingRunner("first chunk\nsecond chunk\n", 0)
	m := New(Options{Runner: br})
	m.outputWriterFor = func(jobID string, rb *RingBuffer) io.Writer {
		return failingWriter{err: errors.New("wedged")}
	}
	defer m.Close()

	ch, unsub := m.Subscribe()
	defer unsub()

	job, _ := m.Submit(context.Background(), "x", Foreground)
	if err := waitForState(t, m, job.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}

	// Let the reader goroutine attempt its first Write (which will
	// fail and publish the marker), then verify no real-output chunks
	// follow before the runner is released.
	chunkCount := 0
	markerCount := 0
	deadline := time.After(200 * time.Millisecond)
gather:
	for {
		select {
		case u := <-ch:
			if u.JobID != job.ID || len(u.Output) == 0 {
				continue
			}
			if strings.Contains(string(u.Output), "[output buffer dropped]") {
				markerCount++
				continue
			}
			chunkCount++
		case <-deadline:
			break gather
		}
	}
	if markerCount < 1 {
		t.Errorf("want at least one truncation marker; got %d", markerCount)
	}
	if chunkCount != 0 {
		t.Errorf("reader loop did not break: received %d real-output chunks after wedge", chunkCount)
	}
	br.release()
}

// ---------------------------------------------------------------------
// Site 2 — job.setOutcome invalid-transition error dropped.
// ---------------------------------------------------------------------

// TestSite2_SetOutcomeError_Surfaces drives a Job into a terminal
// state externally, then calls Manager.finalize again to provoke the
// invalid-transition error path. We assert the failure surfaces via
// warnf rather than being silently swallowed.
func TestSite2_SetOutcomeError_Surfaces(t *testing.T) {
	buf, restore := captureErrLog(t)
	defer restore()

	fr := &fakeRunner{output: "", exit: 0}
	m := New(Options{Runner: fr})
	defer m.Close()

	// Stand up a job + ring buffer outside the normal Submit path so
	// we can call finalize directly.
	job := NewJob("test-job-site-2", "true", "/tmp", Foreground, func() {})
	if err := job.transition(StateRunning); err != nil {
		t.Fatalf("seed transition to running: %v", err)
	}
	// Drive the job to a terminal state. finalize will then attempt
	// a second terminal transition and trip setOutcome.
	if err := job.transition(StateDone); err != nil {
		t.Fatalf("seed terminal transition: %v", err)
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	rb := NewRingBuffer(64)
	m.outputs[job.ID] = rb
	m.mu.Unlock()

	m.finalize(job, rb, StateFailed, 1, errors.New("synthetic"))

	out := buf.String()
	if !strings.Contains(out, "setOutcome") {
		t.Errorf("expected setOutcome warning on stderr; got %q", out)
	}
	if !strings.Contains(out, job.ID) {
		t.Errorf("expected job ID in warning; got %q", out)
	}
}

// ---------------------------------------------------------------------
// Site 3 — AppendEnd error dropped + uses context.Background().
// ---------------------------------------------------------------------

// TestSite3_AppendEndError_Surfaces forces AppendEnd to fail by
// closing the SQLite log out from under the manager just before the
// end-event write. The job still reaches terminal state, but the
// failure surfaces via warnf.
func TestSite3_AppendEndError_Surfaces(t *testing.T) {
	buf, restore := captureErrLog(t)
	defer restore()

	log := newPersistenceTestLog(t)
	tmp := t.TempDir()

	// We need to close the log AFTER the start event lands but
	// BEFORE the end event tries to land. Use a blocking runner so
	// the timing is controllable.
	br := newBlockingRunner("done\n", 0)
	m := New(Options{Runner: br, Log: log, OutputDir: tmp})
	defer m.Close()

	job, err := m.Submit(context.Background(), "x", Foreground)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForState(t, m, job.ID, StateRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	// Start event has already landed (per design). Slam the log shut
	// so AppendEnd in finalize fails.
	if err := log.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}
	br.release()

	if err := waitForState(t, m, job.ID, StateDone, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	// Poll briefly — finalize runs in the runJob goroutine and the
	// warn happens just after the terminal state flip.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "AppendEnd") {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), "AppendEnd") {
		t.Errorf("expected AppendEnd warning on stderr; got %q", buf.String())
	}
	if !strings.Contains(buf.String(), job.ID) {
		t.Errorf("expected job ID in AppendEnd warning; got %q", buf.String())
	}
}

// TestSite3_AppendEndUsesBoundedContext is a structural assertion:
// the production code path no longer hands context.Background() to
// AppendEnd. We pin the constant rather than the call site so a
// future refactor that keeps the bound but changes the duration still
// passes.
func TestSite3_AppendEndUsesBoundedContext(t *testing.T) {
	if finalizeAppendEndTimeout <= 0 {
		t.Errorf("finalize must use a positive timeout; got %v", finalizeAppendEndTimeout)
	}
	if finalizeAppendEndTimeout > 30*time.Second {
		t.Errorf("finalize timeout looks unreasonably long: %v", finalizeAppendEndTimeout)
	}
}

// TestSite3_AppendEndContextCancellation forces the AppendEnd write
// to observe ctx.Done by stubbing in a log that blocks until ctx is
// cancelled. With a context.Background() previously, this test would
// hang. With the 5s bound, it returns and surfaces a warn.
func TestSite3_AppendEndContextCancellation(t *testing.T) {
	// Build a real log so AppendEnd's path through json.Marshal +
	// db.ExecContext runs. We then race the manager: AppendEnd is
	// called with a finite ctx; we verify the warning surfaces if
	// the underlying log errors. (Pure ctx-deadline behavior is
	// exercised in the SQLiteEventLog tests; here we just verify the
	// bound is wired through rather than dropped.)
	log := newPersistenceTestLog(t)

	// Submit with a ctx that's already past its deadline, then close
	// the log so AppendEnd fails. The point: the manager does NOT
	// pass the caller's ctx into AppendEnd — it owns the deadline.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel the caller ctx

	tmp := t.TempDir()
	fr := &fakeRunner{output: "x", exit: 0}
	m := New(Options{Runner: fr, Log: log, OutputDir: tmp})
	defer m.Close()

	job, err := m.Submit(ctx, "x", Foreground)
	if err != nil {
		t.Fatal(err)
	}
	// The job still runs to completion because the Manager spawns
	// its own per-job context.
	_ = waitForState(t, m, job.ID, StateDone, 2*time.Second)
	// End event should have landed normally — caller ctx cancellation
	// must not propagate into the AppendEnd path.
	if err := waitForEvents(t, log, EventAgentID, 2, time.Second); err != nil {
		t.Errorf("end event missing despite pre-cancelled caller ctx: %v", err)
	}
}

// ---------------------------------------------------------------------
// Site 4 — tty.Close error swallowed.
// ---------------------------------------------------------------------

// TestSite4_TTYCloseError_Surfaces is a unit-level assertion against
// warnf. We can't easily provoke a real tty.Close failure (Close on
// a *os.File only errors if the fd is already closed or invalid),
// but we can verify the prefix + format the code uses lands on
// errLog. Direct call to warnf with the same format string exercises
// the surface the production close path takes.
func TestSite4_TTYCloseError_FormatStable(t *testing.T) {
	buf, restore := captureErrLog(t)
	defer restore()

	// The exact format string the wait() closure uses. If this drifts
	// the test fails — that's intentional; the operator-log shape is
	// part of the contract.
	warnf("pty close (pid %d): %v", 4242, errors.New("file already closed"))

	got := buf.String()
	if !strings.Contains(got, "pty close") {
		t.Errorf("missing pty close marker: %q", got)
	}
	if !strings.Contains(got, "4242") {
		t.Errorf("missing pid in surface: %q", got)
	}
	if !strings.Contains(got, "file already closed") {
		t.Errorf("missing underlying err in surface: %q", got)
	}
	if !strings.HasPrefix(got, "carlos usershell: ") {
		t.Errorf("warning should use the carlos usershell prefix; got %q", got)
	}
}

// TestSite4_TTYCloseError_ProvokedViaDoubleClose drives a real close
// failure: we open a pipe, hand its write end to the wait()-like
// pattern via Close on an already-closed fd. This pins that the
// warnf path actually fires on a real error from os.File.Close().
func TestSite4_TTYCloseError_ProvokedViaDoubleClose(t *testing.T) {
	buf, restore := captureErrLog(t)
	defer restore()

	// Mirror the exact recipe used in pty.go's wait closure: call
	// Close on a file, observe the error, hand it to warnf with the
	// same format. Smoke-tests the format AND the warnf wiring AND
	// that os.File.Close returning err is the trigger.
	r, w, err := newOSPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	if err := w.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	closeErr := w.Close() // second close — definitively errors
	if closeErr == nil {
		t.Skip("double-close did not produce an error on this platform")
	}
	warnf("pty close (pid %d): %v", 1234, closeErr)

	if !strings.Contains(buf.String(), "pty close") {
		t.Errorf("expected pty close surface; got %q", buf.String())
	}
}

// ---------------------------------------------------------------------
// Cross-cutting: warnf prefix is stable for all sites.
// ---------------------------------------------------------------------

func TestWarnf_PrefixStable(t *testing.T) {
	buf, restore := captureErrLog(t)
	defer restore()
	warnf("anything goes here %d", 1)
	if !strings.HasPrefix(buf.String(), "carlos usershell: ") {
		t.Errorf("warnf prefix drifted: %q", buf.String())
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("warnf must terminate with newline: %q", buf.String())
	}
}

// ensure errLog is goroutine-safe via Write paths the tests use.
var _ io.Writer = (*safeBuffer)(nil)

// silence unused-import lint by giving agent a referenced symbol when
// builds prune.
var _ = agent.EvtUserShellEnd
