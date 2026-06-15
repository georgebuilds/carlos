import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import GroupSection from './GroupSection.vue'
import type { Group } from '@/api/types'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: {
      patchGroup: vi.fn().mockResolvedValue(undefined),
      deleteGroup: vi.fn().mockResolvedValue(undefined),
      listGroups: vi.fn().mockResolvedValue([]),
      listThreads: vi.fn().mockResolvedValue([]),
    },
  }
})
import { api } from '@/api/client'

const group: Group = { id: 'g1', name: 'Work', pos: 0, threads: 2 }

function mountSection() {
  setActivePinia(createPinia())
  return mount(GroupSection, { props: { group } })
}
function btn(w: ReturnType<typeof mountSection>, text: string) {
  return w.findAll('button').find((b) => b.text() === text)!
}

describe('GroupSection · header actions', () => {
  beforeEach(() => vi.clearAllMocks())

  it('renames a group from the header menu', async () => {
    const w = mountSection()
    await w.find('.g-menu-btn').trigger('click')
    await btn(w, 'rename group').trigger('click')
    const input = w.find('.mm-input')
    expect((input.element as HTMLInputElement).value).toBe('Work') // prefilled
    await input.setValue('Personal')
    await input.trigger('keyup.enter')
    await flushPromises()
    expect(api.patchGroup).toHaveBeenCalledWith('g1', { name: 'Personal' })
  })

  it('two-step deletes a group', async () => {
    const w = mountSection()
    await w.find('.g-menu-btn').trigger('click')
    await btn(w, 'delete group').trigger('click')
    expect(w.text()).toContain('revert to ungrouped')
    await btn(w, 'yes, delete').trigger('click')
    await flushPromises()
    expect(api.deleteGroup).toHaveBeenCalledWith('g1')
  })

  it('the backdrop closes the menu without acting', async () => {
    const w = mountSection()
    await w.find('.g-menu-btn').trigger('click')
    expect(w.find('.g-menu').exists()).toBe(true)
    await w.find('.menu-backdrop').trigger('click')
    expect(w.find('.g-menu').exists()).toBe(false)
    expect(api.deleteGroup).not.toHaveBeenCalled()
  })
})
