// carlos web · transcript render helpers.
// Folds the persisted event stream into render rows: tool_call + matching
// tool_result collapse into one ToolCard row; state / research_phase become
// hairline event lines; everything unknown is skipped (forward compat).

import type { WireEvent } from './types'

export type RenderRow =
  | { type: 'user'; key: string; text: string }
  | { type: 'assistant'; key: string; text: string; error: boolean }
  | {
      type: 'tool'
      key: string
      name: string
      input: string
      output: string
      isError: boolean
      truncated: boolean
    }
  | { type: 'event'; key: string; text: string }

export function stringifyInput(input: unknown): string {
  if (input == null) return ''
  if (typeof input === 'string') return input
  try {
    return JSON.stringify(input)
  } catch {
    return String(input)
  }
}

function eventText(ev: WireEvent): string {
  if (ev.kind === 'state') {
    const d = ev.data as { state?: string; detail?: string }
    const head = `state: ${d.state ?? ''}`
    return d.detail ? `${head} · ${d.detail}` : head
  }
  if (ev.kind === 'research_phase') {
    const d = ev.data as { phase?: string; elapsed_ms?: number; err?: string }
    const secs = d.elapsed_ms ? `${Math.round(d.elapsed_ms / 1000)}s` : ''
    const base = `research: ${d.phase ?? ''}`
    if (d.err) return `${base} · error: ${d.err}`
    return secs ? `${base} · ${secs}` : base
  }
  if (ev.kind === 'session_reset') return 'session reset'
  if (ev.kind === 'shell_start') return 'shell session started'
  if (ev.kind === 'shell_end') return 'shell session ended'
  return ev.kind
}

// Build render rows from persisted events (assumed seq-ordered).
export function buildRows(events: WireEvent[]): RenderRow[] {
  const rows: RenderRow[] = []
  // pending tool_call awaiting its result, keyed by name (FIFO per name).
  const pending: Record<string, number> = {} // name -> rows index

  for (const ev of events) {
    const key = ev.seq !== undefined ? `s${ev.seq}` : `${ev.kind}-${rows.length}`
    switch (ev.kind) {
      case 'user_message': {
        const d = ev.data as { text?: string }
        rows.push({ type: 'user', key, text: d.text ?? '' })
        break
      }
      case 'assistant_message': {
        const d = ev.data as { text?: string; error?: boolean }
        rows.push({ type: 'assistant', key, text: d.text ?? '', error: !!d.error })
        break
      }
      case 'tool_call': {
        const d = ev.data as { name?: string; input?: unknown }
        const name = d.name ?? 'tool'
        rows.push({
          type: 'tool',
          key,
          name,
          input: stringifyInput(d.input),
          output: '',
          isError: false,
          truncated: false,
        })
        pending[name] = rows.length - 1
        break
      }
      case 'tool_result': {
        const d = ev.data as {
          name?: string
          output_preview?: string
          is_error?: boolean
          truncated?: boolean
        }
        const name = d.name ?? 'tool'
        const idx = pending[name]
        if (idx !== undefined && rows[idx]?.type === 'tool') {
          const row = rows[idx] as Extract<RenderRow, { type: 'tool' }>
          row.output = d.output_preview ?? ''
          row.isError = !!d.is_error
          row.truncated = !!d.truncated
          delete pending[name]
        } else {
          // orphan result (no matching call seen): render standalone
          rows.push({
            type: 'tool',
            key,
            name,
            input: '',
            output: d.output_preview ?? '',
            isError: !!d.is_error,
            truncated: !!d.truncated,
          })
        }
        break
      }
      case 'state':
      case 'research_phase':
      case 'session_reset':
      case 'shell_start':
      case 'shell_end': {
        rows.push({ type: 'event', key, text: eventText(ev) })
        break
      }
      default:
        // unknown / non-render kinds (children, approval_resolved, ...): skip
        break
    }
  }
  return rows
}
