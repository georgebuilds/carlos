import { describe, it, expect } from 'vitest'
import { applyEvent, insertEvent } from './transcript'
import type { WireEvent } from '@/api/types'

function freshT() {
  return { events: [] as WireEvent[], seqs: new Set<number>(), delta: '', maxSeq: 0, conn: null }
}

function persisted(seq: number, kind = 'user_message', data: Record<string, unknown> = {}): WireEvent {
  return { seq, thread: 't', ts: '', kind, data }
}

describe('transcript insert ordering and dedupe', () => {
  it('keeps events ordered by seq when delivered out of order', () => {
    const t = freshT()
    insertEvent(t, persisted(3))
    insertEvent(t, persisted(1))
    insertEvent(t, persisted(2))
    expect(t.events.map((e) => e.seq)).toEqual([1, 2, 3])
    expect(t.maxSeq).toBe(3)
  })

  it('drops duplicate seqs (reconnect splice safety)', () => {
    const t = freshT()
    insertEvent(t, persisted(1, 'user_message', { text: 'a' }))
    insertEvent(t, persisted(1, 'user_message', { text: 'b' }))
    insertEvent(t, persisted(2))
    expect(t.events.map((e) => e.seq)).toEqual([1, 2])
    // first write wins; duplicate is dropped, not overwritten
    expect((t.events[0].data as { text: string }).text).toBe('a')
  })

  it('handles interleaved in-order and out-of-order inserts', () => {
    const t = freshT()
    for (const s of [5, 1, 4, 2, 3, 6, 2, 1]) insertEvent(t, persisted(s))
    expect(t.events.map((e) => e.seq)).toEqual([1, 2, 3, 4, 5, 6])
  })

  it('ignores events without a seq', () => {
    const t = freshT()
    insertEvent(t, { thread: 't', ts: '', kind: 'delta', data: { text: 'x' } })
    expect(t.events).toHaveLength(0)
  })
})

describe('delta buffering, seal, and reset', () => {
  it('accumulates delta text in a buffer beside the event list', () => {
    const t = freshT()
    applyEvent(t, { thread: 't', ts: '', kind: 'delta', data: { text: 'Hel' } })
    applyEvent(t, { thread: 't', ts: '', kind: 'delta', data: { text: 'lo' } })
    expect(t.delta).toBe('Hello')
    expect(t.events).toHaveLength(0)
  })

  it('delta_reset clears the buffer without touching events', () => {
    const t = freshT()
    applyEvent(t, persisted(1))
    applyEvent(t, { thread: 't', ts: '', kind: 'delta', data: { text: 'partial' } })
    applyEvent(t, { thread: 't', ts: '', kind: 'delta_reset', data: {} })
    expect(t.delta).toBe('')
    expect(t.events).toHaveLength(1)
  })

  it('assistant_message seals the buffer and lands as a persisted row', () => {
    const t = freshT()
    applyEvent(t, { thread: 't', ts: '', kind: 'delta', data: { text: 'streaming...' } })
    applyEvent(t, persisted(7, 'assistant_message', { text: 'final answer' }))
    expect(t.delta).toBe('')
    expect(t.events).toHaveLength(1)
    expect(t.events[0].kind).toBe('assistant_message')
    expect(t.maxSeq).toBe(7)
  })

  it('session_reset clears events and buffer but records the marker', () => {
    const t = freshT()
    applyEvent(t, persisted(1))
    applyEvent(t, persisted(2))
    applyEvent(t, { thread: 't', ts: '', kind: 'delta', data: { text: 'mid' } })
    applyEvent(t, persisted(3, 'session_reset', {}))
    expect(t.delta).toBe('')
    expect(t.events.map((e) => e.kind)).toEqual(['session_reset'])
    expect(t.maxSeq).toBe(3)
  })
})

describe('transcript store reactivity (regression: get() must return the proxy)', () => {
  it('a Vue effect re-runs when events are ingested through the store', async () => {
    const { createPinia, setActivePinia } = await import('pinia')
    const { effect, nextTick } = await import('vue')
    const { useTranscriptStore } = await import('./transcript')
    setActivePinia(createPinia())
    const store = useTranscriptStore()

    // An effect that tracks the active thread's event count, exactly like the
    // App's `activeEvents` computed does. If get() returns a raw (non-reactive)
    // object, ingest mutates data the effect never sees and `seen` stays 0.
    let seen = -1
    effect(() => {
      seen = store.events('t1').length
    })
    expect(seen).toBe(0)

    store.ingest('t1', { seq: 1, thread: 't1', ts: '', kind: 'user_message', data: { text: 'hi' } })
    await nextTick()
    expect(seen).toBe(1)

    store.ingest('t1', { seq: 2, thread: 't1', ts: '', kind: 'assistant_message', data: { text: 'yo' } })
    await nextTick()
    expect(seen).toBe(2)
  })
})
