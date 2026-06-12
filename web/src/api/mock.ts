// carlos web · dev-only mock backend.
// Gated behind `?mock` in the URL or VITE_MOCK=1. Patches window.fetch and
// window.EventSource so the real stores and client.ts run unmodified against
// fake wire data mirroring docs/_web-mockup-pressbox.html. Never bundled into a
// production build path on its own; main.ts only calls installMock() when the
// flag is present.

import type { ChildSnapshot, Group, ThreadSummary, WireEvent } from './types'

export function mockEnabled(): boolean {
  if (typeof window === 'undefined') return false
  if (new URLSearchParams(window.location.search).has('mock')) return true
  return import.meta.env?.VITE_MOCK === '1'
}

interface MockThread {
  summary: ThreadSummary
  events: WireEvent[]
  children: ChildSnapshot[]
  streamText?: string
  approval?: { request_id: string; name: string; input: unknown; layer_reason?: string }
}

const now = new Date()
const ago = (mins: number) => new Date(now.getTime() - mins * 60_000).toISOString()

let seq = 0
function ev(thread: string, kind: string, data: Record<string, unknown>): WireEvent {
  seq += 1
  return { seq, thread, ts: ago(0), kind, data }
}

const GROUPS: Group[] = [
  { id: 'g-web', name: 'carlos web', pos: 0, threads: 2 },
  { id: 'g-anneal', name: 'anneal', pos: 1, threads: 1 },
  { id: 'g-house', name: 'around the house', pos: 2, threads: 2 },
]

function cap(): ThreadSummary['capabilities'] {
  return { attach: true, spawn: true, approvals: true }
}

