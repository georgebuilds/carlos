import { describe, it, expect, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import ThreadCard from './ThreadCard.vue'
import type { ThreadSummary } from '@/api/types'

function thread(over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id: 't1',
    title: 'a title',
    model: 'm',
    state: 'running',
    attached: true,
    created_at: '2026-06-15T11:00:00Z',
    updated_at: new Date().toISOString(),
    preview: 'a preview line',
    user_msgs: 4,
    frame: 'personal',
    backend: 'carlos',
    capabilities: {},
    ...over,
  }
}

function mountCard(over: Partial<ThreadSummary> = {}) {
  setActivePinia(createPinia())
  return mount(ThreadCard, { props: { thread: thread(over) } })
}

describe('ThreadCard', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('renders title, preview, frame, msg count and the state word', () => {
    const w = mountCard()
    expect(w.text()).toContain('a title')
    expect(w.text()).toContain('a preview line')
    expect(w.find('.tc-frame').text()).toBe('personal')
    expect(w.text()).toContain('4 msgs')
    expect(w.find('.tc-state').text()).toBe('running')
  })

  it('marks the dot live for a running thread', () => {
    const w = mountCard({ state: 'running' })
    expect(w.find('.tc-dot').classes()).toContain('live')
  })

  it('a done thread is not live', () => {
    const w = mountCard({ state: 'done' })
    expect(w.find('.tc-dot').classes()).not.toContain('live')
  })

  it('omits the frame chip when there is no frame', () => {
    const w = mountCard({ frame: '' })
    expect(w.find('.tc-frame').exists()).toBe(false)
  })

  it('carries the backend data-attribute on the avatar', () => {
    const w = mountCard({ backend: 'cc' })
    expect(w.find('.tc-avatar').attributes('data-backend')).toBe('cc')
  })

  it('emits select with the thread id on click', async () => {
    const w = mountCard()
    await w.find('.thread-card').trigger('click')
    expect(w.emitted('select')?.[0]).toEqual(['t1'])
  })

  it('renders rel-time buckets (now / m / h / d)', () => {
    const now = Date.now()
    const at = (ms: number) => new Date(now - ms).toISOString()
    expect(mountCard({ updated_at: at(0) }).text()).toContain('now')
    expect(mountCard({ updated_at: at(5 * 60_000) }).text()).toContain('5m ago')
    expect(mountCard({ updated_at: at(3 * 3_600_000) }).text()).toContain('3h ago')
    expect(mountCard({ updated_at: at(2 * 86_400_000) }).text()).toContain('2d ago')
  })
})
