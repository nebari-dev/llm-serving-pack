import { describe, expect, it } from 'vitest'
import { navGroups } from '@/components/shell/app-sidebar'
import { parseHiddenPages } from '@/data/catalog'
import { spendDims } from '@/pages/spend-data'

describe('hidden pages', () => {
  it('reads VITE_HIDE_PAGES as a list of paths', () => {
    expect([...parseHiddenPages(' /routing, /guardrails ,,')]).toEqual(['/routing', '/guardrails'])
    expect(parseHiddenPages(undefined).size).toBe(0)
  })

  it('leaves hidden pages out of the sidebar, and a group with none left', () => {
    const groups = navGroups(new Set(['/routing', '/models', '/guardrails', '/settings']))
    expect(groups.map((g) => g.label)).toEqual(['Monitor', 'Govern', ''])
    expect(groups[1].items.map((i) => i.to)).toEqual(['/spend', '/keys'])
    expect(groups[2].items.map((i) => i.to)).toEqual(['/onboarding'])
  })

  it('shows every page by default', () => {
    expect(navGroups(new Set()).flatMap((g) => g.items.map((i) => i.to))).toEqual(['/', '/traffic', '/activity', '/spend', '/guardrails', '/keys', '/routing', '/models', '/settings', '/onboarding'])
  })
})

describe('spend dimensions', () => {
  it('add Person against a control plane, which can group spend by who made the requests', () => {
    expect(spendDims('api').map((d) => d.value)).toContain('person')
    expect(spendDims('mock').map((d) => d.value)).not.toContain('person')
  })
})
