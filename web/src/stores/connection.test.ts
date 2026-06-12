import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { readTokenFromHash, useConnectionStore } from './connection'
import { getToken, setToken } from '@/api/client'

function fakeLocation(hash: string, pathname = '/', search = ''): Location {
  return { hash, pathname, search } as Location
}

describe('token handshake', () => {
  it('reads token from the location hash and scrubs the fragment', () => {
    const replaceState = vi.fn()
    const hist = { replaceState } as unknown as History
    const loc = fakeLocation('#token=abc123', '/console', '?x=1')

    const token = readTokenFromHash(loc, hist)
    expect(token).toBe('abc123')
    // fragment scrubbed, query + path preserved, no new history entry
    expect(replaceState).toHaveBeenCalledWith(null, '', '/console?x=1')
  })

  it('returns null and does not scrub when no token is present', () => {
    const replaceState = vi.fn()
    const hist = { replaceState } as unknown as History
    expect(readTokenFromHash(fakeLocation(''), hist)).toBeNull()
    expect(readTokenFromHash(fakeLocation('#other=1'), hist)).toBeNull()
    expect(replaceState).not.toHaveBeenCalled()
  })

  it('handles a hash with multiple params', () => {
    const hist = { replaceState: vi.fn() } as unknown as History
    expect(readTokenFromHash(fakeLocation('#a=1&token=zzz&b=2'), hist)).toBe('zzz')
  })
})

describe('token survives refresh via sessionStorage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    window.sessionStorage.clear()
    setToken(null)
    window.location.hash = ''
  })

  it('persists the launch-URL token and reuses it when the fragment is gone', () => {
    // First load: token arrives in the fragment.
    window.location.hash = '#token=launch-tok'
    useConnectionStore().boot()
    expect(getToken()).toBe('launch-tok')
    expect(window.sessionStorage.getItem('carlos.web.token')).toBe('launch-tok')

    // Refresh: fragment was scrubbed, but a fresh store boot recovers it.
    setActivePinia(createPinia())
    window.location.hash = ''
    const conn2 = useConnectionStore()
    conn2.boot()
    expect(getToken()).toBe('launch-tok')
    expect(conn2.needsToken).toBe(false)
  })

  it('flags needsToken when no fragment and nothing stored', () => {
    const conn = useConnectionStore()
    conn.boot()
    expect(getToken()).toBeNull()
    expect(conn.needsToken).toBe(true)
  })
})
