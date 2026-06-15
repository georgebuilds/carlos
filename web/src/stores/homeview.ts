// carlos web · home view-mode store (WB-2).
// The launch surface (shown when no thread is selected) toggles between a
// repo-grouped board and the existing manual-group roster. The choice is
// presentation, persisted to localStorage; default is "by repo".

import { defineStore } from 'pinia'
import { ref } from 'vue'

export type HomeView = 'repo' | 'group'

const VIEW_KEY = 'carlos.web.home.view'
const DEFAULT_VIEW: HomeView = 'repo'

// Read the persisted choice. Tolerant of corruption/absence; anything that is
// not a known mode falls back to the default.
export function loadHomeView(store: Storage): HomeView {
  try {
    const raw = store.getItem(VIEW_KEY)
    return raw === 'repo' || raw === 'group' ? raw : DEFAULT_VIEW
  } catch {
    return DEFAULT_VIEW
  }
}

export function saveHomeView(store: Storage, view: HomeView): void {
  try {
    store.setItem(VIEW_KEY, view)
  } catch {
    // private mode / quota: the choice silently degrades to per-session.
  }
}

export const useHomeViewStore = defineStore('homeview', () => {
  const view = ref<HomeView>(
    typeof localStorage !== 'undefined' ? loadHomeView(localStorage) : DEFAULT_VIEW,
  )

  function setView(v: HomeView): void {
    view.value = v
    if (typeof localStorage !== 'undefined') saveHomeView(localStorage, v)
  }

  return { view, setView }
})
