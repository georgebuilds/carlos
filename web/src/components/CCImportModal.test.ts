import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import CCImportModal from './CCImportModal.vue'
import { useToastStore } from '@/stores/toast'
import type { ThreadSummary } from '@/api/types'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: {
      ccImportable: vi.fn(),
      ccImport: vi.fn(),
      listThreads: vi.fn().mockResolvedValue([]),
    },
  }
})
import { api } from '@/api/client'

function session(id: string, over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id,
    title: id,
    model: 'm',
    state: 'done',
    attached: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: new Date().toISOString(),
    preview: '',
    user_msgs: 0,
    frame: '',
    backend: 'cc',
    capabilities: {},
    ...over,
  }
}

function mountModal() {
  return mount(CCImportModal)
}

describe('CCImportModal', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('shows a loading note while fetching', () => {
    vi.mocked(api.ccImportable).mockReturnValueOnce(new Promise(() => {}))
    const w = mountModal()
    expect(w.text()).toContain('looking for Claude Code sessions')
  })

  it('lists candidates with title and repo once loaded', async () => {
    vi.mocked(api.ccImportable).mockResolvedValueOnce({
      sessions: [session('cc:1', { title: 'old work', repo: { root: '/r/carlos', name: 'carlos' } })],
    })
    const w = mountModal()
    await flushPromises()
    expect(w.text()).toContain('old work')
    expect(w.find('.cc-row-repo').text()).toBe('carlos')
  })

  it('renders age buckets for the candidates (now / m / h / d)', async () => {
    const now = Date.now()
    const at = (ms: number) => new Date(now - ms).toISOString()
    vi.mocked(api.ccImportable).mockResolvedValueOnce({
      sessions: [
        session('a', { title: 'fresh', updated_at: at(0) }),
        session('b', { title: 'mins', updated_at: at(5 * 60_000) }),
        session('c', { title: 'hours', updated_at: at(3 * 3_600_000) }),
        session('d', { title: 'days', updated_at: at(2 * 86_400_000) }),
      ],
    })
    const w = mountModal()
    await flushPromises()
    const text = w.text()
    expect(text).toContain('now')
    expect(text).toContain('5m ago')
    expect(text).toContain('3h ago')
    expect(text).toContain('2d ago')
  })

  it('shows an empty message when there are no candidates', async () => {
    vi.mocked(api.ccImportable).mockResolvedValueOnce({ sessions: [] })
    const w = mountModal()
    await flushPromises()
    expect(w.text()).toContain('no other Claude Code sessions found')
  })

  it('shows an error state with a retry that refetches', async () => {
    vi.mocked(api.ccImportable).mockRejectedValueOnce(new Error('boom'))
    const w = mountModal()
    await flushPromises()
    expect(w.text()).toContain('could not load Claude Code sessions')

    vi.mocked(api.ccImportable).mockResolvedValueOnce({ sessions: [session('cc:1', { title: 'recovered' })] })
    await w.find('.cc-modal-retry').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('recovered')
  })

  it('imports on pick: calls the api, toasts, emits imported + close', async () => {
    vi.mocked(api.ccImportable).mockResolvedValueOnce({ sessions: [session('cc:7', { title: 'pick me' })] })
    const imported = session('cc:7')
    vi.mocked(api.ccImport).mockResolvedValueOnce(imported)
    vi.mocked(api.listThreads).mockResolvedValueOnce([imported])
    const toast = useToastStore()
    const w = mountModal()
    await flushPromises()

    await w.find('.cc-row').trigger('click')
    await flushPromises()

    expect(api.ccImport).toHaveBeenCalledWith('cc:7')
    expect(toast.html).toContain('imported')
    expect(w.emitted('imported')?.[0]).toEqual(['cc:7'])
    expect(w.emitted('close')).toBeTruthy()
  })

  it('toasts and stays open when import fails', async () => {
    vi.mocked(api.ccImportable).mockResolvedValueOnce({ sessions: [session('cc:7', { title: 'pick me' })] })
    vi.mocked(api.ccImport).mockRejectedValueOnce(new Error('nope'))
    const toast = useToastStore()
    const w = mountModal()
    await flushPromises()

    await w.find('.cc-row').trigger('click')
    await flushPromises()

    expect(toast.html).toContain('could not import')
    expect(w.emitted('close')).toBeFalsy()
  })

  it('closes on the backdrop and the × button', async () => {
    vi.mocked(api.ccImportable).mockResolvedValue({ sessions: [] })
    const w = mountModal()
    await flushPromises()
    await w.find('.cc-modal-backdrop').trigger('click')
    await w.find('.cc-modal-x').trigger('click')
    expect(w.emitted('close')?.length).toBe(2)
  })

  it('a second pick is ignored while an import is in flight', async () => {
    vi.mocked(api.ccImportable).mockResolvedValueOnce({
      sessions: [session('cc:1', { title: 'one' }), session('cc:2', { title: 'two' })],
    })
    let resolveImport: (t: ThreadSummary) => void = () => {}
    vi.mocked(api.ccImport).mockReturnValueOnce(
      new Promise<ThreadSummary>((r) => {
        resolveImport = r
      }),
    )
    const w = mountModal()
    await flushPromises()

    const rows = w.findAll('.cc-row')
    await rows[0].trigger('click') // starts import of cc:1
    await rows[1].trigger('click') // ignored: importing in flight
    resolveImport(session('cc:1'))
    await flushPromises()

    expect(api.ccImport).toHaveBeenCalledTimes(1)
    expect(api.ccImport).toHaveBeenCalledWith('cc:1')
  })
})
