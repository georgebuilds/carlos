import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import ThreadRow from './ThreadRow.vue'
import type { ThreadSummary } from '@/api/types'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: {
      hideThread: vi.fn().mockResolvedValue(undefined),
      unhideThread: vi.fn().mockResolvedValue(undefined),
      deleteThread: vi.fn().mockResolvedValue({ deleted: 1 }),
      setThreadGroup: vi.fn().mockResolvedValue(undefined),
      createGroup: vi.fn().mockResolvedValue({ id: 'g1', name: 'Work', pos: 0, threads: 0 }),
      listThreads: vi.fn().mockResolvedValue([]),
      listGroups: vi.fn().mockResolvedValue([]),
    },
  }
})
import { api } from '@/api/client'

function thread(over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id: 't1',
    title: 'a thread',
    model: '',
    state: 'done',
    attached: false,
    created_at: '',
    updated_at: '2026-01-01T00:00:00Z',
    preview: '',
    user_msgs: 0,
    frame: '',
    backend: 'carlos',
    capabilities: {},
    ...over,
  }
}

function mountRow(t: ThreadSummary) {
  setActivePinia(createPinia())
  return mount(ThreadRow, { props: { thread: t, active: false } })
}

function btn(w: ReturnType<typeof mountRow>, text: string) {
  return w.findAll('button').find((b) => b.text() === text)!
}

describe('ThreadRow · menu', () => {
  beforeEach(() => vi.clearAllMocks())

  it('opening the menu shows move-to and a backend-aware remove (carlos)', async () => {
    const w = mountRow(thread({ backend: 'carlos' }))
    await w.find('.t-move').trigger('click')
    expect(w.text()).toContain('move to')
    expect(w.text()).toContain('delete conversation')
    expect(w.text()).not.toContain('hide from list')
  })

  it('a cc thread offers hide and delete session', async () => {
    const w = mountRow(thread({ id: 'cc:x', backend: 'cc' }))
    await w.find('.t-move').trigger('click')
    expect(w.text()).toContain('hide from list')
    expect(w.text()).toContain('delete session')
  })

  it('hide calls the api', async () => {
    const w = mountRow(thread({ id: 'cc:x', backend: 'cc' }))
    await w.find('.t-move').trigger('click')
    await btn(w, 'hide from list').trigger('click')
    await flushPromises()
    expect(api.hideThread).toHaveBeenCalledWith('cc:x')
  })

  it('a hidden thread offers restore instead of hide', async () => {
    const w = mountRow(thread({ id: 'cc:x', backend: 'cc', hidden: true }))
    await w.find('.t-move').trigger('click')
    expect(w.text()).toContain('restore to list')
    await btn(w, 'restore to list').trigger('click')
    await flushPromises()
    expect(api.unhideThread).toHaveBeenCalledWith('cc:x')
  })

  it('inline new-group creates the group and moves the thread', async () => {
    const w = mountRow(thread())
    await w.find('.t-move').trigger('click')
    await btn(w, '+ new group…').trigger('click')
    const input = w.find('.mm-input')
    await input.setValue('Work')
    await input.trigger('keyup.enter')
    await flushPromises()
    expect(api.createGroup).toHaveBeenCalledWith('Work')
    expect(api.setThreadGroup).toHaveBeenCalledWith('t1', 'g1')
  })

  it('two-step delete: confirm then delete', async () => {
    const w = mountRow(thread())
    await w.find('.t-move').trigger('click')
    await btn(w, 'delete conversation').trigger('click')
    expect(w.text()).toContain('cannot be undone')
    await btn(w, 'yes, delete').trigger('click')
    await flushPromises()
    expect(api.deleteThread).toHaveBeenCalledWith('t1')
  })

  it('the backdrop closes the menu', async () => {
    const w = mountRow(thread())
    await w.find('.t-move').trigger('click')
    expect(w.find('.move-menu').exists()).toBe(true)
    await w.find('.menu-backdrop').trigger('click')
    expect(w.find('.move-menu').exists()).toBe(false)
  })

  it('move to ungrouped calls assign(null)', async () => {
    const w = mountRow(thread())
    await w.find('.t-move').trigger('click')
    await btn(w, 'ungrouped').trigger('click')
    await flushPromises()
    expect(api.setThreadGroup).toHaveBeenCalledWith('t1', null)
  })

  it('handles a hide failure without throwing (menu still closes)', async () => {
    vi.mocked(api.hideThread).mockRejectedValueOnce(new Error('boom'))
    const w = mountRow(thread({ id: 'cc:x', backend: 'cc' }))
    await w.find('.t-move').trigger('click')
    await btn(w, 'hide from list').trigger('click')
    await flushPromises()
    expect(w.find('.move-menu').exists()).toBe(false)
  })

  it('surfaces a live-thread delete error path', async () => {
    vi.mocked(api.deleteThread).mockRejectedValueOnce(new Error('thread is live'))
    const w = mountRow(thread())
    await w.find('.t-move').trigger('click')
    await btn(w, 'delete conversation').trigger('click')
    await btn(w, 'yes, delete').trigger('click')
    await flushPromises()
    expect(api.deleteThread).toHaveBeenCalledWith('t1')
  })
})
