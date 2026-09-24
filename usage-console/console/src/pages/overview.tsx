import { ArrowRight, GitBranch, MonitorCog } from 'lucide-react'
import { Link, useNavigate } from 'react-router-dom'
import { StackedArea } from '@/components/gw/charts'
import { Delta, Money } from '@/components/gw/numbers'
import { PageHeader, Section } from '@/components/gw/page'
import { StateChip, toneFill, toneText } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { backends, budgets, changes, rules, trafficSeries } from '@/data/mock'
import { ago, clock, int, money } from '@/lib/format'
import { cn } from '@/lib/utils'
import { rangeLabel, useApp } from '@/state/app-state'

// §7.5.2 Overview — not a tile grid. A single vertical narrative:
// status strip → traffic → three numbers → what changed → attention list.

// Stack order is part of the palette: validate_palette.js checks adjacent pairs,
// and green↔teal and yellow↔red fail as neighbors. This order passes both themes.
const verdictSeries = [
  { key: 'allowed', label: 'Allowed', color: toneFill.allowed },
  { key: 'redacted', label: 'Redacted', color: toneFill.redacted },
  { key: 'truncated', label: 'Truncated', color: toneFill.degraded },
  { key: 'rerouted', label: 'Rerouted', color: toneFill.rerouted },
  { key: 'blocked', label: 'Blocked', color: toneFill.blocked },
]

