// Slice 3b: OTP-style restart intensity counter.
//
// SPEC § Manage mode § Safety rails calls out the OTP "restart intensity"
// pattern: more than MaxR retries within MaxT seconds trips a
// circuit-breaker that terminates the subtree and surfaces a loud alert.
// Anthropic's reference architecture lacks this; carlos does not.
//
// Implementation: per-agent-id ring-buffer of attempt timestamps,
// guarded by the supervisor's mutex (shared with the active-children
// map; the two structures are touched by the same Spawn/Retry/complete
// code paths so one lock keeps things simple). We trim old timestamps
// (older than restartMaxT) on each check so the slice doesn't grow
// unbounded for a flapping agent over a long session.
//
// What's implemented in slice 3b:
//   - recordRetry(id) appends a timestamp.
//   - retryCount(id) returns the count of attempts in the last
//     restartMaxT window AND trims stale entries.
//   - circuitBroken(id) flags the agent (and, eventually, its
//     subtree — see TODO) as broken. Slice 4 will surface this as a
//     roster badge.
//
// What's deliberately TODO'd for slice 4:
//   - Actual subtree termination on breaker trip. Today the breaker
//     trip is recorded as the agent's `circuitBroken` flag; the TUI
//     will render the badge and the user decides whether to abandon
//     the subtree. A future slice can promote this to automatic
//     cancellation of all descendants (requires the parent-walk in
//     depth.go to operate in reverse, descending from the breaker
//     agent through the projection cache).
package agent

import "time"

// retryAttempts is the per-agent timestamp ring. Held under
// Supervisor.mu (same mutex as the active-children map).
type retryAttempts struct {
	stamps []time.Time
	broken bool // set true once the breaker has tripped
}

// recordRetry appends ts to the agent's attempt list. Caller MUST hold
// s.mu.
func (s *Supervisor) recordRetry(id string, ts time.Time) {
	if s.retries == nil {
		s.retries = map[string]*retryAttempts{}
	}
	r, ok := s.retries[id]
	if !ok {
		r = &retryAttempts{}
		s.retries[id] = r
	}
	r.stamps = append(r.stamps, ts)
}

// retryCount returns the number of attempts recorded within the last
// restartMaxT window, relative to `now`. Trims older entries as a
// side-effect so the slice doesn't grow unbounded across a long-lived
// session. Caller MUST hold s.mu.
func (s *Supervisor) retryCount(id string, now time.Time) int {
	r, ok := s.retries[id]
	if !ok {
		return 0
	}
	cutoff := now.Add(-s.restartMaxT)
	// Trim. Stamps are append-only in order, so first-stale-onward is
	// the cutoff point.
	keep := 0
	for _, ts := range r.stamps {
		if ts.After(cutoff) || ts.Equal(cutoff) {
			break
		}
		keep++
	}
	if keep > 0 {
		r.stamps = append(r.stamps[:0], r.stamps[keep:]...)
	}
	return len(r.stamps)
}

// markCircuitBroken flips the per-agent breaker flag. Caller MUST hold
// s.mu. Idempotent. Slice 4 surfaces this as a roster badge AND (TODO)
// cancels descendants.
func (s *Supervisor) markCircuitBroken(id string) {
	if s.retries == nil {
		s.retries = map[string]*retryAttempts{}
	}
	r, ok := s.retries[id]
	if !ok {
		r = &retryAttempts{}
		s.retries[id] = r
	}
	r.broken = true
	// Slice 4 surfaces this as a roster badge and cancels descendants.
}

// IsCircuitBroken reports whether the breaker has tripped for id.
// Exposed (PascalCase) so tests + the eventual TUI can read it.
func (s *Supervisor) IsCircuitBroken(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.retries[id]
	if !ok {
		return false
	}
	return r.broken
}
