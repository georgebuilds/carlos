package agent

import "errors"

// EventKind is a state-machine trigger. Distinct from eventlog.EventType,
// which classifies persisted log rows; EventKind drives state transitions.
//
// (SPEC amendment #1 in the Phase 1 preflight notes recommends renaming this
// to `Trigger` to remove the EventKind/EventType naming collision; deferred
// to a separate refactor so this slice stays focused on behavior, not
// renames.)
type EventKind int

const (
	EvSpawnStarted EventKind = iota
	EvSpawnSucceeded
	EvSpawnFailed
	// EvQueueAdmitted was declared in the preflight skeleton but had no
	// home in the transition function - `queued` is an initial-state
	// assignment owned by the supervisor, not a transition destination
	// (SPEC § Manage mode § State machine; preflight finding).
	EvProviderCallStarted
	EvProviderCallEnded
	EvToolCallBoundary
	EvAwaitingUserInput
	EvUserInputReceived
	EvExternalBlocked
	EvExternalUnblocked
	EvUserPaused
	EvUserResumed
	EvCompactionStarted
	EvCompactionEnded
	EvCancelRequested
	EvDrainComplete
	EvCompletedSuccess
	EvCompletedFailure
	EvHeartbeatLost
)

var ErrIllegalTransition = errors.New("agent: illegal state transition")

// Transition returns the destination state for (s, ev), or
// (s, ErrIllegalTransition) if the pair is not legal. The legal set is the
// SPEC's canonical table plus the post-preflight AMENDs:
//
//   - EvCancelRequested is legal from {running, awaiting-input, blocked,
//     paused-by-user}. A user pressing `stop` on a paused or blocked agent
//     must still be able to cancel it without first un-pausing.
//   - EvHeartbeatLost is legal from every non-terminal state. An agent that
//     stops checking in becomes `orphaned` regardless of which state it was
//     last in (otherwise an agent stuck in `awaiting-input` could never be
//     detected as lost).
//
// `queued` and `spawning` are initial-state assignments - no transition
// writes into them. A retry of a failed agent is a new attempt with a new
// ULID, NOT a transition from `failed` back to `spawning`.
func Transition(s State, ev EventKind) (State, error) {
	// Terminal-sticky guard: nothing escapes a terminal state. The single
	// exception (heartbeat-loss promoting a non-terminal to orphaned) is
	// dispatched per-state below; we still want this guard for cases
	// where the supervisor tries to push a stale event into a terminal
	// row (race during teardown, etc.).
	if s.IsTerminal() {
		return s, ErrIllegalTransition
	}

	// Heartbeat-loss is universal across non-terminal states. Handle
	// before the per-state switch so we don't repeat it in every case.
	if ev == EvHeartbeatLost {
		return StateOrphaned, nil
	}

	switch s {
	case StateSpawning:
		switch ev {
		case EvSpawnSucceeded:
			return StateRunning, nil
		case EvSpawnFailed:
			return StateFailed, nil
		}
	case StateQueued:
		if ev == EvSpawnStarted {
			return StateSpawning, nil
		}
	case StateRunning:
		switch ev {
		case EvAwaitingUserInput:
			return StateAwaitingInput, nil
		case EvExternalBlocked:
			return StateBlocked, nil
		case EvUserPaused:
			return StatePausedByUser, nil
		case EvCompactionStarted:
			return StateCompacting, nil
		case EvCancelRequested:
			return StateCancelling, nil
		case EvCompletedSuccess:
			return StateDone, nil
		case EvCompletedFailure:
			return StateFailed, nil
		}
	case StateAwaitingInput:
		switch ev {
		case EvUserInputReceived:
			return StateRunning, nil
		case EvCancelRequested:
			return StateCancelling, nil
		}
	case StateBlocked:
		switch ev {
		case EvExternalUnblocked:
			return StateRunning, nil
		case EvCancelRequested:
			return StateCancelling, nil
		}
	case StatePausedByUser:
		switch ev {
		case EvUserResumed:
			return StateRunning, nil
		case EvCancelRequested:
			return StateCancelling, nil
		}
	case StateCompacting:
		if ev == EvCompactionEnded {
			return StateRunning, nil
		}
	case StateCancelling:
		if ev == EvDrainComplete {
			return StateDone, nil
		}
		if ev == EvCompletedFailure {
			return StateFailed, nil
		}
	}
	return s, ErrIllegalTransition
}
