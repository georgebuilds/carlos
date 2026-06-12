// carlos web · SSE lifecycle (client half of spec §9.3).
// One EventSource per attached thread. Persisted kinds carry id:<seq> on the
// frame, so the browser's native Last-Event-ID resume does the cursor work.
// On every (re)open the server replays the step-4 ephemeral snapshot; the
// transcript/approval stores replace their ephemeral state wholesale, never
// merge. Unknown kinds are ignored for forward compat.

import type { WireEvent } from './types'

export interface SseHandlers {
  onEvent: (ev: WireEvent) => void
  onOpen?: () => void
  onError?: (err: Event) => void
}

export interface SseConn {
  close: () => void
}

// EventSource cannot set Authorization headers, so the token rides as a query
// param (spec D9 carve-out for the stream endpoint only).
export function openStream(
  threadId: string,
  token: string | null,
  from: number,
  handlers: SseHandlers,
): SseConn {
  const params = new URLSearchParams()
  params.set('from', String(from))
  if (token) params.set('token', token)

  const url = `/api/threads/${encodeURIComponent(threadId)}/stream?${params.toString()}`
  const es = new EventSource(url)

  es.onopen = () => handlers.onOpen?.()
  es.onerror = (err) => handlers.onError?.(err)
  es.onmessage = (msg: MessageEvent<string>) => {
    let ev: WireEvent
    try {
      ev = JSON.parse(msg.data) as WireEvent
    } catch {
      return // malformed frame, ignore (e.g. a stray heartbeat comment)
    }
    if (!ev || typeof ev.kind !== 'string') return
    handlers.onEvent(ev)
  }

  return {
    close: () => es.close(),
  }
}
