// carlos web · connection store.
// Reads the bearer token from location.hash on boot, scrubs the fragment,
// and tracks online state + server meta.
//
// Token storage: sessionStorage, NOT localStorage. This survives a page
// refresh (the original memory-only approach lost the token on reload, since
// the fragment was already scrubbed) while preserving the spec D9 intent:
// the token still dies when the tab closes, and it never returns to the URL
// or to a persistent store shared across tabs.

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, ApiError, setToken } from '@/api/client'
import type { Meta } from '@/api/types'

const TOKEN_KEY = 'carlos.web.token'

function saveToken(t: string): void {
  try {
    window.sessionStorage.setItem(TOKEN_KEY, t)
  } catch {
    // sessionStorage unavailable (private-mode quirk); memory-only fallback.
  }
}

function loadStoredToken(): string | null {
  try {
    return window.sessionStorage.getItem(TOKEN_KEY)
  } catch {
    return null
  }
}

function clearStoredToken(): void {
  try {
    window.sessionStorage.removeItem(TOKEN_KEY)
  } catch {
    // ignore
  }
}

// Pull `#token=...` out of the URL fragment, then scrub it from the address
// bar with history.replaceState. Exported for unit tests.
export function readTokenFromHash(loc: Location, hist: History): string | null {
  const hash = loc.hash.startsWith('#') ? loc.hash.slice(1) : loc.hash
  if (!hash) return null
  const params = new URLSearchParams(hash)
  const token = params.get('token')
  if (!token) return null

  // scrub the fragment without reloading or adding a history entry
  const cleanUrl = loc.pathname + loc.search
  hist.replaceState(null, '', cleanUrl)
  return token
}

export const useConnectionStore = defineStore('connection', () => {
  const token = ref<string | null>(null)
  const online = ref(true)
  // needsToken is set when there is no token at all (fresh tab, no launch
  // URL) or the stored token was rejected; the UI can prompt for the launch
  // link instead of silently failing every request.
  const needsToken = ref(false)
  const meta = ref<Meta | null>(null)

  function boot(): void {
    // Precedence: a launch-URL fragment wins (a fresh `carlos web` mints a new
    // token), and we persist it for subsequent refreshes. With no fragment,
    // fall back to the sessionStorage copy so a reload stays authenticated.
    let t = readTokenFromHash(window.location, window.history)
    if (t) {
      saveToken(t)
    } else {
      t = loadStoredToken()
    }
    token.value = t
    needsToken.value = !t
    setToken(t)
  }

  async function loadMeta(): Promise<void> {
    try {
      meta.value = await api.meta()
      online.value = true
      needsToken.value = false
    } catch (e) {
      online.value = false
      // A 401 means the stored token is stale (server relaunched with a new
      // one). Drop it so we don't keep replaying a dead token, and signal the
      // UI to ask for the current launch link.
      if (e instanceof ApiError && e.status === 401) {
        clearStoredToken()
        token.value = null
        needsToken.value = true
        setToken(null)
      }
    }
  }

  function setOnline(v: boolean): void {
    online.value = v
  }

  return { token, online, needsToken, meta, boot, loadMeta, setOnline }
})
