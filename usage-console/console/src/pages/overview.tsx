import { ArrowRight, GitBranch, MonitorCog } from 'lucide-react'
import { Link, useNavigate } from 'react-router-dom'
import { StackedArea } from '@/components/gw/charts'
import { Delta, Money } from '@/components/gw/numbers'
import { PageHeader, Section } from '@/components/gw/page'
import { StateChip, toneFill, toneText } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { type ActivityView, backends, budgets, type Change, type ChangeImpact, changes, dataMode, rules, type SeriesPoint, seedChangeImpacts, seedSummary, session, type Summary, trafficSeries } from '@/data/catalog'
import { age, ago, clock, int, money } from '@/lib/format'
import { cn } from '@/lib/utils'
import { rangeLabel, type TimeRange, useApp, useReceipts } from '@/state/app-state'
import { useDegradations } from '@/state/degradations'
import { useLive, useNow } from '@/state/live'

// §7.5.2 Overview — not a tile grid. A single vertical narrative:
// status strip → traffic → three numbers → what changed → attention list.

// Two panels on one time axis (small multiples, one y-scale each). Allowed is
// ~85% of traffic, so stacking it with the rest flattens blocked and redacted
// into hairlines; the spec wants those visible at this altitude (§7.5.2).
// Volume is drawn quiet — "green is the absence of information" — and the
// non-allowed verdicts get their own scale.
const volumeSeries = [{ key: 'total', label: 'Requests', color: 'var(--muted-foreground-strong)' }]

