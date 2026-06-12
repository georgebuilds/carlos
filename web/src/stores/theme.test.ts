import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { THEME_KEY, applyTheme, readStoredTheme, useThemeStore } from './theme'
import indexHtml from '../../index.html?raw'

const root = () => document.documentElement

beforeEach(() => {
  setActivePinia(createPinia())
  window.localStorage.clear()
  root().removeAttribute('data-theme')
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('readStoredTheme', () => {
  it('defaults to system when no key is stored', () => {
    expect(readStoredTheme()).toBe('system')
  })

  it('reads an explicit light or dark deviation', () => {
    window.localStorage.setItem(THEME_KEY, 'light')
    expect(readStoredTheme()).toBe('light')
    window.localStorage.setItem(THEME_KEY, 'dark')
    expect(readStoredTheme()).toBe('dark')
  })

  it('treats garbage as system and scrubs the key', () => {
    window.localStorage.setItem(THEME_KEY, 'banana')
    expect(readStoredTheme()).toBe('system')
    expect(window.localStorage.getItem(THEME_KEY)).toBeNull()
  })

  it('falls back to system when localStorage throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied')
    })
    expect(readStoredTheme()).toBe('system')
  })

  it('still reads garbage as system when the scrub itself throws', () => {
    window.localStorage.setItem(THEME_KEY, 'banana')
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
      throw new Error('denied')
    })
    expect(readStoredTheme()).toBe('system')
  })
})

describe('applyTheme', () => {
  it('pins data-theme for explicit modes and removes it for system', () => {
    applyTheme('dark')
    expect(root().getAttribute('data-theme')).toBe('dark')
    applyTheme('light')
    expect(root().getAttribute('data-theme')).toBe('light')
    applyTheme('system')
    expect(root().hasAttribute('data-theme')).toBe(false)
  })

  it('is a no-op when document is unavailable (non-DOM environment)', () => {
    vi.stubGlobal('document', undefined)
    expect(() => applyTheme('dark')).not.toThrow()
    vi.unstubAllGlobals()
    expect(root().hasAttribute('data-theme')).toBe(false)
  })
})

describe('theme store', () => {
  it('boots into system with no attribute and no key', () => {
    const theme = useThemeStore()
    expect(theme.mode).toBe('system')
    expect(root().hasAttribute('data-theme')).toBe(false)
    expect(window.localStorage.getItem(THEME_KEY)).toBeNull()
  })

  it('persists only deviations: set stores the key, system removes it', () => {
    const theme = useThemeStore()

    theme.set('dark')
    expect(theme.mode).toBe('dark')
    expect(window.localStorage.getItem(THEME_KEY)).toBe('dark')
    expect(root().getAttribute('data-theme')).toBe('dark')

    theme.set('light')
    expect(window.localStorage.getItem(THEME_KEY)).toBe('light')
    expect(root().getAttribute('data-theme')).toBe('light')

    theme.set('system')
    expect(window.localStorage.getItem(THEME_KEY)).toBeNull()
    expect(root().hasAttribute('data-theme')).toBe(false)
  })

  it('restores a deviation on refresh (fresh pinia, same storage)', () => {
    useThemeStore().set('dark')

    // simulate a reload: new pinia, attribute already painted by the boot
    // script; the store re-reads the key and agrees.
    setActivePinia(createPinia())
    const theme = useThemeStore()
    expect(theme.mode).toBe('dark')
    expect(root().getAttribute('data-theme')).toBe('dark')
  })

  it('scrubs a garbage key at init and stays in system', () => {
    window.localStorage.setItem(THEME_KEY, '☃')
    const theme = useThemeStore()
    expect(theme.mode).toBe('system')
    expect(window.localStorage.getItem(THEME_KEY)).toBeNull()
    expect(root().hasAttribute('data-theme')).toBe(false)
  })

  it('cycles system → light → dark → system', () => {
    const theme = useThemeStore()
    theme.cycle()
    expect(theme.mode).toBe('light')
    theme.cycle()
    expect(theme.mode).toBe('dark')
    theme.cycle()
    expect(theme.mode).toBe('system')
    expect(window.localStorage.getItem(THEME_KEY)).toBeNull()
  })

  it('still applies the theme in-tab when localStorage writes throw', () => {
    const theme = useThemeStore()
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('quota')
    })
    theme.set('dark')
    expect(theme.mode).toBe('dark')
    expect(root().getAttribute('data-theme')).toBe('dark')
  })
})

describe('boot script agreement', () => {
  // The inline script in index.html cannot import this module (it must run
  // synchronously before first paint, not as a deferred ES module), so it
  // mirrors readStoredTheme/applyTheme. These assertions pin the mirror to
  // the store's key and accepted values so they cannot drift silently.
  const script = /<script>([\s\S]*?)<\/script>/.exec(indexHtml)?.[1] ?? ''

  it('index.html carries an inline boot script', () => {
    expect(script).not.toBe('')
  })

  it('reads the same localStorage key and accepts only light/dark', () => {
    expect(script).toContain(`localStorage.getItem('${THEME_KEY}')`)
    expect(script).toContain(`t === 'light' || t === 'dark'`)
    expect(script).toContain(`setAttribute('data-theme', t)`)
  })

  it('scrubs garbage values like the store does', () => {
    expect(script).toContain(`localStorage.removeItem('${THEME_KEY}')`)
  })

  it('behaves like the store for every stored value', () => {
    // execute the extracted script body against jsdom for the three cases
    const run = () => new Function(script)()

    window.localStorage.setItem(THEME_KEY, 'dark')
    run()
    expect(root().getAttribute('data-theme')).toBe('dark')

    root().removeAttribute('data-theme')
    window.localStorage.setItem(THEME_KEY, 'junk')
    run()
    expect(root().hasAttribute('data-theme')).toBe(false)
    expect(window.localStorage.getItem(THEME_KEY)).toBeNull()

    run() // no key at all
    expect(root().hasAttribute('data-theme')).toBe(false)
  })
})
