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

function StatusStrip() {
  const now = useNow()
  const receipts = useReceipts()
  const warden = useLive(dataMode === 'api' ? '/session' : null, session, 15_000).data.warden
  // A backend is called out when it's configured down, or failing now.
  const degradedBackends = backends.filter((b) => b.health === 'down' || b.sync === 'failed' || b.errorRate >= 5)
  const failOpen = rules.filter((r) => r.failMode === 'open' && r.mode !== 'draft' && r.mode !== 'disabled')
  const last = receipts.reduce((m, r) => Math.max(m, r.ts), 0)
  const quiet = !last || now - last > 5 * 60_000
  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-border bg-header px-6 py-2.5 text-sm" aria-label="System status">
      <span className="inline-flex items-center gap-2">
        <span className={cn('size-2 rounded-full', quiet ? 'bg-v-degraded-bar' : 'bg-v-allowed-bar')} aria-hidden="true" />
        Gateway{' '}
        <span className="text-muted-foreground">
          {dataMode === 'api' ? (last ? `last receipt ${ago(Math.min(last, now), now)}` : 'no receipts yet') : 'nominal · p50 overhead 2.1ms'}
        </span>
      </span>
      <span className="inline-flex flex-wrap items-center gap-2">
        Providers
        {degradedBackends.map((b) => (
          <StateChip key={b.name} tone={b.health === 'down' ? 'blocked' : 'degraded'}>
            <span className="font-mono">{b.name}</span> {b.health === 'down' ? 'down, not routed' : `${b.errorRate}% errors, last hour`}
          </StateChip>
        ))}
        <span className="text-muted-foreground">{backends.length - degradedBackends.length} others nominal</span>
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
  const movers = metrics.filter((m) => m.before > 0 && Math.abs((m.after - m.before) / m.before) >= 0.05)
  // Every metric here is better when it goes down.
  const allBetter = movers.every((m) => m.after < m.before)
  const moved = movers.map((m) => `${m.label.toLowerCase()} ${m.after < m.before ? '−' : '+'}${Math.abs(Math.round(((m.after - m.before) / m.before) * 100))}%`)
  const w = impact?.windowMinutes ?? 40
  return (
    <article className="grid gap-5 rounded-md border border-border p-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)]">
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <MonitorCog className="size-3.5" aria-hidden="true" />
          <span className="num font-mono">{clock(c.ts).slice(0, 5)}</span> · {ago(c.ts)} · {c.actor}
        </div>
        <p className="text-base leading-6">
          {c.action} <span className="font-mono font-medium">{c.target}</span> at <span className="num font-mono">{clock(c.ts).slice(0, 5)}</span>.
        </p>
        {comparable && (
          <p className={cn('text-base leading-6 font-medium', !moved.length ? 'text-muted-foreground-strong' : allBetter ? toneText.allowed : 'text-foreground')}>
            {moved.length ? `${moved.join(', ')}.` : 'Nothing moved by 5% or more.'}
          </p>
        )}
        <p className="text-sm text-muted-foreground">
          {!loaded
            ? 'Comparing traffic either side of the change…'
            : !impact
              ? 'No traffic comparison for this change.'
              : comparable
                ? `Compared over ${w} minutes either side of the change, ${int(impact.before.requests + impact.after.requests)} requests across the tenant.`
                : `Too little traffic to compare yet: ${int(impact.before.requests)} requests in the ${w} minutes before, ${int(impact.after.requests)} after.`}
        </p>
        <div className="mt-auto flex flex-wrap gap-2 pt-2">
          <Button variant="outline" size="sm" render={<Link to={`/traffic?since=${Math.round(c.ts)}`} />}>
            Receipts after the change
          </Button>
          {c.targetKind === 'Route' && (
            <Button variant="ghost" size="sm" render={<Link to="/routing" />}>
              View route
            </Button>
          )}
        </div>
      </div>
      {comparable && (
        <table className="w-full self-start text-sm">
          <caption className="sr-only">Before and after the change</caption>
          <thead>
            <tr className="border-b border-border text-xs text-muted-foreground">
              <th className="py-1 text-left font-medium">Metric</th>
              <th className="py-1 text-right font-medium">{w}m before</th>
              <th className="py-1 text-right font-medium">{w}m after</th>
              <th className="w-36 py-1 pl-4 text-left font-medium">Change</th>
            </tr>
          </thead>
          <tbody>
            {metrics.map((m) => {
              const pct = m.before > 0 ? ((m.after - m.before) / m.before) * 100 : 0
              const max = Math.max(m.before, m.after) || 1
              return (
                <tr key={m.label} className="border-b border-border last:border-0">
                  <td className="py-2">{m.label}</td>
                  <td className="num py-2 text-right font-mono text-muted-foreground">{m.fmt(m.before)}</td>
                  <td className="num py-2 text-right font-mono">{m.fmt(m.after)}</td>
                  <td className="py-2 pl-4">
                    <div className="flex items-center gap-2">
                      <div className="flex w-16 flex-col gap-0.5" aria-hidden="true">
                        <span className="h-1 rounded-full bg-border-strong" style={{ width: `${(m.before / max) * 100}%` }} />
                        <span className="h-1 rounded-full bg-foreground" style={{ width: `${(m.after / max) * 100}%` }} />
                      </div>
                      {Math.abs(pct) < 5 ? <span className="text-xs text-muted-foreground">no change</span> : <Delta pct={pct} goodWhen={m.goodWhen} />}
                    </div>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
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
