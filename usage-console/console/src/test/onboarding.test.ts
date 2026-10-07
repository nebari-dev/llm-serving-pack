import { describe, expect, it } from 'vitest'
import { servesShort } from '@/pages/onboarding-live'

describe('servesShort', () => {
  it('lists up to two models, then a count, so a big backend stays one line', () => {
    expect(servesShort(['smollm2'])).toBe('smollm2')
    expect(servesShort(['gpt-5-mini', 'gpt-5.5'])).toBe('gpt-5-mini, gpt-5.5')
    expect(servesShort(['a', 'b', 'c', 'd', 'e'])).toBe('a, b +3 more')
  })
})
