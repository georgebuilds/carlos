import { describe, it, expect, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import RosterHeader from './RosterHeader.vue'
import { useConnectionStore } from '@/stores/connection'

function mountHeader(agents?: unknown) {
  setActivePinia(createPinia())
  const conn = useConnectionStore()
  if (agents !== undefined) conn.meta = { agents } as never
  return mount(RosterHeader, { props: { count: 3 } })
}
function btn(w: ReturnType<typeof mountHeader>, text: string) {
  return w.findAll('button').find((b) => b.text().includes(text))!
}

const AGENTS = [
  { name: 'carlos', display: 'carlos thread', can_create: true },
  { name: 'cc', display: 'Claude Code', can_create: true },
]

describe('RosterHeader · new dropdown', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('lists carlos first then detected agents, only creatable ones', () => {
    const w = mountHeader([
      ...AGENTS,
      { name: 'opencode', display: 'opencode', can_create: false },
    ])
    return w.find('.btn-new').trigger('click').then(() => {
      expect(w.text()).toContain('carlos thread')
      expect(w.text()).toContain('Claude Code')
      expect(w.text()).not.toContain('opencode') // can_create false, filtered
    })
  })

  it('picking carlos emits new() with no backend (default)', async () => {
    const w = mountHeader(AGENTS)
    await w.find('.btn-new').trigger('click')
    await btn(w, 'carlos thread').trigger('click')
    expect(w.emitted('new')?.[0]).toEqual([undefined])
  })

  it('picking Claude Code emits new("cc")', async () => {
    const w = mountHeader(AGENTS)
    await w.find('.btn-new').trigger('click')
    await btn(w, 'Claude Code').trigger('click')
    expect(w.emitted('new')?.[0]).toEqual(['cc'])
  })

  it('falls back to a lone carlos item when meta has no agents', async () => {
    const w = mountHeader() // no meta
    await w.find('.btn-new').trigger('click')
    expect(w.text()).toContain('carlos thread')
  })

  it('offers an "open existing CC session" entry that emits importCc', async () => {
    const w = mountHeader(AGENTS)
    await w.find('.btn-new').trigger('click')
    const item = w.find('.nm-import')
    expect(item.text()).toContain('open existing CC session')
    await item.trigger('click')
    expect(w.emitted('importCc')).toBeTruthy()
  })
})
