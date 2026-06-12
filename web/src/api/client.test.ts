import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api, ApiError, setToken } from './client'

describe('client error envelope and 409 typing', () => {
  beforeEach(() => setToken('tok'))
  afterEach(() => vi.restoreAllMocks())

  it('injects the bearer token on requests', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify([]), { status: 200 }),
    )
    vi.stubGlobal('fetch', fetchMock)
    await api.listThreads()
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect((init.headers as Record<string, string>)['Authorization']).toBe('Bearer tok')
  })

  it('parses the error envelope into an ApiError', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: 'bad_request', message: 'empty name' }), {
          status: 400,
        }),
      ),
    )
    await expect(api.createGroup('')).rejects.toMatchObject({
      status: 400,
      code: 'bad_request',
      message: 'empty name',
    })
  })

  it('parses the nested { error: { code, message } } envelope (real server shape)', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ error: { code: 'unsupported', message: 'read-only mode' } }),
          { status: 501 },
        ),
      ),
    )
    await expect(api.sendMessage('t1', 'hi')).rejects.toMatchObject({
      status: 501,
      code: 'unsupported',
      message: 'read-only mode', // NOT "[object Object]"
    })
  })

  it('types the 409 thread_owned refusal', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: 'thread_owned', message: 'owned by tui' }), {
          status: 409,
        }),
      ),
    )
    try {
      await api.attach('t1')
      throw new Error('should have thrown')
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError)
      const err = e as ApiError
      expect(err.isOwned).toBe(true)
      expect(err.isGone).toBe(false)
    }
  })

  it('returns undefined for 204 responses', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })))
    await expect(api.detach('t1')).resolves.toBeUndefined()
  })

  it('deleteThread returns the { deleted } count on success', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ deleted: 3 }), { status: 200 }),
      ),
    )
    await expect(api.deleteThread('t1')).resolves.toEqual({ deleted: 3 })
  })

  it('deleteThread issues a DELETE to the encoded thread path', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ deleted: 1 }), { status: 200 }),
    )
    vi.stubGlobal('fetch', fetchMock)
    await api.deleteThread('a/b')
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/threads/a%2Fb')
    expect((init as RequestInit).method).toBe('DELETE')
  })

  it('types the 409 thread_live refusal on delete', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: { code: 'thread_live', message: 'driven by a live process' } }), {
          status: 409,
        }),
      ),
    )
    try {
      await api.deleteThread('t1')
      throw new Error('should have thrown')
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError)
      const err = e as ApiError
      expect(err.isThreadLive).toBe(true)
      expect(err.isOwned).toBe(false)
      expect(err.message).toBe('driven by a live process')
    }
  })

  it('flags 404 as isGone', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: 'not_found', message: 'gone' }), { status: 404 }),
      ),
    )
    try {
      await api.getThread('nope')
      throw new Error('should have thrown')
    } catch (e) {
      expect((e as ApiError).isGone).toBe(true)
    }
  })
})
