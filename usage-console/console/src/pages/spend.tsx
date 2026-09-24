import { ArrowRight, Download, FileText, TrendingUp } from 'lucide-react'
import { useMemo, useState } from 'react'
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
import { Table, TableBody, TableCell, TableFooter, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import { type Budget, budgets, modelById, spendSeries } from '@/data/mock'
import { int, money } from '@/lib/format'
import { cn } from '@/lib/utils'
import { rangeLabel, useApp } from '@/state/app-state'
import {
  type BreakdownRow,
  breakdown,
  type Dim,
  dims,
  fmtDate,
  monthToDate,
  periodInfo,
  trailingDailyAvg,
  trend,
  trendDays,
} from './spend-data'

// §7.5.5 Spend and budgets. Two modes on one screen (trend / breakdown),
// every cell drills through to the filtered traffic view, budgets state their
// enforcement in words, and projections always carry their basis.

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

function trafficHref(dim: Dim, id: string, extra?: Record<string, string>) {
  const p = new URLSearchParams({ [dim]: id, ...extra })
  return `/traffic?${p.toString()}`
}

export function SpendPage() {
  const { range } = useApp()
  const navigate = useNavigate()
  const [mode, setMode] = useState<'trend' | 'breakdown'>('trend')
  const [dim, setDim] = useState<Dim>('team')

  const period = periodInfo()
  const mtd = monthToDate()
  const dailyAvg = trailingDailyAvg(null)
  const projected = mtd + dailyAvg * period.remainingDays
  const days = trendDays(range)
  const surgeIndex = spendSeries.length - 6 // support surge starts 6 days ago in the demo tenant
  const surgeDay = spendSeries[surgeIndex].day
  const rows = useMemo(() => breakdown(dim, range), [dim, range])
  const windowTotal = rows.reduce((a, r) => a + r.spend, 0)
  const prevTotal = rows.reduce((a, r) => a + r.prevSpend, 0)

  return (
    <div className="flex flex-col">
      <PageHeader
        title="Spend"
        description={
          <>
            Attributed cost for {rangeLabel(range)}, from daily and hourly aggregates of every receipt. In-flight requests are excluded until their usage
            arrives.
          </>
        }
        actions={
          <>
            <Button
              variant="outline"
              onClick={() => toast.add({ title: 'CSV exported', description: `spend-${dim}-${range}.csv · export recorded in the audit log`, type: 'success' })}
            >
              <Download /> Export CSV
            </Button>
            <Button
              variant="outline"
              onClick={() =>
                toast.add({ title: 'Close report generated', description: `${fmtDate(period.start)} – ${fmtDate(new Date())} PDF, prices as billed per receipt`, type: 'success' })
              }
            >
              <FileText /> Export PDF for close
            </Button>
          </>
        }
      >
        {/* Summary strip: a ledger line, not tiles. */}
        <dl className="grid grid-cols-2 divide-border border-t border-border pt-3 md:grid-cols-4 md:divide-x">
          <div className="pr-4">
            <dt className="text-xs text-muted-foreground">Spend, {rangeLabel(range)}</dt>
            <dd className="flex items-baseline gap-2">
              <Money value={windowTotal} className="text-xl font-semibold" />
              <Delta pct={((windowTotal - prevTotal) / (prevTotal || 1)) * 100} goodWhen="down" />
            </dd>
          </div>
          <div className="md:px-4">
            <dt className="text-xs text-muted-foreground">Month to date</dt>
            <dd>
              <Money value={mtd} className="text-xl font-semibold" />
              <span className="ml-2 text-xs text-muted-foreground">since {fmtDate(period.start)}</span>
            </dd>
          </div>
          <div className="pt-3 md:px-4 md:pt-0">
            <dt className="text-xs text-muted-foreground">Projected at period end</dt>
            <dd>
              <Money value={projected} className="text-xl font-semibold" />
              <span className="ml-2 text-xs text-muted-foreground">by {fmtDate(new Date(period.end.getTime() - 1))}</span>
            </dd>
          </div>
          <div className="pt-3 md:pl-4 md:pt-0">
            <dt className="text-xs text-muted-foreground">Projection basis</dt>
            <dd className="text-xs text-muted-foreground-strong">
              Trailing 7-day average of <Money value={dailyAvg} className="text-foreground" />
              /day × {period.remainingDays.toFixed(1)} days remaining. Assumes no budget enforcement and current prices.
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
            <Alert variant="warning">
              <TrendingUp />
              <AlertTitle>Support spend is up 2.9× since {surgeDay}.</AlertTitle>
              <AlertDescription>
                Almost all of it is <span className="font-mono">support-bot</span> on <span className="font-mono">claude-sonnet-5</span>. The support budget
                crossed its cap and is throttling.
              </AlertDescription>
              <AlertAction>
                <Button size="sm" variant="outline" render={<Link to="/traffic?key=support-bot&model=claude-sonnet-5" />}>
                  Open receipts <ArrowRight />
                </Button>
              </AlertAction>
            </Alert>
            <TrendChart dim={dim} days={days} surgeFrom={days - 6} />
            {range !== '30d' && range !== '7d' && (
              <p className="text-xs text-muted-foreground">
                Spend is charted at daily grain, so ranges under 7 days show the last {days} days for context. Totals above use {rangeLabel(range)}.
              </p>
            )}
          </TabsPanel>

          <TabsPanel value="breakdown">
            <BreakdownTable rows={rows} dim={dim} onDrill={(id) => navigate(trafficHref(dim, id))} />
          </TabsPanel>
        </Tabs>
      </Section>

      <Section
        id="budgets"
        title="Budgets"
        description={`Monthly caps. Period ${fmtDate(period.start)} – ${fmtDate(new Date(period.end.getTime() - 1))}, resets in ${Math.ceil(period.remainingDays)} days.`}
        actions={
          <Button variant="outline" onClick={() => toast.add({ title: 'Budget editor is not part of this mockup', type: 'info' })}>
            Add budget
          </Button>
        }
      >
        <BudgetTable />
      </Section>

      <Section
        id="savings"
        title="Savings opportunities"
        description="Requests where a cheaper model would plausibly have served, based on output length and task shape. Each one is a draft you review — nothing is applied automatically."
      >
        <Savings />
      </Section>
    </div>
  )
}

function TrendChart({ dim, days, surgeFrom }: { dim: Dim; days: number; surgeFrom: number }) {
  const navigate = useNavigate()
  const { rows, series } = useMemo(() => trend(dim, days), [dim, days])
  return (
    <StackedBars
      data={rows}
      series={series}
      height={220}
      valueFormat={(n) => money(n)}
      caption={`Daily spend stacked by ${dim}, last ${days} days`}
      highlightFrom={dim === 'team' || dim === 'key' ? surgeFrom : undefined}
      onBarClick={(day) => navigate(`/traffic?day=${day}`)}
    />
  )
}

type SortKey = 'label' | 'spend' | 'delta' | 'requests' | 'tokens' | 'costPerRequest' | 'p50'

function BreakdownTable({ rows, dim, onDrill }: { rows: BreakdownRow[]; dim: Dim; onDrill: (id: string) => void }) {
  const [sort, setSort] = useState<{ key: SortKey; dir: 'asc' | 'desc' }>({ key: 'spend', dir: 'desc' })
  const total = rows.reduce((a, r) => a + r.spend, 0)
  const val = (r: BreakdownRow, k: SortKey) => (k === 'delta' ? (r.spend - r.prevSpend) / (r.prevSpend || 1) : k === 'label' ? r.label : r[k])
  const sorted = [...rows].sort((a, b) => {
    const x = val(a, sort.key)
    const y = val(b, sort.key)
    const c = typeof x === 'string' ? x.localeCompare(y as string) : (x as number) - (y as number)
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
          {head('p50', 'p50 latency')}
        </TableRow>
      </TableHeader>
      <TableBody>
        {sorted.map((r) => (
          <TableRow key={r.id} className="cursor-pointer" onClick={() => onDrill(r.id)}>
            <TableCell className="h-10 py-1.5">
              <button
                type="button"
                className="text-left hover:underline"
                onClick={(e) => {
                  e.stopPropagation()
                  onDrill(r.id)
                }}
                aria-label={`Open receipts for ${r.label}`}
              >
                <span className={cn(dim !== 'team' && dim !== 'provider' && 'font-mono text-[0.8125rem]')}>{r.label}</span>
              </button>
              {r.sub && <div className="text-xs text-muted-foreground">{r.sub}</div>}
            </TableCell>
            <TableCell className="h-10 py-1.5 text-right">
              <Money value={r.spend} />
            </TableCell>
            <TableCell className="h-10 w-40 py-1.5">
              <div className="flex items-center justify-end gap-2">
                <span className="h-1.5 w-20 rounded-full bg-muted" aria-hidden="true">
                  <span className="block h-full rounded-full bg-foreground/50" style={{ width: `${(r.spend / (total || 1)) * 100}%` }} />
                </span>
                <span className="num w-12 text-right font-mono text-xs">{((r.spend / (total || 1)) * 100).toFixed(1)}%</span>
              </div>
            </TableCell>
            <TableCell className="h-10 py-1.5 text-right">
              <Delta pct={((r.spend - r.prevSpend) / (r.prevSpend || 1)) * 100} goodWhen="down" />
            </TableCell>
            <TableCell className="num h-10 py-1.5 text-right font-mono">{int(r.requests)}</TableCell>
            <TableCell className="h-10 py-1.5 text-right">
              <TokenCount value={r.tokens} />
            </TableCell>
            <TableCell className="h-10 py-1.5 text-right">
              <Money value={r.costPerRequest} precision="micro" />
            </TableCell>
            <TableCell className="h-10 py-1.5 text-right">
              <Duration ms={r.p50} />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
      <TableFooter>
        <TableRow>
          <TableCell className="font-medium">Total</TableCell>
          <TableCell className="text-right font-medium">
            <Money value={total} />
          </TableCell>
          <TableCell colSpan={6} className="text-xs text-muted-foreground">
            Click any row to open the receipts behind it.
          </TableCell>
        </TableRow>
      </TableFooter>
    </Table>
  )
}

// ---- budgets -------------------------------------------------------------

function enforcement(b: Budget, projectedEnd: number) {
  const pct = b.currentUsd / b.capUsd
  const cap = money(b.capUsd, 0)
  if (b.currentUsd > b.capUsd) {
    const since = new Date(Date.now() - 2.6 * 86_400_000)
    const verb = b.onExceed === 'block' ? 'Blocking' : b.onExceed === 'throttle' ? 'Throttling' : 'Warning owners about'
    return {
      tone: b.onExceed === 'warn' ? ('degraded' as const) : ('blocked' as const),
      words: `Over cap by ${money(b.currentUsd - b.capUsd)}. ${verb} new requests since ${fmtDate(since)}, ${since.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })}.`,
    }
  }
  const action =
    b.onExceed === 'block'
      ? `Blocks new requests at ${cap}`
      : b.onExceed === 'throttle'
        ? `Throttles to 10 requests/min at ${cap}`
        : `Warns owners at ${cap} — no enforcement`
  const risk = projectedEnd > b.capUsd ? ' Projected to cross before period end.' : ''
  return { tone: pct >= 0.8 ? ('degraded' as const) : ('neutral' as const), words: `${action}. ${money(b.capUsd - b.currentUsd)} left.${risk}` }
}

function BudgetTable() {
  const period = periodInfo()
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
        </TableRow>
      </TableHeader>
      <TableBody>
        {budgets.map((b) => {
          const daily = trailingDailyAvg(b.scopeType === 'team' ? b.scope : 'batch') * (b.scopeType === 'key' ? 0.42 : 1)
          const projectedEnd = b.currentUsd + daily * period.remainingDays
          const e = enforcement(b, projectedEnd)
          const pct = (b.currentUsd / b.capUsd) * 100
          const crossDay =
            b.currentUsd < b.capUsd && projectedEnd > b.capUsd ? new Date(Date.now() + ((b.capUsd - b.currentUsd) / daily) * 86_400_000) : null
          return (
            <TableRow key={b.id}>
              <TableCell className="py-2">
                <Link
                  to={b.scopeType === 'key' ? `/traffic?key=${b.scope}` : `/traffic?team=${b.scope}`}
                  className="font-mono text-[0.8125rem] hover:underline"
                >
                  {b.scope}
                </Link>
                <div className="text-xs text-muted-foreground">{b.scopeType} · monthly</div>
              </TableCell>
              <TableCell className="py-2 text-right">
                <Money value={b.currentUsd} />
                <div className="num font-mono text-xs text-muted-foreground">{pct.toFixed(0)}%</div>
              </TableCell>
              <TableCell className="py-2 text-right">
                <Money value={b.capUsd} />
              </TableCell>
              <TableCell className="py-2">
                <Meter value={b.currentUsd} cap={b.capUsd} projected={projectedEnd} />
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
                    <StateChip tone={e.tone}>{b.currentUsd > b.capUsd ? (b.onExceed === 'block' ? 'Blocking' : b.onExceed === 'throttle' ? 'Throttling' : 'Over cap') : 'Over 80%'}</StateChip>
                    <span className="text-muted-foreground-strong">{e.words}</span>
                  </span>
                )}
              </TableCell>
              <TableCell className="py-2 text-right">
                <Money value={projectedEnd} className={cn(projectedEnd > b.capUsd && 'text-v-degraded-fg')} />
                <div className="text-xs text-muted-foreground" title="Trailing 7-day average × days remaining">
                  {crossDay ? `crosses cap ~${fmtDate(crossDay)}` : `7-day avg ${money(daily)}/day`}
                </div>
              </TableCell>
            </TableRow>
          )
        })}
      </TableBody>
    </Table>
  )
}

// ---- savings -------------------------------------------------------------

interface Opportunity {
  id: string
  headline: React.ReactNode
  monthly: number
  basis: string
  receipts: number
  href: string
  alias: string
  diff: string
}

function perRequest(model: string, inTok: number, outTok: number) {
  const m = modelById[model]
  return (inTok * m.inPerM + outTok * m.outPerM) / 1e6
}

const opportunities: Opportunity[] = (() => {
  const perDay1 = 412
  const d1 = perRequest('claude-opus-4-1', 12_000, 600) - perRequest('gpt-5-mini', 12_000, 600)
  const perDay2 = 5_900
  const d2 = perRequest('claude-sonnet-5', 1_400, 90) - perRequest('claude-haiku-4-5', 1_400, 90)
  return [
    {
      id: 'o1',
      headline: (
        <>
          <Money value={d1 * perDay1 * 30} precision="whole" className="font-semibold" />
          /mo if <span className="font-mono">summarize-*</span> moved to <span className="font-mono">gpt-5-mini</span>
        </>
      ),
      monthly: d1 * perDay1 * 30,
      basis: `${int(perDay1 * 30)} requests in 30 days from batch-summarize call claude-opus-4-1 directly with summarize-shaped prompts (~12k in, <800 out). Same prompts routed through the summarize-* alias already run on gpt-5-mini.`,
      receipts: perDay1 * 30,
      href: '/traffic?key=batch-summarize&model=claude-opus-4-1',
      alias: 'summarize-*',
      diff: `
 apiVersion: gateway.nebari.dev/v1
 kind: ModelAlias
 metadata:
   name: summarize
 spec:
   match: "summarize-*"
-  target: claude-opus-4-1
+  target: gpt-5-mini
+  conditions:
+    - field: key.name
+      op: in
+      value: [batch-summarize]
   fallback: [llama-3.3-70b]`,
    },
    {
      id: 'o2',
      headline: (
        <>
          <Money value={d2 * perDay2 * 30} precision="whole" className="font-semibold" />
          /mo if short <span className="font-mono">support-bot</span> classification calls moved to <span className="font-mono">claude-haiku-4-5</span>
        </>
      ),
      monthly: d2 * perDay2 * 30,
      basis: `${int(perDay2 * 30)} requests in 30 days on claude-sonnet-5 with under 100 output tokens and a fixed system prompt. Same model family; quality not measured — run a shadow comparison before promoting.`,
      receipts: perDay2 * 30,
      href: '/traffic?key=support-bot&model=claude-sonnet-5',
      alias: 'support-classify',
      diff: `
 apiVersion: gateway.nebari.dev/v1
 kind: ModelAlias
 metadata:
+  name: support-classify
+spec:
+  match: "support-classify"
+  target: claude-haiku-4-5
+  fallback: [claude-sonnet-5]`,
    },
  ]
})()

function Savings() {
  const [draft, setDraft] = useState<Opportunity | null>(null)
  return (
    <>
      <ul className="divide-y divide-border rounded-md border border-border bg-card">
        {opportunities.map((o) => (
          <li key={o.id} className="flex flex-wrap items-start justify-between gap-4 px-4 py-3">
            <div className="min-w-0 max-w-3xl">
              <div className="text-base">{o.headline}</div>
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
