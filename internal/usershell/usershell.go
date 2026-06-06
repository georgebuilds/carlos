// Package usershell implements the `!`-prefix feature: shell commands
// the USER types in the chat composer, run in their own context (not
// the agent's sandboxed bash tool), with output added to the
// conversation as context for the next model turn.
//
// # Design refs
//
//   - personal/projects/carlos/research/2026-06-05 How to Make a TUI
//     Feel Awesome in 2026.md — the discoverability + responsiveness
//     principles this surface enforces.
//   - personal/projects/carlos/roadmap.md § "Phase U" — slice breakdown
//     S0…S9. This package is S0+S1+S2 (types, PTY exec, queue/bg pool).
//
// # Mental model
//
// A `!<cmd>` becomes a Job. Jobs flow through a per-session Manager
// that owns:
//
//   - A foreground queue (one running, rest FIFO)
//   - A background pool (up to N concurrent, default 3)
//   - All persistence + cancellation + state notifications
//
// Lifecycle: pending → running → done | failed | cancelled. The
// "backgrounded" flag is orthogonal to running (background jobs are
// running, just not in the foreground slot).
//
// # Why not reuse the bash tool
//
// The agent's bash tool is sandboxed, output-truncated, approval-
// gated, and modeled as a *tool call* in the event log. The `!`
// feature is the user's own shell, no fence, no approval — same
// authority as their terminal. Modeling it as user-shell rather than
// tool-call keeps the chat projection honest about who did what.
package usershell

import (
	"context"
	"errors"
	"sync"
	"time"
)

// State enumerates the job lifecycle. Transitions are validated in
// (*Job).transition — invalid moves return ErrInvalidTransition so a
// caller that thinks it's owning the job loudly discovers a bug.
type State int

const (
	// StatePending — Job is in the queue, hasn't been picked up yet.
	StatePending State = iota
	// StateRunning — process is alive. The Manager flips this when
	// the goroutine has actually spawned the PTY.
	StateRunning
	// StateDone — process exited with code 0.
	StateDone
	// StateFailed — process exited non-zero. ExitCode carries the
	// reason; FailErr may carry a spawn-time error (e.g. shell not
	// found) for cases where there's no exit code to report.
	StateFailed
	// StateCancelled — user requested cancellation; process tree was
	// SIGTERM'd → SIGKILL'd. ExitCode is whatever the OS returned,
	// usually -1 or 130 (SIGINT).
	StateCancelled
)

// String emits the canonical lowercase identifier the event log and
// projections use. Stable across releases — payloads on disk hang off
// these strings.
func (s State) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateRunning:
		return "running"
	case StateDone:
		return "done"
	case StateFailed:
		return "failed"
	case StateCancelled:
		return "cancelled"
	}
	return "unknown"
}

// IsTerminal reports whether s is a final state — no further
// transitions are allowed from here. Used by the Manager to decide
// when a job slot is reclaimable.
func (s State) IsTerminal() bool {
	switch s {
	case StateDone, StateFailed, StateCancelled:
		return true
	}
	return false
}

// Mode controls how Submit places the job: in the foreground queue
// (one running at a time, others wait) or the background pool
// (parallel, capped at N). User picks via Ctrl+Enter at submit time
// or by calling Background(id) on an already-running job.
type Mode int

const (
	// Foreground — at most one foreground job runs at a time; later
	// foreground submissions queue FIFO.
	Foreground Mode = iota
	// Background — parallel pool, capped by Manager config. Multiple
	// background jobs run concurrently.
	Background
)

func (m Mode) String() string {
	switch m {
	case Foreground:
		return "foreground"
	case Background:
		return "background"
	}
	return "unknown"
}

// ErrInvalidTransition is returned from (*Job).transition when the
// caller asks for a move that isn't legal from the current state. The
// state machine is intentionally narrow: legal moves are listed in
// validTransitions below and tested exhaustively.
var ErrInvalidTransition = errors.New("usershell: invalid state transition")

// validTransitions maps each State to the set of States it may move
// to. The map is the source of truth for both Job.transition AND the
// table-driven test that pins every legal AND illegal pair.
var validTransitions = map[State]map[State]bool{
	StatePending:   {StateRunning: true, StateCancelled: true},
	StateRunning:   {StateDone: true, StateFailed: true, StateCancelled: true},
	StateDone:      {},
	StateFailed:    {},
	StateCancelled: {},
}

// Job is the in-memory record of one user-shell command. The Manager
// owns the only mutable copy; readers (TUI, projection) get a
// Snapshot() value-type that they may not mutate.
type Job struct {
	// ID is a ULID minted at Submit time. Stable for the job's
	// lifetime; used as the artifact correlation key and the
	// j<short> shorthand the TUI surfaces in the footer.
	ID string

	// Command is the raw text the user typed after the "!" prefix.
	// Passed verbatim to $SHELL -c — no parsing carlos-side.
	Command string

	// Cwd is the working directory the Manager spawned the shell in.
	// Captured at Submit time, not job-start time, so a `!cd` in a
	// prior job doesn't surprise the user.
	Cwd string

	// Mode records whether the job was submitted as foreground or
	// background. Live "I moved this to bg mid-run" status is
	// tracked separately in Backgrounded.
	Mode Mode

	// Backgrounded is true iff the user moved a running foreground
	// job to the background slot via Ctrl+Z or /bg <id>. Distinct
	// from Mode (which records the original submission intent) so
	// the event log + projections can tell "started in bg" from
	// "promoted to bg".
	Backgrounded bool

	// SubmittedAt is when the user pressed enter on `!cmd`. Used
	// for queue-position sorting + the "j<id> queued 12s ago" hint
	// the footer can render.
	SubmittedAt time.Time

	// StartedAt is when the Manager actually spawned the PTY. Zero
	// while the job is still pending.
	StartedAt time.Time

	// EndedAt is when the job entered a terminal state. Zero while
	// the job is still pending or running.
	EndedAt time.Time

	// ExitCode is the process exit status. 0 for success, non-zero
	// for failure, -1 when the process was killed before producing
	// an exit code (Manager.Cancel during spawn).
	ExitCode int

	// FailErr is a spawn-time error (shell not found, working
	// directory missing, etc.) when the process never produced an
	// exit code. nil otherwise.
	FailErr error

	// state is the current lifecycle position. Access via State()
	// from outside this package; the Manager mutates via transition.
	state State

	// cancel cancels the per-job context. Manager.Cancel calls this;
	// the spawn goroutine watches the context and reaps the PTY.
	cancel context.CancelFunc

	mu sync.RWMutex
}

