import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { groupByRepo, NO_REPO_LABEL, useThreadsStore } from './threads'
import type { ThreadSummary } from '@/api/types'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: {
      listThreads: vi.fn().mockResolvedValue([]),
      ccImportable: vi.fn(),
      ccImport: vi.fn(),
    },
  }
})
import { api } from '@/api/client'

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

const repo = (root: string, name: string) => ({ root, name })

describe('groupByRepo selector', () => {
  it('returns no groups for an empty list', () => {
    expect(groupByRepo([])).toEqual([])
  })

  it('groups a single repo into one section', () => {
    const out = groupByRepo([
      thread('a', { repo: repo('/Code/carlos', 'carlos') }),
      thread('b', { repo: repo('/Code/carlos', 'carlos') }),
    ])
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ root: '/Code/carlos', name: 'carlos' })
    expect(out[0].threads.map((t) => t.id)).toEqual(['a', 'b'])
  })

  it('folds repo-less threads into a "No repository" catch-all that sorts last', () => {
    const out = groupByRepo([
      thread('orphan'), // no repo
      thread('a', { repo: repo('/Code/carlos', 'carlos') }),
    ])
    expect(out).toHaveLength(2)
    expect(out[0].root).toBe('/Code/carlos')
    const last = out[out.length - 1]
    expect(last.root).toBe('')
    expect(last.name).toBe(NO_REPO_LABEL)
    expect(last.threads.map((t) => t.id)).toEqual(['orphan'])
  })

  it('orders repo sections by newest member activity, newest first', () => {
    const out = groupByRepo([
      thread('old', { repo: repo('/r/anneal', 'anneal'), updated_at: '2026-01-01T00:00:00Z' }),
      thread('new', { repo: repo('/r/carlos', 'carlos'), updated_at: '2026-06-01T00:00:00Z' }),
    ])
    expect(out.map((g) => g.root)).toEqual(['/r/carlos', '/r/anneal'])
  })

  it('sorts threads within a section by updated_at desc', () => {
    const out = groupByRepo([
      thread('older', { repo: repo('/r', 'r'), updated_at: '2026-01-01T00:00:00Z' }),
      thread('newer', { repo: repo('/r', 'r'), updated_at: '2026-02-01T00:00:00Z' }),
    ])
    expect(out[0].threads.map((t) => t.id)).toEqual(['newer', 'older'])
  })

  it('keeps the catch-all last even when it has the newest activity', () => {
    const out = groupByRepo([
      thread('orphan', { updated_at: '2030-01-01T00:00:00Z' }),
      thread('a', { repo: repo('/r/carlos', 'carlos'), updated_at: '2026-01-01T00:00:00Z' }),
    ])
    expect(out[out.length - 1].root).toBe('')
  })

  it('falls back to the root as name when repo.name is missing', () => {
    const out = groupByRepo([thread('a', { repo: { root: '/lonely', name: '' } })])
    expect(out[0].name).toBe('/lonely')
  })
})

describe('threads store · byRepo computed', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('groups visible threads and drops sub-agents + hidden + non-matching', () => {
    const s = useThreadsStore()
    s.threads = [
      thread('top', { repo: repo('/r/carlos', 'carlos') }),
      thread('child', { repo: repo('/r/carlos', 'carlos'), parent_id: 'top' }),
      thread('hid', { repo: repo('/r/carlos', 'carlos'), hidden: true }),
    ]
    const sections = s.byRepo
    expect(sections).toHaveLength(1)
    expect(sections[0].threads.map((t) => t.id)).toEqual(['top'])
  })

  it('honors the live search query', () => {
    const s = useThreadsStore()
    s.threads = [
      thread('alpha', { title: 'alpha', repo: repo('/r', 'r') }),
      thread('beta', { title: 'beta', repo: repo('/r', 'r') }),
    ]
    s.query = 'alph'
    expect(s.byRepo[0].threads.map((t) => t.id)).toEqual(['alpha'])
  })

  it('includes hidden threads when showHidden is on', () => {
    const s = useThreadsStore()
    s.threads = [thread('hid', { repo: repo('/r', 'r'), hidden: true })]
    expect(s.byRepo).toHaveLength(0)
    s.showHidden = true
    expect(s.byRepo[0].threads.map((t) => t.id)).toEqual(['hid'])
  })
})

describe('threads store · CC import (WA-2)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('importable returns the session list', async () => {
    const s = useThreadsStore()
    vi.mocked(api.ccImportable).mockResolvedValueOnce({ sessions: [thread('cc:1', { backend: 'cc' })] })
    const out = await s.importable()
    expect(out.map((t) => t.id)).toEqual(['cc:1'])
  })

  it('importable tolerates a missing sessions field', async () => {
    const s = useThreadsStore()
    vi.mocked(api.ccImportable).mockResolvedValueOnce({} as never)
    expect(await s.importable()).toEqual([])
  })

  it('importSession imports, refreshes the roster, and returns the summary', async () => {
    const s = useThreadsStore()
    const imported = thread('cc:9', { backend: 'cc' })
    vi.mocked(api.ccImport).mockResolvedValueOnce(imported)
    vi.mocked(api.listThreads).mockResolvedValueOnce([imported])

    const out = await s.importSession('cc:9')

    expect(api.ccImport).toHaveBeenCalledWith('cc:9')
    expect(api.listThreads).toHaveBeenCalled()
    expect(out.id).toBe('cc:9')
    expect(s.threads.map((t) => t.id)).toEqual(['cc:9'])
  })
})
