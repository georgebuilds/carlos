// ThinkingIndicator · the dots row that paints in the assistant reply
// position while carlos works. Covers the render shape (three dots, quiet
// label, no timer at first), the 3s elapsed threshold with fake timers, the
// per-second tick, and interval cleanup on unmount.
//
// Reduced motion is a CSS-only freeze: the global prefers-reduced-motion
// rule in app.css collapses the wave animation to a single instant run.
// jsdom does not run CSS animations, so there is nothing for a JS test to
// observe; the behavior is pinned by the stylesheet, not by script.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ThinkingIndicator from './ThinkingIndicator.vue'

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('ThinkingIndicator · render shape', () => {
  it('paints three dots, the carlos who-row, and the quiet label', () => {
    const w = mount(ThinkingIndicator)
    expect(w.findAll('.dot')).toHaveLength(3)
    expect(w.find('.who').text()).toBe('carlos')
    expect(w.find('.label').text()).toBe('thinking')
  })

  it('announces itself as a status for assistive tech', () => {
    const w = mount(ThinkingIndicator)
    const root = w.find('.thinking')
    expect(root.attributes('role')).toBe('status')
    expect(root.attributes('aria-label')).toBe('carlos is thinking')
    // the dots are decoration; only the label/timer text should be read.
    expect(w.find('.dots').attributes('aria-hidden')).toBe('true')
  })
})

describe('ThinkingIndicator · elapsed threshold', () => {
  it('hides the timer under 3 seconds (quick replies stay quiet)', async () => {
    const w = mount(ThinkingIndicator)
    expect(w.find('.t-elapsed').exists()).toBe(false)
    await vi.advanceTimersByTimeAsync(2000)
    expect(w.find('.t-elapsed').exists()).toBe(false)
  })

  it('shows the timer once the wait crosses 3 seconds', async () => {
    const w = mount(ThinkingIndicator)
    await vi.advanceTimersByTimeAsync(3000)
    expect(w.find('.t-elapsed').text()).toBe('3s')
  })

  it('ticks once per second after the threshold', async () => {
    const w = mount(ThinkingIndicator)
    await vi.advanceTimersByTimeAsync(3000)
    expect(w.find('.t-elapsed').text()).toBe('3s')
    await vi.advanceTimersByTimeAsync(1000)
    expect(w.find('.t-elapsed').text()).toBe('4s')
    await vi.advanceTimersByTimeAsync(6000)
    expect(w.find('.t-elapsed').text()).toBe('10s')
  })
})

describe('ThinkingIndicator · cleanup', () => {
  it('clears its interval on unmount (no orphan tick keeps firing)', async () => {
    const w = mount(ThinkingIndicator)
    await vi.advanceTimersByTimeAsync(1000)
    w.unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})
