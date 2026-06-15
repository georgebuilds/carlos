import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return { ...actual, api: { createThread: vi.fn() } }
})

import { api } from '@/api/client'
import { useNewThread } from './useNewThread'
import { useToastStore } from '@/stores/toast'

function tsum(id: string) {
  return {
    id,
    title: id,
    model: '',
    state: 'running',
    attached: true,
    created_at: '',
    updated_at: '',
    preview: '',
    user_msgs: 0,
    frame: '',
    backend: 'carlos',
    capabilities: {},
  }
}

describe('useNewThread', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('creates a carlos thread, toasts the mint message, returns the id', async () => {
    vi.mocked(api.createThread).mockResolvedValueOnce(tsum('carlos:1') as never)
    const toast = useToastStore()
    const spy = vi.spyOn(toast, 'show')
    const { newThread } = useNewThread()

    const id = await newThread()
    expect(id).toBe('carlos:1')
    expect(spy).toHaveBeenCalledWith(expect.stringContaining('thread minted'))
  })

  it('creates a cc session and toasts the claude-code message', async () => {
    vi.mocked(api.createThread).mockResolvedValueOnce(tsum('cc:1') as never)
    const toast = useToastStore()
    const spy = vi.spyOn(toast, 'show')
    const { newThread } = useNewThread()

    const id = await newThread('cc')
    expect(id).toBe('cc:1')
    expect(spy).toHaveBeenCalledWith(expect.stringContaining('claude code session started'))
  })

  it('returns null and toasts a failure when create rejects', async () => {
    vi.mocked(api.createThread).mockRejectedValueOnce(new Error('nope'))
    const toast = useToastStore()
    const spy = vi.spyOn(toast, 'show')
    const { newThread } = useNewThread()

    const id = await newThread()
    expect(id).toBeNull()
    expect(spy).toHaveBeenCalledWith(expect.stringContaining('could not start'))
  })
})
