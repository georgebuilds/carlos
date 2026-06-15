import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import HomeBoard from './HomeBoard.vue'
import RepoSection from './RepoSection.vue'
import ThreadCard from './ThreadCard.vue'
import { useThreadsStore } from '@/stores/threads'
import { useGroupsStore } from '@/stores/groups'
import { useHomeViewStore } from '@/stores/homeview'
import type { Group, ThreadSummary } from '@/api/types'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: { listGroups: vi.fn().mockResolvedValue([]), listThreads: vi.fn().mockResolvedValue([]) },
  }
})

function thread(id: string, over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id,
    title: id,
    model: 'm',
    state: 'done',
    attached: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    preview: '',
    user_msgs: 0,
    frame: '',
    backend: 'carlos',
    capabilities: {},
    ...over,
  }
}

function setup() {
  localStorage.clear()
  setActivePinia(createPinia())
  const threads = useThreadsStore()
  const groups = useGroupsStore()
  const home = useHomeViewStore()
  return { threads, groups, home }
}

describe('HomeBoard', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('shows the empty hint when there are no threads', () => {
    const { threads } = setup()
    threads.threads = []
    const w = mount(HomeBoard)
    expect(w.find('.home-empty').exists()).toBe(true)
    expect(w.text()).toContain('no thread yet')
  })

  it('defaults to the by-repo view and renders repo sections', () => {
    const { threads, home } = setup()
    threads.threads = [
      thread('a', { repo: { root: '/Code/carlos', name: 'carlos' } }),
      thread('b'), // catch-all
    ]
    expect(home.view).toBe('repo')
    const w = mount(HomeBoard)
    const secs = w.findAllComponents(RepoSection)
    expect(secs).toHaveLength(2)
    // first section is the real repo, last is the catch-all
    expect(secs[0].props('group').root).toBe('/Code/carlos')
    expect(secs[secs.length - 1].props('group').root).toBe('')
  })

  it('toggles to the by-group view and persists the choice', async () => {
    const { threads, home } = setup()
    threads.threads = [thread('a', { repo: { root: '/r', name: 'r' } })]
    const w = mount(HomeBoard)

    const byGroup = w.findAll('.ht-btn').find((b) => b.text() === 'By group')!
    await byGroup.trigger('click')

    expect(home.view).toBe('group')
    expect(localStorage.getItem('carlos.web.home.view')).toBe('group')
    // by-group renders the manual-group container, not repo sections
    expect(w.find('.home-groups').exists()).toBe(true)
    expect(w.findAllComponents(RepoSection)).toHaveLength(0)
  })

  it('by-group view lists ungrouped threads and visible groups', async () => {
    const { threads, groups, home } = setup()
    home.setView('group')
    const g: Group = { id: 'g1', name: 'Work', pos: 0, threads: 1 }
    groups.groups = [g]
    threads.threads = [thread('free'), thread('inwork', { group_id: 'g1' })]
    const w = mount(HomeBoard)
    expect(w.text()).toContain('free')
    expect(w.text()).toContain('Work')
  })

  it('selecting a card bubbles select up', async () => {
    const { threads } = setup()
    threads.threads = [thread('a', { repo: { root: '/r', name: 'r' } })]
    const w = mount(HomeBoard)
    await w.findComponent(ThreadCard).find('.thread-card').trigger('click')
    expect(w.emitted('select')?.[0]).toEqual(['a'])
  })
})
