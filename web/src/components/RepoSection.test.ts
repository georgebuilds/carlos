import { describe, it, expect, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import RepoSection from './RepoSection.vue'
import ThreadCard from './ThreadCard.vue'
import type { RepoGroup } from '@/stores/threads'
import type { ThreadSummary } from '@/api/types'

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

function mountSection(group: RepoGroup) {
  setActivePinia(createPinia())
  return mount(RepoSection, { props: { group } })
}

describe('RepoSection', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('renders the repo name, path and member count', () => {
    const w = mountSection({
      root: '/Code/carlos',
      name: 'carlos',
      threads: [thread('a'), thread('b')],
    })
    expect(w.find('.repo-name').text()).toBe('carlos')
    expect(w.find('.repo-path').text()).toBe('/Code/carlos')
    expect(w.find('.repo-count').text()).toBe('2')
    expect(w.findAllComponents(ThreadCard)).toHaveLength(2)
  })

  it('drops the path and adds the nogit class for the catch-all', () => {
    const w = mountSection({ root: '', name: 'No repository', threads: [thread('a')] })
    expect(w.find('.repo-path').exists()).toBe(false)
    expect(w.find('.repo-sec').classes()).toContain('nogit')
  })

  it('bubbles a card select up', async () => {
    const w = mountSection({ root: '/r', name: 'r', threads: [thread('x')] })
    await w.findComponent(ThreadCard).find('.thread-card').trigger('click')
    expect(w.emitted('select')?.[0]).toEqual(['x'])
  })
})
