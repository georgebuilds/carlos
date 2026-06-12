// carlos web · groups store (plan §4).
// Two separate state slices: server data (group list + membership, sourced from
// GET /api/groups and ThreadSummary.group_id) and local data (the collapsed
// set, persisted to localStorage). Membership is data; collapse is presentation.

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import type { Group } from '@/api/types'

const COLLAPSE_KEY = 'carlos.web.groups.collapsed'

// Read the collapsed set from localStorage. Tolerant of corruption/absence.
export function loadCollapsed(store: Storage): Set<string> {
  try {
    const raw = store.getItem(COLLAPSE_KEY)
    if (!raw) return new Set()
    const arr = JSON.parse(raw)
    if (Array.isArray(arr)) return new Set(arr.filter((x) => typeof x === 'string'))
    return new Set()
  } catch {
    return new Set()
  }
}

export function saveCollapsed(store: Storage, set: Set<string>): void {
  try {
    store.setItem(COLLAPSE_KEY, JSON.stringify([...set]))
  } catch {
    // private mode / quota: collapse silently degrades to per-session
  }
}

export const useGroupsStore = defineStore('groups', () => {
  const groups = ref<Group[]>([])
  const collapsed = ref<Set<string>>(
    typeof localStorage !== 'undefined' ? loadCollapsed(localStorage) : new Set(),
  )

  function isCollapsed(id: string): boolean {
    return collapsed.value.has(id)
  }

  function toggle(id: string): void {
    const next = new Set(collapsed.value)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    collapsed.value = next
    if (typeof localStorage !== 'undefined') saveCollapsed(localStorage, next)
  }

  async function refresh(): Promise<void> {
    groups.value = (await api.listGroups()).sort((a, b) => a.pos - b.pos)
  }

  async function create(name: string): Promise<Group> {
    const g = await api.createGroup(name)
    await refresh()
    return g
  }

  async function rename(id: string, name: string): Promise<void> {
    await api.patchGroup(id, { name })
    await refresh()
  }

  async function reorder(id: string, pos: number): Promise<void> {
    await api.patchGroup(id, { pos })
    await refresh()
  }

  async function remove(id: string): Promise<void> {
    await api.deleteGroup(id)
    // members revert to ungrouped server-side; drop any local collapse entry
    if (collapsed.value.has(id)) toggle(id)
    await refresh()
  }

  async function assign(threadId: string, groupId: string | null): Promise<void> {
    await api.setThreadGroup(threadId, groupId)
  }

  return {
    groups,
    collapsed,
    isCollapsed,
    toggle,
    refresh,
    create,
    rename,
    reorder,
    remove,
    assign,
  }
})
