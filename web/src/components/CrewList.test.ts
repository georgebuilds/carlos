// CrewList · the crew rail's per-child rows. Two regressions pinned here:
//
//  1. Layout: the title must truncate INSIDE the card (block-level
//     .c-title inside a min-width-0 .c-body) and the cost must be a
//     flex sibling occupying reserved space - never an overlay painted
//     on top of the title.
//  2. Meta row: an empty field hides together with its separator, so a
//     child with no last tool reads "done · 0 tok" - never the
//     "· · 0 tok" orphan-dot string the bug report showed.

import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import CrewList from './CrewList.vue'
import type { ChildSnapshot } from '@/api/types'

function child(over: Partial<ChildSnapshot> = {}): ChildSnapshot {
  return {
    id: 'c1',
    state: 'done',
    title: 'Generate a detailed report of a mysterious deep-sea creature',
    last_tool: 'read_file',
    tokens: 48000,
    cost_cents: 31,
    started_at: '2026-06-12T00:00:00Z',
    ...over,
  }
}

describe('CrewList · row structure (truncation contract)', () => {
  it('keeps title and meta inside .c-body with the cost as a flex sibling', () => {
    const w = mount(CrewList, { props: { children: [child()] } })
    const item = w.find('.crew-item')
    expect(item.exists()).toBe(true)

    // The title lives inside the shrinkable body, not loose in the row.
    const body = item.find('.c-body')
    expect(body.exists()).toBe(true)
    expect(body.find('.c-title').exists()).toBe(true)
    expect(body.find('.c-sub').exists()).toBe(true)

    // The cost is a SIBLING of the body (flex reserves its width); it
    // must never live inside the body or overlay the title.
    const cost = item.find('.c-cost')
    expect(cost.exists()).toBe(true)
    expect(body.find('.c-cost').exists()).toBe(false)
    expect(cost.element.parentElement).toBe(item.element)
  })

  it('renders the full wire fields into the right slots', () => {
    const w = mount(CrewList, { props: { children: [child()] } })
    expect(w.find('.c-title').text()).toBe(
      'Generate a detailed report of a mysterious deep-sea creature',
    )
    expect(w.find('.c-sub').text()).toBe('done · read_file · 48k tok')
    expect(w.find('.c-cost').text()).toBe('$0.31')
  })

  it('formats sub-1k token counts and zero cost honestly', () => {
    const w = mount(CrewList, {
      props: { children: [child({ tokens: 950, cost_cents: 0 })] },
    })
    expect(w.find('.c-sub').text()).toBe('done · read_file · 950 tok')
    expect(w.find('.c-cost').text()).toBe('$0.00')
  })
})

describe('CrewList · meta row hides empty fields with their separators', () => {
  it('drops the last-tool slot when the child never ran a tool', () => {
    const w = mount(CrewList, {
      props: { children: [child({ last_tool: '', tokens: 0 })] },
    })
    const sub = w.find('.c-sub').text()
    expect(sub).toBe('done · 0 tok')
    expect(sub).not.toContain('· ·')
  })

  it('never emits orphan separators even when state AND tool are empty', () => {
    const w = mount(CrewList, {
      props: {
        children: [child({ state: '' as ChildSnapshot['state'], last_tool: '', tokens: 0 })],
      },
    })
    const sub = w.find('.c-sub').text()
    expect(sub).toBe('0 tok')
    expect(sub.startsWith('·')).toBe(false)
    expect(sub).not.toContain('· ·')
  })

  it('keeps every separator when every field is present', () => {
    const w = mount(CrewList, {
      props: { children: [child({ state: 'running', last_tool: 'bash', tokens: 1500 })] },
    })
    expect(w.find('.c-sub').text()).toBe('running · bash · 2k tok')
  })
})

describe('CrewList · empty crew', () => {
  it('shows the explainer instead of rows', () => {
    const w = mount(CrewList, { props: { children: [] } })
    expect(w.find('.crew-item').exists()).toBe(false)
    expect(w.find('.crew-empty').text()).toContain('No sub-agents on this thread')
  })
})
