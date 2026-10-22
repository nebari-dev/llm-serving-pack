import { ArrowRight } from 'lucide-react'
import { Link } from 'react-router-dom'
import { StackedArea } from '@/components/gw/charts'
import { Delta } from '@/components/gw/numbers'
import { PageHeader, Section } from '@/components/gw/page'
import { toneFill } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { dataMode, type SeriesPoint, seedSummary, session, type Summary, trafficSeries } from '@/data/catalog'
import { ago, clock, int, money, unpricedNote } from '@/lib/format'
import { cn } from '@/lib/utils'
import { rangeLabel, type TimeRange, useApp, useReceipts } from '@/state/app-state'
import { useLive, useNow } from '@/state/live'

// Overview: not a tile grid. A single vertical narrative:
// status strip → traffic → two numbers.

// Two panels on one time axis (small multiples, one y-scale each). Allowed is
// ~85% of traffic, so stacking it with the rest flattens the others into
// hairlines, and they should be visible here.
// Volume is drawn quiet — "green is the absence of information" — and the
// non-allowed verdicts get their own scale.
const volumeSeries = [{ key: 'total', label: 'Requests', color: 'var(--muted-foreground-strong)' }]

// Stack order is part of the palette: adjacent pairs were checked for
// contrast in both themes.
// Throttled shares truncated's degraded hue (there are three state colors),
// lightened so the two read apart in the stack.
const verdictSeries = [
  { key: 'throttled', label: 'Throttled', color: `color-mix(in oklch, ${toneFill.degraded} 50%, var(--canvas))` },
  { key: 'truncated', label: 'Truncated', color: toneFill.degraded },
  { key: 'blocked', label: 'Blocked', color: toneFill.blocked },
]

// Against the control plane every number follows the range picker; mock mode
// has one 24-hour fixture.
function useOverviewData(range: TimeRange) {
  const series = useLive<SeriesPoint[]>(dataMode === 'api' ? `/series/traffic?range=${range}` : null, trafficSeries, 60_000).data
  const summary = useLive<Summary>(dataMode === 'api' ? `/summary?range=${range}` : null, seedSummary, 30_000)
  return { series, summary: summary.data, loaded: summary.loaded }
}

const pctChange = (now: number, before: number) => (before > 0 ? ((now - before) / before) * 100 : null)

/** "per 30 minutes", from the series' own spacing. */
function perBucket(series: SeriesPoint[]) {
  const min = series.length > 1 ? Math.round((series[1].t - series[0].t) / 60_000) : 30
  if (min % 1440 === 0) return min === 1440 ? 'per day' : `per ${min / 1440} days`
  if (min % 60 === 0) return min === 60 ? 'per hour' : `per ${min / 60} hours`
  return `per ${min} minutes`
}

export function OverviewPage() {
  const { range } = useApp()
  const { series, summary } = useOverviewData(range)
  const multiDay = series.length > 1 && series[series.length - 1].t - series[0].t > 86_400_000
  const xFormat = (t: number) => (multiDay ? new Date(t).toLocaleDateString('en-GB', { day: '2-digit', month: 'short' }) : clock(t).slice(0, 5))
  const { current, previous } = summary
  const span = rangeLabel(range).replace('last ', '')
  const since = `vs previous ${span}`
  const delta = (now: number, before: number, goodWhen: 'up' | 'down') => {
    const p = pctChange(now, before)
    return p === null ? null : <Delta pct={p} goodWhen={goodWhen} />
  }
  const none = (before: number) => (before > 0 ? since : `nothing in the previous ${span}`)

  return (
    <div>
      <PageHeader
        title="Overview"
        description={
          <>
            {session.tenant.name} / {session.environment}, {rangeLabel(range)}. Every number here drills to the receipts that compose it.
          </>
        }
      />

      {/* 1. Status strip: green is the absence of information. */}
      <StatusStrip />

      {/* 2. Traffic with verdict composition. */}
      <Section
        title="Traffic"
        description={`Requests ${perBucket(series)}, and the ones not simply allowed, on their own scale.`}
        actions={
          <Button variant="ghost" size="sm" render={<Link to="/traffic" />}>
            Open traffic <ArrowRight />
          </Button>
        }
      >
        <StackedArea
          caption={`All requests ${perBucket(series)}`}
          series={volumeSeries}
          syncId="overview-traffic"
          height={140}
          hideXAxis
          data={series.map((p) => ({ t: p.t, values: { total: p.allowed + p.truncated + p.blocked + p.throttled } }))}
          xFormat={xFormat}
        />
        <h3 className="mt-5 mb-1 text-sm font-medium">Not allowed</h3>
        <StackedArea
          caption={`Throttled, truncated and blocked requests ${perBucket(series)}`}
          series={verdictSeries}
          syncId="overview-traffic"
          height={150}
          data={series.map((p) => ({ t: p.t, values: { throttled: p.throttled, truncated: p.truncated, blocked: p.blocked } }))}
          xFormat={xFormat}
        />
      </Section>

      {/* 3. Two numbers with trend, each a link carrying the time range. */}
      <div className="grid grid-cols-1 border-b border-border sm:grid-cols-2">
        <BigNumber to="/traffic" label="Requests" value={int(current.requests)} delta={delta(current.requests, previous.requests, 'up')} note={none(previous.requests)} />
        <BigNumber
          to="/spend"
          label="Spend"
          value={money(current.spendUsd)}
          delta={delta(current.spendUsd, previous.spendUsd, 'down')}
          note={
            // Unpriced requests aren't in the number; saying so beats the driver.
            current.unpriced
              ? unpricedNote(current.unpriced)
              : summary.topTeamIncrease && previous.spendUsd > 0
                ? `${summary.topTeamIncrease.team} is driving the increase`
                : none(previous.spendUsd)
          }
        />
      </div>
    </div>
  )
}

function StatusStrip() {
  const now = useNow()
  const receipts = useReceipts()
  const last = receipts.reduce((m, r) => Math.max(m, r.ts), 0)
  const quiet = !last || now - last > 5 * 60_000
  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-border bg-header px-6 py-2.5 text-sm" aria-label="System status">
      <span className="inline-flex items-center gap-2">
        <span className={cn('size-2 rounded-full', quiet ? 'bg-v-degraded-bar' : 'bg-v-allowed-bar')} aria-hidden="true" />
        Gateway{' '}
        <span className="text-muted-foreground">
          {dataMode === 'api' ? (last ? `last receipt ${ago(Math.min(last, now), now)}` : 'no receipts yet') : 'nominal'}
        </span>
      </span>
    </div>
  )
}

function BigNumber({ to, label, value, delta, note }: { to: string; label: string; value: string; delta: React.ReactNode; note: string }) {
  return (
    <Link to={to} className="group flex flex-col gap-1 border-border px-6 py-4 outline-none hover:bg-muted/60 focus-visible:bg-muted sm:border-r sm:last:border-r-0">
      <span className="text-sm text-muted-foreground-strong group-hover:underline">{label}</span>
      <span className="num font-mono text-[28px] leading-9 font-medium">{value}</span>
      <span className="flex items-center gap-2 text-xs text-muted-foreground">
        {delta} {note}
      </span>
    </Link>
  )
}
