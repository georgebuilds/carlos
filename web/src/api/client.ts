// carlos web · fetch wrapper.
// Injects the bearer token, normalizes the error envelope, and types the 409
// (thread_owned) refusal so the guard UI can surface it verbatim.

import type {
  ApprovalDecision,
  ChildSnapshot,
  Group,
  Meta,
  ThreadSummary,
  WireError,
  WireEvent,
} from './types'

export class ApiError extends Error implements WireError {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
  get isOwned(): boolean {
    return this.status === 409 && this.code === 'thread_owned'
  }
  // a delete refused because a live process is driving the thread (detach first).
  get isThreadLive(): boolean {
    return this.status === 409 && this.code === 'thread_live'
  }
  get isGone(): boolean {
    return this.status === 404
  }
}

// the connection store seeds this; held in memory only, never persisted.
let token: string | null = null
export function setToken(t: string | null): void {
  token = t
}
export function getToken(): string | null {
  return token
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = {}
  if (token) headers['Authorization'] = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })

  if (res.status === 204) return undefined as T

  let payload: unknown = null
  const text = await res.text()
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = text
    }
  }

  if (!res.ok) {
    // The server's envelope is { error: { code, message } } (spec §9). Parse
    // that shape first; fall back to a flat { code, message } or a bare
    // string so a non-JSON / unexpected body still yields a readable message
    // instead of "[object Object]".
    const p = (payload ?? {}) as {
      error?: { code?: string; message?: string } | string
      code?: string
      message?: string
    }
    let code = `http_${res.status}`
    let message = res.statusText
    if (p.error && typeof p.error === 'object') {
      code = p.error.code ?? code
      message = p.error.message ?? message
    } else if (typeof p.error === 'string') {
      message = p.error
    } else {
      code = p.code ?? code
      message = p.message ?? message
    }
    if (typeof payload === 'string' && payload) message = payload
    throw new ApiError(res.status, code, message)
  }

  return payload as T
}

export const api = {
  // ── meta ──
  meta: () => request<Meta>('GET', '/api/meta'),

  // ── threads ──
  listThreads: () => request<ThreadSummary[]>('GET', '/api/threads'),
  createThread: (body?: { title?: string; frame?: string }) =>
    request<ThreadSummary>('POST', '/api/threads', body ?? {}),
  getThread: (id: string) =>
    request<ThreadSummary>('GET', `/api/threads/${encodeURIComponent(id)}`),
  deleteThread: (id: string) =>
    request<{ deleted: number }>('DELETE', `/api/threads/${encodeURIComponent(id)}`),
  events: (id: string, from = 0, limit = 500) =>
    request<WireEvent[]>(
      'GET',
      `/api/threads/${encodeURIComponent(id)}/events?from=${from}&limit=${limit}`,
    ),
  // attach answers with the refreshed summary: the backend resolves the
  // thread's frame at attach time, and the store adopts it from here
  // rather than waiting out the next roster poll.
  attach: (id: string) =>
    request<ThreadSummary>('POST', `/api/threads/${encodeURIComponent(id)}/attach`),
  detach: (id: string) =>
    request<void>('POST', `/api/threads/${encodeURIComponent(id)}/detach`),
  sendMessage: (id: string, text: string) =>
    request<void>('POST', `/api/threads/${encodeURIComponent(id)}/messages`, { text }),
  resolveApproval: (id: string, requestId: string, decision: ApprovalDecision) =>
    request<void>(
      'POST',
      `/api/threads/${encodeURIComponent(id)}/approvals/${encodeURIComponent(requestId)}`,
      { decision },
    ),
  children: (id: string) =>
    request<{ children: ChildSnapshot[] }>(
      'GET',
      `/api/threads/${encodeURIComponent(id)}/children`,
    ),

  // ── groups (plan §4.3) ──
  listGroups: () => request<Group[]>('GET', '/api/groups'),
  createGroup: (name: string) => request<Group>('POST', '/api/groups', { name }),
  patchGroup: (id: string, body: { name?: string; pos?: number }) =>
    request<Group>('PATCH', `/api/groups/${encodeURIComponent(id)}`, body),
  deleteGroup: (id: string) =>
    request<void>('DELETE', `/api/groups/${encodeURIComponent(id)}`),
  setThreadGroup: (id: string, groupId: string | null) =>
    request<void>('PUT', `/api/threads/${encodeURIComponent(id)}/group`, {
      group_id: groupId,
    }),
}
