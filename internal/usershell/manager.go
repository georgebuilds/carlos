package usershell

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/georgebuilds/carlos/internal/agent"
)

// DefaultBackgroundParallelism is how many background jobs may run
// concurrently when the caller doesn't override via Options. Three is
// the "felt right" default — enough for `cargo test`, `npm run dev`,
// and a `tail -f` simultaneously without the user losing track.
const DefaultBackgroundParallelism = 3

// Options configures a Manager. All fields have sane defaults; nil
// Options is equivalent to an empty struct.
type Options struct {
	// Cwd is the working directory every job spawns in. Defaults to
	// the current process's cwd at New time. Jobs capture their cwd
	// at Submit time so a future SetCwd doesn't retroactively affect
	// already-queued jobs.
	Cwd string

	// BackgroundParallelism caps how many background jobs may run at
	// once. <=0 falls back to DefaultBackgroundParallelism.
	BackgroundParallelism int

	// Now is the clock the Manager reads for SubmittedAt etc. nil
	// falls back to time.Now. Tests inject a fake clock to make
	// snapshots deterministic.
	Now func() time.Time

	// Runner is the subprocess driver. nil falls back to the
	// production PTY runner. Tests substitute a deterministic
	// in-process runner to exercise queue + lifecycle without
	// shelling out.
	Runner runner

	// RingBufferCap is the output buffer's byte capacity per job.
	// <=0 uses the package default (64 KiB).
	RingBufferCap int

	// Log is the SQLite event log to write EvtUserShellStart +
	// EvtUserShellEnd rows into. nil disables persistence — the
	// Manager still runs jobs and surfaces them in Jobs(), but the
	// model context projection won't see them. Tests pass nil for
	// pure lifecycle exercises; production wires the chat
	// session's log.
	Log *agent.SQLiteEventLog

	// OutputDir is the on-disk directory where full per-job output
	// logs land (<id>.log files). Empty falls back to
	// ~/.carlos/usershell/. Tests inject a tempdir.
	OutputDir string
}

// Manager owns the user-shell job lifecycle for one chat session:
// queue + background pool + cancellation + persistence handoff.
// Safe for concurrent use; the TUI's Update goroutine and the per-
// job spawn goroutines both call methods.
//
// S0 ships only the surface — Submit returns ErrNotImplemented, the
// queue is allocated but never advanced, and there's no PTY. S1
// fills in execution; S2 fills in the queue + bg pool semantics.
// The shape here is what the TUI codes against.
type Manager struct {
	mu sync.RWMutex

	cwd       string
	bgLimit   int
	now       func() time.Time
	runner    runner
	bufCap    int
	log       *agent.SQLiteEventLog
	outputDir string

	// jobs holds every Job the Manager has seen, keyed by ID. Stays
	// around after termination so the jobs overlay can render
	// "recent" history; size-bounded by a future GC pass.
	jobs map[string]*Job

	// outputs holds the per-job ring buffer. Populated when a job
	// transitions to running. Kept in this map (not on Job) so the
	// Job struct stays pure data + readers don't have to chase a
	// pointer.
	outputs map[string]*RingBuffer

	// fgQueue is the foreground waiting list (FIFO). Index 0 is up
	// next. When a foreground slot opens, the Manager pops index 0.
	fgQueue []string

	// bgQueue is the background waiting list — analog of fgQueue for
	// the bg pool. Used when Submit Background lands while the pool
	// is full; popped FIFO as bg slots free.
	bgQueue []string

	// fgRunning is the ID of the single foreground job currently
	// executing, or "" if the slot is open.
	fgRunning string

	// bgRunning is the set of background job IDs currently
	// executing. Length is bounded by bgLimit.
	bgRunning map[string]struct{}

	// closed flags whether Close has been called; further Submit
	// calls return ErrClosed.
	closed bool

	// subscribers is the in-process fan-out for state changes. Each
	// Subscribe call appends a channel; publish sends best-effort
	// (non-blocking) to all of them. Used by the TUI to drive
	// transcript redraws without polling.
	subMu       sync.Mutex
	subscribers []chan Update

	// ulidEntropy is the monotonic-random reader for fresh job IDs.
	// Guarded by ulidMu — ulid.MonotonicEntropy is not safe for
	// concurrent reads.
	ulidMu      sync.Mutex
	ulidEntropy *ulid.MonotonicEntropy
}

// Update is the notification a TUI subscriber receives when a job's
// state or output changes. Output is empty for state-only events;
// non-empty when new bytes arrived from the PTY (chunked at the
// reader's natural cadence).
type Update struct {
	JobID  string
	State  State
	Output []byte // non-nil iff this is an output chunk
}

