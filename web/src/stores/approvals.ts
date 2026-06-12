// carlos web · approvals store (spec §5.2).
// Entries appear on approval_request (ephemeral, no seq), keyed by request_id.
// They clear on approval_resolved regardless of which client resolved.
// Resolving an already-cleared (expired) request surfaces the 404 as a quiet
// toast, not an error state.

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, ApiError } from '@/api/client'
import type { ApprovalDecision, WireEvent } from '@/api/types'

export interface PendingApproval {
  requestId: string
  name: string
  input: unknown
  layerReason?: string
}

export const useApprovalsStore = defineStore('approvals', () => {
  // keyed by thread id, then by request_id
  const byThread = ref<Record<string, Record<string, PendingApproval>>>({})

  function pendingFor(threadId: string): PendingApproval[] {
    const bucket = byThread.value[threadId]
    return bucket ? Object.values(bucket) : []
  }

  function add(threadId: string, a: PendingApproval): void {
    if (!byThread.value[threadId]) byThread.value[threadId] = {}
    byThread.value[threadId][a.requestId] = a
  }

  function remove(threadId: string, requestId: string): void {
    const bucket = byThread.value[threadId]
    if (bucket) delete bucket[requestId]
  }

  // Replace the whole pending set for a thread (used on SSE (re)open with the
  // step-4 snapshot: never merge).
  function replace(threadId: string, list: PendingApproval[]): void {
    const next: Record<string, PendingApproval> = {}
    for (const a of list) next[a.requestId] = a
    byThread.value[threadId] = next
  }

  // Drive from a wire event. Returns true if it consumed the event.
  function ingest(threadId: string, ev: WireEvent): boolean {
    if (ev.kind === 'approval_request') {
      const d = ev.data as {
        request_id: string
        name: string
        input: unknown
        layer_reason?: string
      }
      add(threadId, {
        requestId: d.request_id,
        name: d.name,
        input: d.input,
        layerReason: d.layer_reason,
      })
      return true
    }
    if (ev.kind === 'approval_resolved') {
      const d = ev.data as { request_id: string }
      remove(threadId, d.request_id)
      return true
    }
    return false
  }

  // Resolve a request via REST. On 404 (already resolved elsewhere / expired)
  // we clear it locally and report via the quiet-toast callback, not an error.
  async function resolve(
    threadId: string,
    requestId: string,
    decision: ApprovalDecision,
    onQuiet?: (msg: string) => void,
  ): Promise<void> {
    try {
      await api.resolveApproval(threadId, requestId, decision)
    } catch (e) {
      if (e instanceof ApiError && e.isGone) {
        remove(threadId, requestId)
        onQuiet?.('that request was already resolved')
        return
      }
      throw e
    }
    // optimistic clear; the approval_resolved fan-out will confirm
    remove(threadId, requestId)
  }

  return { byThread, pendingFor, add, remove, replace, ingest, resolve }
})
