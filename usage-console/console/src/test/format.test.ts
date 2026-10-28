import { describe, expect, it } from 'vitest'
import { money } from '@/lib/format'

describe('money', () => {
  // Spend under a cent is real spend: "$0.00" would say nothing was spent.
  it('shows amounts under a cent to four places, and cents otherwise', () => {
    expect(money(0.0041)).toBe('$0.0041')
    expect(money(0.00004)).toBe('<$0.0001')
    expect(money(0)).toBe('$0.00')
    expect(money(1234.5)).toBe('$1,234.50')
    expect(money(12, 0)).toBe('$12')
  })
})
