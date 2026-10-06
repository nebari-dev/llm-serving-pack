import { describe, expect, it } from 'vitest'
import { priceChangesCsv, spendCsv, toCsv } from '@/lib/csv'

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

describe('spendCsv', () => {
  it('writes each row’s unpriced requests, and no price rather than $0 when none of its spend is priced', () => {
    const view = {
      range: '24h', by: 'model' as const, from: 0, to: 1, prevFrom: -1,
      rows: [
        { id: 'gpt-5-mini', label: 'gpt-5-mini', sub: 'OpenAI', spendUsd: 1.5, prevSpendUsd: 1, requests: 10, tokens: 900, unpriced: 0 },
        { id: 'llama-3.3-70b', label: 'llama-3.3-70b', spendUsd: 0, prevSpendUsd: 0, requests: 4, tokens: 300, unpriced: 4 },
        { id: 'mix', label: 'mix', spendUsd: 0.25, prevSpendUsd: 0, requests: 3, tokens: 30, unpriced: 1 },
        { id: 'blocked', label: 'blocked', spendUsd: 0, prevSpendUsd: 0, requests: 2, tokens: 0 },
      ],
      trend: { bucketMs: 1, points: [], order: [], labels: {} },
      period: { periodStart: 0, periodEnd: 1, monthToDateUsd: 0, trailingDailyUsd: 0, trailingDays: 0, remainingDays: 0, projectedUsd: 0 },
    }
    expect(spendCsv(view).split('\n')).toEqual([
      'model,detail,spend_usd,previous_spend_usd,requests,unpriced_requests,tokens',
      'gpt-5-mini,OpenAI,1.50,1.00,10,0,900',
      'llama-3.3-70b,,no price,0.00,4,4,300',
      'mix,,0.25,0.00,3,1,30',
      'blocked,,0.00,0.00,2,0,0',
      '',
    ])
  })
})
