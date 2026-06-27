import { describe, it, expect } from 'vitest'
import { toolMeta } from './toolMeta'

describe('toolMeta', () => {
  it('classifies write/edit/read with the path as primary', () => {
    expect(toolMeta('write', { path: 'a.ts', content: 'x' })).toMatchObject({
      kind: 'write',
      primary: 'a.ts',
    })
    expect(toolMeta('edit', { path: 'b.ts', search: 's', replace: 'r' })).toMatchObject({
      kind: 'edit',
      primary: 'b.ts',
    })
    expect(toolMeta('read', { path: 'c.ts' })).toMatchObject({ kind: 'read', primary: 'c.ts' })
  })

  it('classifies bash with the command as primary', () => {
    expect(toolMeta('bash', { cmd: 'ls -la' })).toMatchObject({ kind: 'bash', primary: 'ls -la' })
  })

  it('classifies search and fetch', () => {
    expect(toolMeta('notes_search', { query: 'foo' })).toMatchObject({
      kind: 'search',
      primary: 'foo',
    })
    expect(toolMeta('grep', { pattern: 'TODO' })).toMatchObject({ kind: 'search', primary: 'TODO' })
    expect(toolMeta('http_request', { url: 'https://x' })).toMatchObject({
      kind: 'fetch',
      primary: 'https://x',
    })
  })

  it('splits the MCP server prefix and surfaces a useful primary', () => {
    const m = toolMeta('digitalocean-mcp-local__domain-list', { name: 'example.com' })
    expect(m.kind).toBe('mcp')
    expect(m.server).toBe('digitalocean-mcp-local')
    expect(m.display).toBe('domain-list')
    expect(m.primary).toBe('example.com')
  })

  it('falls back to a generic glyph and empty primary for unknown tools', () => {
    const m = toolMeta('mystery', {})
    expect(m.kind).toBe('tool')
    expect(m.glyph).toBeTruthy()
    expect(m.primary).toBe('')
  })

  it('tolerates non-object input', () => {
    expect(() => toolMeta('bash', 'raw string')).not.toThrow()
    expect(toolMeta('bash', null).primary).toBe('')
  })
})