// ErrClosed is returned from Submit (and related verbs) after Close
// has been called on the Manager. The chat surface calls Close when
// its enclosing context is cancelled.
var ErrClosed = errors.New("usershell: manager is closed")

// ErrUnknownJob is returned by Cancel/Background/Foreground when the
// caller hands an ID the Manager doesn't know about.
var ErrUnknownJob = errors.New("usershell: unknown job id")

// New constructs a Manager. Always succeeds; misconfiguration
// (e.g. zero parallelism) is normalized to defaults rather than
// erroring out so the chat surface doesn't have to handle a New
// failure path.
func New(opts Options) *Manager {
	bg := opts.BackgroundParallelism
	if bg <= 0 {
		bg = DefaultBackgroundParallelism
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	r := opts.Runner
	if r == nil {
		r = ptyRunner{}
	}
	outputDir := opts.OutputDir
	if outputDir == "" {
		outputDir = defaultOutputDir()
	}
	return &Manager{
		cwd:         opts.Cwd,
		bgLimit:     bg,
		now:         now,
		runner:      r,
		bufCap:      opts.RingBufferCap,
		log:         opts.Log,
		outputDir:   outputDir,
		jobs:        map[string]*Job{},
		outputs:     map[string]*RingBuffer{},
		bgRunning:   map[string]struct{}{},
		ulidEntropy: ulid.Monotonic(rand.Reader, 0),
	}
}

// defaultOutputDir returns ~/.carlos/usershell/ for the per-job log
// files. Falls back to a relative path if the home dir isn't
// resolvable (same recipe agent/artifacts.go uses).
func defaultOutputDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".carlos", "usershell")
	}
	return filepath.Join(".carlos", "usershell")
}

// Submit enqueues a new shell command and immediately advances the
// queue. Returns the freshly-minted Job so callers can subscribe to
// its state OR look up its short ID. If a slot is open the job is
// already running by the time Submit returns; otherwise it sits in
// the appropriate queue.
//
// Foreground path: if no foreground job is running, this submission
// is picked up immediately. Otherwise it joins the FIFO and waits.
//
// Background path: if the bg pool has room (< bgLimit running), this
// submission spawns immediately in parallel. Otherwise it queues.
func (m *Manager) Submit(ctx context.Context, command string, mode Mode) (*Job, error) {
	command = trimCommand(command)
	if command == "" {
		return nil, errors.New("usershell: empty command")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	id, err := m.newJobID()
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("usershell: mint job id: %w", err)
	}
	jobCtx, cancel := context.WithCancel(ctx)
	job := NewJob(id, command, m.cwd, mode, cancel)
	job.SubmittedAt = m.now().UTC().Truncate(time.Millisecond)
	m.jobs[id] = job
	switch mode {
	case Foreground:
		m.fgQueue = append(m.fgQueue, id)
	case Background:
		m.bgQueue = append(m.bgQueue, id)
	}
	// Pick up jobs while there's capacity. This is a no-op when
	// the queues are empty AND every slot is full; the same loop
	// runs after every terminal transition to drain backlog.
	m.advanceLocked(jobCtx)
	m.mu.Unlock()
	return job, nil
}

// advanceLocked promotes pending jobs to running while there's
// room. Caller must hold m.mu.
//
// We pass spawnCtx so the very first promotion of a freshly-
// submitted job inherits the caller's context. Subsequent
// promotions (driven by other jobs ending) use context.Background()
// — the Manager outlives the Submit caller; we don't want a Submit
// caller cancelling its ctx to also kill jobs queued behind it.
func (m *Manager) advanceLocked(spawnCtx context.Context) {
	// Foreground: at most one running.
	for m.fgRunning == "" && len(m.fgQueue) > 0 {
		id := m.fgQueue[0]
		m.fgQueue = m.fgQueue[1:]
		job, ok := m.jobs[id]
		if !ok || job.State() != StatePending {
			// Cancelled-before-pickup or stale entry; skip.
			continue
		}
		m.fgRunning = id
		m.startLocked(spawnCtx, job)
		spawnCtx = context.Background() // only the first uses caller ctx
	}
	// Background: fill up to bgLimit.
	for len(m.bgRunning) < m.bgLimit && len(m.bgQueue) > 0 {
		id := m.bgQueue[0]
		m.bgQueue = m.bgQueue[1:]
		job, ok := m.jobs[id]
		if !ok || job.State() != StatePending {
			continue
		}
		m.bgRunning[id] = struct{}{}
		m.startLocked(spawnCtx, job)
		spawnCtx = context.Background()
	}
}

