// Rail · the crew column's lifecycle: it only takes space when the active
// thread has at least one sub-agent (any ever spawned, live or finished).
// The aside always stays in the DOM and collapses via the `collapsed` class
// so the width transition can carry the appear/disappear moment.

import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import Rail from './Rail.vue'
import CrewList from './CrewList.vue'
import ApprovalCard from './ApprovalCard.vue'
import type { ChildSnapshot } from '@/api/types'
import type { PendingApproval } from '@/stores/approvals'

function child(id: string, over: Partial<ChildSnapshot> = {}): ChildSnapshot {
  return {
    id,
    state: 'done',
    title: `sub ${id}`,
    last_tool: 'Read',
    tokens: 48000,
    cost_cents: 31,
    started_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

function approval(id: string): PendingApproval {
  return { requestId: id, name: 'Bash', input: 'git push' }
}

describe('Rail · collapsed without sub-agents', () => {
  it('collapses and hides from the a11y tree when there are no children', () => {
    const w = mount(Rail, { props: { approvals: [], children: [] } })
    const aside = w.find('aside.rail')
    expect(aside.classes()).toContain('collapsed')
    expect(aside.attributes('aria-hidden')).toBe('true')
    expect(w.find('.rail-inner').exists()).toBe(false)
    expect(w.findComponent(CrewList).exists()).toBe(false)
  })

  it('stays collapsed even with pending approvals (the banner carries those)', () => {
    const w = mount(Rail, { props: { approvals: [approval('r1')], children: [] } })
    expect(w.find('aside.rail').classes()).toContain('collapsed')
    expect(w.findComponent(ApprovalCard).exists()).toBe(false)
  })
})

describe('Rail · visible with sub-agents', () => {
  it('expands and renders the crew when children exist', () => {
    const w = mount(Rail, { props: { approvals: [], children: [child('c1'), child('c2')] } })
    const aside = w.find('aside.rail')
    expect(aside.classes()).not.toContain('collapsed')
    expect(aside.attributes('aria-hidden')).toBe('false')
    expect(w.findComponent(CrewList).exists()).toBe(true)
    expect(w.findAll('.crew-item')).toHaveLength(2)
  })

  it('shows historical children of a finished thread (inspection still matters)', () => {
    const w = mount(Rail, {
      props: { approvals: [], children: [child('c1', { state: 'done' })] },
    })
    expect(w.find('aside.rail').classes()).not.toContain('collapsed')
  })

  it('renders overflow approval cards alongside the crew', () => {
    const w = mount(Rail, {
      props: { approvals: [approval('r1'), approval('r2')], children: [child('c1')] },
    })
    expect(w.findAllComponents(ApprovalCard)).toHaveLength(2)
  })

  it('collapses again when the children drain away', async () => {
    const w = mount(Rail, { props: { approvals: [], children: [child('c1')] } })
    expect(w.find('aside.rail').classes()).not.toContain('collapsed')

    await w.setProps({ children: [] })
    expect(w.find('aside.rail').classes()).toContain('collapsed')
    expect(w.find('.rail-inner').exists()).toBe(false)
  })
})
