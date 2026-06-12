import { describe, it, expect } from 'vitest'
import { buildRows, stringifyInput } from './render'
import type { WireEvent } from './types'

function e(seq: number, kind: string, data: Record<string, unknown>): WireEvent {
  return { seq, thread: 't', ts: '', kind, data }
}

describe('stringifyInput', () => {
  it('passes strings through', () => {
    expect(stringifyInput('git push')).toBe('git push')
  })
  it('serializes objects', () => {
    expect(stringifyInput({ path: 'a.go' })).toBe('{"path":"a.go"}')
  })
  it('handles null', () => {
    expect(stringifyInput(null)).toBe('')
  })
})

describe('buildRows folds tool_call + tool_result', () => {
  it('merges a call with its matching result into one tool row', () => {
    const rows = buildRows([
      e(1, 'tool_call', { name: 'Bash', input: 'ls' }),
      e(2, 'tool_result', { name: 'Bash', output_preview: 'a\nb', is_error: false, truncated: false }),
    ])
    expect(rows).toHaveLength(1)
    expect(rows[0]).toMatchObject({
      type: 'tool',
      name: 'Bash',
      input: 'ls',
      output: 'a\nb',
      isError: false,
      truncated: false,
    })
  })

  it('carries the truncated flag through', () => {
    const rows = buildRows([
      e(1, 'tool_call', { name: 'Read', input: 'big.md' }),
      e(2, 'tool_result', { name: 'Read', output_preview: '...', is_error: false, truncated: true }),
    ])
    expect((rows[0] as { truncated: boolean }).truncated).toBe(true)
  })

  it('renders state and research_phase as event lines', () => {
    const rows = buildRows([
      e(1, 'state', { state: 'running', detail: 'turn 6' }),
      e(2, 'research_phase', { phase: 'sweep done', done: true, elapsed_ms: 252000 }),
    ])
    expect(rows[0]).toMatchObject({ type: 'event', text: 'state: running · turn 6' })
    expect((rows[1] as { text: string }).text).toContain('252s')
  })

  it('skips unknown kinds (forward compat)', () => {
    const rows = buildRows([
      e(1, 'user_message', { text: 'hi' }),
      e(2, 'children', { children: [] }),
      e(3, 'some_future_kind', {}),
    ])
    expect(rows.map((r) => r.type)).toEqual(['user'])
  })

  it('renders an orphan tool_result standalone', () => {
    const rows = buildRows([
      e(1, 'tool_result', { name: 'Bash', output_preview: 'late', is_error: true, truncated: false }),
    ])
    expect(rows[0]).toMatchObject({ type: 'tool', name: 'Bash', output: 'late', isError: true })
  })
})