// startLocked spawns the runner for a job and wires the reader/wait
// goroutines. Caller must hold m.mu. Failure to start transitions
// the job to Failed inline.
func (m *Manager) startLocked(parent context.Context, job *Job) {
	// Allocate the per-job ring buffer + transition to running.
	rb := NewRingBuffer(m.bufCap)
	m.outputs[job.ID] = rb
	if err := job.transition(StateRunning); err != nil {
		// Shouldn't happen — the queues are supposed to only hold
		// pending jobs — but be defensive.
		return
	}
	m.publishStateLocked(job.ID, StateRunning)

	// Persist the start event. We do this OUTSIDE the goroutine
	// (still under m.mu) so the start row is in the log before the
	// run goroutine has a chance to write the end row — projection
	// scans rely on start-before-end ordering.
	if m.log != nil {
		_, _ = AppendStart(context.Background(), m.log, StartPayload{
			JobID:      job.ID,
			Command:    job.Command,
			Cwd:        job.Cwd,
			Mode:       job.Mode.String(),
			Background: job.Mode == Background,
			StartedAt:  job.StartedAt,
		})
	}

	// Per-job context: descendant of the parent + Manager-cancellable.
	jobCtx, cancel := context.WithCancel(parent)
	job.cancel = cancel

	go m.runJob(jobCtx, job, rb)
}

// runJob is the per-job goroutine that drives the subprocess
// lifecycle. NOT lock-held — it runs concurrently with the manager.
func (m *Manager) runJob(ctx context.Context, job *Job, rb *RingBuffer) {
	reader, wait, kill, err := m.runner.Start(ctx, job.Command, job.Cwd)
	if err != nil {
		m.finalize(job, rb, StateFailed, -1, err)
		return
	}

	// Reader goroutine: copy PTY → ring buffer + publish chunks.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				_, _ = rb.Write(buf[:n])
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				m.publish(Update{JobID: job.ID, State: StateRunning, Output: chunk})
			}
			if err != nil {
				return
			}
		}
	}()

	exit := 0
	cancelled := false
	select {
	case <-ctx.Done():
		cancelled = true
		kill()
	default:
	}

	if !cancelled {
		exitCh := make(chan int, 1)
		errCh := make(chan error, 1)
		go func() {
			ec, werr := wait()
			if werr != nil {
				errCh <- werr
				return
			}
			exitCh <- ec
		}()
		select {
		case ec := <-exitCh:
			exit = ec
		case werr := <-errCh:
			<-readDone
			m.finalize(job, rb, StateFailed, -1, werr)
			return
		case <-ctx.Done():
			cancelled = true
			kill()
			ec, _ := wait()
			exit = ec
		}
	} else {
		ec, _ := wait()
		exit = ec
	}

	<-readDone

	var next State
	switch {
	case cancelled:
		next = StateCancelled
	case exit == 0:
		next = StateDone
	default:
		next = StateFailed
	}
	m.finalize(job, rb, next, exit, nil)
}

// finalize is the single terminal-transition path: writes the per-
// job output log to disk, persists the end event, transitions the
// Job state, publishes the state update, and drains the queue.
//
// All persistence side effects are best-effort — a missing OutputDir
// or a sick event log degrade to "job finished, but the model won't
// see it" rather than crashing the chat session.
func (m *Manager) finalize(job *Job, rb *RingBuffer, next State, exit int, failErr error) {
	output := rb.Snapshot()
	var outputPath string
	if len(output) > 0 {
		outputPath = m.writeOutputLog(job.ID, output)
	}

	// Atomic-with-the-lock outcome stamp so the End event observes
	// EndedAt + ExitCode internally consistent.
	_ = job.setOutcome(next, exit, failErr)
	snap := job.Snapshot()

	if m.log != nil {
		inline, dropped := TruncateForInline(string(output))
		failMsg := ""
		if failErr != nil {
			failMsg = failErr.Error()
		}
		_, _ = AppendEnd(context.Background(), m.log, EndPayload{
			JobID:          job.ID,
			ExitCode:       exit,
			Duration:       snap.Duration(),
			Cancelled:      next == StateCancelled,
			Backgrounded:   snap.Backgrounded,
			FailErrMsg:     failMsg,
			OutputInline:   inline,
			TruncatedBytes: dropped,
			OutputPath:     outputPath,
		})
	}

	m.publishState(job.ID, next)
	m.onJobTerminal(job.ID)
}

