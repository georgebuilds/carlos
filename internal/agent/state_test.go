package agent

import (
	"errors"
	"fmt"
	"testing"
)

// allStates is the canonical 10-state set declared in state.go.
// Listed explicitly (not via a loop bound) so that adding a new state forces
// the test author to consciously include it in every relevant table.
var allStates = []State{
	StateSpawning,
	StateQueued,
	StateRunning,
	StateAwaitingInput,
	StateBlocked,
	StatePausedByUser,
	StateCompacting,
	StateCancelling,
	StateDone,
	StateFailed,
	StateOrphaned,
}

// allEvents enumerates every EventKind currently declared in state.go.
// EvQueueAdmitted was removed in the post-preflight AMEND: queued is an
// initial-state assignment owned by the supervisor, not a transition
// destination, so the trigger had no semantic home.
var allEvents = []EventKind{
	EvSpawnStarted,
	EvSpawnSucceeded,
	EvSpawnFailed,
	EvProviderCallStarted,
	EvProviderCallEnded,
	EvToolCallBoundary,
	EvAwaitingUserInput,
	EvUserInputReceived,
	EvExternalBlocked,
	EvExternalUnblocked,
	EvUserPaused,
	EvUserResumed,
	EvCompactionStarted,
	EvCompactionEnded,
	EvCancelRequested,
	EvDrainComplete,
	EvCompletedSuccess,
	EvCompletedFailure,
	EvHeartbeatLost,
}

func eventName(ev EventKind) string {
	switch ev {
	case EvSpawnStarted:
		return "SpawnStarted"
	case EvSpawnSucceeded:
		return "SpawnSucceeded"
	case EvSpawnFailed:
		return "SpawnFailed"
	case EvProviderCallStarted:
		return "ProviderCallStarted"
	case EvProviderCallEnded:
		return "ProviderCallEnded"
	case EvToolCallBoundary:
		return "ToolCallBoundary"
	case EvAwaitingUserInput:
		return "AwaitingUserInput"
	case EvUserInputReceived:
		return "UserInputReceived"
	case EvExternalBlocked:
		return "ExternalBlocked"
	case EvExternalUnblocked:
		return "ExternalUnblocked"
	case EvUserPaused:
		return "UserPaused"
	case EvUserResumed:
		return "UserResumed"
	case EvCompactionStarted:
		return "CompactionStarted"
	case EvCompactionEnded:
		return "CompactionEnded"
	case EvCancelRequested:
		return "CancelRequested"
	case EvDrainComplete:
		return "DrainComplete"
	case EvCompletedSuccess:
		return "CompletedSuccess"
	case EvCompletedFailure:
		return "CompletedFailure"
	case EvHeartbeatLost:
		return "HeartbeatLost"
	}
	return fmt.Sprintf("EventKind(%d)", int(ev))
}

// legalTransition is one row of the canonical truth table.
// Listing every legal transition (and only those) lets us check both the
// happy path and — by complement — the illegal-pair coverage in one place.
type legalTransition struct {
	from  State
	event EventKind
	to    State
}

// legalTransitions enumerates every (state, event, dest) tuple that the
// current production implementation accepts. This is the spec embodied
// as a table; if state.go diverges, exactly one test in this file will
// fail and pinpoint the disagreement.
var legalTransitions = []legalTransition{
	// spawning
	{StateSpawning, EvSpawnSucceeded, StateRunning},
	{StateSpawning, EvSpawnFailed, StateFailed},
	{StateSpawning, EvHeartbeatLost, StateOrphaned},

	// queued
	{StateQueued, EvSpawnStarted, StateSpawning},
	{StateQueued, EvHeartbeatLost, StateOrphaned},

	// running
	{StateRunning, EvAwaitingUserInput, StateAwaitingInput},
	{StateRunning, EvExternalBlocked, StateBlocked},
	{StateRunning, EvUserPaused, StatePausedByUser},
	{StateRunning, EvCompactionStarted, StateCompacting},
	{StateRunning, EvCancelRequested, StateCancelling},
	{StateRunning, EvCompletedSuccess, StateDone},
	{StateRunning, EvCompletedFailure, StateFailed},
	{StateRunning, EvHeartbeatLost, StateOrphaned},

	// awaiting-input
	{StateAwaitingInput, EvUserInputReceived, StateRunning},
	// AMEND (post-preflight): cancel must work even when paused/blocked
	// so the user's `stop` verb doesn't require an un-pause prelude.
	{StateAwaitingInput, EvCancelRequested, StateCancelling},
	{StateAwaitingInput, EvHeartbeatLost, StateOrphaned},

	// blocked
	{StateBlocked, EvExternalUnblocked, StateRunning},
	{StateBlocked, EvCancelRequested, StateCancelling},
	{StateBlocked, EvHeartbeatLost, StateOrphaned},

	// paused-by-user
	{StatePausedByUser, EvUserResumed, StateRunning},
	{StatePausedByUser, EvCancelRequested, StateCancelling},
	{StatePausedByUser, EvHeartbeatLost, StateOrphaned},

	// compacting
	{StateCompacting, EvCompactionEnded, StateRunning},
	{StateCompacting, EvHeartbeatLost, StateOrphaned},

	// cancelling
	{StateCancelling, EvDrainComplete, StateDone},
	{StateCancelling, EvCompletedFailure, StateFailed},
	{StateCancelling, EvHeartbeatLost, StateOrphaned},

	// terminal states: no legal outgoing transitions
	// (Transition() returns the input state + ErrIllegalTransition for every event.)
}

