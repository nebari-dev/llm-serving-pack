import { ArrowRight, Download, FileText, Pencil, Trash2, TrendingUp } from 'lucide-react'
import { useEffect, useMemo, useReducer, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { StackedBars, Meter } from '@/components/gw/charts'
import { DiffView } from '@/components/gw/diff-view'
import { Delta, Duration, Money, TokenCount } from '@/components/gw/numbers'
import { PageHeader, Section } from '@/components/gw/page'
import { StateChip } from '@/components/gw/verdict'
import { Alert, AlertAction, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableFooter, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import { type Budget, budgetLabel, budgets, dataMode, syncBudgets, throttleShare, type SavingsOpportunity, type SpendRow, type SpendView, seedSavings, seedSpendSurge } from '@/data/catalog'
import { downloadText, spendCsv } from '@/lib/csv'
import { int, money, unpricedNote } from '@/lib/format'
import { cn } from '@/lib/utils'
import { rangeLabel, type TimeRange, useApp } from '@/state/app-state'
import { useLive } from '@/state/live'
import { BudgetDialog, capMoney, DeleteBudgetDialog } from './budget-dialogs'
import { type Dim, dims, mockSpendView } from './spend-data'

// §7.5.5 Spend and budgets. Two modes on one screen (trend / breakdown),
// every cell drills through to the filtered traffic view, budgets state their
// enforcement in words, and projections always carry their basis.

const api = dataMode === 'api'
const DAY = 86_400_000

/** A UTC day, as the month and budget periods are UTC. */
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

function trafficHref(dim: Dim, r: SpendRow) {
  return r.noDrill ? null : `/traffic?${new URLSearchParams({ [dim]: r.id }).toString()}`
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
  const liveBudgets = useLive<Budget[]>(api ? '/budgets' : null, budgets, 60_000)
  // Mock-mode writes edit the catalog's fixtures; this re-renders after one.
  const [, bumpBudgets] = useReducer((n: number) => n + 1, 0)
  const budgetList = api ? liveBudgets.data : budgets
  useEffect(() => {
    if (api && liveBudgets.loaded) syncBudgets(liveBudgets.data)
  }, [liveBudgets.data, liveBudgets.loaded])
  const [editing, setEditing] = useState<Budget | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Budget | null>(null)
  const budgetsChanged = () => (api ? liveBudgets.reload() : bumpBudgets())
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
          <>
            <Button variant="outline" disabled={!loaded || !view.rows.length} onClick={() => downloadCsv(view)}>
              <Download /> Export CSV
            </Button>
            {api ? (
              <Button variant="outline" disabled title="The PDF close report isn't connected yet: the control plane doesn't render reports.">
                <FileText /> Export PDF for close
              </Button>
            ) : (
              <Button
                variant="outline"
                onClick={() =>
                  toast.add({ title: 'Close report generated', description: `${utcDate(period.periodStart)} – ${utcDate(Date.now())} PDF, prices as billed per receipt`, type: 'success' })
                }
              >
                <FileText /> Export PDF for close
              </Button>
            )}
          </>
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
                  /day × {period.remainingDays.toFixed(1)} days remaining in the UTC month. Assumes current prices and no budget enforcement.
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
            {seedSpendSurge && (
              <Alert variant="warning">
                <TrendingUp />
                <AlertTitle>
                  {seedSpendSurge.team} spend is up {seedSpendSurge.ratio}× since {seedSpendSurge.since}.
                </AlertTitle>
                <AlertDescription>
                  <p>
                    Almost all of it is <span className="font-mono">{seedSpendSurge.key}</span> on <span className="font-mono">{seedSpendSurge.model}</span>.
                    The support budget crossed its cap and is throttling.
                  </p>
                </AlertDescription>
                <AlertAction>
                  <Button size="sm" variant="outline" render={<Link to={`/traffic?key=${seedSpendSurge.key}&model=${seedSpendSurge.model}`} />}>
                    Open receipts <ArrowRight />
                  </Button>
                </AlertAction>
              </Alert>
            )}
            {loaded ? <TrendChart view={view} dim={dim} surge={!!seedSpendSurge} /> : <Skeleton shape="block" className="h-[252px]" />}
            {loaded && !!view.trend.unpriced && (
              <p className="text-xs text-muted-foreground">
                {unpricedNote(view.trend.unpriced, 'these bars')}.{' '}
                <Link to="/models?tab=pricing" className="underline underline-offset-4">
                  Set prices
                </Link>
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
                No spend in {rangeLabel(range)}.{' '}
                <Link to="/onboarding" className="text-foreground underline">
                  Point an app at the gateway →
                </Link>
              </p>
            )}
          </TabsPanel>
        </Tabs>
      </Section>

      <Section
        id="budgets"
        title="Budgets"
        description={
          loaded
            ? `Monthly caps, UTC. Period ${utcDate(period.periodStart)} – ${utcDate(lastDay)}, resets in ${Math.ceil(period.remainingDays)} days.`
            : 'Monthly caps, UTC.'
        }
        actions={
          <Button variant="outline" disabled={!liveBudgets.loaded} onClick={() => setEditing('new')}>
            Add budget
          </Button>
        }
      >
        {liveBudgets.loaded ? (
          <BudgetTable budgets={budgetList} remainingDays={period.remainingDays} onEdit={setEditing} onDelete={setDeleting} />
        ) : (
          <Skeleton shape="block" className="h-[240px]" />
        )}
        {editing && <BudgetDialog budget={editing === 'new' ? null : editing} onClose={() => setEditing(null)} onSaved={budgetsChanged} />}
        {deleting && <DeleteBudgetDialog budget={deleting} onClose={() => setDeleting(null)} onDeleted={budgetsChanged} />}
      </Section>

      <Section
        id="savings"
        title="Savings opportunities"
        description="Requests where a cheaper model in the same family would plausibly have served, based on output length and task shape. Each one is a draft you review — nothing is applied automatically."
      >
        {seedSavings ? (
          <Savings opportunities={seedSavings} />
        ) : (
          <p className="rounded-md border border-dashed border-border px-4 py-3 text-sm text-muted-foreground-strong">
            Savings analysis isn't connected yet. It needs each request's output length and task shape, which the spend aggregates don't carry, and drafting
            an alias change needs alias writes.
          </p>
        )}
      </Section>
    </div>
  )
}

/** Under a total that leaves unpriced requests out: how many, and where to price them. */
function UnpricedNote({ n }: { n: number }) {
  return (
    <dd className="mt-0.5 text-xs text-muted-foreground">
      {unpricedNote(n)}.{' '}
      <Link to="/models?tab=pricing" className="underline underline-offset-4">
        Set prices
      </Link>
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

function TrendChart({ view, dim, surge }: { view: SpendView; dim: Dim; surge: boolean }) {
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
      highlightFrom={surge && (dim === 'team' || dim === 'key') ? n - 6 : undefined}
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
                {d !== null ? <Delta pct={d} goodWhen="down" /> : <span className="text-xs text-muted-foreground">{r.spendUsd > 0 ? 'new' : '—'}</span>}
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

// ---- budgets -------------------------------------------------------------

/**
 * What the budget does at its cap, in words (§7.5.5). Api mode says only what
 * the gateway does: block returns 429; throttle refuses a share of requests
 * with 429 and Retry-After (half at the cap, all at 120% of it); warn admits
 * the request and records the budget step in its receipt.
 */
function enforcement(b: Budget) {
  const pct = b.currentUsd / b.capUsd
  const cap = capMoney(b.capUsd)
  const over = b.currentUsd >= b.capUsd
  if (api) {
    if (over) {
      const what =
        b.onExceed === 'block'
          ? 'Blocking new requests.'
          : b.onExceed === 'throttle'
            ? `Throttling: ${Math.round(throttleShare(b) * 100)}% of new requests get 429 budget_throttled with Retry-After, rising to all of them at ${capMoney(Math.round(b.capUsd * 120) / 100)}.`
            : 'Warn only: requests are admitted and their receipts record the budget over cap.'
      return {
        tone: b.onExceed === 'block' ? ('blocked' as const) : ('degraded' as const),
        chip: b.onExceed === 'block' ? 'Blocking' : b.onExceed === 'throttle' ? 'Throttling' : 'Over cap',
        words: `Over cap by ${money(b.currentUsd - b.capUsd)}. ${what}`,
      }
    }
    const action =
      b.onExceed === 'block'
        ? `Blocks new requests at ${cap}`
        : b.onExceed === 'throttle'
          ? `Throttles at ${cap}: half of new requests get 429 with Retry-After, all of them at ${capMoney(Math.round(b.capUsd * 120) / 100)}`
          : `Marks requests over ${cap} in their receipts, no enforcement`
    const risk = b.projectedUsd > b.capUsd ? ' Projected to cross before period end.' : ''
    return { tone: pct >= 0.8 ? ('degraded' as const) : ('neutral' as const), chip: 'Over 80%', words: `${action}. ${money(b.capUsd - b.currentUsd)} left.${risk}` }
  }
  if (b.currentUsd > b.capUsd) {
    const since = new Date(Date.now() - 2.6 * DAY)
    const verb = b.onExceed === 'block' ? 'Blocking' : b.onExceed === 'throttle' ? 'Throttling' : 'Warning owners about'
    return {
      tone: b.onExceed === 'warn' ? ('degraded' as const) : ('blocked' as const),
      chip: b.onExceed === 'block' ? 'Blocking' : b.onExceed === 'throttle' ? 'Throttling' : 'Over cap',
      words: `Over cap by ${money(b.currentUsd - b.capUsd)}. ${verb} new requests since ${utcDate(since.getTime())}, ${since.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })}.`,
    }
  }
  const action =
    b.onExceed === 'block'
      ? `Blocks new requests at ${cap}`
      : b.onExceed === 'throttle'
        ? `Throttles to 10 requests/min at ${cap}`
        : `Warns owners at ${cap} — no enforcement`
  const risk = b.projectedUsd > b.capUsd ? ' Projected to cross before period end.' : ''
  return { tone: pct >= 0.8 ? ('degraded' as const) : ('neutral' as const), chip: 'Over 80%', words: `${action}. ${money(b.capUsd - b.currentUsd)} left.${risk}` }
}

function BudgetTable({
  budgets,
  remainingDays,
  onEdit,
  onDelete,
}: {
  budgets: Budget[]
  remainingDays: number
  onEdit: (b: Budget) => void
  onDelete: (b: Budget) => void
}) {
  if (!budgets.length) return <p className="py-6 text-center text-sm text-muted-foreground">No budgets set.</p>
  return (
    <Table aria-label="Budgets">
      <TableHeader>
        <TableRow>
          <TableHead>Scope</TableHead>
          <TableHead className="text-right">Spent</TableHead>
          <TableHead className="text-right">Cap</TableHead>
          <TableHead className="w-56">Consumption</TableHead>
          <TableHead>Enforcement</TableHead>
          <TableHead className="text-right">Projected</TableHead>
          <TableHead className="w-20">
            <span className="sr-only">Actions</span>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {budgets.map((b) => {
          const e = enforcement(b)
          const pct = (b.currentUsd / b.capUsd) * 100
          const daysToCap = b.trailingDailyUsd > 0 ? (b.capUsd - b.currentUsd) / b.trailingDailyUsd : Infinity
          const crossDay = b.currentUsd < b.capUsd && daysToCap < remainingDays ? Date.now() + daysToCap * DAY : null
          return (
            <TableRow key={b.id}>
              <TableCell className="py-2">
                <Link to={`/traffic?${b.scopeType}=${encodeURIComponent(budgetLabel(b))}`} className="font-mono text-[0.8125rem] hover:underline">
                  {budgetLabel(b)}
                </Link>
                <div className="text-xs text-muted-foreground">{b.scopeType} · monthly</div>
              </TableCell>
              <TableCell className="py-2 text-right">
                <Money value={b.currentUsd} />
                <div className="num font-mono text-xs text-muted-foreground">{pct.toFixed(0)}%</div>
                {!!b.unpricedRequests && (
                  <div className="text-xs text-muted-foreground" title={`${unpricedNote(b.unpricedRequests, 'what this budget has spent')}, so they don't count toward its cap.`}>
                    + {int(b.unpricedRequests)} no price
                  </div>
                )}
              </TableCell>
              <TableCell className="py-2 text-right">
                <Money value={b.capUsd} />
              </TableCell>
              <TableCell className="py-2">
                <Meter value={b.currentUsd} cap={b.capUsd} projected={b.projectedUsd} />
                <div className="mt-1 flex justify-between text-[11px] text-muted-foreground">
                  <span>solid: spent · dashed: projected</span>
                  <span>| cap</span>
                </div>
              </TableCell>
              <TableCell className="max-w-80 py-2 text-sm whitespace-normal">
                {e.tone === 'neutral' ? (
                  <span className="text-muted-foreground-strong">{e.words}</span>
                ) : (
                  <span className="flex flex-col items-start gap-1">
                    <StateChip tone={e.tone}>{e.chip}</StateChip>
                    <span className="text-muted-foreground-strong">{e.words}</span>
                  </span>
                )}
              </TableCell>
              <TableCell className="py-2 text-right">
                <Money value={b.projectedUsd} className={cn(b.projectedUsd > b.capUsd && 'text-v-degraded-fg')} />
                <div className="text-xs text-muted-foreground" title="Month to date plus the trailing 7-day average × days remaining">
                  {crossDay ? `crosses cap ~${utcDate(crossDay)}` : `7-day avg ${money(b.trailingDailyUsd)}/day`}
                </div>
              </TableCell>
              <TableCell className="py-2 text-right whitespace-nowrap">
                <Button variant="ghost" size="icon-sm" aria-label={`Edit budget ${budgetLabel(b)}`} title="Edit cap or action" onClick={() => onEdit(b)}>
                  <Pencil />
                </Button>
                <Button variant="ghost" size="icon-sm" aria-label={`Delete budget ${budgetLabel(b)}`} title="Delete budget" onClick={() => onDelete(b)}>
                  <Trash2 />
                </Button>
              </TableCell>
            </TableRow>
          )
        })}
      </TableBody>
    </Table>
  )
}

// ---- savings (mock mode only) --------------------------------------------

function Savings({ opportunities }: { opportunities: SavingsOpportunity[] }) {
  const [draft, setDraft] = useState<SavingsOpportunity | null>(null)
  return (
    <>
      <ul className="divide-y divide-border rounded-md border border-border bg-card">
        {opportunities.map((o) => (
          <li key={o.id} className="flex flex-wrap items-start justify-between gap-4 px-4 py-3">
            <div className="min-w-0 max-w-3xl">
              <div className="text-base">
                <Money value={o.monthly} precision="whole" className="font-semibold" />
                /mo if {o.before}
                <span className="font-mono">{o.subject}</span>
                {o.after} moved to <span className="font-mono">{o.target}</span>
              </div>
              <p className="mt-0.5 text-sm text-muted-foreground">{o.basis}</p>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <Button variant="ghost" size="sm" render={<Link to={o.href} />}>
                View {int(o.receipts)} receipts
              </Button>
              <Button variant="outline" size="sm" onClick={() => setDraft(o)}>
                Draft alias change
              </Button>
            </div>
          </li>
        ))}
      </ul>
      <Dialog open={!!draft} onOpenChange={(o) => !o && setDraft(null)}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>Draft alias change</DialogTitle>
            <DialogDescription>
              This saves a draft of <span className="font-mono">{draft?.alias}</span>. Nothing changes in the gateway until someone reviews and applies it from
              Models → Aliases.
            </DialogDescription>
          </DialogHeader>
          {draft && <DiffView diff={draft.diff} title={`ModelAlias/${draft.alias} · draft`} />}
          <p className="text-xs text-muted-foreground">
            Estimated saving <Money value={draft?.monthly ?? 0} precision="whole" className="text-foreground" />
            /mo at current prices and volume.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDraft(null)}>
              Cancel
            </Button>
            <Button
              onClick={() => {
                toast.add({ title: 'Draft saved', description: `${draft?.alias} is waiting for review. Nothing was applied.`, type: 'success' })
                setDraft(null)
              }}
            >
              Save as draft
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
