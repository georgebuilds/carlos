// carlos web · theme store.
// Three modes: 'system' (default), 'light', 'dark'. System is the absence of
// a preference: no localStorage key, no data-theme attribute, and tokens.css
// (color-scheme: light dark + light-dark()) tracks the OS live. Only an
// explicit deviation is persisted; choosing system again removes the key.
//
// The inline boot script in index.html mirrors readStoredTheme + applyTheme
// so the attribute lands before first paint; keep the three in sync (the
// theme.test.ts boot-agreement test pins the script to this key + values).

import { defineStore } from 'pinia'
import { ref } from 'vue'

export const THEME_KEY = 'carlos.theme'

export type ThemeMode = 'system' | 'light' | 'dark'

const CYCLE: ThemeMode[] = ['system', 'light', 'dark']

function isExplicit(v: string | null): v is 'light' | 'dark' {
  return v === 'light' || v === 'dark'
}

// Read the stored deviation. Anything other than 'light'/'dark' (including
// garbage left by older builds) reads as system, and garbage is scrubbed so
// the key only ever holds a real deviation.
export function readStoredTheme(): ThemeMode {
  let raw: string | null = null
  try {
    raw = window.localStorage.getItem(THEME_KEY)
  } catch {
    return 'system' // storage unavailable (private-mode quirk, vitest stub)
  }
  if (isExplicit(raw)) return raw
  if (raw !== null) {
    try {
      window.localStorage.removeItem(THEME_KEY)
    } catch {
      // ignore; the value is still treated as system
    }
  }
  return 'system'
}

// Pin or release data-theme on <html>. No attribute means tokens.css follows
// the OS; an attribute pins color-scheme to the chosen side.
export function applyTheme(mode: ThemeMode): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  if (mode === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', mode)
}

export const useThemeStore = defineStore('theme', () => {
  // Initialize from the same key the index.html boot script read, then
  // re-apply: the store always agrees with whatever the script painted.
  const mode = ref<ThemeMode>(readStoredTheme())
  applyTheme(mode.value)

  function set(next: ThemeMode): void {
    mode.value = next
    applyTheme(next)
    try {
      if (next === 'system') window.localStorage.removeItem(THEME_KEY)
      else window.localStorage.setItem(THEME_KEY, next)
    } catch {
      // storage unavailable; the in-tab theme still applies
    }
  }

  // system → light → dark → system
  function cycle(): void {
    set(CYCLE[(CYCLE.indexOf(mode.value) + 1) % CYCLE.length])
  }

  return { mode, set, cycle }
})
