package chat

import (
	"unicode/utf8"

	"github.com/georgebuilds/carlos/internal/theme"
)

// Typewriter reveal for streaming assistant text (slice 9b).
//
// Provider deltas land in the TextSource in bursts (network-sized
// chunks, not tokens), so painting Get() raw makes the live row jump
// several words at a time. Instead the view keeps a revealed-rune
// cursor that advances on every 33ms textTick with catch-up pacing:
// the more buffered text waits behind the cursor, the bigger the step,
// so the reveal trails a steady stream by well under a second and
// never stalls. When the turn seals (EvtAssistantMessage) the
// transcript entry renders in full immediately - the reveal cursor
// only ever gates the LIVE buffer.
//
// Subtle is the goal: no cursor block, no styling change; just the
// paced reveal.

// reducedMotion caches PREFERS_REDUCED_MOTION at process start - the
// repo-wide reduced-motion convention introduced alongside this slice
// (see theme.ReducedMotion). When set, the typewriter is disabled
// (live text appears exactly as it arrives, the pre-9b behavior) and
// the thinking-row dots render their static variant. Package var, not
// const, so tests can flip it.
var reducedMotion = theme.ReducedMotion(nil)

const (
	// typewriterMinStep is the floor on revealed runes per tick. At the
	// 33ms textTick cadence 2 runes/tick = about 60 chars/sec - fast
	// enough to read as typing, slow enough to read as motion.
	typewriterMinStep = 2

	// typewriterCatchupDiv scales the step with backlog: step grows by
	// one rune per this many runes of unrevealed text. With a steady
	// provider stream of about 150 chars/sec the cursor settles around
	// 12 runes behind the head (2 + 12/4 = 5 runes/tick = the stream
	// rate), i.e. under 100ms of visual lag. Backlog decays by a
	// quarter per tick, so even a 4KB burst is fully revealed in
	// about 25 ticks (~830ms) - the reveal never falls far behind and
	// never stalls.
	typewriterCatchupDiv = 4
)

// advanceReveal returns the next revealed-rune cursor given the
// current cursor and the total rune count of the live buffer. Pure
// function - the pacing policy in one testable place.
//
//   - empty buffer resets the cursor (turn sealed / idle);
//   - cursor beyond total means the buffer was reset and a new turn
//     started streaming: restart the reveal from the top;
//   - otherwise advance by typewriterMinStep plus a backlog-
//     proportional catch-up term, clamped to total.
func advanceReveal(revealed, total int) int {
	if total <= 0 {
		return 0
	}
	if revealed > total {
		revealed = 0
	}
	backlog := total - revealed
	step := typewriterMinStep + backlog/typewriterCatchupDiv
	if step >= backlog {
		return total
	}
	return revealed + step
}

// revealPrefix returns the first n runes of s. n at or past the rune
// count returns s itself (no allocation); n <= 0 returns "".
func revealPrefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		// len(s) bytes >= rune count, so n covers everything.
		return s
	}
	seen := 0
	for i := range s {
		if seen == n {
			return s[:i]
		}
		seen++
	}
	return s
}

// advanceTypewriter moves the reveal cursor toward the live buffer's
// head. Called from the textTickMsg handler so pacing is tick-driven
// (event-triggered rerenders repaint at the current cursor without
// advancing it). No-op under reduced motion - liveReveal shows the
// full buffer in that mode, so the cursor is never consulted.
func (m *Model) advanceTypewriter() {
	if reducedMotion || m.source == nil {
		return
	}
	m.typeRevealed = advanceReveal(m.typeRevealed, utf8.RuneCountInString(m.source.Get(m.agentID)))
}

// liveReveal gates the live assistant buffer behind the typewriter
// cursor. Full text under reduced motion; sealed turns never pass
// through here, so they always render complete (snap-on-seal).
func (m *Model) liveReveal(full string) string {
	if reducedMotion {
		return full
	}
	return revealPrefix(full, m.typeRevealed)
}
