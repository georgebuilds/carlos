import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import Roster from './Roster.vue'
import { useThreadsStore } from '@/stores/threads'
import type { ThreadSummary } from '@/api/types'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: {
      createGroup: vi.fn().mockResolvedValue({ id: 'g1', name: 'X', pos: 0, threads: 0 }),
      listGroups: vi.fn().mockResolvedValue([]),
      listThreads: vi.fn().mockResolvedValue([]),
      createThread: vi.fn().mockResolvedValue({ id: 'new', backend: 'carlos' }),
    },
  }
})
import { api } from '@/api/client'

function thread(over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id: 't1', title: 't', model: '', state: 'done', attached: false,
    created_at: '', updated_at: '2026-01-01T00:00:00Z', preview: '',
    user_msgs: 0, frame: '', backend: 'carlos', capabilities: {}, ...over,
  }
}

function mountRoster() {
  setActivePinia(createPinia())
  const s = useThreadsStore()
  return { w: mount(Roster), s }
}

describe('Roster · tools', () => {
  beforeEach(() => vi.clearAllMocks())

  it('show-hidden toggle appears only with hidden threads and flips the flag', async () => {
    const { w, s } = mountRoster()
    s.threads = [thread()]
    await flushPromises()
    expect(w.text()).not.toContain('show hidden')

    s.threads = [thread(), thread({ id: 't2', hidden: true })]
    await flushPromises()
    const toggle = w.findAll('button').find((b) => b.text().includes('show hidden'))!
    expect(toggle).toBeTruthy()
    await toggle.trigger('click')
    expect(s.showHidden).toBe(true)
  })

  it('creates a group from the + group inline input', async () => {
    const { w } = mountRoster()
    await w.findAll('button').find((b) => b.text() === '+ group')!.trigger('click')
    const input = w.find('.rt-input')
    await input.setValue('Personal')
    await input.trigger('keyup.enter')
    await flushPromises()
    expect(api.createGroup).toHaveBeenCalledWith('Personal')
  })

  it('shows a no-results hint when the search matches nothing', async () => {
    const { w, s } = mountRoster()
    s.threads = [thread({ title: 'alpha' })]
    s.query = 'zzz'
    await flushPromises()
    expect(w.text()).toContain('no conversations match')
  })

  it('the + new dropdown creates a thread (newThread handler)', async () => {
    const { w } = mountRoster()
    await w.find('.btn-new').trigger('click')
    // fallback lone carlos item is always present
    await w.findAll('button').find((b) => b.text().includes('carlos thread'))!.trigger('click')
    await flushPromises()
    expect(api.createThread).toHaveBeenCalled()
  })

  it('an empty + group input creates nothing', async () => {
    const { w } = mountRoster()
    await w.findAll('button').find((b) => b.text() === '+ group')!.trigger('click')
    await w.find('.rt-input').trigger('keyup.enter')
    await flushPromises()
    expect(api.createGroup).not.toHaveBeenCalled()
  })
})