const THREADS: MockThread[] = [
  {
    summary: {
      id: '01JXQF8MWN', title: 'web: W-1 server skeleton', state: 'running',
      attached: true, frame: 'carlos', model: 'claude-fable-5', backend: 'carlos',
      user_msgs: 6, updated_at: ago(2), created_at: ago(40),
      preview: 'wire the SSE backfill, splice, then go live. gap repair per F3.',
      group_id: 'g-web', capabilities: cap(),
    },
    events: [
      ev('01JXQF8MWN', 'user_message', { text: 'start W-1: the server skeleton plus the read path. follow the spec ordering: subscribe first, backfill, splice, then live. gap repair is not optional.' }),
      ev('01JXQF8MWN', 'assistant_message', { text: 'On it. Plan: auth middleware first, then GET /threads over ListUserSessions, then the stream handler. The splice is the part worth being careful with, so I will write the overflow regression test before the handler itself.' }),
      ev('01JXQF8MWN', 'tool_call', { name: 'Read', input: 'internal/agent/eventlog.go' }),
      ev('01JXQF8MWN', 'tool_result', { name: 'Read', output_preview: '119: type EventLog interface {\n120:     Append(ctx context.Context, e Event) (int64, error)\n121:     Read(ctx context.Context, agentID string, fromSeq int64, limit int) ([]Event, error)\n122:     Subscribe(agentID string) (<-chan Event, func())\n123:     Close() error\n124: }', is_error: false, truncated: false }),
      ev('01JXQF8MWN', 'tool_call', { name: 'Bash', input: 'go test ./internal/web/... -run TestStreamBackfillSplice -count=1' }),
      ev('01JXQF8MWN', 'tool_result', { name: 'Bash', output_preview: 'ok   github.com/georgebuilds/carlos/internal/web   0.41s', is_error: false, truncated: false }),
      ev('01JXQF8MWN', 'state', { state: 'running', detail: 'turn 6' }),
    ],
    streamText: 'The splice dedupe holds the last backfilled seq and drops anything at or below it from the subscription buffer. With the overflow test forcing a 64-deep channel drop, the gap repair path re-reads seqs 88 through 91 and emits them in order before resuming live. Backfill, splice, and gap repair are all green. Next: the heartbeat comment line every 25s, then curl verification against the session you made this morning.',
    children: [
      { id: 'c1', title: 'explore: eventlog seams', state: 'done', last_tool: 'Read', tokens: 48000, cost_cents: 31, started_at: ago(38) },
      { id: 'c2', title: 'write: sse handler tests', state: 'running', last_tool: 'Bash', tokens: 112000, cost_cents: 74, started_at: ago(20) },
    ],
  },
  {
    summary: {
      id: '01JXQE2K7P', title: 'anneal: slice 7d preflight', state: 'blocked',
      attached: true, frame: 'anneal', model: 'claude-fable-5', backend: 'carlos',
      user_msgs: 3, updated_at: ago(11), created_at: ago(60),
      preview: 'preflight notes written. GO issued. wants to push the notes commit.',
      group_id: 'g-anneal', capabilities: cap(),
    },
    events: [
      ev('01JXQE2K7P', 'user_message', { text: 'run the slice 7d preflight. verify the symbolic dispatch premises against uop/ and write the notes file. GO or STOP at the end.' }),
      ev('01JXQE2K7P', 'tool_call', { name: 'Bash', input: 'go test ./uop/... -run TestSymbolicDispatch -count=1' }),
      ev('01JXQE2K7P', 'tool_result', { name: 'Bash', output_preview: 'ok   anneal/uop   2.83s\nok   anneal/rewrite   1.12s', is_error: false, truncated: false }),
      ev('01JXQE2K7P', 'tool_call', { name: 'Write', input: 'notes/slice-7d-preflight.md' }),
      ev('01JXQE2K7P', 'tool_result', { name: 'Write', output_preview: 'wrote 184 lines', is_error: false, truncated: false }),
      ev('01JXQE2K7P', 'assistant_message', { text: 'Preflight clean. All three premises held: the dispatch table covers N=2,4,8, the rewrite path stays reflection-free, and max-abs-diff against the oracle is 3.2e-6 at N=8. GO. Notes are committed locally; pushing them now.' }),
    ],
    approval: {
      request_id: 'req_8841', name: 'Bash', input: 'git push origin main',
      layer_reason: 'fallback prompt, write action outside the read-only allowlist',
    },
    children: [],
  },
  {
    summary: {
      id: '01JXQCT4RH', title: 'research: WebGPU timestamp queries', state: 'done',
      attached: false, frame: 'personal', model: 'claude-fable-5', backend: 'carlos',
      user_msgs: 2, updated_at: ago(60), created_at: ago(120),
      preview: 'report written to the vault. wgpu exposes them behind a feature flag.',
      group_id: 'g-house', capabilities: cap(),
    },
    events: [
      ev('01JXQCT4RH', 'user_message', { text: 'research: can anneal get GPU kernel timings via WebGPU timestamp queries without CGO? check wgpu-native and Dawn.' }),
      ev('01JXQCT4RH', 'research_phase', { phase: 'sweep done, 14 sources, 3 verified claims', done: true, elapsed_ms: 252000 }),
      ev('01JXQCT4RH', 'assistant_message', { text: 'Short answer: yes, behind a feature flag. Both wgpu-native and Dawn expose timestamp-query when the adapter supports it, and the zero-CGO bindings already surface the feature bits. The catch is quantization: Metal rounds to microseconds unless you request unfiltered timing. Full report with citations is in the vault under personal/research/webgpu-timestamps.md.' }),
    ],
    children: [
      { id: 'c3', title: 'sweep: wgpu-native docs', state: 'done', last_tool: 'WebFetch', tokens: 61000, cost_cents: 42, started_at: ago(110) },
      { id: 'c4', title: 'sweep: dawn source', state: 'done', last_tool: 'WebFetch', tokens: 55000, cost_cents: 38, started_at: ago(108) },
      { id: 'c5', title: 'verify: metal quantization', state: 'done', last_tool: 'WebSearch', tokens: 23000, cost_cents: 17, started_at: ago(100) },
    ],
  },
  {
    summary: {
      id: '01JXQB9XVC', title: 'vault: weekly dashboard sweep', state: 'awaiting_input',
      attached: true, frame: 'personal', model: 'claude-fable-5', backend: 'carlos',
      user_msgs: 4, updated_at: ago(26), created_at: ago(70),
      preview: 'dashboard regenerated. 3 stale todos found, asked which to archive.',
      group_id: 'g-house', capabilities: cap(),
    },
    events: [
      ev('01JXQB9XVC', 'user_message', { text: 'sweep the vault dashboard. regenerate the unified todo view and flag anything stale.' }),
      ev('01JXQB9XVC', 'tool_call', { name: 'Read', input: '/Volumes/nas/vault/dashboard.md' }),
      ev('01JXQB9XVC', 'tool_result', { name: 'Read', output_preview: '# dashboard\n\n## work\n- [ ] ludus: review PR #214\n- [ ] ludus: AI feature spike notes\n\n## personal\n- [ ] carlos: W-0 runtime extraction\n- [ ] kino: OFX importer edge cases ...', is_error: false, truncated: true }),
      ev('01JXQB9XVC', 'assistant_message', { text: 'Dashboard regenerated. Three todos have not moved in over a month: "kino: OFX importer edge cases", "degu: thumbnail cache invalidation", and "packrat: OSM region diffing". Want me to archive them to the someday list, or keep any of them hot?' }),
    ],
    children: [],
  },
  {
    summary: {
      id: '01JXQ9KD2M', title: 'chat with george (anthropic)', state: 'running',
      attached: false, frame: 'carlos', model: 'claude-fable-5', backend: 'carlos',
      user_msgs: 18, updated_at: ago(0), created_at: ago(200),
      preview: 'this is your TUI session. heartbeat is fresh, so attach is guarded.',
      owner: 'tui', heartbeat_age: '4s', capabilities: cap(),
    },
    events: [
      ev('01JXQ9KD2M', 'user_message', { text: 'what is left on the frames audit checklist?' }),
      ev('01JXQ9KD2M', 'assistant_message', { text: 'Two boxes open: the mid-conversation Ctrl+F switch with a sub-agent in flight, and the cwd-hint fallback when the vault is unmounted. Everything else is checked off as of Tuesday.' }),
      ev('01JXQ9KD2M', 'state', { state: 'running', detail: 'transcript shown read-only, live events stay in the owning process (spec F3)' }),
    ],
    children: [],
  },
  {
    summary: {
      id: '01JXQ7VN8B', title: 'chore: bump goreleaser to v2', state: 'failed',
      attached: false, frame: 'carlos', model: 'claude-fable-5', backend: 'carlos',
      user_msgs: 1, updated_at: ago(180), created_at: ago(190),
      preview: 'release dry-run failed: deprecated archive format key.',
      group_id: 'g-web', capabilities: cap(),
    },
    events: [
      ev('01JXQ7VN8B', 'user_message', { text: 'bump goreleaser to v2 and run the release dry-run.' }),
      ev('01JXQ7VN8B', 'tool_call', { name: 'Bash', input: 'goreleaser release --snapshot --clean' }),
      ev('01JXQ7VN8B', 'tool_result', { name: 'Bash', output_preview: 'error: yaml: unmarshal errors:\n  line 12: field format not found in type config.Archive\n(deprecated in v2: use formats)', is_error: true, truncated: false }),
      ev('01JXQ7VN8B', 'assistant_message', { text: 'The dry-run failed: .goreleaser.yaml line 12 uses the v1 "format" key, which v2 renamed to "formats". I stopped rather than edit the release config without you looking at it first, since this file gates what ships.', error: true }),
    ],
    children: [],
  },
]

