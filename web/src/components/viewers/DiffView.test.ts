import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DiffView from './DiffView.vue'

describe('DiffView', () => {
  it('renders add, del, and ctx rows with line numbers', () => {
    const w = mount(DiffView, { props: { oldText: 'a\nb\nc', newText: 'a\nB\nc' } })
    expect(w.findAll('.op-add').length).toBeGreaterThan(0)
    expect(w.findAll('.op-del').length).toBeGreaterThan(0)
    expect(w.findAll('.op-ctx').length).toBeGreaterThan(0)
    // the unchanged 'a' keeps both line numbers
    expect(w.find('.op-ctx .diff-old').text()).toBe('1')
  })

  it('renders a gap marker for a long unchanged run', () => {
    const big = Array.from({ length: 40 }, (_, i) => `line${i}`).join('\n')
    const w = mount(DiffView, { props: { oldText: big, newText: 'HEAD\n' + big } })
    const gap = w.find('.diff-gap')
    expect(gap.exists()).toBe(true)
    expect(gap.text()).toContain('unchanged')
  })

  it('renders a new file as all additions', () => {
    const w = mount(DiffView, { props: { oldText: '', newText: 'one\ntwo' } })
    expect(w.findAll('.op-add')).toHaveLength(2)
    expect(w.findAll('.op-del')).toHaveLength(0)
  })
})
