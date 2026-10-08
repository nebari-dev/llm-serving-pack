import { describe, expect, it } from 'vitest'
import { failedAfterAllowed } from '@/components/gw/verdict'

describe('failedAfterAllowed', () => {
  // The verdict is the guardrails' (allowed, redacted…); a provider that then
  // fails is a different thing, shown apart from both success and a block.
  it('is a request the guardrails let through whose upstream failed', () => {
    expect(failedAfterAllowed({ verdict: 'allowed', status: 503 })).toBe(true)
    expect(failedAfterAllowed({ verdict: 'redacted', status: 429 })).toBe(true)
    expect(failedAfterAllowed({ verdict: 'allowed', status: 200 })).toBe(false)
    expect(failedAfterAllowed({ verdict: 'blocked', status: 403 })).toBe(false) // a guardrail's refusal, red already
    expect(failedAfterAllowed({ verdict: 'throttled', status: 429 })).toBe(false)
    expect(failedAfterAllowed({ verdict: 'allowed', status: 503, inFlight: true })).toBe(false)
  })
})
