// carlos web · dependency-free line diff.
//
// Powers the write/edit tool viewers: edits ship a search/replace pair and
// writes ship full file content, both of which read far better as a colored
// unified diff than as a wall of mono text. We compute an LCS line diff in
// plain TS (the web app deliberately carries no diff/highlight dependency)
// and collapse long unchanged runs into a gutter so a 400-line file with a
// two-line change shows the change, not the haystack.

export type DiffOp = 'add' | 'del' | 'ctx' | 'gap'

export interface DiffLine {
  op: DiffOp
  text: string
  // 1-based line numbers in the old / new text; null where the line does
  // not exist on that side (an add has no old number, a del no new one).
  oldNo: number | null
  newNo: number | null
  // for op === 'gap': how many unchanged lines were collapsed.
  count?: number
}

export interface DiffResult {
  lines: DiffLine[]
  added: number
  removed: number
}

// maxCells guards the O(m*n) LCS table: past this product we fall back to a
// whole-block replace (every old line removed, every new line added) rather
// than allocate a multi-hundred-MB matrix. Tool payloads are small, so this
// only trips on pathological inputs.
const maxCells = 1_500_000

function splitLines(s: string): string[] {
  if (s === '') return []
  return s.replace(/\r?\n$/, '').split(/\r?\n/)
}

// lineDiff returns the line-level diff of oldText -> newText. contextPad is
// how many unchanged lines to keep around each change before collapsing the
// middle of a long unchanged run into a single 'gap' line.
export function lineDiff(oldText: string, newText: string, contextPad = 3): DiffResult {
  const a = splitLines(oldText)
  const b = splitLines(newText)

  let raw: DiffLine[]
  if (a.length * b.length > maxCells) {
    raw = replaceAll(a, b)
  } else {
    raw = lcsDiff(a, b)
  }

  const added = raw.reduce((n, l) => n + (l.op === 'add' ? 1 : 0), 0)
  const removed = raw.reduce((n, l) => n + (l.op === 'del' ? 1 : 0), 0)
  return { lines: collapseContext(raw, contextPad), added, removed }
}

function replaceAll(a: string[], b: string[]): DiffLine[] {
  const out: DiffLine[] = []
  a.forEach((t, i) => out.push({ op: 'del', text: t, oldNo: i + 1, newNo: null }))
  b.forEach((t, i) => out.push({ op: 'add', text: t, oldNo: null, newNo: i + 1 }))
  return out
}

function lcsDiff(a: string[], b: string[]): DiffLine[] {
  const m = a.length
  const n = b.length
  // lcs[i][j] = length of the longest common subsequence of a[i:] and b[j:].
  const lcs: number[][] = Array.from({ length: m + 1 }, () => new Array<number>(n + 1).fill(0))
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1])
    }
  }
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  let oldNo = 1
  let newNo = 1
  while (i < m && j < n) {
    if (a[i] === b[j]) {
      out.push({ op: 'ctx', text: a[i], oldNo: oldNo++, newNo: newNo++ })
      i++
      j++
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      out.push({ op: 'del', text: a[i], oldNo: oldNo++, newNo: null })
      i++
    } else {
      out.push({ op: 'add', text: b[j], oldNo: null, newNo: newNo++ })
      j++
    }
  }
  while (i < m) out.push({ op: 'del', text: a[i++], oldNo: oldNo++, newNo: null })
  while (j < n) out.push({ op: 'add', text: b[j++], oldNo: null, newNo: newNo++ })
  return out
}

// collapseContext replaces interior runs of unchanged lines longer than
// 2*pad+1 with a single 'gap' marker, keeping pad lines of context on each
// side of every change. Leading/trailing unchanged runs collapse fully
// (only pad lines kept) so an unchanged preamble doesn't dominate.
function collapseContext(lines: DiffLine[], pad: number): DiffLine[] {
  if (pad < 0) pad = 0
  const keep = new Array<boolean>(lines.length).fill(false)
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].op === 'add' || lines[i].op === 'del') {
      for (let k = Math.max(0, i - pad); k <= Math.min(lines.length - 1, i + pad); k++) {
        keep[k] = true
      }
    }
  }
  const out: DiffLine[] = []
  let run = 0
  for (let i = 0; i < lines.length; i++) {
    if (keep[i]) {
      out.push(lines[i])
      continue
    }
    run++
    const last = i === lines.length - 1 || keep[i + 1]
    if (last) {
      out.push({ op: 'gap', text: '', oldNo: null, newNo: null, count: run })
      run = 0
    }
  }
  return out
}