// writeOutputLog persists output under <OutputDir>/<job-id>.log via
// temp + rename so a crash mid-write doesn't leave a partial file.
// Returns the final path on success, "" on failure (the caller
// gracefully degrades — the End event simply lacks an OutputPath).
//
// Mode 0600 because user-shell output may include secrets the user
// echoed (env vars, paths under home dir, etc.); mode 0700 on the
// directory matches the rest of ~/.carlos.
func (m *Manager) writeOutputLog(jobID string, output []byte) string {
	if err := os.MkdirAll(m.outputDir, 0o700); err != nil {
		return ""
	}
	dest := filepath.Join(m.outputDir, jobID+".log")
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return ""
	}
	if _, err := f.Write(output); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return ""
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return ""
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return ""
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return ""
	}
	return dest
}

// onJobTerminal removes the job from running slots + drains the
// queue. Called from runJob; takes the lock internally.
func (m *Manager) onJobTerminal(id string) {
	m.mu.Lock()
	if m.fgRunning == id {
		m.fgRunning = ""
	}
	delete(m.bgRunning, id)
	m.advanceLocked(context.Background())
	m.mu.Unlock()
}

// Cancel requests termination of the named job. If the job is
// pending, it's removed from the queue without ever running and
// transitions to StateCancelled directly. If running, the per-job
// context is cancelled — S1's spawn goroutine watches that ctx and
// reaps the PTY.
//
// Idempotent: cancelling an already-terminal job returns nil.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return ErrUnknownJob
	}
	// Pop from fgQueue if still pending.
	if job.State() == StatePending {
		m.removeFromQueueLocked(id)
		m.mu.Unlock()
		if err := job.transition(StateCancelled); err != nil && !errors.Is(err, ErrInvalidTransition) {
			return err
		}
		return nil
	}
	cancel := job.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Background moves a running foreground job to the background slot,
// freeing the fg queue. The next-queued foreground job picks up in
// the freed slot. Returns ErrUnknownJob or ErrInvalidTransition if
// the job isn't currently the foreground runner.
func (m *Manager) Background(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fgRunning != id {
		return ErrInvalidTransition
	}
	job, ok := m.jobs[id]
	if !ok {
		return ErrUnknownJob
	}
	if len(m.bgRunning) >= m.bgLimit {
		return fmt.Errorf("usershell: background pool full (%d/%d)", len(m.bgRunning), m.bgLimit)
	}
	if err := job.markBackgrounded(true); err != nil {
		return err
	}
	m.fgRunning = ""
	m.bgRunning[id] = struct{}{}
	return nil
}

// Foreground moves a running background job to the foreground slot.
// If a foreground job is already running, it's automatically
// backgrounded (subject to bgLimit). Errors if the job isn't a
// running background job.
func (m *Manager) Foreground(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.bgRunning[id]; !ok {
		return ErrInvalidTransition
	}
	job, ok := m.jobs[id]
	if !ok {
		return ErrUnknownJob
	}
	// If something's currently running in fg, we have to demote it.
	if m.fgRunning != "" {
		incumbent, inOK := m.jobs[m.fgRunning]
		if inOK {
			if len(m.bgRunning) >= m.bgLimit {
				return fmt.Errorf("usershell: cannot swap — bg pool full")
			}
			if err := incumbent.markBackgrounded(true); err != nil {
				return err
			}
			m.bgRunning[m.fgRunning] = struct{}{}
		}
	}
	delete(m.bgRunning, id)
	m.fgRunning = id
	return job.markBackgrounded(false)
}

// Jobs returns a snapshot of every Job the Manager knows about, in
// insertion order. Safe to call concurrently with Submit/Cancel —
// the slice is a fresh copy each call.
// Cwd returns the working directory new jobs will spawn in. Used by
// the chat surface's Phase F-8 footer hint check after an in-band `!cd`
// has updated SetCwd.
func (m *Manager) Cwd() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cwd
}

// SetCwd updates the working directory new jobs spawn in. Used by the
// chat surface to intercept `!cd <path>` so the cwd actually persists
// across foreground shell calls instead of dying with each subshell.
// Already-queued jobs keep the cwd they captured at Submit time.
func (m *Manager) SetCwd(path string) {
	m.mu.Lock()
	m.cwd = path
	m.mu.Unlock()
}

