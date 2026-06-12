// carlos web · transcript store (spec §5.2 invariants).
// Per-thread: persisted events keyed and ordered by seq (duplicates dropped on
// insert, reconnect-splice safety on the client too); a live delta buffer that
// lives BESIDE the event list, never inside it. delta_reset clears the buffer;
// an arriving assistant_message seals it (the buffer empties, the row lands as
// a persisted event).

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { openStream, type SseConn } from '@/api/sse'
import { api } from '@/api/client'
import type { WireEvent } from '@/api/types'

interface ThreadTranscript {
  // persisted events, ordered by seq ascending, deduped
  events: WireEvent[]
  seqs: Set<number>
  // live delta buffer (ephemeral, no seq); empty string means no live stream
  delta: string
  // highest seq seen, the SSE resume cursor
  maxSeq: number
  conn: SseConn | null
}

function emptyTranscript(): ThreadTranscript {
  return { events: [], seqs: new Set(), delta: '', maxSeq: 0, conn: null }
}

// Insert a persisted event in seq order, dropping duplicates. Out-of-order and
// repeated seqs are both handled (binary-ish linear insert; transcripts are
// small enough that this stays cheap). Exported for unit tests.
export function insertEvent(t: ThreadTranscript, ev: WireEvent): void {
  if (ev.seq === undefined) return // not a persisted event
  if (t.seqs.has(ev.seq)) return // duplicate, drop
  t.seqs.add(ev.seq)
  if (ev.seq > t.maxSeq) t.maxSeq = ev.seq

  // fast path: append (the common case, in-order delivery)
  const arr = t.events
  if (arr.length === 0 || ev.seq > arr[arr.length - 1].seq!) {
    arr.push(ev)
    return
  }
  // slow path: find insertion point to keep ascending order
  let lo = 0
  let hi = arr.length
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (arr[mid].seq! < ev.seq) lo = mid + 1
    else hi = mid
  }
  arr.splice(lo, 0, ev)
}

// Apply one ephemeral or persisted event to the transcript. Returns nothing;
// mutates in place. Pulled out so tests can drive the reducer directly.
export function applyEvent(t: ThreadTranscript, ev: WireEvent): void {
  switch (ev.kind) {
    case 'delta': {
      const text = (ev.data as { text?: string }).text ?? ''
      t.delta += text
      return
    }
    case 'delta_reset': {
      t.delta = ''
      return
    }
    case 'assistant_message': {
      // seal: the buffer empties, the assistant row lands as a persisted event
      t.delta = ''
      insertEvent(t, ev)
      return
    }
    case 'session_reset': {
      // a reset clears the visible transcript; persisted seq still recorded so
      // the marker renders and the cursor advances
      t.events = []
      t.seqs = ev.seq !== undefined ? new Set([ev.seq]) : new Set()
      t.delta = ''
      if (ev.seq !== undefined) {
        t.events.push(ev)
        if (ev.seq > t.maxSeq) t.maxSeq = ev.seq
      }
      return
    }
    default: {
      // every other persisted kind (user_message, tool_call, tool_result,
      // state, research_phase, shell_start/end, children, approval_resolved)
      // is just inserted by seq; ephemeral approval_request is handled by the
      // approvals store and carries no seq, so insertEvent ignores it.
      insertEvent(t, ev)
    }
  }
}

export const useTranscriptStore = defineStore('transcript', () => {
  const byThread = ref<Record<string, ThreadTranscript>>({})

  function get(id: string): ThreadTranscript {
    // Read back through byThread.value AFTER assigning so callers mutate the
    // reactive proxy, not the raw object. Returning the freshly-created raw
    // object would let backfill/ingest push events that Vue never tracks, so
    // the transcript loads into memory but the view never re-renders.
    if (!byThread.value[id]) {
      byThread.value[id] = emptyTranscript()
    }
    return byThread.value[id]
  }

  function events(id: string): WireEvent[] {
    return byThread.value[id]?.events ?? []
  }

  function delta(id: string): string {
    return byThread.value[id]?.delta ?? ''
  }

  // Replace the ephemeral state wholesale on (re)open. The persisted events are
  // kept; only the delta buffer resets, since the snapshot is the new truth.
  function resetEphemeral(id: string): void {
    get(id).delta = ''
  }

  // Backfill persisted events via REST, then go live over SSE.
  async function backfill(id: string): Promise<void> {
    const t = get(id)
    const evs = await api.events(id, 0, 1000)
    for (const ev of evs) applyEvent(t, ev)
  }

  function ingest(id: string, ev: WireEvent): void {
    applyEvent(get(id), ev)
  }

  function startStream(
    id: string,
    token: string | null,
    onAnyEvent?: (ev: WireEvent) => void,
  ): void {
    const t = get(id)
    if (t.conn) t.conn.close()
    t.conn = openStream(id, token, t.maxSeq, {
      onOpen: () => {
        // step-4 snapshot incoming: drop the live buffer, the snapshot is truth
        resetEphemeral(id)
      },
      onEvent: (ev) => {
        applyEvent(t, ev)
        onAnyEvent?.(ev)
      },
    })
  }

  function stopStream(id: string): void {
    const t = byThread.value[id]
    if (t?.conn) {
      t.conn.close()
      t.conn = null
    }
  }

  function clear(id: string): void {
    stopStream(id)
    byThread.value[id] = emptyTranscript()
  }

  return {
    byThread,
    get,
    events,
    delta,
    resetEphemeral,
    backfill,
    ingest,
    startStream,
    stopStream,
    clear,
  }
})
