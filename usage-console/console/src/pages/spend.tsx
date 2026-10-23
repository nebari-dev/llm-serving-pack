import { Download } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { StackedBars } from '@/components/gw/charts'
import { Delta, Duration, Money, TokenCount } from '@/components/gw/numbers'
import { PageHeader, Section } from '@/components/gw/page'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableFooter, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import { dataMode, type SpendRow, type SpendView } from '@/data/catalog'
import { downloadText, spendCsv } from '@/lib/csv'
import { int, money, unpricedNote } from '@/lib/format'
import { cn } from '@/lib/utils'
import { rangeLabel, type TimeRange, useApp } from '@/state/app-state'
import { useLive } from '@/state/live'
import { type Dim, dims, mockSpendView } from './spend-data'

// Spend. Two modes on one screen (trend / breakdown), every cell drills
// through to the filtered traffic view, and projections always carry their
// basis.

const api = dataMode === 'api'
const DAY = 86_400_000

/** A UTC day, as the month is UTC. */
function utcDate(ms: number) {
  return new Date(ms).toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })
}

function emptyView(range: TimeRange, by: Dim): SpendView {
  return {
    range,
    by,
    from: 0,
    to: 0,
    prevFrom: 0,
    rows: [],
    trend: { bucketMs: DAY, points: [], order: [], labels: {} },
    period: { periodStart: 0, periodEnd: 0, monthToDateUsd: 0, trailingDailyUsd: 0, trailingDays: 0, remainingDays: 0, projectedUsd: 0 },
  }
}