func (m *Manager) Jobs() []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Snapshot, 0, len(m.jobs))
	// Stable order: walk fgQueue first (still pending), then
	// fgRunning, then bgRunning, then completed by SubmittedAt asc.
	seen := map[string]bool{}
	add := func(id string) {
		if seen[id] {
			return
		}
		if j, ok := m.jobs[id]; ok {
			out = append(out, j.Snapshot())
			seen[id] = true
		}
	}
	for _, id := range m.fgQueue {
		add(id)
	}
	add(m.fgRunning)
	for id := range m.bgRunning {
		add(id)
	}
	// Remaining (mostly terminal) — order by SubmittedAt asc for
	// stable output.
	rest := make([]*Job, 0, len(m.jobs))
	for id, j := range m.jobs {
		if !seen[id] {
			rest = append(rest, j)
		}
	}
	sortBySubmitted(rest)
	for _, j := range rest {
		out = append(out, j.Snapshot())
	}
	return out
}

// Get returns the Snapshot of the named job, or ErrUnknownJob.
func (m *Manager) Get(id string) (Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok {
		return Snapshot{}, ErrUnknownJob
	}
	return j.Snapshot(), nil
}

// Subscribe returns a channel that receives Update notifications
// for every job state change + output chunk. The channel buffer is
// generous (256) but non-blocking sends mean a slow consumer will
// drop events — callers that need exact-once delivery should also
// poll Jobs() / Output().
//
// The unsubscribe func must be called when the consumer is done so
// the Manager can free the slot. Safe to call after Manager.Close.
func (m *Manager) Subscribe() (<-chan Update, func()) {
	ch := make(chan Update, 256)
	m.subMu.Lock()
	m.subscribers = append(m.subscribers, ch)
	m.subMu.Unlock()
	unsub := func() {
		m.subMu.Lock()
		defer m.subMu.Unlock()
		for i, c := range m.subscribers {
			if c == ch {
				m.subscribers = append(m.subscribers[:i], m.subscribers[i+1:]...)
				return
			}
		}
	}
	return ch, unsub
}

// Output returns a snapshot of the named job's captured output, or
// nil if no buffer exists yet (job still pending, or unknown id).
// The returned slice is freshly allocated; caller may mutate it.
func (m *Manager) Output(id string) []byte {
	m.mu.RLock()
	rb, ok := m.outputs[id]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return rb.Snapshot()
}

// publish best-effort fans an Update out to every subscriber. A
// full channel drops; we never block the runJob goroutine.
func (m *Manager) publish(u Update) {
	m.subMu.Lock()
	subs := make([]chan Update, len(m.subscribers))
	copy(subs, m.subscribers)
	m.subMu.Unlock()
	for _, c := range subs {
		select {
		case c <- u:
		default:
		}
	}
}

// publishState is the convenience wrapper for state-only updates.
func (m *Manager) publishState(id string, st State) {
	m.publish(Update{JobID: id, State: st})
}

// publishStateLocked is called from advanceLocked which already
// holds m.mu. We still take subMu inside publish.
func (m *Manager) publishStateLocked(id string, st State) {
	m.publish(Update{JobID: id, State: st})
}

// Close cancels every still-running job and prevents further
// Submits. Idempotent. The Manager is unusable after Close.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	cancels := make([]context.CancelFunc, 0, len(m.jobs))
	for _, j := range m.jobs {
		if j.cancel != nil && !j.State().IsTerminal() {
			cancels = append(cancels, j.cancel)
		}
	}
	m.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	return nil
}

// removeFromQueueLocked drops id from the fgQueue if present. Caller
// must hold m.mu.
func (m *Manager) removeFromQueueLocked(id string) {
	out := m.fgQueue[:0]
	for _, qid := range m.fgQueue {
		if qid != id {
			out = append(out, qid)
		}
	}
	m.fgQueue = out
}

// newJobID mints a fresh ULID. Guarded by ulidMu because
// ulid.MonotonicEntropy is not safe for concurrent reads.
func (m *Manager) newJobID() (string, error) {
	m.ulidMu.Lock()
	defer m.ulidMu.Unlock()
	now := m.now()
	u, err := ulid.New(uint64(now.UnixMilli()), m.ulidEntropy)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// trimCommand strips leading/trailing whitespace from the raw input.
// The "!" prefix is the composer's responsibility — by the time the
// command lands here we already know the user wanted shell mode.
func trimCommand(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// sortBySubmitted insertion-sorts jobs by SubmittedAt ascending.
// Insertion sort because N is tiny (the Jobs() slice tops out at
// the number of jobs the user has run in this chat session — dozens,
// not thousands).
func sortBySubmitted(a []*Job) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j].SubmittedAt.Before(a[j-1].SubmittedAt); j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
