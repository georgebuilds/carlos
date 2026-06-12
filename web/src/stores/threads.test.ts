import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useThreadsStore } from './threads'
import { ApiError } from '@/api/client'
import type { ThreadSummary } from '@/api/types'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: {
      deleteThread: vi.fn(),
      attach: vi.fn(),
      detach: vi.fn(),
      listThreads: vi.fn(),
      children: vi.fn(),
    },
  }
})

import { api } from '@/api/client'

function thread(id: string, over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id,
    title: id,
    model: 'claude-fable-5',
    state: 'done',
    attached: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    preview: '',
    user_msgs: 0,
    frame: 'carlos',
    backend: 'carlos',
    capabilities: {},
    ...over,
  }
}

describe('threads store · attach/detach frame sync', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('attach adopts the frame the server resolved at attach time', async () => {
    const s = useThreadsStore()
    s.threads = [thread('a', { frame: '', attached: false, state: 'awaiting_input', group_id: 'g1' })]
    vi.mocked(api.attach).mockResolvedValueOnce(
      thread('a', { frame: 'work', attached: true, state: 'running' }),
    )

    await s.attach('a')

    const t = s.threads[0]
    expect(t.attached).toBe(true)
    expect(t.frame).toBe('work')
    expect(t.state).toBe('running')
    // the attach handler builds its summary without the group overlay;
    // adopting it must not wipe the membership we already know.
    expect(t.group_id).toBe('g1')
  })

  it('attach tolerates a backend that answers with no summary body', async () => {
    // the dev mock answers 204; the optimistic flip must survive that.
    const s = useThreadsStore()
    s.threads = [thread('a', { frame: '', attached: false })]
    vi.mocked(api.attach).mockResolvedValueOnce(undefined as never)

    await s.attach('a')

    expect(s.threads[0].attached).toBe(true)
    expect(s.threads[0].frame).toBe('')
  })

  it('attach reverts the optimistic flip and rethrows on failure', async () => {
    const s = useThreadsStore()
    s.threads = [thread('a', { frame: '', attached: false })]
    vi.mocked(api.attach).mockRejectedValueOnce(new ApiError(409, 'thread_owned', 'owned'))

    await expect(s.attach('a')).rejects.toMatchObject({ status: 409, code: 'thread_owned' })

    expect(s.threads[0].attached).toBe(false)
    expect(s.threads[0].frame).toBe('')
  })

  it('detach clears the frame mirror (the frame resolves at attach)', async () => {
    const s = useThreadsStore()
    s.threads = [thread('a', { frame: 'work', attached: true })]
    vi.mocked(api.detach).mockResolvedValueOnce(undefined)

    await s.detach('a')

    expect(s.threads[0].attached).toBe(false)
    expect(s.threads[0].frame).toBe('')
  })

  it('detach restores attached + frame and rethrows on failure', async () => {
    const s = useThreadsStore()
    s.threads = [thread('a', { frame: 'work', attached: true })]
    vi.mocked(api.detach).mockRejectedValueOnce(new ApiError(500, 'boom', 'server'))

    await expect(s.detach('a')).rejects.toThrow('server')

    expect(s.threads[0].attached).toBe(true)
    expect(s.threads[0].frame).toBe('work')
  })
})