function findThread(id: string): MockThread | undefined {
  return THREADS.find((t) => t.summary.id === id)
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

// ── fetch shim ──────────────────────────────────────────────────────────
const realFetch = typeof window !== 'undefined' ? window.fetch.bind(window) : fetch

async function mockFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
  const path = url.replace(/^https?:\/\/[^/]+/, '').split('?')[0]
  const method = (init?.method ?? 'GET').toUpperCase()

  if (path === '/api/meta') {
    return jsonResponse({ version: 'mock-0.1.0', addr: '127.0.0.1:7777', backends: ['carlos'] })
  }
  if (path === '/api/threads' && method === 'GET') {
    return jsonResponse(THREADS.map((t) => t.summary))
  }
  if (path === '/api/threads' && method === 'POST') {
    const id = '01JXNEW' + Math.floor(Math.random() * 1000).toString().padStart(3, '0')
    const summary: ThreadSummary = {
      id, title: 'untitled thread', state: 'awaiting_input', attached: true,
      frame: 'carlos', model: 'claude-fable-5', backend: 'carlos',
      user_msgs: 0, updated_at: ago(0), created_at: ago(0),
      preview: 'nothing yet. say something.', capabilities: cap(),
    }
    THREADS.unshift({ summary, events: [], children: [] })
    return jsonResponse(summary)
  }
  if (path === '/api/groups' && method === 'GET') {
    return jsonResponse(GROUPS)
  }

  const m = path.match(/^\/api\/threads\/([^/]+)(\/(\w+))?(\/([^/]+))?$/)
  if (m) {
    const id = decodeURIComponent(m[1])
    const sub = m[3]
    const t = findThread(id)
    if (!t) return jsonResponse({ code: 'not_found', message: 'no such thread' }, 404)

    if (!sub && method === 'GET') return jsonResponse(t.summary)
    if (!sub && method === 'DELETE') {
      const deleted = 1 + t.children.length
      const idx = THREADS.indexOf(t)
      if (idx >= 0) THREADS.splice(idx, 1)
      return jsonResponse({ deleted })
    }
    if (sub === 'events') return jsonResponse(t.events)
    if (sub === 'children') return jsonResponse({ children: t.children })
    // attach mirrors the real server: 200 with the refreshed summary (the
    // store adopts frame/state from it).
    if (sub === 'attach') {
      t.summary.attached = true
      if (!t.summary.frame) t.summary.frame = 'carlos'
      return jsonResponse(t.summary)
    }
    if (sub === 'detach') { t.summary.attached = false; return jsonResponse(null, 204) }
    if (sub === 'messages') {
      t.summary.user_msgs += 1
      return jsonResponse(null, 204)
    }
    if (sub === 'approvals') {
      // resolve: drop the pending approval if present
      if (t.approval) t.approval = undefined
      return jsonResponse(null, 204)
    }
    if (sub === 'group') return jsonResponse(null, 204)
  }

  // unknown path: defer to the real network (lets fonts etc. through)
  return realFetch(input as RequestInfo, init)
}

