import { describe, expect, it } from 'vitest'
import { changeIsProviders, modelChips } from '@/pages/onboarding-live'

describe('modelChips', () => {
  it('shows up to three models, then how many more, so a backend serving hundreds stays one line', () => {
    expect(modelChips(['smollm2'])).toEqual({ shown: ['smollm2'], more: 0 })
    expect(modelChips(['a', 'b', 'c'])).toEqual({ shown: ['a', 'b', 'c'], more: 0 })
    expect(modelChips(['a', 'b', 'c', 'd', 'e'])).toEqual({ shown: ['a', 'b', 'c'], more: 2 })
  })
})

describe('changeIsProviders', () => {
  const c = (kind: string, name: string) => ({ kind, name, change: 'added' as const, diff: '' })
  it('counts the provider, its key and its Anthropic twin as this provider’s, and the routing tables', () => {
    for (const [kind, name] of [
      ['Backend', 'testing-anthropic'],
      ['Secret', 'testing-anthropic-key'],
      ['BackendSecurityPolicy', 'testing-anthropic-key'],
      ['AIServiceBackend', 'testing-anthropic-native'],
      ['BackendSecurityPolicy', 'testing-anthropic-native-key'],
      ['AIGatewayRoute', 'aigw-run'],
    ]) expect(changeIsProviders(c(kind, name), 'testing-anthropic'), `${kind}/${name}`).toBe(true)
  })
  it('leaves another provider’s changes out, even one whose name starts the same', () => {
    expect(changeIsProviders(c('Backend', 'openai'), 'testing-anthropic')).toBe(false)
    expect(changeIsProviders(c('Backend', 'testing-anthropic-2'), 'testing-anthropic')).toBe(false)
  })
})
