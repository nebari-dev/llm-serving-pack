import { describe, expect, it } from 'vitest'
import { priceChangesCsv, toCsv } from '@/lib/csv'

describe('toCsv', () => {
  it('quotes commas, quotes and newlines, and defuses spreadsheet formulas', () => {
    expect(toCsv([['a,b', 'say "hi"', 'x\ny', '=HYPERLINK("x")', 1.5, null]])).toBe('"a,b","say ""hi""","x\ny","\'=HYPERLINK(""x"")",1.5,\n')
  })
})

describe('priceChangesCsv', () => {
  it('writes one row per rate change, with no price as empty', () => {
    const csv = priceChangesCsv([
      { model: 'gpt-4o-mini', backend: 'openrouter', field: 'input', from: null, to: 0.15, source: 'litellm', effective: '2026-10-05', effectiveAt: 1, scheduled: false },
      { model: 'gpt-5-mini', backend: 'openai-prod', field: 'output', from: 2, to: 2.5, source: 'manual', effective: '2026-11-01', effectiveAt: 2, scheduled: true },
    ])
    expect(csv.split('\n')).toEqual([
      'model,backend,rate,from_usd_per_1m,to_usd_per_1m,source,effective,scheduled',
      'gpt-4o-mini,openrouter,input,,0.15,litellm,2026-10-05,false',
      'gpt-5-mini,openai-prod,output,2,2.5,manual,2026-11-01,true',
      '',
    ])
  })
})
