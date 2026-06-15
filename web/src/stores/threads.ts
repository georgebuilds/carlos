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
  if (!s) return ''
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

// ── repo grouping (WB-2) ──────────────────────────────────────────────
// A by-repo home section: a set of threads that share a git repo, plus the
// single "No repository" catch-all. `root` is '' for the catch-all.
export interface RepoGroup {
  root: string
  name: string
  threads: ThreadSummary[]
}

// The catch-all label. Threads with no `repo` collect here; it always sorts
// last regardless of activity.
export const NO_REPO_LABEL = 'No repository'

// Pure selector: group a flat thread list into ordered repo sections.
//   - keyed by repo.root; threads with no repo fall into the '' catch-all
//   - threads inside a section sort by updated_at desc (newest first)
//   - real repo sections order by their newest member's updated_at (desc)
//   - the catch-all ('' root) always sorts LAST
//   - only sections with >= 1 thread are returned (a list with no orphans
//     yields no catch-all)
export function groupByRepo(list: ThreadSummary[]): RepoGroup[] {
  const byRoot = new Map<string, RepoGroup>()
  for (const t of list) {
    const root = t.repo?.root ?? ''
    const name = root ? t.repo?.name || root : NO_REPO_LABEL
    let g = byRoot.get(root)
    if (!g) {
      g = { root, name, threads: [] }
      byRoot.set(root, g)
    }
    g.threads.push(t)
  }
  const groups = [...byRoot.values()]
  for (const g of groups) {
    g.threads.sort((a, b) => b.updated_at.localeCompare(a.updated_at))
  }
  // newest-member activity desc; catch-all ('' root) forced last.
  groups.sort((a, b) => {
    if (a.root === '' && b.root === '') return 0
    if (a.root === '') return 1
    if (b.root === '') return -1
    const aTop = a.threads[0]?.updated_at ?? ''
    const bTop = b.threads[0]?.updated_at ?? ''
    return bTop.localeCompare(aTop)
  })
  return groups
}

export const useThreadsStore = defineStore('threads', () => {
  const threads = ref<ThreadSummary[]>([])
  const activeId = ref<string | null>(null)
  const children = ref<Record<string, ChildSnapshot[]>>({})
  // live roster search: a case-insensitive substring filter over title +
  // preview, applied to both the ungrouped list and each group's members.
  const query = ref('')
  // hidden (blacklisted) threads are folded out of the roster unless the
  // user flips "show hidden". Mainly for foreign Claude Code threads.
  const showHidden = ref(false)
  let pollTimer: ReturnType<typeof setInterval> | null = null

  function matchesQuery(t: ThreadSummary): boolean {
    const q = query.value.trim().toLowerCase()
    if (!q) return true
    return (
      (t.title ?? '').toLowerCase().includes(q) || (t.preview ?? '').toLowerCase().includes(q)
    )
  }

  // a thread shows in the roster when it matches the search AND is not
  // hidden (unless "show hidden" is on).
  function visible(t: ThreadSummary): boolean {
    if (t.hidden && !showHidden.value) return false
    return matchesQuery(t)
  }

  const hiddenCount = computed(() => threads.value.filter((t) => t.hidden).length)

  const active = computed<ThreadSummary | null>(
    () => threads.value.find((t) => t.id === activeId.value) ?? null,
  )

  // ungrouped first (plan §4.1), then sorted by updated_at desc inside.
  const ungrouped = computed(() =>
    threads.value
      .filter((t) => !t.group_id && visible(t))
      .sort((a, b) => b.updated_at.localeCompare(a.updated_at)),
  )

  // by-repo home sections (WB-2): the same visible set as the roster, grouped
  // by git repo. Honors the live search + hidden filter exactly like the
  // group view, so a query narrows both views identically.
  const byRepo = computed<RepoGroup[]>(() =>
    groupByRepo(threads.value.filter((t) => !t.parent_id && visible(t))),
  )

  function membersOf(groupId: string): ThreadSummary[] {
    return threads.value
      .filter((t) => t.group_id === groupId && visible(t))
      .sort((a, b) => b.updated_at.localeCompare(a.updated_at))
  }

  // While searching, a group with no matching members is hidden from the
  // roster; with no query every group shows (even empty ones).
  function groupVisible(groupId: string): boolean {
    return query.value.trim() === '' || membersOf(groupId).length > 0
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

  async function create(backend?: string): Promise<ThreadSummary> {
    const t = await api.createThread(backend ? { backend } : {})
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

  // hide / unhide: a web-local roster blacklist (no data is deleted). The
  // active thread, if hidden, stays selected; it simply leaves the default
  // list. Optimistic with revert on failure.
  async function hide(id: string): Promise<void> {
    const t = threads.value.find((x) => x.id === id)
    if (t) t.hidden = true
    try {
      await api.hideThread(id)
    } catch (e) {
      if (t) t.hidden = false
      throw e
    }
  }

  async function unhide(id: string): Promise<void> {
    const t = threads.value.find((x) => x.id === id)
    if (t) t.hidden = false
    try {
      await api.unhideThread(id)
    } catch (e) {
      if (t) t.hidden = true
      throw e
    }
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

  // ── Claude Code import (WA-2) ───────────────────────────────────────
  // Historical CC sessions carlos web does not yet own (the "+ new → open
  // existing CC session" modal lists these).
  async function importable(): Promise<ThreadSummary[]> {
    const res = await api.ccImportable()
    return res.sessions ?? []
  }

  // Adopt a historical CC session (idempotent server-side). Refresh the roster
  // so it joins the home view, then return the imported summary.
  async function importSession(id: string): Promise<ThreadSummary> {
    const t = await api.ccImport(id)
    await poll()
    return t
  }

  return {
    threads,
    activeId,
    active,
    children,
    query,
    showHidden,
    hiddenCount,
    ungrouped,
    byRepo,
    membersOf,
    groupVisible,
    rollup,
    setActive,
    poll,
    startPolling,
    stopPolling,
    attach,
    detach,
    create,
    remove,
    hide,
    unhide,
    loadChildren,
    setChildren,
    importable,
    importSession,
  }
})
