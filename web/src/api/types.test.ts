import { describe, it, expect } from 'vitest'
import { isChildrenData } from './types'

describe('isChildrenData', () => {
  it('accepts a well-formed children payload', () => {
    expect(isChildrenData({ children: [] })).toBe(true)
    expect(isChildrenData({ children: [{ id: 'a' }] })).toBe(true)
  })

  it('rejects payloads without a children array', () => {
    expect(isChildrenData({})).toBe(false)
    expect(isChildrenData({ children: 'nope' })).toBe(false)
    expect(isChildrenData({ children: null })).toBe(false)
  })

  it('rejects non-object input', () => {
    expect(isChildrenData(null)).toBe(false)
    expect(isChildrenData(undefined)).toBe(false)
    expect(isChildrenData(42)).toBe(false)
    expect(isChildrenData('children')).toBe(false)
  })
})
