// Stage · the gating layer above the pure isThinking predicate. The dots
// only paint on threads this tab is live-streaming (attached, not foreign)
// and never under a pending approval banner, which is its own wait signal;
// double-signaling a blocked tool call would read as "still chewing" when
// carlos is actually waiting on the user.

import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import Stage from './Stage.vue'
import type { ThreadSummary, WireEvent } from '@/api/types'
import type { PendingApproval } from '@/stores/approvals'

function thread(over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id: 't1',
    title: 'a thread',
    model: 'claude-fable-5',
    state: 'running',
    attached: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    preview: '',
    user_msgs: 1,
    frame: 'work',
    backend: 'carlos',
    capabilities: {},
    ...over,
  }
}

function user(seq: number): WireEvent {
  return { seq, thread: 't1', ts: '', kind: 'user_message', data: { text: 'go' } }
}
function stateEv(seq: number, state: string): WireEvent {
  return { seq, thread: 't1', ts: '', kind: 'state', data: { state } }
}

function approval(): PendingApproval {
  return { requestId: 'r1', name: 'bash', input: { command: 'rm -rf /tmp/x' } }
}

function mountStage(over: {
  thread?: Partial<ThreadSummary>
  events?: WireEvent[]
  delta?: string
  approvals?: PendingApproval[]
} = {}) {
  return mount(Stage, {
    props: {
      thread: thread(over.thread),
      events: over.events ?? [user(1), stateEv(2, 'running')],
      delta: over.delta ?? '',
      approvals: over.approvals ?? [],
    },
  })
}

describe('Stage · thinking gating', () => {
  it('paints the dots on an attached running thread awaiting first output', () => {
    const w = mountStage()
    expect(w.find('.thinking').exists()).toBe(true)
  })

  it('hides the dots while an approval is pending (the banner is the signal)', () => {
    const w = mountStage({ approvals: [approval()] })
    expect(w.find('.approval').exists()).toBe(true)
    expect(w.find('.thinking').exists()).toBe(false)
  })

  it('hides the dots on a detached thread (no live stream to trust)', () => {
    const w = mountStage({ thread: { attached: false } })
    expect(w.find('.thinking').exists()).toBe(false)
  })

  it('hides the dots on a foreign (TUI-owned) thread despite a running backfill', () => {
    const w = mountStage({ thread: { owner: 'tui' } })
    expect(w.find('.thinking').exists()).toBe(false)
  })

  it('hides the dots once the wire state leaves in-flight', () => {
    const w = mountStage({ events: [user(1), stateEv(2, 'awaiting_input')] })
    expect(w.find('.thinking').exists()).toBe(false)
  })

  it('hides the dots while live text streams', () => {
    const w = mountStage({ delta: 'token token' })
    expect(w.find('.thinking').exists()).toBe(false)
  })
})
