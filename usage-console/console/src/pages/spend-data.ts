// Mock-mode spend views for the demo tenant. In api mode GET /spend serves
// these from the receipts continuous aggregates (§4.6); here the seeded
// daily-by-team series is split across keys, projects and models with
// hand-tuned shares, so the breakdown and the trend reconcile to the same
// totals.

import { keys, modelById, now, seedRates, type SpendDim, type SpendView, spendSeries, teams } from '@/data/catalog'
import type { TimeRange } from '@/state/app-state'

export type Dim = SpendDim

export const dims: { value: Dim; label: string }[] = [
  { value: 'team', label: 'Team' },
  { value: 'project', label: 'Project' },
  { value: 'key', label: 'Key' },
  { value: 'model', label: 'Model' },
  { value: 'provider', label: 'Provider' },
]

/** One attribution cell: a key × model pair and its share of the team's daily spend. */
export interface Cell {
  team: string
  project: string
  projectId: string
  key: string
  keyId: string
  model: string
  provider: string
  /** fraction of the team's daily spend */
  share: number
  avgCost: number
  p50: number
}

const typicalTokens = { in: 3_200, out: 520 }

function avgCost(model: string) {
  const m = seedRates?.[model]
  return m ? (typicalTokens.in * m.inPerM + typicalTokens.out * m.outPerM) / 1e6 : 0
}

const p50ByModel: Record<string, number> = {
  'gpt-5-mini': 410,
  'gpt-5.5': 1_140,
  'claude-sonnet-5': 980,
  'claude-opus-4-1': 3_620,
  'claude-haiku-4-5': 520,
  'llama-3.3-70b': 240,
}

// Hand-tuned weights so the story is legible: support's surge is Sonnet,
// batch leans on Opus (the savings opportunity), agents are Sonnet-heavy.
const modelWeight: Record<string, Record<string, number>> = {
  support: { 'claude-sonnet-5': 0.78, 'gpt-5-mini': 0.22 },
  agents: { 'claude-sonnet-5': 0.55, 'claude-opus-4-1': 0.3, 'gpt-5.5': 0.15 },
  batch: { 'claude-opus-4-1': 0.62, 'gpt-5-mini': 0.3, 'llama-3.3-70b': 0.08 },
  web: { 'gpt-5-mini': 0.64, 'claude-haiku-4-5': 0.36 },
  research: { 'claude-opus-4-1': 0.58, 'gpt-5.5': 0.3, 'claude-sonnet-5': 0.12 },
  security: { 'llama-3.3-70b': 0.7, 'claude-haiku-4-5': 0.3 },
}

export const cells: Cell[] = (() => {
  const out: Cell[] = []
  for (const t of teams) {
    const teamKeys = keys.filter((k) => k.team === t.id && k.status !== 'revoked')
    const totalReq = teamKeys.reduce((a, k) => a + k.requests24h, 0) || 1
    for (const k of teamKeys) {
      const kShare = k.requests24h / totalReq
      const weights = modelWeight[t.id] ?? {}
      const allowed = k.allowedModels.filter((m) => weights[m])
      const wSum = allowed.reduce((a, m) => a + weights[m], 0) || 1
      for (const m of allowed) {
        out.push({
          team: t.id,
          project: k.project,
          projectId: k.projectId,
          key: k.name,
          keyId: k.id,
          model: m,
          provider: modelById[m].provider,
          share: kShare * (weights[m] / wSum),
          avgCost: avgCost(m),
          p50: p50ByModel[m],
        })
      }
    }
  }
  return out
})()

export const windowDays: Record<TimeRange, number> = {
  '15m': 1 / 96,
  '1h': 1 / 24,
  '6h': 0.25,
  '24h': 1,
  '7d': 7,
  '30d': 30,
}

/** Days shown in the trend chart. Spend is aggregated daily, so short ranges show a week of context. */
export function trendDays(range: TimeRange) {
  return range === '30d' ? 30 : range === '7d' ? 7 : 14
}

function teamSpendOver(days: number, offsetDays = 0) {
  // Sum of the trailing `days` (fractional allowed) of the daily series, ending `offsetDays` ago.
  const series = spendSeries.slice(0, spendSeries.length - offsetDays)
  const out: Record<string, number> = {}
  let remaining = days
  for (let i = series.length - 1; i >= 0 && remaining > 0; i--) {
    const f = Math.min(1, remaining)
    for (const [team, v] of Object.entries(series[i].byTeam)) out[team] = (out[team] ?? 0) + v * f
    remaining -= f
  }
  return out
}

export interface BreakdownRow {
  id: string
  label: string
  dim: Dim
  spend: number
  prevSpend: number
  requests: number
  tokens: number
  costPerRequest: number
  p50: number
  sub?: string
}