describe('threads store · remove (hard delete)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('removes the thread from the roster and returns the count on success', async () => {
    const s = useThreadsStore()
    s.threads = [thread('a'), thread('b')]
    s.activeId = 'b'
    vi.mocked(api.deleteThread).mockResolvedValueOnce({ deleted: 1 })

    const res = await s.remove('a')

    expect(res).toEqual({ deleted: 1 })
    expect(s.threads.map((t) => t.id)).toEqual(['b'])
    // deleting a non-active thread leaves activeId untouched
    expect(s.activeId).toBe('b')
  })

  it('advances activeId to the first survivor when the active thread is deleted', async () => {
    const s = useThreadsStore()
    s.threads = [thread('a'), thread('b'), thread('c')]
    s.activeId = 'a'
    vi.mocked(api.deleteThread).mockResolvedValueOnce({ deleted: 3 })

    const res = await s.remove('a')

    expect(res).toEqual({ deleted: 3 })
    expect(s.threads.map((t) => t.id)).toEqual(['b', 'c'])
    expect(s.activeId).toBe('b')
  })

  it('clears activeId to null when the last thread is deleted', async () => {
    const s = useThreadsStore()
    s.threads = [thread('only')]
    s.activeId = 'only'
    vi.mocked(api.deleteThread).mockResolvedValueOnce({ deleted: 1 })

    await s.remove('only')

    expect(s.threads).toEqual([])
    expect(s.activeId).toBeNull()
  })

  it('drops any cached children for the deleted thread', async () => {
    const s = useThreadsStore()
    s.threads = [thread('a'), thread('b')]
    s.children['a'] = [
      { id: 'c1', state: 'done', title: 'sub', last_tool: 'Read', tokens: 1, cost_cents: 1, started_at: '' },
    ]
    vi.mocked(api.deleteThread).mockResolvedValueOnce({ deleted: 2 })

    await s.remove('a')

    expect(s.children['a']).toBeUndefined()
  })

  it('leaves the roster intact and rethrows on a 409 thread_live error', async () => {
    const s = useThreadsStore()
    s.threads = [thread('a'), thread('b')]
    s.activeId = 'a'
    vi.mocked(api.deleteThread).mockRejectedValueOnce(
      new ApiError(409, 'thread_live', 'driven by a live process'),
    )

    await expect(s.remove('a')).rejects.toMatchObject({ status: 409, code: 'thread_live' })
    // nothing removed, activeId untouched, so the UI can toast and leave the row
    expect(s.threads.map((t) => t.id)).toEqual(['a', 'b'])
    expect(s.activeId).toBe('a')
  })

  it('setChildren adopts a streamed children snapshot for the thread', () => {
    // the SSE "children" event drives the crew rail's appearance mid-turn;
    // the snapshot replaces (not merges) whatever was cached.
    const s = useThreadsStore()
    const snap = (id: string) => ({
      id, state: 'running' as const, title: id, last_tool: 'Read',
      tokens: 10, cost_cents: 1, started_at: '2026-01-01T00:00:00Z',
    })
    s.setChildren('a', [snap('c1')])
    expect(s.children['a'].map((c) => c.id)).toEqual(['c1'])

    s.setChildren('a', [snap('c1'), snap('c2')])
    expect(s.children['a'].map((c) => c.id)).toEqual(['c1', 'c2'])
  })

  it('loadChildren caches the thread crew, finished children included', async () => {
    // the column must appear for ANY thread with sub-agents, even ones
    // whose children finished long ago: selection loads them from
    // GET /api/threads/{id}/children with their final state + spend.
    const s = useThreadsStore()
    vi.mocked(api.children).mockResolvedValueOnce({
      children: [
        { id: 'c1', state: 'done', title: 'first', last_tool: 'read_file', tokens: 1200, cost_cents: 3, started_at: '2026-06-12T00:00:00Z' },
        { id: 'c2', state: 'failed', title: 'second', last_tool: '', tokens: 400, cost_cents: 1, started_at: '2026-06-12T00:00:01Z' },
      ],
    })

    await s.loadChildren('a')

    expect(s.children['a'].map((c) => c.id)).toEqual(['c1', 'c2'])
    expect(s.children['a'].map((c) => c.state)).toEqual(['done', 'failed'])
  })

  it('rethrows other errors without mutating the roster', async () => {
    const s = useThreadsStore()
    s.threads = [thread('a')]
    s.activeId = 'a'
    vi.mocked(api.deleteThread).mockRejectedValueOnce(new ApiError(500, 'boom', 'server'))

    await expect(s.remove('a')).rejects.toThrow('server')
    expect(s.threads.map((t) => t.id)).toEqual(['a'])
    expect(s.activeId).toBe('a')
  })
})

describe('threads store · roster excludes sub-agents (defense in depth)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('poll drops any summary carrying a parent_id, even if the wire sends one', async () => {
    // the backend filters children server-side; if one ever leaks (older
    // server, regression), the left bar still must not show it.
    const s = useThreadsStore()
    vi.mocked(api.listThreads).mockResolvedValueOnce([
      thread('top'),
      thread('child-1', { parent_id: 'top' }),
      thread('child-2', { parent_id: 'top', state: 'running' }),
    ])

    await s.poll()

    expect(s.threads.map((t) => t.id)).toEqual(['top'])
    // and the first SURVIVOR becomes active, never a dropped child
    expect(s.activeId).toBe('top')
  })

  it('poll keeps normal top-level threads untouched (empty/absent parent_id)', async () => {
    const s = useThreadsStore()
    vi.mocked(api.listThreads).mockResolvedValueOnce([
      thread('a'),
      thread('b', { parent_id: null }),
      thread('c', { parent_id: '' }),
    ])

    await s.poll()

    expect(s.threads.map((t) => t.id)).toEqual(['a', 'b', 'c'])
  })
})
