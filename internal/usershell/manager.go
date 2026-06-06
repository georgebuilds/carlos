package usershell

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
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

	cwd     string
	bgLimit int
	now     func() time.Time

	// jobs holds every Job the Manager has seen, keyed by ID. Stays
	// around after termination so the jobs overlay can render
	// "recent" history; size-bounded by a future GC pass.
	jobs map[string]*Job

	// fgQueue is the foreground waiting list (FIFO). Index 0 is up
	// next. When a foreground slot opens, the Manager pops index 0.
	fgQueue []string

	// fgRunning is the ID of the single foreground job currently
	// executing, or "" if the slot is open.
	fgRunning string

	// bgRunning is the set of background job IDs currently
	// executing. Length is bounded by bgLimit.
	bgRunning map[string]struct{}

	// closed flags whether Close has been called; further Submit
	// calls return ErrClosed.
	closed bool

	// ulidEntropy is the monotonic-random reader for fresh job IDs.
	// Guarded by ulidMu — ulid.MonotonicEntropy is not safe for
	// concurrent reads.
	ulidMu      sync.Mutex
	ulidEntropy *ulid.MonotonicEntropy
}

// ErrNotImplemented is returned from Submit until S1 wires the PTY
// spawn path. Caller-visible so tests can pin the staged delivery
// without having to compile-toggle the feature.
var ErrNotImplemented = errors.New("usershell: spawn not implemented in S0")

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
	return &Manager{
		cwd:         opts.Cwd,
		bgLimit:     bg,
		now:         now,
		jobs:        map[string]*Job{},
		bgRunning:   map[string]struct{}{},
		ulidEntropy: ulid.Monotonic(rand.Reader, 0),
	}
}

// Submit enqueues a new shell command. Returns the freshly-minted
// Job so callers can subscribe to its state OR look up its short
// ID. The Manager mints a ULID, stamps SubmittedAt, captures cwd,
// and adds to the appropriate queue/pool — but does NOT spawn the
// PTY in S0 (that's S1).
//
// On the foreground path: if no foreground job is running, the
// caller's expectation is that the Manager picks this one up
// immediately. S0 just records "ready to run"; S2 wires the actual
// pickup.
//
// On the background path: the job joins the bg pool. S2 enforces
// the bgLimit; S0 just records the intent.
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
	_, cancel := context.WithCancel(ctx)
	job := NewJob(id, command, m.cwd, mode, cancel)
	job.SubmittedAt = m.now().UTC().Truncate(time.Millisecond)
	m.jobs[id] = job
	switch mode {
	case Foreground:
		m.fgQueue = append(m.fgQueue, id)
	case Background:
		// S2 will gate this on bgLimit + actually spawn; S0 just
		// records the intent so Jobs() reports it as "pending".
	}
	m.mu.Unlock()
	return job, ErrNotImplemented
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