// ── EventSource shim ─────────────────────────────────────────────────────
// Minimal: emits the step-4 ephemeral snapshot (pending approval if any), then
// drips the streamText as delta events for a running thread, then seals it.
class MockEventSource {
  onopen: ((ev: Event) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  private timers: ReturnType<typeof setTimeout>[] = []
  private closed = false

  constructor(url: string) {
    const u = new URL(url, 'http://localhost')
    const m = u.pathname.match(/^\/api\/threads\/([^/]+)\/stream$/)
    const id = m ? decodeURIComponent(m[1]) : ''
    const t = findThread(id)
    this.timers.push(
      setTimeout(() => {
        if (this.closed) return
        this.onopen?.(new Event('open'))
        if (!t) return
        // step-4 snapshot: re-emit a pending approval as an ephemeral request
        if (t.approval) {
          this.emit({ thread: id, ts: ago(0), kind: 'approval_request', data: t.approval })
        }
        if (t.summary.state === 'running' && t.streamText && !t.summary.owner) {
          this.dripStream(id, t.streamText)
        }
      }, 60),
    )
  }

  private emit(ev: WireEvent): void {
    this.onmessage?.(new MessageEvent('message', { data: JSON.stringify(ev) }))
  }

  private dripStream(id: string, text: string): void {
    let i = 0
    const step = () => {
      if (this.closed) return
      if (i >= text.length) {
        // seal: persisted assistant_message lands with a fresh seq
        seq += 1
        this.emit({ seq, thread: id, ts: ago(0), kind: 'assistant_message', data: { text } })
        return
      }
      const chunk = 2 + Math.floor(Math.random() * 4)
      this.emit({ thread: id, ts: ago(0), kind: 'delta', data: { text: text.slice(i, i + chunk) } })
      i += chunk
      this.timers.push(setTimeout(step, 40))
    }
    // reset any prior buffer first, then drip
    this.emit({ thread: id, ts: ago(0), kind: 'delta_reset', data: {} })
    this.timers.push(setTimeout(step, 40))
  }

  close(): void {
    this.closed = true
    for (const tm of this.timers) clearTimeout(tm)
  }
}

export function installMock(): void {
  if (typeof window === 'undefined') return
  window.fetch = mockFetch as typeof window.fetch
  ;(window as unknown as { EventSource: unknown }).EventSource =
    MockEventSource as unknown as typeof EventSource
  // seed a token into the hash so the handshake path exercises end to end
  if (!window.location.hash) {
    window.location.hash = 'token=mock-dev-token'
  }
  // eslint-disable-next-line no-console
  console.info('[carlos web] mock backend installed')
}