// NewJob constructs a Job in StatePending. cancel may be nil for jobs
// the caller hasn't yet attached a context to; production callers
// (the Manager) wire a real cancel before transitioning to running.
//
// The ID + SubmittedAt + Cwd + Command + Mode must be set by the
// caller — this is a constructor for the fields, not a policy.
func NewJob(id, command, cwd string, mode Mode, cancel context.CancelFunc) *Job {
	return &Job{
		ID:          id,
		Command:     command,
		Cwd:         cwd,
		Mode:        mode,
		SubmittedAt: time.Now().UTC(),
		state:       StatePending,
		cancel:      cancel,
	}
}

// State returns a snapshot of the current lifecycle position.
// Reads via the internal RWMutex so concurrent callers don't tear.
func (j *Job) State() State {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.state
}

// transition mutates the job state if next is reachable from the
// current state per validTransitions. Returns ErrInvalidTransition
// on a disallowed move so the Manager can log + ignore rather than
// silently corrupting state.
//
// Side effects beyond state are deliberate: StartedAt is stamped on
// the pending→running edge, EndedAt is stamped on any →terminal
// edge. Keeping these atomic with the state change keeps Snapshot()
// internally consistent.
func (j *Job) transition(next State) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	allowed, ok := validTransitions[j.state]
	if !ok {
		return ErrInvalidTransition
	}
	if !allowed[next] {
		return ErrInvalidTransition
	}
	prev := j.state
	j.state = next
	now := time.Now().UTC().Truncate(time.Millisecond)
	if prev == StatePending && next == StateRunning {
		j.StartedAt = now
	}
	if next.IsTerminal() {
		j.EndedAt = now
	}
	return nil
}

// markBackgrounded flips the Backgrounded flag. Idempotent. Returns
// ErrInvalidTransition if the job is not currently running — only
// running jobs can be moved between fg and bg.
func (j *Job) markBackgrounded(bg bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != StateRunning {
		return ErrInvalidTransition
	}
	j.Backgrounded = bg
	return nil
}

// setOutcome atomically writes the post-execution outcome (exit
// code + optional spawn-time error) and transitions to next. Used
// by the Manager's runJob goroutine so the write happens under the
// same lock Snapshot() takes — no race on ExitCode / FailErr reads.
//
// Returns ErrInvalidTransition if next isn't reachable from the
// current state. On error, ExitCode + FailErr are NOT updated so
// the Job's externally-observable state stays internally consistent.
func (j *Job) setOutcome(next State, exitCode int, failErr error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	allowed, ok := validTransitions[j.state]
	if !ok || !allowed[next] {
		return ErrInvalidTransition
	}
	j.ExitCode = exitCode
	j.FailErr = failErr
	j.state = next
	if next.IsTerminal() {
		j.EndedAt = time.Now().UTC().Truncate(time.Millisecond)
	}
	return nil
}

// Snapshot returns a value-copy of the Job's externally-visible
// fields. TUI and projection callers consume Snapshots so a stale
// concurrent read doesn't expose a half-transitioned Job.
type Snapshot struct {
	ID           string
	Command      string
	Cwd          string
	Mode         Mode
	Backgrounded bool
	State        State
	SubmittedAt  time.Time
	StartedAt    time.Time
	EndedAt      time.Time
	ExitCode     int
	FailErrMsg   string
}

// Snapshot captures the Job's current state into a value-type the
// caller may pass around freely.
func (j *Job) Snapshot() Snapshot {
	j.mu.RLock()
	defer j.mu.RUnlock()
	s := Snapshot{
		ID:           j.ID,
		Command:      j.Command,
		Cwd:          j.Cwd,
		Mode:         j.Mode,
		Backgrounded: j.Backgrounded,
		State:        j.state,
		SubmittedAt:  j.SubmittedAt,
		StartedAt:    j.StartedAt,
		EndedAt:      j.EndedAt,
		ExitCode:     j.ExitCode,
	}
	if j.FailErr != nil {
		s.FailErrMsg = j.FailErr.Error()
	}
	return s
}

// Duration returns how long the job has been running, or how long it
// ran for if it's already in a terminal state. Zero for jobs that
// haven't yet started.
func (s Snapshot) Duration() time.Duration {
	if s.StartedAt.IsZero() {
		return 0
	}
	if s.EndedAt.IsZero() {
		return time.Since(s.StartedAt)
	}
	return s.EndedAt.Sub(s.StartedAt)
}
