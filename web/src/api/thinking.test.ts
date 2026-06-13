// thinking · the indicator predicate, web translation of the TUI's
// isThinking (internal/tui/chat/thinking.go). On between a user submit and
// the first streamed token while the agent is in-flight; off the moment live
// text streams, the turn finishes (including error replies), or the agent
// leaves an in-flight state (failed, blocked on an approval, your turn).

import { describe, it, expect } from 'vitest'
import { isThinking, lastAnchor, latestWireState } from './thinking'
import type { WireEvent, WireState } from './types'

let seq = 0
function ev(kind: string, data: Record<string, unknown> = {}): WireEvent {
  return { seq: ++seq, thread: 't', ts: '', kind, data }
}
function state(s: WireState): WireEvent {
  return ev('state', { state: s })
}
function user(text = 'hi'): WireEvent {
  return ev('user_message', { text })
}

describe('isThinking · headline on/off', () => {
  it('turns on after a user message once the running state lands', () => {
    expect(isThinking([user(), state('running')], '')).toBe(true)
  })

  it('turns on at send via the summary fallback before any state event', () => {
    // fresh thread: the user message rides back over SSE before the first
    // state transition is persisted; the roster summary carries the state.
    expect(isThinking([user()], '', 'running')).toBe(true)
    expect(isThinking([user()], '', 'spawning')).toBe(true)
  })

  it('turns off at the first streamed token (delta buffer non-empty)', () => {
    expect(isThinking([user(), state('running')], 'first tok')).toBe(false)
  })

  it('turns off when the assistant message seals the turn', () => {
    const events = [user(), state('running'), ev('assistant_message', { text: 'done' })]
    expect(isThinking(events, '')).toBe(false)
  })

  it('turns off on an error reply (assistant_message with error: true)', () => {
    const events = [
      user(),
      state('running'),
      ev('assistant_message', { text: 'provider exploded', error: true }),
    ]
    expect(isThinking(events, '')).toBe(false)
  })
})

describe('isThinking · state gate', () => {
  it.each<WireState>(['awaiting_input', 'blocked', 'paused', 'cancelling', 'done', 'failed', 'orphaned', 'queued'])(
    'stays off in non-in-flight state %s',
    (s) => {
      expect(isThinking([user(), state(s)], '')).toBe(false)
    },
  )

  it.each<WireState>(['spawning', 'running', 'compacting'])('paints in in-flight state %s', (s) => {
    expect(isThinking([user(), state(s)], '')).toBe(true)
  })

  it('the latest state event wins over the (stale) summary fallback', () => {
    // summary still says running from the last poll; the wire already
    // delivered failed. The dots must not spin over a dead turn.
    expect(isThinking([user(), state('failed')], '', 'running')).toBe(false)
    // and the inverse: summary lags at awaiting_input, wire says running.
    expect(isThinking([user(), state('running')], '', 'awaiting_input')).toBe(true)
  })

  it('stays off with no state signal at all', () => {
    expect(isThinking([user()], '')).toBe(false)
  })
})

describe('isThinking · transcript anchor', () => {
  it('stays off over an empty transcript even while running', () => {
    expect(isThinking([], '', 'running')).toBe(false)
    expect(isThinking([state('running')], '')).toBe(false)
  })

  it('reappears between tool calls (tool_result anchor)', () => {
    const events = [
      user(),
      state('running'),
      ev('tool_call', { name: 'bash', input: {} }),
      ev('tool_result', { name: 'bash', output_preview: 'ok' }),
    ]
    expect(isThinking(events, '')).toBe(true)
  })

  it('anchors on shell markers (user-shell parity with the TUI)', () => {
    expect(isThinking([user(), ev('shell_start'), state('running')], '')).toBe(true)
    expect(isThinking([user(), ev('shell_end'), state('running')], '')).toBe(true)
  })

  it('skips hairline kinds when scanning for the anchor', () => {
    // state / research_phase / children land after the user message; they
    // must not hide the dots.
    const events = [
      user(),
      state('running'),
      ev('research_phase', { phase: 'plan' }),
      ev('children', { children: [] }),
    ]
    expect(isThinking(events, '')).toBe(true)
  })

  it('stays off right after a session reset (no anchor survives)', () => {
    expect(isThinking([ev('session_reset'), state('running')], '')).toBe(false)
  })
})

describe('latestWireState / lastAnchor helpers', () => {
  it('latestWireState picks the most recent state event', () => {
    const events = [state('spawning'), user(), state('running'), state('compacting')]
    expect(latestWireState(events)).toBe('compacting')
  })

  it('latestWireState returns null when no state event exists', () => {
    expect(latestWireState([user()])).toBeNull()
  })

  it('latestWireState skips a malformed state event with no state field', () => {
    expect(latestWireState([state('running'), ev('state', {})])).toBe('running')
  })

  it('lastAnchor returns null for a transcript of hairline events only', () => {
    expect(lastAnchor([state('running'), ev('children', { children: [] })])).toBeNull()
  })

  it('lastAnchor finds the newest content event', () => {
    const result = ev('tool_result', { name: 'bash' })
    expect(lastAnchor([user(), result, state('running')])).toBe(result)
  })
})