export function OverviewPage() {
  const { range } = useApp()
  const navigate = useNavigate()
  const totals = trafficSeries.reduce(
    (a, p) => ({
      req: a.req + p.allowed + p.redacted + p.rerouted + p.blocked + p.truncated,
      br: a.br + p.blocked + p.redacted,
    }),
    { req: 0, br: 0 },
  )
  const spend = 3_184.62
  const routeChange = changes[0]

  return (
    <div>
      <PageHeader
        title="Overview"
        description={
          <>
            acme / production, {rangeLabel(range)}. Every number here drills to the receipts that compose it.
          </>
        }
      />

      {/* 1. Status strip — green is the absence of information. */}
      <StatusStrip />

      {/* 2. Traffic with verdict composition. */}
      <Section
        title="Traffic"
        description="Requests per 30 minutes by verdict. Blocked and redacted stay visible at this altitude."
        actions={
          <Button variant="ghost" size="sm" render={<Link to="/traffic" />}>
            Open traffic <ArrowRight />
          </Button>
        }
      >
        <StackedArea
          caption="Requests per 30 minutes by verdict"
          series={verdictSeries}
          data={trafficSeries.map((p) => ({ t: p.t, values: { allowed: p.allowed, rerouted: p.rerouted, redacted: p.redacted, truncated: p.truncated, blocked: p.blocked } }))}
          xFormat={(t) => clock(t).slice(0, 5)}
          annotations={[{ t: routeChange.ts, label: 'route default → sonnet-5' }]}
        />
      </Section>

      {/* 3. Three numbers with trend, each a link carrying the time range. */}
      <div className="grid grid-cols-1 border-b border-border sm:grid-cols-3">
        <BigNumber to="/traffic" label="Requests" value={int(totals.req)} delta={<Delta pct={6.2} goodWhen="up" />} note="vs previous 24 hours" />
        <BigNumber to="/spend" label="Spend" value={money(spend)} delta={<Delta pct={41.8} goodWhen="down" />} note="support is driving the increase" />
        <BigNumber
          to="/traffic?verdict=blocked&verdict=redacted"
          label="Blocked + redacted"
          value={int(totals.br)}
          delta={<Delta pct={23.5} goodWhen="down" />}
          note="block-src fired 7× its baseline"
        />
      </div>

      {/* 4. What changed — config changes joined to their traffic effect. */}
      <Section
        title="What changed"
        description="Config changes in this window and what they did to traffic."
        actions={
          <Button variant="ghost" size="sm" render={<Link to="/activity" />}>
            Full activity <ArrowRight />
          </Button>
        }
      >
        <FeaturedChange />
        <ol className="mt-4 divide-y divide-border border-y border-border">
          {changes.slice(1, 5).map((c) => (
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
      <Section title="Needs attention" description="Budgets over 80%, anomalous spend, rules above baseline, failovers.">
        <AttentionList onGo={navigate} />
      </Section>
    </div>
  )
}

function StatusStrip() {
  const degradedBackends = backends.filter((b) => b.health !== 'healthy' || b.sync === 'failed')
  const failOpen = rules.filter((r) => r.failMode === 'open' && r.mode !== 'draft')
  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-border bg-header px-6 py-2.5 text-sm" aria-label="System status">
      <span className="inline-flex items-center gap-2">
        <span className="size-2 rounded-full bg-v-allowed-bar" aria-hidden="true" />
        Gateway <span className="text-muted-foreground">nominal · p50 overhead 2.1ms</span>
      </span>
      <span className="inline-flex flex-wrap items-center gap-2">
        Providers
        {degradedBackends.map((b) => (
          <StateChip key={b.name} tone={b.health === 'down' ? 'blocked' : 'degraded'}>
            <span className="font-mono">{b.name}</span> {b.health === 'down' ? 'down, not routed' : `${b.errorRate}% errors, failing over`}
          </StateChip>
        ))}
        <span className="text-muted-foreground">{backends.length - degradedBackends.length} others nominal</span>
      </span>
      <span className="inline-flex items-center gap-2">
        Warden cache <StateChip tone="degraded">4m 12s old</StateChip>
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

/** The differentiating component: one change, its before/after window, the delta in words. */
function FeaturedChange() {
  const c = changes[0]
  const metrics = [
    { label: 'p50 latency', before: 1_320, after: 980, fmt: (n: number) => `${int(n)}ms`, goodWhen: 'down' as const },
    { label: 'Cost / request', before: 0.0341, after: 0.0211, fmt: (n: number) => `$${n.toFixed(4)}`, goodWhen: 'down' as const },
    { label: 'Error rate', before: 0.6, after: 0.7, fmt: (n: number) => `${n.toFixed(1)}%`, goodWhen: 'down' as const },
    { label: 'Blocked + redacted', before: 10.4, after: 10.6, fmt: (n: number) => `${n.toFixed(1)}%`, goodWhen: 'down' as const },
  ]
  return (
    <article className="grid gap-5 rounded-md border border-border p-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)]">
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <MonitorCog className="size-3.5" aria-hidden="true" />
          <span className="num font-mono">{clock(c.ts).slice(0, 5)}</span> · {ago(c.ts)} · {c.actor}
        </div>
        <p className="text-base leading-6">
          Route <span className="font-mono font-medium">default</span> switched to <span className="font-mono font-medium">claude-sonnet-5</span> at{' '}
          <span className="num font-mono">{clock(c.ts).slice(0, 5)}</span>.
        </p>
        <p className={cn('text-base leading-6 font-medium', toneText.allowed)}>p50 latency −340ms, cost/request −38%.</p>
        <p className="text-sm text-muted-foreground">
          Compared over 40 minutes either side of the change, 3,912 requests on route default. Error rate and policy verdicts did not move meaningfully.
        </p>
        <div className="mt-auto flex flex-wrap gap-2 pt-2">
          <Button variant="outline" size="sm" render={<Link to={`/traffic?since=${Math.round(c.ts)}&model=claude-sonnet-5`} />}>
            Receipts after the change
          </Button>
          <Button variant="ghost" size="sm" render={<Link to="/routing" />}>
            View route diff
          </Button>
        </div>
      </div>
      <table className="w-full self-start text-sm">
        <caption className="sr-only">Before and after the route change</caption>
        <thead>
          <tr className="border-b border-border text-xs text-muted-foreground">
            <th className="py-1 text-left font-medium">Metric</th>
            <th className="py-1 text-right font-medium">40m before</th>
            <th className="py-1 text-right font-medium">40m after</th>
            <th className="w-36 py-1 pl-4 text-left font-medium">Change</th>
          </tr>
        </thead>
        <tbody>
          {metrics.map((m) => {
            const pct = ((m.after - m.before) / m.before) * 100
            const max = Math.max(m.before, m.after)
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
    </article>
  )
}

function AttentionList({ onGo }: { onGo: (to: string) => void }) {
  const over = budgets.filter((b) => b.currentUsd / b.capUsd >= 0.8).sort((a, b) => b.currentUsd / b.capUsd - a.currentUsd / a.capUsd)
  const items: { id: string; tone: 'blocked' | 'degraded'; what: React.ReactNode; detail: string; action: string; to: string }[] = [
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
    {
      id: 'anom',
      tone: 'degraded',
      what: (
        <>
          Key <span className="font-mono">support-bot</span> spend 2.9× its 7-day baseline
        </>
      ),
      detail: 'Started 6 days ago. 71% of the increase is claude-sonnet-5 with long histories.',
      action: 'Open key',
      to: '/keys?key=k1',
    },
    {
      id: 'rule',
      tone: 'degraded',
      what: (
        <>
          Rule <span className="font-mono">block-src</span> fired 96 times, baseline 14
        </>
      ),
      detail: 'Since v12 was published 5h ago. 82 of the blocks are from support.',
      action: 'Replay rule',
      to: '/guardrails?rule=r3',
    },
    {
      id: 'fo',
      tone: 'degraded',
      what: (
        <>
          <span className="font-mono">anthropic-prod</span> failing over to <span className="font-mono">bedrock-eu</span>
        </>
      ),
      detail: '8% of Claude requests since 13:51. Upstream 529 overloaded.',
      action: 'View fallback',
      to: '/traffic?reason=fallback',
    },
  ]
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
