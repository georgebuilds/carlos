import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import BackendMark from './BackendMark.vue'

describe('BackendMark', () => {
  it('renders the carlos cap tinted by the carlos backend token', () => {
    const w = mount(BackendMark, { props: { backend: 'carlos' } })
    const svg = w.find('svg')
    expect(svg.exists()).toBe(true)
    expect(svg.attributes('aria-label')).toContain('carlos')
    expect(svg.attributes('style') || '').toContain('--backend-carlos')
  })

  it('renders the cc terminal tinted by the cc backend token', () => {
    const w = mount(BackendMark, { props: { backend: 'cc' } })
    const svg = w.find('svg')
    expect(svg.exists()).toBe(true)
    expect(svg.attributes('aria-label')).toContain('claude code')
    expect(svg.attributes('style') || '').toContain('--backend-cc')
  })

  it('falls back to a neutral dot for an unknown backend (forward-compat)', () => {
    const w = mount(BackendMark, { props: { backend: 'opencode' } })
    expect(w.find('svg').exists()).toBe(false)
    const dot = w.find('.backend-dot')
    expect(dot.exists()).toBe(true)
    expect(dot.attributes('aria-label')).toContain('opencode')
  })

  it('honors the size prop', () => {
    const w = mount(BackendMark, { props: { backend: 'carlos', size: 20 } })
    expect(w.find('svg').attributes('width')).toBe('20')
  })
})