// Stack order is part of the palette: validate_palette.js checks adjacent
// pairs, and yellow↔red fails as neighbors. This order passes in both themes.
const verdictSeries = [
  { key: 'redacted', label: 'Redacted', color: toneFill.redacted },
  { key: 'truncated', label: 'Truncated', color: toneFill.degraded },
  { key: 'rerouted', label: 'Rerouted', color: toneFill.rerouted },
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
  const navigate = useNavigate()
  const { series, summary } = useOverviewData(range)
  // Api mode's effects are the ones Activity computes from the aggregates,
  // not the audit row's stored text; a change older than its week has none.
  const activity = useLive<ActivityView | null>(dataMode === 'api' ? '/activity?range=7d' : null, null, 60_000).data
  const effectOf = (c: Change): Pick<Change, 'effect' | 'effectTone'> => {
    if (dataMode !== 'api') return c
    const a = activity?.changes.find((x) => x.id === c.id)
    return { effect: a?.effect, effectTone: a?.effectTone }
  }
  const multiDay = series.length > 1 && series[series.length - 1].t - series[0].t > 86_400_000
  const xFormat = (t: number) => (multiDay ? new Date(t).toLocaleDateString('en-GB', { day: '2-digit', month: 'short' }) : clock(t).slice(0, 5))
  // Changes inside the charted window; the latest one is labeled.
  const windowStart = series[0]?.t ?? 0
  const inWindow = changes.filter((c) => c.ts >= windowStart)
  const annotations = inWindow.map((c, i) => ({ t: c.ts, label: i === 0 ? c.target : '' }))
  const { current, previous } = summary
  const span = rangeLabel(range).replace('last ', '')
  const since = `vs previous ${span}`
  const hotRule = rules
    .filter((r) => r.baseline7d > 0 && r.fired24h >= 10 && r.fired24h >= 2 * r.baseline7d)
    .sort((a, b) => b.fired24h / b.baseline7d - a.fired24h / a.baseline7d)[0]
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

      {/* 1. Status strip — green is the absence of information. */}
      <StatusStrip />

      {/* 2. Traffic with verdict composition. */}
      <Section
        title="Traffic"
        description={`Requests ${perBucket(series)}, and the ones Warden did not simply allow, on their own scale.`}
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
          data={series.map((p) => ({ t: p.t, values: { total: p.allowed + p.redacted + p.rerouted + p.truncated + p.blocked } }))}
          xFormat={xFormat}
          annotations={annotations}
        />
        <h3 className="mt-5 mb-1 text-sm font-medium">Not allowed</h3>
        <StackedArea
          caption={`Redacted, truncated, rerouted, and blocked requests ${perBucket(series)}`}
          series={verdictSeries}
          syncId="overview-traffic"
          height={150}
          data={series.map((p) => ({ t: p.t, values: { redacted: p.redacted, truncated: p.truncated, rerouted: p.rerouted, blocked: p.blocked } }))}
          xFormat={xFormat}
          annotations={annotations.map((a) => ({ ...a, label: '' }))}
        />
      </Section>

      {/* 3. Three numbers with trend, each a link carrying the time range. */}
      <div className="grid grid-cols-1 border-b border-border sm:grid-cols-3">
        <BigNumber to="/traffic" label="Requests" value={int(current.requests)} delta={delta(current.requests, previous.requests, 'up')} note={none(previous.requests)} />
        <BigNumber
          to="/spend"
          label="Spend"
          value={money(current.spendUsd)}
          delta={delta(current.spendUsd, previous.spendUsd, 'down')}
          note={summary.topTeamIncrease && previous.spendUsd > 0 ? `${summary.topTeamIncrease.team} is driving the increase` : none(previous.spendUsd)}
        />
        <BigNumber
          to="/traffic?verdict=blocked&verdict=redacted"
          label="Blocked + redacted"
          value={int(current.blocked + current.redacted)}
          delta={delta(current.blocked + current.redacted, previous.blocked + previous.redacted, 'down')}
          note={
            hotRule && range === '24h'
              ? `${hotRule.name} fired ${Math.round(hotRule.fired24h / hotRule.baseline7d)}× its baseline`
              : none(previous.blocked + previous.redacted)
          }
        />
      </div>

      {/* 4. What changed — config changes joined to their traffic effect. */}
      <Section
        title="What changed"
        description="Config changes and what they did to traffic."
        actions={
          <Button variant="ghost" size="sm" render={<Link to="/activity" />}>
            Full activity <ArrowRight />
          </Button>
        }
      >
        {changes[0] ? <FeaturedChange change={changes[0]} /> : <p className="text-sm text-muted-foreground">No config changes yet.</p>}
        <ol className="mt-4 divide-y divide-border border-y border-border">
          {changes.slice(1, 5).map((row) => ({ ...row, ...effectOf(row) })).map((c) => (
            <li key={c.id} className="grid grid-cols-[5rem_1fr_auto] items-baseline gap-4 py-2.5 text-sm">
              <span className="num font-mono text-xs text-muted-foreground">{clock(c.ts).slice(0, 5)}</span>
              <div className="min-w-0">
                <span className="inline-flex items-center gap-1.5">
                  {c.source === 'git' ? <GitBranch className="size-3.5 text-muted-foreground" aria-label="from Git" /> : <MonitorCog className="size-3.5 text-muted-foreground" aria-label="from console" />}
                  <span className="font-medium">{c.action}</span>
                  <span className="font-mono text-[0.8125rem]">{c.target}</span>
                </span>
                <span className="text-muted-foreground"> · {c.actor}</span>
                {c.effect && (
                  <div
                    className={cn(
                      'mt-0.5 text-[0.8125rem]',
                      c.effectTone === 'good' ? toneText.allowed : c.effectTone === 'bad' ? toneText.blocked : 'text-muted-foreground-strong',
                    )}
                  >
                    {c.effect}
                  </div>
                )}
              </div>
              <Link to={`/traffic?since=${Math.round(c.ts)}`} className="text-xs whitespace-nowrap text-muted-foreground underline-offset-4 hover:text-foreground hover:underline">
                Receipts after →
              </Link>
            </li>
          ))}
        </ol>
      </Section>

      {/* 5. Attention list — each row has one action. */}
      <Section title="Needs attention" description="Budgets over 80%, anomalous spend, rules above baseline, failing backends.">
        <AttentionList onGo={navigate} summary={summary} />
      </Section>
    </div>
  )
}

