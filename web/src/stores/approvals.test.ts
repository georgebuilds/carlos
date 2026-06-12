import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useApprovalsStore } from './approvals'
import { ApiError } from '@/api/client'
import type { WireEvent } from '@/api/types'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: { resolveApproval: vi.fn() },
  }
})

import { api } from '@/api/client'

function req(threadEv: Partial<WireEvent> = {}): WireEvent {
  return {
    thread: 't',
    ts: '',
    kind: 'approval_request',
    data: { request_id: 'req_1', name: 'Bash', input: 'git push', layer_reason: 'write' },
    ...threadEv,
  }
}

describe('approvals lifecycle', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('appears on approval_request, clears on approval_resolved', () => {
    const s = useApprovalsStore()
    s.ingest('t', req())
    expect(s.pendingFor('t')).toHaveLength(1)
    s.ingest('t', {
      thread: 't',
      ts: '',
      kind: 'approval_resolved',
      data: { request_id: 'req_1', decision: 'allow' },
    })
    expect(s.pendingFor('t')).toHaveLength(0)
  })

  it('clears regardless of which client resolved (resolved id, not local op)', () => {
    const s = useApprovalsStore()
    s.ingest('t', req())
    s.ingest('t', req({ data: { request_id: 'req_2', name: 'Write', input: 'x' } }))
    expect(s.pendingFor('t')).toHaveLength(2)
    // a second tab resolved req_1
    s.ingest('t', {
      thread: 't',
      ts: '',
      kind: 'approval_resolved',
      data: { request_id: 'req_1', decision: 'deny' },
    })
    expect(s.pendingFor('t').map((a) => a.requestId)).toEqual(['req_2'])
  })

  it('replace() swaps the whole pending set (SSE snapshot, no merge)', () => {
    const s = useApprovalsStore()
    s.ingest('t', req())
    s.replace('t', [{ requestId: 'req_9', name: 'Edit', input: 'y' }])
    expect(s.pendingFor('t').map((a) => a.requestId)).toEqual(['req_9'])
  })

  it('resolve-after-expire: 404 clears locally and fires a quiet toast', async () => {
    const s = useApprovalsStore()
    s.ingest('t', req())
    vi.mocked(api.resolveApproval).mockRejectedValueOnce(
      new ApiError(404, 'not_found', 'gone'),
    )
    const quiet = vi.fn()
    await s.resolve('t', 'req_1', 'allow', quiet)
    expect(quiet).toHaveBeenCalledOnce()
    expect(s.pendingFor('t')).toHaveLength(0)
  })

  it('resolve success clears the entry optimistically', async () => {
    const s = useApprovalsStore()
    s.ingest('t', req())
    vi.mocked(api.resolveApproval).mockResolvedValueOnce(undefined)
    await s.resolve('t', 'req_1', 'allow')
    expect(s.pendingFor('t')).toHaveLength(0)
  })

  it('resolve rethrows non-404 errors', async () => {
    const s = useApprovalsStore()
    s.ingest('t', req())
    vi.mocked(api.resolveApproval).mockRejectedValueOnce(
      new ApiError(500, 'boom', 'server'),
    )
    await expect(s.resolve('t', 'req_1', 'allow')).rejects.toThrow('server')
    // entry stays put since the resolve genuinely failed
    expect(s.pendingFor('t')).toHaveLength(1)
  })
})
