// carlos web · thinking-indicator derivation.
// Web translation of the TUI activity indicator (internal/tui/chat/thinking.go):
// the gap between a user submission and the first streamed token reads as
// frozen without a sign of life in the reply position. The TUI derives its
// predicate from the projection state plus the transcript tail; here the same
// three questions are asked of the wire-event list and the live delta buffer.
//
//   1. Is the agent in-flight? (latest `state` event, falling back to the
//      roster summary when no state event has landed yet, e.g. the very first
//      send on a fresh thread)
//   2. Is live text already streaming? (the delta buffer is its own alive
//      signal; the dots would compete with it)
//   3. Is the transcript tail something the model is responding to? (a user
//      message, tool call/result, or shell marker. Never an assistant
//      message: dots under a finished turn read as stalling, and an error
//      reply lands as assistant_message too, which is what dismisses the
//      indicator on failure)

import type { WireEvent, WireState } from './types'

// In-flight wire states, mirroring the TUI's Spawning / Running / Compacting
// gate. blocked (approval wait), awaiting_input, failed, done etc. all read
// as "not thinking".
const THINKING_STATES: ReadonlySet<string> = new Set(['spawning', 'running', 'compacting'])

// Transcript kinds that anchor the indicator: the model is responding to
// these. assistant_message is in the set so the tail scan stops on it (and
// then answers "no"); hairline kinds (state, research_phase, children,
// approval_resolved) are skipped so a state event landing after the user
// message does not hide the dots.
const ANCHOR_KINDS: ReadonlySet<string> = new Set([
  'user_message',
  'assistant_message',
  'tool_call',
  'tool_result',
  'shell_start',
  'shell_end',
])

// latestWireState scans backward for the most recent persisted `state` event.
// Null when none has landed (fresh thread before the first transition).
export function latestWireState(events: WireEvent[]): WireState | null {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].kind === 'state') {
      const d = events[i].data as { state?: WireState }
      if (d.state) return d.state
    }
  }
  return null
}

// lastAnchor returns the most recent transcript event the model could be
// responding to (or finishing with), skipping hairline/meta kinds.
export function lastAnchor(events: WireEvent[]): WireEvent | null {
  for (let i = events.length - 1; i >= 0; i--) {
    if (ANCHOR_KINDS.has(events[i].kind)) return events[i]
  }
  return null
}

// isThinking decides whether the transcript should paint the activity row in
// the assistant reply position. Pure over (events, delta, fallbackState) so
// tests drive it directly; callers layer ownership/approval gating on top.
export function isThinking(
  events: WireEvent[],
  delta: string,
  fallbackState?: WireState,
): boolean {
  const state = latestWireState(events) ?? fallbackState ?? null
  if (!state || !THINKING_STATES.has(state)) return false
  if (delta !== '') return false
  const anchor = lastAnchor(events)
  if (!anchor) return false // no dots over a blank transcript
  return anchor.kind !== 'assistant_message'
}
