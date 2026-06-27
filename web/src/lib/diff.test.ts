import { describe, it, expect } from 'vitest'
import { lineDiff } from './diff'

describe('lineDiff', () => {
  it('reports a new file as all additions', () => {
    const r = lineDiff('', 'a\nb\nc')
    expect(r.added).toBe(3)
    expect(r.removed).toBe(0)
    expect(r.lines.filter((l) => l.op === 'add')).toHaveLength(3)
  })

  it('diffs a search/replace edit', () => {
    const r = lineDiff('hello\nworld', 'hello\nthere')
    expect(r.added).toBe(1)
    expect(r.removed).toBe(1)
    // "hello" stays as context, "world" -> "there".
    const ctx = r.lines.find((l) => l.op === 'ctx')
    expect(ctx?.text).toBe('hello')
    expect(r.lines.find((l) => l.op === 'del')?.text).toBe('world')
    expect(r.lines.find((l) => l.op === 'add')?.text).toBe('there')
  })

  it('assigns old/new line numbers correctly', () => {
    const r = lineDiff('a\nb', 'a\nB\nc')
    const add = r.lines.filter((l) => l.op === 'add')
    // 'B' is new line 2, 'c' new line 3.
    expect(add.map((l) => l.newNo)).toEqual([2, 3])
    expect(r.lines.find((l) => l.op === 'del')?.oldNo).toBe(2)
  })

  it('collapses long unchanged runs into a gap', () => {
    const big = Array.from({ length: 30 }, (_, i) => `line${i}`).join('\n')
    const changed = big + '\nTAIL'
    const r = lineDiff(big, changed, 3)
    const gap = r.lines.find((l) => l.op === 'gap')
    expect(gap).toBeTruthy()
    expect(gap!.count).toBeGreaterThan(0)
    // The single tail addition survives.
    expect(r.lines.some((l) => l.op === 'add' && l.text === 'TAIL')).toBe(true)
  })

  it('handles identical text as all context, no changes', () => {
    const r = lineDiff('same\ntext', 'same\ntext')
    expect(r.added).toBe(0)
    expect(r.removed).toBe(0)
  })

  it('falls back to whole-block replace on huge inputs (maxCells guard)', () => {
    const a = Array.from({ length: 1300 }, (_, i) => `a${i}`).join('\n')
    const b = Array.from({ length: 1300 }, (_, i) => `b${i}`).join('\n')
    const r = lineDiff(a, b)
    expect(r.added).toBe(1300)
    expect(r.removed).toBe(1300)
  })

  it('collapses a unchanged run in the middle, keeping context both sides', () => {
    const lines = Array.from({ length: 30 }, (_, i) => `l${i}`)
    const oldT = lines.join('\n')
    const newT = lines.map((l, i) => (i === 15 ? 'CHANGED' : l)).join('\n')
    const r = lineDiff(oldT, newT, 3)
    expect(r.lines.filter((l) => l.op === 'gap').length).toBeGreaterThanOrEqual(1)
    // context lines immediately around the change are preserved
    expect(r.lines.some((l) => l.op === 'ctx' && l.text === 'l14')).toBe(true)
    expect(r.lines.some((l) => l.op === 'add' && l.text === 'CHANGED')).toBe(true)
  })
})