export function breakdown(dim: Dim, range: TimeRange): BreakdownRow[] {
  const days = windowDays[range]
  const now = teamSpendOver(days)
  // previous window of equal length; for short ranges compare with the same window yesterday
  const prev = teamSpendOver(days, Math.max(1, Math.ceil(days)))
  const groups = new Map<string, BreakdownRow & { p50w: number }>()
  for (const c of cells) {
    // Projects group by id, as the control plane does: names repeat across teams.
    const id = dim === 'project' ? c.projectId : c[dim]
    const spend = (now[c.team] ?? 0) * c.share
    const prevSpend = (prev[c.team] ?? 0) * c.share
    const requests = spend / c.avgCost
    const g =
      groups.get(id) ??
      ({
        id,
        label: dim === 'team' ? (teams.find((t) => t.id === id)?.name ?? id) : dim === 'project' ? c.project : id,
        dim,
        spend: 0,
        prevSpend: 0,
        requests: 0,
        tokens: 0,
        costPerRequest: 0,
        p50: 0,
        p50w: 0,
        sub:
          dim === 'team'
            ? teams.find((t) => t.id === id)?.costCenter
            : dim === 'key'
              ? `${c.team} / ${c.project}`
              : dim === 'project'
                ? c.team
                : dim === 'model'
                  ? c.provider
                  : undefined,
      } as BreakdownRow & { p50w: number })
    g.spend += spend
    g.prevSpend += prevSpend
    g.requests += requests
    g.tokens += requests * (typicalTokens.in + typicalTokens.out)
    g.p50w += c.p50 * requests
    groups.set(id, g)
  }
  return [...groups.values()].map(({ p50w, ...g }) => ({
    ...g,
    requests: Math.round(g.requests),
    tokens: Math.round(g.tokens),
    costPerRequest: g.requests ? g.spend / g.requests : 0,
    p50: g.requests ? p50w / g.requests : 0,
  }))
}

/** Daily spend per group over the last `days` days, and every group ranked by its 30-day total. */
function trend(dim: Dim, days: number): SpendView['trend'] {
  const points = spendSeries.slice(-days).map((d) => {
    const values: Record<string, number> = {}
    for (const c of cells) values[c[dim]] = (values[c[dim]] ?? 0) + (d.byTeam[c.team] ?? 0) * c.share
    return { t: dayStart(d.day), values }
  })
  const totals = new Map<string, number>()
  for (const d of spendSeries) {
    for (const c of cells) totals.set(c[dim], (totals.get(c[dim]) ?? 0) + (d.byTeam[c.team] ?? 0) * c.share)
  }
  const order = [...totals.entries()].sort((a, b) => b[1] - a[1]).map(([k]) => k)
  const labels = Object.fromEntries(order.map((k) => [k, dim === 'team' ? (teams.find((t) => t.id === k)?.name ?? k) : k]))
  return { bucketMs: 86_400_000, points, order, labels }
}

/** A series day (MM-DD, UTC) as its start in ms, in the year that puts it in the past. */
function dayStart(day: string) {
  const [m, d] = day.split('-').map(Number)
  const y = new Date(now()).getUTCFullYear()
  const t = Date.UTC(y, m - 1, d)
  return t > now() ? Date.UTC(y - 1, m - 1, d) : t
}

/**
 * GET /spend in mock mode, from the seeded daily series. The trend stays at
 * daily grain, since the fixtures have no finer one: short ranges show the
 * last 14 days.
 */
export function mockSpendView(range: TimeRange, dim: Dim): SpendView {
  const t = now()
  const span = windowDays[range] * 86_400_000
  const at = new Date(t)
  const periodStart = Date.UTC(at.getUTCFullYear(), at.getUTCMonth(), 1)
  const periodEnd = Date.UTC(at.getUTCFullYear(), at.getUTCMonth() + 1, 1)
  const monthToDateUsd = Object.values(teamSpendOver((t - periodStart) / 86_400_000)).reduce((a, b) => a + b, 0)
  const trailingDailyUsd = spendSeries.slice(-7).reduce((a, d) => a + Object.values(d.byTeam).reduce((x, y) => x + y, 0), 0) / 7
  const remainingDays = (periodEnd - t) / 86_400_000
  return {
    range,
    by: dim,
    from: t - span,
    to: t,
    prevFrom: t - 2 * span,
    rows: breakdown(dim, range).map((r) => ({
      id: r.id,
      label: r.label,
      sub: r.sub,
      spendUsd: r.spend,
      prevSpendUsd: r.prevSpend,
      requests: r.requests,
      tokens: r.tokens,
      p50Ms: r.p50,
    })),
    trend: trend(dim, trendDays(range)),
    period: { periodStart, periodEnd, monthToDateUsd, trailingDailyUsd, trailingDays: 7, remainingDays, projectedUsd: monthToDateUsd + trailingDailyUsd * remainingDays },
  }
}

/** 24h spend for a key, from the same attribution cells as the breakdown. */
export function keySpend24h(keyId: string) {
  const day = spendSeries[spendSeries.length - 1].byTeam
  return cells.filter((c) => c.keyId === keyId).reduce((a, c) => a + (day[c.team] ?? 0) * c.share, 0)
}

export function fmtDate(d: Date) {
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}
