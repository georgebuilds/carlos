import { describe, it, expect, beforeEach } from 'vitest'
import { loadCollapsed, saveCollapsed } from './groups'

// minimal in-memory Storage shim
function memStorage(): Storage {
  const m = new Map<string, string>()
  return {
    get length() {
      return m.size
    },
    clear: () => m.clear(),
    getItem: (k: string) => (m.has(k) ? (m.get(k) as string) : null),
    key: (i: number) => [...m.keys()][i] ?? null,
    removeItem: (k: string) => void m.delete(k),
    setItem: (k: string, v: string) => void m.set(k, v),
  }
}

describe('collapsed-set localStorage round trip', () => {
  let store: Storage
  beforeEach(() => {
    store = memStorage()
  })

  it('saves and reloads a collapsed set', () => {
    const set = new Set(['g-house', 'g-anneal'])
    saveCollapsed(store, set)
    const back = loadCollapsed(store)
    expect([...back].sort()).toEqual(['g-anneal', 'g-house'])
  })

  it('returns an empty set when nothing is stored', () => {
    expect(loadCollapsed(store).size).toBe(0)
  })

  it('tolerates corrupt JSON without throwing', () => {
    store.setItem('carlos.web.groups.collapsed', '{not json')
    expect(loadCollapsed(store).size).toBe(0)
  })

  it('ignores non-string members', () => {
    store.setItem('carlos.web.groups.collapsed', JSON.stringify(['g-web', 42, null]))
    expect([...loadCollapsed(store)]).toEqual(['g-web'])
  })

  it('round-trips an empty set', () => {
    saveCollapsed(store, new Set())
    expect(loadCollapsed(store).size).toBe(0)
  })
})
