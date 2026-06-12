// StageHeader · the per-thread header that absorbed the old "this thread"
// rail card. Covers the persistent row (title, state, frame chip, attach
// control), the details disclosure, and the two-step delete.
//
// Migrated regression (from ThreadMetaCard.test.ts, card since dissolved):
// a detached thread's summary carries frame "" (the frame resolves at
// attach), and the frame chip must render a placeholder dash there, never
// an empty span.

import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import StageHeader from './StageHeader.vue'
import type { ThreadSummary } from '@/api/types'

function thread(over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id: 't1',
    title: 'a thread',
    model: 'claude-fable-5',
    state: 'running',
    attached: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    preview: '',
    user_msgs: 3,
    frame: '',
    backend: 'carlos',
    capabilities: {},
    ...over,
  }
}

describe('StageHeader · persistent row', () => {
  it('renders title and state word', () => {
    const w = mount(StageHeader, { props: { thread: thread({ state: 'awaiting_input' }) } })
    expect(w.find('.s-title').text()).toBe('a thread')
    expect(w.find('.s-state').text()).toBe('your turn')
  })

  it('shows the resolved frame name in the chip for an attached thread', () => {
    const w = mount(StageHeader, {
      props: { thread: thread({ attached: true, frame: 'work' }) },
    })
    expect(w.find('.s-frame').text()).toBe('work')
  })

  it('renders a dash, not an empty chip, when the frame is unresolved', () => {
    const w = mount(StageHeader, {
      props: { thread: thread({ attached: false, frame: '' }) },
    })
    expect(w.find('.s-frame').text()).not.toBe('')
    expect(w.find('.s-frame').text()).toBe('-')
  })
})

describe('StageHeader · attach control', () => {
  it('emits attach for a detached thread', async () => {
    const w = mount(StageHeader, { props: { thread: thread({ attached: false }) } })
    await w.find('.sh-act').trigger('click')
    expect(w.emitted('attach')).toHaveLength(1)
    expect(w.emitted('detach')).toBeUndefined()
  })

  it('emits detach for an attached thread', async () => {
    const w = mount(StageHeader, { props: { thread: thread({ attached: true }) } })
    await w.find('.sh-act').trigger('click')
    expect(w.emitted('detach')).toHaveLength(1)
  })

  it('emits attachForeign for a TUI-owned thread', async () => {
    const w = mount(StageHeader, { props: { thread: thread({ owner: 'tui' }) } })
    expect(w.find('.s-state').text()).toBe('in the TUI')
    await w.find('.sh-act').trigger('click')
    expect(w.emitted('attachForeign')).toHaveLength(1)
  })
})

describe('StageHeader · details disclosure', () => {
  it('hides the meta panel until details is clicked', () => {
    const w = mount(StageHeader, { props: { thread: thread() } })
    expect(w.find('.meta-panel').exists()).toBe(false)
    expect(w.find('.sh-more').attributes('aria-expanded')).toBe('false')
  })

  it('reveals id, model, backend, attached, and messages on open', async () => {
    const w = mount(StageHeader, {
      props: { thread: thread({ attached: true, frame: 'work' }) },
    })
    await w.find('.sh-more').trigger('click')
    expect(w.find('.sh-more').attributes('aria-expanded')).toBe('true')

    const panel = w.find('.meta-panel')
    expect(panel.exists()).toBe(true)
    const text = panel.text()
    expect(text).toContain('t1')
    expect(text).toContain('claude-fable-5')
    expect(text).toContain('carlos')
    expect(text).toContain('yes, this process')
    expect(text).toContain('3')
  })

  it('labels a foreign thread as owned by the TUI in the attached cell', async () => {
    const w = mount(StageHeader, { props: { thread: thread({ owner: 'tui' }) } })
    await w.find('.sh-more').trigger('click')
    expect(w.find('.meta-panel').text()).toContain('no · owned by the TUI')
  })

  it('closes again on a second click', async () => {
    const w = mount(StageHeader, { props: { thread: thread() } })
    await w.find('.sh-more').trigger('click')
    await w.find('.sh-more').trigger('click')
    expect(w.find('.meta-panel').exists()).toBe(false)
  })

  it('closes when the active thread changes', async () => {
    const w = mount(StageHeader, { props: { thread: thread() } })
    await w.find('.sh-more').trigger('click')
    expect(w.find('.meta-panel').exists()).toBe(true)

    await w.setProps({ thread: thread({ id: 't2' }) })
    expect(w.find('.meta-panel').exists()).toBe(false)
  })
})

describe('StageHeader · two-step delete (in the disclosure)', () => {
  async function openPanel(over: Partial<ThreadSummary> = {}) {
    const w = mount(StageHeader, { props: { thread: thread(over) } })
    await w.find('.sh-more').trigger('click')
    return w
  }

  it('arms on the first click without emitting delete', async () => {
    const w = await openPanel()
    await w.find('.btn-delete').trigger('click')
    expect(w.emitted('delete')).toBeUndefined()
    expect(w.find('.delete-confirm').exists()).toBe(true)
  })

  it('emits delete with the thread id on confirm', async () => {
    const w = await openPanel({ id: 'victim' })
    await w.find('.btn-delete').trigger('click')
    await w.find('.btn-ghost.danger').trigger('click')
    expect(w.emitted('delete')).toEqual([['victim']])
    // confirm row disarms after committing
    expect(w.find('.delete-confirm').exists()).toBe(false)
  })

  it('cancel disarms without emitting', async () => {
    const w = await openPanel()
    await w.find('.btn-delete').trigger('click')
    await w.find('.btn-ghost:not(.danger)').trigger('click')
    expect(w.emitted('delete')).toBeUndefined()
    expect(w.find('.delete-confirm').exists()).toBe(false)
    expect(w.find('.btn-delete').exists()).toBe(true)
  })
})
