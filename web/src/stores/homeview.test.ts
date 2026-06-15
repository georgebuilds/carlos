import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { loadHomeView, saveHomeView, useHomeViewStore } from './homeview'

const KEY = 'carlos.web.home.view'

describe('homeview persistence helpers', () => {
  beforeEach(() => localStorage.clear())

  it('defaults to "repo" when nothing is stored', () => {
    expect(loadHomeView(localStorage)).toBe('repo')
  })

  it('reads a stored "group" choice', () => {
    localStorage.setItem(KEY, 'group')
    expect(loadHomeView(localStorage)).toBe('group')
  })

  it('falls back to the default on a corrupt value', () => {
    localStorage.setItem(KEY, 'garbage')
    expect(loadHomeView(localStorage)).toBe('repo')
  })

  it('round-trips through save', () => {
    saveHomeView(localStorage, 'group')
    expect(loadHomeView(localStorage)).toBe('group')
  })

  it('load tolerates a throwing storage', () => {
    const bad = {
      getItem() {
        throw new Error('blocked')
      },
    } as unknown as Storage
    expect(loadHomeView(bad)).toBe('repo')
  })

  it('save swallows a throwing storage', () => {
    const bad = {
      setItem() {
        throw new Error('quota')
      },
    } as unknown as Storage
    expect(() => saveHomeView(bad, 'repo')).not.toThrow()
  })
})

describe('homeview store', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('starts at the default and persists setView', () => {
    const s = useHomeViewStore()
    expect(s.view).toBe('repo')
    s.setView('group')
    expect(s.view).toBe('group')
    expect(localStorage.getItem(KEY)).toBe('group')
  })

  it('hydrates from a previously persisted choice', () => {
    localStorage.setItem(KEY, 'group')
    setActivePinia(createPinia())
    const s = useHomeViewStore()
    expect(s.view).toBe('group')
  })
})
