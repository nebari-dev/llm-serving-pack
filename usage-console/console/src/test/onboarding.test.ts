import { describe, expect, it } from 'vitest'
import { modelChips } from '@/pages/onboarding-live'

describe('modelChips', () => {
  it('shows up to three models, then how many more, so a backend serving hundreds stays one line', () => {
    expect(modelChips(['smollm2'])).toEqual({ shown: ['smollm2'], more: 0 })
    expect(modelChips(['a', 'b', 'c'])).toEqual({ shown: ['a', 'b', 'c'], more: 0 })
    expect(modelChips(['a', 'b', 'c', 'd', 'e'])).toEqual({ shown: ['a', 'b', 'c'], more: 2 })
  })
})