function DimSelect({ value, onChange, label }: { value: Dim; onChange: (d: Dim) => void; label: string }) {
  return (
    <label className="flex items-center gap-2 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <Select items={dims} value={value} onValueChange={(v) => v && onChange(v as Dim)}>
        <SelectTrigger className="w-32" aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {dims.map((d) => (
            <SelectItem key={d.value} value={d.value}>
              {d.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </label>
  )
}

/** Traffic has no provider filter: receipts don't carry one. */
function trafficHref(dim: Dim, r: SpendRow) {
  return r.noDrill || dim === 'provider' ? null : `/traffic?${new URLSearchParams({ [dim]: r.id }).toString()}`
}

function pctChange(now: number, prev: number) {
  return prev > 0 ? ((now - prev) / prev) * 100 : null
}

function downloadCsv(view: SpendView) {
  const name = `spend-${view.by}-${view.range}.csv`
  downloadText(name, spendCsv(view))
  toast.add({ title: 'CSV downloaded', description: `${name}: ${view.rows.length} rows, ${new Date(view.from).toISOString()} to ${new Date(view.to).toISOString()}`, type: 'success' })
}

export function SpendPage() {
  const { range } = useApp()
  const navigate = useNavigate()
  const [mode, setMode] = useState<'trend' | 'breakdown'>('trend')
  const [dim, setDim] = useState<Dim>('team')

  const initial = useMemo(() => (api ? emptyView(range, dim) : mockSpendView(range, dim)), [range, dim])
  const { data: view, loaded } = useLive<SpendView>(api ? `/spend?range=${range}&by=${dim}` : null, initial, 60_000)
  const { period } = view
  const windowTotal = view.rows.reduce((a, r) => a + r.spendUsd, 0)
  const prevTotal = view.rows.reduce((a, r) => a + r.prevSpendUsd, 0)
  const delta = pctChange(windowTotal, prevTotal)
  const lastDay = period.periodEnd - 1

  return (
    <div className="flex flex-col">
      <PageHeader
        title="Spend"
        description={
          <>
            Attributed cost for {rangeLabel(range)}, from the 5-minute and daily aggregates of every receipt. In-flight requests are excluded until their
            usage arrives.
          </>
        }
        actions={
          <Button variant="outline" disabled={!loaded || !view.rows.length} onClick={() => downloadCsv(view)}>
            <Download /> Export CSV
          </Button>
        }
      >
        {/* Summary strip: a ledger line, not tiles. */}
        <dl className="grid grid-cols-2 divide-border border-t border-border pt-3 md:grid-cols-4 md:divide-x">
          <div className="pr-4">
            <dt className="text-xs text-muted-foreground">Spend, {rangeLabel(range)}</dt>
            <dd className="flex items-baseline gap-2">
              {loaded ? (
                <>
                  <Money value={windowTotal} className="text-xl font-semibold" />
                  {delta !== null && <Delta pct={delta} goodWhen="down" />}
                </>
              ) : (
                <Skeleton className="my-1.5 h-5 w-28" />
              )}
            </dd>
            {!!view.unpriced && <UnpricedNote n={view.unpriced} />}
          </div>
          <div className="md:px-4">
            <dt className="text-xs text-muted-foreground">Month to date</dt>
            <dd>
              {loaded ? (
                <>
                  <Money value={period.monthToDateUsd} className="text-xl font-semibold" />
                  <span className="ml-2 text-xs text-muted-foreground">since {utcDate(period.periodStart)}</span>
                </>
              ) : (
                <Skeleton className="my-1.5 h-5 w-28" />
              )}
            </dd>
            {loaded && !!period.unpriced && <UnpricedNote n={period.unpriced} />}
          </div>
          <div className="pt-3 md:px-4 md:pt-0">
            <dt className="text-xs text-muted-foreground">Projected at period end</dt>
            <dd>
              {loaded ? (
                <>
                  <Money value={period.projectedUsd} className="text-xl font-semibold" />
                  <span className="ml-2 text-xs text-muted-foreground">by {utcDate(lastDay)}</span>
                </>
              ) : (
                <Skeleton className="my-1.5 h-5 w-28" />
              )}
            </dd>
          </div>
          <div className="pt-3 md:pl-4 md:pt-0">
            <dt className="text-xs text-muted-foreground">Projection basis</dt>
            <dd className="text-xs text-muted-foreground-strong">
              {loaded ? (
                <>
                  Month to date plus the trailing {period.trailingDays < 7 ? `${period.trailingDays}-day` : '7-day'} average of{' '}
                  <Money value={period.trailingDailyUsd} className="text-foreground" />
                  /day × {period.remainingDays.toFixed(1)} days remaining in the UTC month. Assumes current prices.
                  {!!period.unpriced && ' Requests with no price add nothing to it.'}
                </>
              ) : (
                <Skeleton className="h-8 w-full" />
              )}
            </dd>
          </div>
        </dl>
      </PageHeader>

      <Section>
        <Tabs value={mode} onValueChange={(v) => setMode(v as 'trend' | 'breakdown')}>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <TabsList aria-label="Spend view">
              <TabsTab value="trend">Trend</TabsTab>
              <TabsTab value="breakdown">Breakdown</TabsTab>
              <TabsIndicator />
            </TabsList>
            <DimSelect label={mode === 'trend' ? 'Stack by' : 'Group by'} value={dim} onChange={setDim} />
          </div>

          <TabsPanel value="trend" className="flex flex-col gap-4">
            {loaded ? <TrendChart view={view} dim={dim} /> : <Skeleton shape="block" className="h-[252px]" />}
            {loaded && !!view.trend.unpriced && (
              <p className="text-xs text-muted-foreground">
                {unpricedNote(view.trend.unpriced, 'these bars')}.
              </p>
            )}
            {view.trend.bucketMs === DAY && (range === '15m' || range === '1h' || range === '6h' || range === '24h') && (
              <p className="text-xs text-muted-foreground">
                Spend is charted at daily grain, so ranges under 7 days show the last {view.trend.points.length} days for context. Totals above use{' '}
                {rangeLabel(range)}.
              </p>
            )}
          </TabsPanel>

          <TabsPanel value="breakdown">
            {!loaded ? (
              <Skeleton shape="block" className="h-[320px]" />
            ) : view.rows.length ? (
              <BreakdownTable
                rows={view.rows}
                dim={dim}
                onDrill={(r) => {
                  const href = trafficHref(dim, r)
                  if (href) navigate(href)
                }}
              />
            ) : (
              <p className="py-8 text-center text-sm text-muted-foreground">
                No spend in {rangeLabel(range)}.
              </p>
            )}
          </TabsPanel>
        </Tabs>
      </Section>
    </div>
  )
}

/** Under a total that leaves unpriced requests out: how many. */
function UnpricedNote({ n }: { n: number }) {
  return (
    <dd className="mt-0.5 text-xs text-muted-foreground">
      {unpricedNote(n)}.
    </dd>
  )
}

/** Cost per priced request; null (no price) when every served request in the row lacks one. */
function costPerRequest(r: SpendRow) {
  const priced = r.requests - (r.unpriced ?? 0)
  if (r.unpriced && priced <= 0) return null
  return priced > 0 ? r.spendUsd / priced : 0
}

const palette = ['var(--series-1)', 'var(--series-2)', 'var(--series-3)', 'var(--series-4)', 'var(--series-5)']

function TrendChart({ view, dim }: { view: SpendView; dim: Dim }) {
  const navigate = useNavigate()
  const { trend } = view
  const daily = trend.bucketMs === DAY
  const label = (t: number) =>
    daily ? new Date(t).toISOString().slice(5, 10) : new Date(t).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  const { rows, series, byLabel } = useMemo(() => {
    // Top 5 by 30-day spend keep their own series and the rest fold into
    // "Other". Color follows that ranking, never the window's, so changing
    // the range doesn't repaint a series. Largest at the base reads steadier.
    const present = trend.order.filter((k) => trend.points.some((p) => (p.values[k] ?? 0) > 0))
    const top = present.slice(0, 5)
    const rest = present.slice(5)
    const series = top.map((k, i) => ({ key: k, label: trend.labels[k] ?? k, color: palette[i] }))
    if (rest.length) series.push({ key: '__other', label: `Other (${rest.length})`, color: 'var(--series-other)' })
    const byLabel = new Map<string, number>()
    const rows = trend.points.map((p) => {
      const values: Record<string, number> = {}
      for (const k of top) values[k] = p.values[k] ?? 0
      if (rest.length) values.__other = rest.reduce((a, k) => a + (p.values[k] ?? 0), 0)
      byLabel.set(label(p.t), p.t)
      return { x: label(p.t), values }
    })
    return { rows, series, byLabel }
    // label depends only on `daily`, which follows trend.bucketMs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [trend])
  const grain = daily ? 'Daily' : trend.bucketMs >= 3_600_000 ? 'Hourly' : `${trend.bucketMs / 60_000}-minute`
  const n = trend.points.length
  return (
    <StackedBars
      data={rows}
      series={series}
      height={220}
      valueFormat={(v) => money(v)}
      caption={`${grain} spend stacked by ${dim}, last ${n} ${daily ? 'days (UTC)' : 'buckets'}`}
      xLabel={daily ? 'Day (UTC)' : 'Time'}
      onBarClick={(x) => {
        const t = byLabel.get(x)
        if (t === undefined) return
        navigate(daily ? `/traffic?day=${x}` : `/traffic?since=${t}&until=${t + trend.bucketMs}`)
      }}
    />
  )
}

type SortKey = 'label' | 'spend' | 'delta' | 'requests' | 'tokens' | 'costPerRequest' | 'p50'

function BreakdownTable({ rows, dim, onDrill }: { rows: SpendRow[]; dim: Dim; onDrill: (r: SpendRow) => void }) {
  const [sort, setSort] = useState<{ key: SortKey; dir: 'asc' | 'desc' }>({ key: 'spend', dir: 'desc' })
  const total = rows.reduce((a, r) => a + r.spendUsd, 0)
  const totalRequests = rows.reduce((a, r) => a + r.requests, 0)
  const totalUnpriced = rows.reduce((a, r) => a + (r.unpriced ?? 0), 0)
  // Latency isn't in the spend aggregates; only the mock fixtures carry it.
  const hasP50 = rows.some((r) => r.p50Ms !== undefined)
  const val = (r: SpendRow, k: SortKey): number | string => {
    switch (k) {
      case 'label':
        return r.label
      case 'spend':
        return r.spendUsd
      case 'delta':
        return pctChange(r.spendUsd, r.prevSpendUsd) ?? Infinity
      case 'costPerRequest':
        return costPerRequest(r) ?? -1
      case 'p50':
        return r.p50Ms ?? 0
      default:
        return r[k]
    }
  }
  const sorted = [...rows].sort((a, b) => {
    const x = val(a, sort.key)
    const y = val(b, sort.key)
    const c = typeof x === 'string' ? x.localeCompare(y as string) : x === y ? 0 : x < (y as number) ? -1 : 1
    return sort.dir === 'asc' ? c : -c
  })
  const head = (k: SortKey, label: string, right = true) => (
    <TableHead
      className={cn(right && 'text-right [&_button]:justify-end')}
      aria-sort={sort.key === k ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}
      onClick={() => setSort((s) => ({ key: k, dir: s.key === k && s.dir === 'desc' ? 'asc' : 'desc' }))}
    >
      {label}
      <span aria-hidden="true" className={cn('text-xs', sort.key !== k && 'invisible')}>
        {sort.dir === 'asc' ? '▲' : '▼'}
      </span>
    </TableHead>
  )
  const dimLabel = dims.find((d) => d.value === dim)?.label ?? dim

  return (
    <Table aria-label={`Spend by ${dimLabel.toLowerCase()}`}>
      <TableHeader>
        <TableRow>
          {head('label', dimLabel, false)}
          {head('spend', 'Spend')}
          <TableHead className="text-right">Share</TableHead>
          {head('delta', 'vs previous')}
          {head('requests', 'Requests')}
          {head('tokens', 'Tokens')}
          {head('costPerRequest', 'Cost / request')}
          {hasP50 && head('p50', 'p50 latency')}
        </TableRow>
      </TableHeader>
      <TableBody>
        {sorted.map((r) => {
          const drillable = !r.noDrill
          const d = pctChange(r.spendUsd, r.prevSpendUsd)
          return (
            <TableRow key={r.id} className={cn(drillable && 'cursor-pointer')} onClick={() => drillable && onDrill(r)}>
              <TableCell className="h-10 py-1.5">
                {drillable ? (
                  <button
                    type="button"
                    className="text-left hover:underline"
                    onClick={(e) => {
                      e.stopPropagation()
                      onDrill(r)
                    }}
                    aria-label={`Open receipts for ${r.label}`}
                  >
                    <span className={cn(dim !== 'team' && dim !== 'provider' && 'font-mono text-[0.8125rem]')}>{r.label}</span>
                  </button>
                ) : (
                  <span title="Traffic has no filter for this group.">{r.label}</span>
                )}
                {r.sub && <div className="text-xs text-muted-foreground">{r.sub}</div>}
              </TableCell>
              <TableCell className="h-10 py-1.5 text-right">
                {/* No priced spend but unpriced requests: no price, not $0. */}
                <Money value={r.unpriced && !r.spendUsd ? null : r.spendUsd} />
                {!!r.unpriced && !!r.spendUsd && (
                  <div className="text-xs text-muted-foreground" title={unpricedNote(r.unpriced)}>
                    + {int(r.unpriced)} no price
                  </div>
                )}
              </TableCell>
              <TableCell className="h-10 w-40 py-1.5">
                <div className="flex items-center justify-end gap-2">
                  <span className="h-1.5 w-20 rounded-full bg-muted" aria-hidden="true">
                    <span className="block h-full rounded-full bg-foreground/50" style={{ width: `${(r.spendUsd / (total || 1)) * 100}%` }} />
                  </span>
                  <span className="num w-12 text-right font-mono text-xs">{((r.spendUsd / (total || 1)) * 100).toFixed(1)}%</span>
                </div>
              </TableCell>
              <TableCell className="h-10 py-1.5 text-right">
                {d !== null ? <Delta pct={d} goodWhen="down" /> : <span className="text-xs text-muted-foreground">{r.spendUsd > 0 ? 'new' : '-'}</span>}
              </TableCell>
              <TableCell className="num h-10 py-1.5 text-right font-mono">{int(r.requests)}</TableCell>
              <TableCell className="h-10 py-1.5 text-right">
                <TokenCount value={r.tokens} />
              </TableCell>
              <TableCell className="h-10 py-1.5 text-right">
                <Money value={costPerRequest(r)} precision="micro" />
              </TableCell>
              {hasP50 && (
                <TableCell className="h-10 py-1.5 text-right">
                  <Duration ms={r.p50Ms ?? 0} />
                </TableCell>
              )}
            </TableRow>
          )
        })}
      </TableBody>
      <TableFooter>
        <TableRow>
          <TableCell className="font-medium">Total</TableCell>
          <TableCell className="text-right font-medium">
            <Money value={total} />
            {!!totalUnpriced && <div className="text-xs font-normal text-muted-foreground">{unpricedNote(totalUnpriced)}</div>}
          </TableCell>
          <TableCell colSpan={2} />
          <TableCell className="num text-right font-mono font-medium">{int(totalRequests)}</TableCell>
          <TableCell colSpan={hasP50 ? 3 : 2} className="text-xs text-muted-foreground">
            Click any row to open the receipts behind it.{!hasP50 && ' Latency isn’t in the spend aggregates; Traffic shows it per request.'}
          </TableCell>
        </TableRow>
      </TableFooter>
    </Table>
  )
}
