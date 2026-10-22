import { describe, expect, it } from 'vitest'
import { failedAfterAllowed } from '@/components/gw/verdict'

describe('failedAfterAllowed', () => {
  // The verdict is the gateway's (allowed, truncated…); a provider that then
  // fails is a different thing, shown apart from both success and a block.
  it('is a request the gateway let through whose upstream failed', () => {
    expect(failedAfterAllowed({ verdict: 'allowed', status: 503 })).toBe(true)
    expect(failedAfterAllowed({ verdict: 'truncated', status: 429 })).toBe(true)
    expect(failedAfterAllowed({ verdict: 'allowed', status: 200 })).toBe(false)
    expect(failedAfterAllowed({ verdict: 'blocked', status: 403 })).toBe(false) // the gateway's refusal, red already
    expect(failedAfterAllowed({ verdict: 'throttled', status: 429 })).toBe(false)
    expect(failedAfterAllowed({ verdict: 'allowed', status: 503, inFlight: true })).toBe(false)
  })
})
