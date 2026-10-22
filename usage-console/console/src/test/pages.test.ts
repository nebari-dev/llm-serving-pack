import { describe, expect, it } from 'vitest'
import { spendDims } from '@/pages/spend-data'

describe('spend dimensions', () => {
  it('add Person against a control plane, which can group spend by who made the requests', () => {
    expect(spendDims('api').map((d) => d.value)).toContain('person')
    expect(spendDims('mock').map((d) => d.value)).not.toContain('person')
  })
})
