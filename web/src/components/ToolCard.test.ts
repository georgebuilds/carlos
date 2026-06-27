import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ToolCard from './ToolCard.vue'

function mountCard(props: Record<string, unknown>) {
  return mount(ToolCard, {
    props: {
      name: 'bash',
      input: '',
      inputRaw: {},
      output: '',
      isError: false,
      truncated: false,
      ...props,
    },
  })
}

describe('ToolCard', () => {
  it('renders a condensed chip with name and primary arg, collapsed by default', () => {
    const w = mountCard({ name: 'bash', inputRaw: { cmd: 'ls -la' }, output: 'a\nb' })
    expect(w.find('.tc-name').text()).toBe('bash')
    expect(w.find('.tc-primary').text()).toBe('ls -la')
    // Body hidden until expanded.
    expect(w.find('.tc-body').exists()).toBe(false)
    expect(w.find('.tc-head').attributes('aria-expanded')).toBe('false')
  })

  it('expands on header click and shows the command + output for bash', async () => {
    const w = mountCard({ name: 'bash', inputRaw: { cmd: 'echo hi' }, output: 'hi' })
    await w.find('.tc-head').trigger('click')
    expect(w.find('.tc-body').exists()).toBe(true)
    expect(w.find('.tc-cmd').text()).toContain('echo hi')
    expect(w.find('.tc-well').text()).toContain('hi')
  })

  it('shows a diff and a +/- stat for an edit', async () => {
    const w = mountCard({
      name: 'edit',
      inputRaw: { path: 'a.ts', search: 'old', replace: 'new' },
      output: 'edited a.ts',
    })
    // Stat visible while collapsed.
    expect(w.find('.tc-stat').text()).toContain('+1')
    expect(w.find('.tc-stat').text()).toContain('-1')
    await w.find('.tc-head').trigger('click')
    // DiffView rendered with both sides.
    expect(w.find('.diff').exists()).toBe(true)
    expect(w.text()).toContain('old')
    expect(w.text()).toContain('new')
    expect(w.text()).toContain('a.ts')
  })

  it('treats a write as a new-file diff (all additions)', async () => {
    const w = mountCard({
      name: 'write',
      inputRaw: { path: 'n.ts', content: 'l1\nl2' },
      output: 'wrote 6 bytes',
    })
    expect(w.find('.tc-stat').text()).toContain('+2')
    await w.find('.tc-head').trigger('click')
    expect(w.find('.diff').exists()).toBe(true)
  })

  it('flags errors with the bad status dot', () => {
    const w = mountCard({ name: 'domain-list', isError: true, output: '401' })
    expect(w.find('.tc-dot.bad').exists()).toBe(true)
  })

  it('shows the MCP server prefix chip', () => {
    const w = mountCard({ name: 'home-tools__list', inputRaw: {}, output: 'ok' })
    expect(w.find('.tc-server').text()).toBe('home-tools')
    expect(w.find('.tc-name').text()).toBe('list')
  })

  it('shows a "no output" body for a read with empty output', async () => {
    const w = mountCard({ name: 'read', inputRaw: { path: 'empty.txt' }, output: '' })
    await w.find('.tc-head').trigger('click')
    expect(w.find('.tc-body').exists()).toBe(true)
    expect(w.find('.tc-empty').text()).toContain('no output')
  })

  it('shows the truncation note when truncated', async () => {
    const w = mountCard({ name: 'read', inputRaw: { path: 'big' }, output: '...', truncated: true })
    await w.find('.tc-head').trigger('click')
    expect(w.find('.tc-trunc').exists()).toBe(true)
  })
})