// TestTransition_AllLegalPairs walks every documented legal (state, event)
// tuple and asserts the destination state matches and no error is returned.
func TestTransition_AllLegalPairs(t *testing.T) {
	for _, lt := range legalTransitions {
		lt := lt
		name := fmt.Sprintf("%s_on_%s", lt.from, eventName(lt.event))
		t.Run(name, func(t *testing.T) {
			got, err := Transition(lt.from, lt.event)
			if err != nil {
				t.Fatalf("legal transition (%s, %s) returned error: %v", lt.from, eventName(lt.event), err)
			}
			if got != lt.to {
				t.Fatalf("legal transition (%s, %s): want dest %s, got %s", lt.from, eventName(lt.event), lt.to, got)
			}
		})
	}
}

// TestTransition_AllIllegalPairs asserts that every (state, event) pair
// NOT present in the legal table is rejected with ErrIllegalTransition AND
// leaves the state unchanged (Transition contract: invalid inputs are
// side-effect-free at the value level).
func TestTransition_AllIllegalPairs(t *testing.T) {
	legal := make(map[[2]int]bool, len(legalTransitions))
	for _, lt := range legalTransitions {
		legal[[2]int{int(lt.from), int(lt.event)}] = true
	}
	for _, s := range allStates {
		for _, ev := range allEvents {
			if legal[[2]int{int(s), int(ev)}] {
				continue
			}
			s, ev := s, ev
			name := fmt.Sprintf("%s_on_%s", s, eventName(ev))
			t.Run(name, func(t *testing.T) {
				got, err := Transition(s, ev)
				if !errors.Is(err, ErrIllegalTransition) {
					t.Fatalf("illegal pair (%s, %s): want ErrIllegalTransition, got err=%v", s, eventName(ev), err)
				}
				if got != s {
					t.Fatalf("illegal pair (%s, %s): state mutated %s -> %s", s, eventName(ev), s, got)
				}
			})
		}
	}
}

// TestTransition_CoverageMatrix is the completeness gate: every one of the
// 10 states must appear as both a source AND a destination of at least one
// legal transition, EXCEPT terminal states (Done/Failed/Orphaned), which by
// design appear only as destinations.
func TestTransition_CoverageMatrix(t *testing.T) {
	srcSeen := map[State]bool{}
	dstSeen := map[State]bool{}
	for _, lt := range legalTransitions {
		srcSeen[lt.from] = true
		dstSeen[lt.to] = true
	}
	for _, s := range allStates {
		isTerm := s.IsTerminal()
		if !isTerm && !srcSeen[s] {
			t.Errorf("state %s is non-terminal but never appears as a source of any legal transition", s)
		}
		if !dstSeen[s] {
			// Spawning has no inbound transition currently. The supervisor
			// is expected to instantiate an agent directly in Spawning;
			// there is no "transition into" it from another tracked state.
			// Queued similarly: agents land in Queued at admit time, not
			// via a state-machine transition. Flag both explicitly so a
			// future reader can re-decide.
			if s == StateSpawning || s == StateQueued {
				t.Logf("note: %s has no inbound state-machine transition; reached via initial-state assignment (see supervisor)", s)
				continue
			}
			t.Errorf("state %s has no inbound legal transition — unreachable", s)
		}
	}
}

// TestTransition_TerminalStatesAreSticky asserts that once an agent reaches
// a terminal state, no event (other than HeartbeatLost, which is itself
// rejected because the agent is already terminal) can move it out.
// This encodes the SPEC invariant: "done → anything" and "failed → running
// without going through spawning" are unrepresentable.
func TestTransition_TerminalStatesAreSticky(t *testing.T) {
	for _, s := range allStates {
		if !s.IsTerminal() {
			continue
		}
		for _, ev := range allEvents {
			s, ev := s, ev
			t.Run(fmt.Sprintf("%s_on_%s", s, eventName(ev)), func(t *testing.T) {
				got, err := Transition(s, ev)
				if !errors.Is(err, ErrIllegalTransition) {
					t.Fatalf("terminal state %s accepted event %s (dest %s, err %v)", s, eventName(ev), got, err)
				}
				if got != s {
					t.Fatalf("terminal state %s mutated to %s under event %s", s, got, eventName(ev))
				}
			})
		}
	}
}

// TestTransition_SpecForbiddenTransitions hard-codes the four SPEC-cited
// "must be unrepresentable" cases as their own named tests, so a regression
// in any one of them produces an obvious, well-named failure.
func TestTransition_SpecForbiddenTransitions(t *testing.T) {
	cases := []struct {
		name  string
		from  State
		event EventKind
	}{
		// "queued → running" (cannot skip spawning)
		{"queued_cannot_skip_to_running_via_SpawnSucceeded", StateQueued, EvSpawnSucceeded},
		{"queued_cannot_skip_to_running_via_CompletedSuccess", StateQueued, EvCompletedSuccess},
		// "done → anything"
		{"done_cannot_resurrect_via_SpawnStarted", StateDone, EvSpawnStarted},
		{"done_cannot_resurrect_via_SpawnSucceeded", StateDone, EvSpawnSucceeded},
		// "failed → running without going through spawning"
		{"failed_cannot_become_running_directly", StateFailed, EvSpawnSucceeded},
		// "paused-by-user → done" (a paused agent did not finish)
		{"paused_cannot_complete_directly", StatePausedByUser, EvCompletedSuccess},
		{"paused_cannot_fail_directly", StatePausedByUser, EvCompletedFailure},
		// "compacting → done directly" (must return to running first)
		{"compacting_cannot_complete_directly", StateCompacting, EvCompletedSuccess},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got, err := Transition(c.from, c.event)
			if !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("SPEC-forbidden (%s, %s) was accepted (dest=%s, err=%v)", c.from, eventName(c.event), got, err)
			}
			if got != c.from {
				t.Fatalf("SPEC-forbidden (%s, %s) mutated state to %s", c.from, eventName(c.event), got)
			}
		})
	}
}
