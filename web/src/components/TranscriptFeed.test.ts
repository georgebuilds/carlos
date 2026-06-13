// TranscriptFeed · placement of the thinking indicator in the feed. The
// dots occupy the position the assistant reply will land in, never beside
// live streaming text (StreamBlock owns that signal), and they remount per
// transcript entry so the elapsed clock resets, matching the TUI's "time
// since the last transcript entry" anchor.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import TranscriptFeed from './TranscriptFeed.vue'
import type { WireEvent } from '@/api/types'

function user(seq: number, text = 'hi'): WireEvent {
  return { seq, thread: 't', ts: '', kind: 'user_message', data: { text } }
}
function toolResult(seq: number): WireEvent {
  return {
    seq,
    thread: 't',
    ts: '',
    kind: 'tool_result',
    data: { name: 'bash', output_preview: 'ok', is_error: false, truncated: false },
  }
}

describe('TranscriptFeed · thinking placement', () => {
  it('renders the indicator after the rows when thinking is on', () => {
    const w = mount(TranscriptFeed, {
      props: { events: [user(1)], delta: '', thinking: true },
    })
    expect(w.find('.thinking').exists()).toBe(true)
    // it sits in the reply position: after the user bubble in DOM order.
    const feed = w.find('.feed').element
    expect(feed.lastElementChild?.classList.contains('thinking')).toBe(true)
  })

  it('hides the indicator when thinking is off', () => {
    const w = mount(TranscriptFeed, {
      props: { events: [user(1)], delta: '', thinking: false },
    })
    expect(w.find('.thinking').exists()).toBe(false)
  })

  it('never paints beside streaming text: the StreamBlock wins', () => {
    const w = mount(TranscriptFeed, {
      props: { events: [user(1)], delta: 'first tokens', thinking: true },
    })
    expect(w.find('.thinking').exists()).toBe(false)
    expect(w.find('.caret').exists()).toBe(true)
  })

  it('defaults to no indicator when the prop is omitted', () => {
    const w = mount(TranscriptFeed, { props: { events: [user(1)], delta: '' } })
    expect(w.find('.thinking').exists()).toBe(false)
  })

  it('appears when thinking flips on mid-session (the send moment)', async () => {
    const assistant: WireEvent = {
      seq: 2,
      thread: 't',
      ts: '',
      kind: 'assistant_message',
      data: { text: 'earlier reply', error: false },
    }
    const w = mount(TranscriptFeed, {
      props: { events: [user(1), assistant], delta: '', thinking: false },
    })
    expect(w.find('.thinking').exists()).toBe(false)
    await w.setProps({ events: [user(1), assistant, user(3)], thinking: true })
    expect(w.find('.thinking').exists()).toBe(true)
  })
})

describe('TranscriptFeed · elapsed clock resets per transcript entry', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('a new persisted event remounts the indicator and zeroes the timer', async () => {
    const w = mount(TranscriptFeed, {
      props: { events: [user(1)], delta: '', thinking: true },
    })
    await vi.advanceTimersByTimeAsync(5000)
    expect(w.find('.t-elapsed').text()).toBe('5s')

    // a tool result lands; the wait the user cares about starts over.
    await w.setProps({ events: [user(1), toolResult(2)] })
    expect(w.find('.thinking').exists()).toBe(true)
    expect(w.find('.t-elapsed').exists()).toBe(false)

    await vi.advanceTimersByTimeAsync(3000)
    expect(w.find('.t-elapsed').text()).toBe('3s')
  })
})
