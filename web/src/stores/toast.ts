// carlos web · toast store.
// Toasts narrate wire effects in one line (attach started, approval_resolved
// fanned out, 409 refused). They carry inline <code> markup, so callers pass
// trusted strings only (never raw user input).

import { defineStore } from 'pinia'
import { ref } from 'vue'

export const useToastStore = defineStore('toast', () => {
  const html = ref('')
  const visible = ref(false)
  let timer: ReturnType<typeof setTimeout> | null = null

  function show(message: string): void {
    html.value = message
    visible.value = true
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      visible.value = false
    }, 3400)
  }

  return { html, visible, show }
})
