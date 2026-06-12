// carlos web · threads store.
// The roster poll (every ~3s, plan L5) is the single writer of summaries.
// attach/detach optimistically flip `attached` and reconcile on the next poll.

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import type { ChildSnapshot, DisplayState, ThreadSummary, WireState } from '@/api/types'

export const POLL_MS = 3000

// Map a summary to its display state, folding foreign ownership into the
// "foreign" overlay (rendered as "in the TUI").
export function displayState(t: ThreadSummary): DisplayState {
  if (t.owner && t.owner !== 'web') return 'foreign'
  return t.state
}

// State word table (plan §2). Anything not listed renders its raw wire word.
const STATE_WORDS: Partial<Record<DisplayState, string>> = {
  running: 'running',
  awaiting_input: 'your turn',
  blocked: 'needs a call',
  done: 'done',
  failed: 'failed',
  foreign: 'in the TUI',
}

export function stateWord(s: DisplayState): string {
  return STATE_WORDS[s] ?? s.replace(/_/g, ' ')
}

// Semantic CSS var for a state (matches tokens.css --state-*).
export function stateVar(s: DisplayState): string {
  switch (s) {
    case 'running':
      return 'var(--state-running)'
    case 'awaiting_input':
      return 'var(--state-turn)'
    case 'blocked':
      return 'var(--state-blocked)'
    case 'done':
      return 'var(--state-done)'
    case 'failed':
      return 'var(--state-failed)'
    case 'foreign':
      return 'var(--state-foreign)'
    default:
      return 'var(--state-muted)'
  }
}

export function isLive(s: DisplayState): boolean {
  return s === 'running' || s === 'foreign'
}

export const useThreadsStore = defineStore('threads', () => {
  const threads = ref<ThreadSummary[]>([])
  const activeId = ref<string | null>(null)
  const children = ref<Record<string, ChildSnapshot[]>>({})
  let pollTimer: ReturnType<typeof setInterval> | null = null

  const active = computed<ThreadSummary | null>(
    () => threads.value.find((t) => t.id === activeId.value) ?? null,
  )

  // ungrouped first (plan §4.1), then sorted by updated_at desc inside.
  const ungrouped = computed(() =>
    threads.value
      .filter((t) => !t.group_id)
      .sort((a, b) => b.updated_at.localeCompare(a.updated_at)),
  )

  function membersOf(groupId: string): ThreadSummary[] {
    return threads.value
      .filter((t) => t.group_id === groupId)
      .sort((a, b) => b.updated_at.localeCompare(a.updated_at))
  }

  // rollup counts for a collapsed group header.
  function rollup(groupId: string): { running: number; blocked: number; turn: number } {
    const members = membersOf(groupId)
    const count = (s: WireState) => members.filter((t) => displayState(t) === s).length
    return {
      running: count('running'),
      blocked: count('blocked'),
      turn: count('awaiting_input'),
    }
  }

  function setActive(id: string | null): void {
    activeId.value = id
  }

  async function poll(): Promise<void> {
    // The roster is top-level conversations ONLY. The backend already
    // filters sub-agents out of /api/threads (parent_id IS NULL), but a
    // summary that somehow carries a parent_id - older server, future
    // regression - must never land in the left bar: children render in
    // the crew column.
    threads.value = (await api.listThreads()).filter((t) => !t.parent_id)
    if (activeId.value === null && threads.value.length) {
      activeId.value = threads.value[0].id
    }
  }

  function startPolling(): void {
    if (pollTimer) return
    pollTimer = setInterval(() => {
      void poll().catch(() => {})
    }, POLL_MS)
  }

  function stopPolling(): void {
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  async function attach(id: string): Promise<void> {
    const t = threads.value.find((x) => x.id === id)
    if (t) t.attached = true // optimistic
    try {
      // The attach response is the refreshed summary. Adopt the fields the
      // backend resolves at attach time (frame in particular: it reads ""
      // until the loop starts) instead of waiting for the next roster poll.
      // Merge selectively: the attach handler builds its summary without
      // the group overlay, so a blanket assign would wipe group_id.
      const fresh = await api.attach(id)
      if (t && fresh) {
        t.attached = fresh.attached
        t.frame = fresh.frame
        t.state = fresh.state
      }
    } catch (e) {
      if (t) t.attached = false // revert the optimistic flip
      throw e
    }
  }

  async function detach(id: string): Promise<void> {
    const t = threads.value.find((x) => x.id === id)
    const prevFrame = t?.frame ?? ''
    if (t) {
      t.attached = false // optimistic
      t.frame = '' // the frame resolves at attach; mirror the detached answer
    }
    try {
      await api.detach(id)
    } catch (e) {
      if (t) {
        t.attached = true
        t.frame = prevFrame
      }
      throw e
    }
  }

  async function create(): Promise<ThreadSummary> {
    const t = await api.createThread({})
    threads.value.unshift(t)
    activeId.value = t.id
    return t
  }

  // hard delete (irreversible). On success drop the summary from the roster and,
  // if it was active, advance activeId to the first survivor (or null) so the
  // Stage never renders a dangling thread. A live thread (409 thread_live)
  // rethrows untouched so the caller can toast and leave the row in place.
  async function remove(id: string): Promise<{ deleted: number }> {
    const res = await api.deleteThread(id)
    threads.value = threads.value.filter((t) => t.id !== id)
    if (activeId.value === id) {
      activeId.value = threads.value.length ? threads.value[0].id : null
    }
    delete children.value[id]
    return res
  }

  async function loadChildren(id: string): Promise<void> {
    const res = await api.children(id)
    children.value[id] = res.children
  }

  // adopt a children snapshot that arrived over the SSE stream (kind
  // "children"); this is what makes the crew rail appear the moment the
  // first sub-agent spawns, without waiting for a re-select.
  function setChildren(id: string, list: ChildSnapshot[]): void {
    children.value[id] = list
  }

  return {
    threads,
    activeId,
    active,
    children,
    ungrouped,
    membersOf,
    rollup,
    setActive,
    poll,
    startPolling,
    stopPolling,
    attach,
    detach,
    create,
    remove,
    loadChildren,
    setChildren,
  }
})