/** GET /gateway/overhead: spec G6, from receipts. */
type GatewayOverhead = { p50Ms: number | null; p95Ms: number | null; samples: number; windowMinutes: number; goalMs: number }

function StatusStrip() {
  const now = useNow()
  const receipts = useReceipts()
  const warden = useLive(dataMode === 'api' ? '/session' : null, session, 15_000).data.warden
  const overhead = useLive<GatewayOverhead | null>(dataMode === 'api' ? '/gateway/overhead' : null, null, 30_000).data
  const overGoal = overhead?.p50Ms != null && overhead.p50Ms > overhead.goalMs
  // A backend is called out when it's down or failing now. In api mode that's
  // observed from receipts; idle backends had no requests to judge by.
  const live = useLive(dataMode === 'api' ? '/backends' : null, backends, 30_000).data
  const degradedBackends = live.filter((b) => b.health === 'down' || b.health === 'degraded' || b.sync === 'failed' || b.errorRate >= 5)
  const idle = live.filter((b) => b.health === 'idle' && !degradedBackends.includes(b)).length
  const failOpen = rules.filter((r) => r.failMode === 'open' && r.mode !== 'draft' && r.mode !== 'disabled')
  const last = receipts.reduce((m, r) => Math.max(m, r.ts), 0)
  const quiet = !last || now - last > 5 * 60_000
  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-border bg-header px-6 py-2.5 text-sm" aria-label="System status">
      <span className="inline-flex items-center gap-2">
        <span className={cn('size-2 rounded-full', quiet || overGoal ? 'bg-v-degraded-bar' : 'bg-v-allowed-bar')} aria-hidden="true" />
        Gateway{' '}
        <span className="text-muted-foreground">
          {dataMode === 'api' ? (last ? `last receipt ${ago(Math.min(last, now), now)}` : 'no receipts yet') : 'nominal · p50 overhead 2.1ms'}
          {overhead?.p50Ms != null && (
            <span
              className={cn(overGoal && 'text-v-degraded-fg')}
              title={`The gateway's own time per request (key check, Warden, Agent Router) over the last ${overhead.windowMinutes} minutes: p95 ${overhead.p95Ms}ms across ${overhead.samples.toLocaleString('en-US')} requests. Goal: ${overhead.goalMs}ms p50.`}
            >
              {` · p50 overhead ${overhead.p50Ms}ms`}
              {overGoal && ` (goal ${overhead.goalMs}ms)`}
            </span>
          )}
        </span>
      </span>
      <span className="inline-flex flex-wrap items-center gap-2">
        Providers
        {degradedBackends.map((b) => (
          <StateChip key={b.name} tone={b.health === 'down' ? 'blocked' : 'degraded'}>
            <span className="font-mono">{b.name}</span>{' '}
            {b.health === 'down' ? (dataMode === 'api' ? 'down, every request failing' : 'down, not routed') : `${b.errorRate}% errors, last hour`}
          </StateChip>
        ))}
        <span className="text-muted-foreground">
          {live.length - degradedBackends.length - idle} others nominal{idle > 0 && ` · ${idle} idle`}
        </span>
      </span>
      <span className="inline-flex items-center gap-2">
        Warden
        {!warden ? (
          <span className="text-muted-foreground">not in this request path</span>
        ) : !warden.connected ? (
          <StateChip tone="blocked">unreachable</StateChip>
        ) : warden.passthrough ? (
          <StateChip tone="blocked">kill switch on</StateChip>
        ) : (warden.snapshotAgeSeconds ?? 0) > 60 ? (
          <StateChip tone="degraded">cache {age(warden.snapshotAgeSeconds ?? 0)} old</StateChip>
        ) : (
          <span className="text-muted-foreground">cache {age(warden.snapshotAgeSeconds ?? 0)} old</span>
        )}
      </span>
      <span className="inline-flex items-center gap-2">
        Fail modes
        <span className="text-muted-foreground">{rules.length - failOpen.length} fail-closed</span>
        {failOpen.map((r) => (
          <StateChip key={r.id} tone="degraded">
            <span className="font-mono">{r.name}</span> fail-open
          </StateChip>
        ))}
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

// Too few requests either side and percentages mean nothing.
const MIN_COMPARE = 20

/** The differentiating component: one change, its before/after window, the delta in words. */
function FeaturedChange({ change: c }: { change: Change }) {
  const { data: impact, loaded } = useLive<ChangeImpact | null>(dataMode === 'api' ? `/changes/${c.id}/impact` : null, seedChangeImpacts[c.id] ?? null, 60_000)
  const metrics = impact
    ? [
        { label: 'p50 latency', before: impact.before.p50Ms, after: impact.after.p50Ms, fmt: (n: number) => `${int(Math.round(n))}ms`, goodWhen: 'down' as const },
        { label: 'Cost / request', before: impact.before.costPerRequestUsd, after: impact.after.costPerRequestUsd, fmt: (n: number) => `$${n.toFixed(4)}`, goodWhen: 'down' as const },
        { label: 'Error rate', before: impact.before.errorRate * 100, after: impact.after.errorRate * 100, fmt: (n: number) => `${n.toFixed(1)}%`, goodWhen: 'down' as const },
        {
          label: 'Blocked + redacted',
          before: impact.before.blockedRedactedShare * 100,
          after: impact.after.blockedRedactedShare * 100,
          fmt: (n: number) => `${n.toFixed(1)}%`,
          goodWhen: 'down' as const,
        },
      ]
    : []
  const comparable = !!impact && impact.before.requests >= MIN_COMPARE && impact.after.requests >= MIN_COMPARE
  const w = impact?.windowMinutes ?? 40
  return (
    <article className="flex flex-col gap-3 rounded-md border border-border p-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <p className="text-base leading-6">
          {c.action} <span className="font-mono font-medium">{c.target}</span>
        </p>
        <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
          <MonitorCog className="size-3.5" aria-hidden="true" />
          {c.actor} · <span className="num font-mono">{clock(c.ts).slice(0, 5)}</span> ({ago(c.ts)})
        </span>
      </div>
      {comparable ? (
        <>
          <dl className="grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-4">
            {metrics.map((m) => {
              const pct = m.before > 0 ? ((m.after - m.before) / m.before) * 100 : 0
              return (
                <div key={m.label} className="flex flex-col gap-0.5">
                  <dt className="text-xs text-muted-foreground">{m.label}</dt>
                  <dd className="flex items-baseline gap-2">
                    <span className="num font-mono text-base">{m.fmt(m.after)}</span>
                    {Math.abs(pct) < 5 ? <span className="text-xs text-muted-foreground">steady</span> : <Delta pct={pct} goodWhen={m.goodWhen} />}
                  </dd>
                  <dd className="num font-mono text-xs text-muted-foreground">was {m.fmt(m.before)}</dd>
                </div>
              )
            })}
          </dl>
          <p className="text-xs text-muted-foreground">
            Whole tenant, {w} minutes before vs after ({int(impact!.before.requests + impact!.after.requests)} requests), not only what this change touched.
          </p>
        </>
      ) : (
        <p className="text-sm text-muted-foreground">
          {!loaded
            ? 'Comparing traffic either side of the change…'
            : !impact
              ? 'No traffic comparison for this change.'
              : `Too little traffic to compare yet: ${int(impact.before.requests)} requests in the ${w} minutes before, ${int(impact.after.requests)} after.`}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" size="sm" render={<Link to={`/traffic?since=${Math.round(c.ts)}`} />}>
          Receipts after the change
        </Button>
        {c.targetKind === 'Route' && (
          <Button variant="ghost" size="sm" render={<Link to="/routing" />}>
            View route
          </Button>
        )}
      </div>
    </article>
  )
}

type Attention = { id: string; tone: 'blocked' | 'degraded'; what: React.ReactNode; detail: string; action: string; to: string }

function AttentionList({ onGo, summary }: { onGo: (to: string) => void; summary: Summary }) {
  const degradations = useDegradations()
  const over = budgets.filter((b) => b.currentUsd / b.capUsd >= 0.8).sort((a, b) => b.currentUsd / b.capUsd - a.currentUsd / a.capUsd)
  const items: Attention[] = [
    ...over.map((b) => {
      const pct = Math.round((b.currentUsd / b.capUsd) * 100)
      const exceeded = pct >= 100
      return {
        id: b.id,
        tone: exceeded ? ('blocked' as const) : ('degraded' as const),
        what: (
          <>
            Budget <span className="font-mono">{b.scope}</span> at {pct}% of <Money value={b.capUsd} precision="whole" />
          </>
        ),
        detail: exceeded
          ? `${b.onExceed === 'throttle' ? 'Throttling' : b.onExceed === 'block' ? 'Blocking' : 'Warning on'} new requests since the cap was crossed. Projected ${money(b.projectedUsd, 0)} by period end.`
          : `Projected ${money(b.projectedUsd, 0)} by period end; ${b.onExceed === 'block' ? 'blocks new requests' : b.onExceed === 'throttle' ? 'throttles' : 'warns'} at ${money(b.capUsd, 0)}.`,
        action: exceeded ? 'Review budget' : 'Adjust cap',
        to: '/spend',
      }
    }),
    ...summary.keyAnomalies.map((a) => ({
      id: `anom-${a.keyId}`,
      tone: 'degraded' as const,
      what: (
        <>
          Key <span className="font-mono">{a.keyName}</span> spend {a.ratio.toFixed(1)}× its 7-day baseline
        </>
      ),
      detail: `${money(a.spendUsd)} in the last 24 hours against a ${money(a.baselineUsd)} daily average. ${Math.round(a.topModelShare * 100)}% of it on ${a.topModel}.`,
      action: 'Open key',
      to: `/keys?key=${a.keyId}`,
    })),
    ...rules
      .filter((r) => r.mode !== 'draft' && r.mode !== 'disabled' && r.baseline7d > 0 && r.fired24h >= 10 && r.fired24h >= 2 * r.baseline7d)
      .map((r) => ({
        id: `rule-${r.id}`,
        tone: 'degraded' as const,
        what: (
          <>
            Rule <span className="font-mono">{r.name}</span> fired {int(r.fired24h)} times, baseline {int(r.baseline7d)}
          </>
        ),
        detail: `In the last 24 hours, against a daily average of ${int(r.baseline7d)} over the week before.`,
        action: 'Open rule',
        to: `/guardrails?rule=${r.id}`,
      })),
    ...degradations
      .filter((d) => d.kind === 'backend_errors')
      .map((d) => ({ id: d.kind + d.title, tone: 'degraded' as const, what: <>{d.title}</>, detail: d.detail, action: d.action, to: d.to })),
  ]
  if (items.length === 0) return <p className="text-sm text-muted-foreground">Nothing needs attention.</p>
  return (
    <ul className="divide-y divide-border border-y border-border">
      {items.map((i) => (
        <li key={i.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5">
          <span className={cn('h-8 w-1 shrink-0 rounded-full', i.tone === 'blocked' ? 'bg-v-blocked-bar' : 'bg-v-degraded-bar')} aria-hidden="true" />
          <div className="min-w-0 flex-1">
            <div className="text-sm font-medium">{i.what}</div>
            <div className="text-[0.8125rem] text-muted-foreground">{i.detail}</div>
          </div>
          <Button variant="outline" size="sm" onClick={() => onGo(i.to)}>
            {i.action}
          </Button>
        </li>
      ))}
    </ul>
  )
}
