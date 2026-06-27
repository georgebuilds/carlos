// carlos web · tool categorization for the redesigned tool card.
//
// Maps a tool name + its raw input to a display kind, a small monospace
// glyph (no icon-font dependency), and a one-line "primary argument" summary
// for the collapsed invocation chip. The kind drives which expanded viewer
// the card routes to (diff for write/edit, terminal for bash, etc.).

export type ToolKind = 'write' | 'edit' | 'read' | 'bash' | 'search' | 'fetch' | 'mcp' | 'tool'

export interface ToolMeta {
  kind: ToolKind
  // glyph: a single mono char shown before the tool name in the chip.
  glyph: string
  // display: the tool name to show (MCP tools drop the "<server>__" prefix).
  display: string
  // server: the MCP server prefix when this is an MCP tool, else ''.
  server: string
  // primary: the most meaningful argument, summarized for the chip (path,
  // command, query, url, ...). May be '' when nothing useful is present.
  primary: string
}

const mcpSeparator = '__'

function asRecord(input: unknown): Record<string, unknown> {
  return input && typeof input === 'object' && !Array.isArray(input)
    ? (input as Record<string, unknown>)
    : {}
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

// firstString returns the first present, non-empty string field from keys.
function firstString(rec: Record<string, unknown>, keys: string[]): string {
  for (const k of keys) {
    const s = str(rec[k])
    if (s !== '') return s
  }
  return ''
}

// classify maps a bare (already de-prefixed) tool name to a kind.
function classify(name: string): ToolKind {
  const n = name.toLowerCase()
  if (n === 'write') return 'write'
  if (n === 'edit' || n === 'str_replace' || n === 'apply_patch') return 'edit'
  if (n === 'read') return 'read'
  if (n === 'bash' || n === 'shell' || n === 'bashoutput') return 'bash'
  if (n.includes('search') || n === 'grep' || n === 'glob') return 'search'
  if (n.includes('fetch') || n.includes('http') || n === 'web' || n.includes('url')) return 'fetch'
  return 'tool'
}

const glyphFor: Record<ToolKind, string> = {
  write: '✎',
  edit: '±',
  read: '◇',
  bash: '❯',
  search: '⌕',
  fetch: '↗',
  mcp: '◈',
  tool: '›',
}

// primaryFor extracts the chip's summary argument from the raw input,
// trying the field names carlos's built-in tools actually use.
function primaryFor(kind: ToolKind, rec: Record<string, unknown>): string {
  switch (kind) {
    case 'write':
    case 'edit':
    case 'read':
      return firstString(rec, ['path', 'file', 'filename'])
    case 'bash':
      return firstString(rec, ['cmd', 'command', 'script'])
    case 'search':
      return firstString(rec, ['query', 'pattern', 'regex', 'q'])
    case 'fetch':
      return firstString(rec, ['url', 'final_url'])
    default:
      // For MCP/other tools, surface the first string-valued field so the
      // chip says something useful (e.g. an id or name) instead of "{}".
      for (const k of Object.keys(rec)) {
        const s = str(rec[k])
        if (s !== '') return s
      }
      return ''
  }
}

export function toolMeta(name: string, input: unknown): ToolMeta {
  const rec = asRecord(input)
  let display = name
  let server = ''
  let kind: ToolKind
  let isMcp = false

  const sep = name.indexOf(mcpSeparator)
  if (sep > 0) {
    isMcp = true
    server = name.slice(0, sep)
    display = name.slice(sep + mcpSeparator.length)
    kind = 'mcp'
  } else {
    kind = classify(name)
  }

  return {
    kind,
    glyph: glyphFor[isMcp ? 'mcp' : kind],
    display,
    server,
    primary: primaryFor(isMcp ? 'tool' : kind, rec),
  }
}
