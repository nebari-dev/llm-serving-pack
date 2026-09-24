import { ArrowRight, Radio, SlidersHorizontal } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Sparkline } from '@/components/gw/charts'
import { PageHeader } from '@/components/gw/page'
import { ProvenanceBadge } from '@/components/gw/provenance'
import { StateChip, type Tone } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { type Change, changes, now, trafficSeries } from '@/data/mock'
import { ago, clock } from '@/lib/format'
import { cn } from '@/lib/utils'
import { rangeLabel, type TimeRange, useApp } from '@/state/app-state'

// §7.5.9 Activity: config changes and traffic on one timeline. Audit records
// and receipts share an actor identity space and a clock, so each change is
// shown next to what it did to traffic.

const rangeMs: Record<TimeRange, number> = {
  '15m': 15 * 60_000,
  '1h': 3_600_000,
  '6h': 6 * 3_600_000,
  '24h': 24 * 3_600_000,
  '7d': 7 * 86_400_000,
  '30d': 30 * 86_400_000,
}

type Metric = 'total' | 'blocked' | 'rerouted'

// Per-change before/after readouts. Numbers come from the receipt aggregates
// for the 2h before and after the change (synthetic in the demo tenant).
const impact: Record<string, { metric: string; before: string; after: string; series: Metric; resource: string }> = {
  c1: { metric: 'p50 latency, route default', before: '1,240ms', after: '900ms', series: 'total', resource: '/routing' },
  c2: { metric: 'p95 latency, eu-private', before: '1,180ms', after: '1,790ms', series: 'rerouted', resource: '/routing' },
  c3: { metric: 'blocked share of requests', before: '1.2%', after: '8.9%', series: 'blocked', resource: '/guardrails?rule=r3' },
  c4: { metric: 'throttled requests, agents', before: '312/h', after: '0/h', series: 'total', resource: '/spend' },
  c5: { metric: 'traffic on new secret', before: '0%', after: '61%', series: 'total', resource: '/keys?key=k4' },
  c6: { metric: 'would-redact (monitor)', before: '—', after: '22 / 24h', series: 'total', resource: '/guardrails?rule=r4' },
}

const effectTone: Record<NonNullable<Change['effectTone']>, { tone: Tone; label: string }> = {
  good: { tone: 'allowed', label: 'Improved' },
  bad: { tone: 'degraded', label: 'Regressed' },
  neutral: { tone: 'neutral', label: 'Informational' },
}

interface TrafficEvent {
  id: string
  ts: number
  title: string
  detail: string
  tone: Tone
  to: string
}

const NOW = now()
const trafficEvents: TrafficEvent[] = [
  { id: 't1', ts: NOW - 70 * 60_000, title: 'anthropic-prod failover began', detail: '8% of Claude traffic moved to bedrock-eu after 529 overloaded responses', tone: 'degraded', to: '/traffic?backend=bedrock-eu&reason=fallback' },
  { id: 't2', ts: NOW - 4.5 * 3_600_000, title: `Blocks spiked to 9% at ${clock(NOW - 4.5 * 3_600_000).slice(0, 5)}`, detail: '82 of 96 blocks from support-bot, all on block-src', tone: 'blocked', to: '/traffic?verdict=blocked' },
  { id: 't3', ts: NOW - 3 * 3_600_000, title: 'Blocks back under baseline', detail: 'block-src hits dropped to 14/h after support changed its prompt template', tone: 'allowed', to: '/traffic?verdict=blocked' },
  { id: 't4', ts: NOW - 14 * 3_600_000, title: 'Budget "support" crossed its cap', detail: '$12,000 reached; throttle policy engaged', tone: 'degraded', to: '/spend' },
]

/** Slice trafficSeries around a timestamp: 4 points before, 4 after. */
function around(ts: number, metric: Metric) {
  let i = trafficSeries.findIndex((p) => p.t >= ts)
  if (i < 0) i = trafficSeries.length - 1
  const lo = Math.max(0, i - 4)
  const hi = Math.min(trafficSeries.length, i + 5)
  const pts = trafficSeries.slice(lo, hi)
  const val = (p: (typeof trafficSeries)[number]) =>
    metric === 'total' ? p.allowed + p.redacted + p.rerouted + p.blocked + p.truncated : p[metric]
  return { values: pts.map(val), split: i - lo, ok: pts.length > 2 }
}

function Pick({ label, value, onChange, options }: { label: string; value: string; onChange: (v: string) => void; options: { value: string; label: string }[] }) {
  return (
    <Select value={value} onValueChange={(v) => v != null && onChange(v as string)} items={options}>
      <SelectTrigger aria-label={label} className="h-8 w-auto min-w-36">
        <span className="text-muted-foreground">{label}:</span>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

type Item = { kind: 'change'; ts: number; c: Change } | { kind: 'traffic'; ts: number; e: TrafficEvent }

export function ActivityPage() {
  const { range, setRange } = useApp()
  const [actor, setActor] = useState('all')
  const [kind, setKind] = useState('all')
  const [effect, setEffect] = useState('all')
  const [showTraffic, setShowTraffic] = useState(true)

  const since = NOW - rangeMs[range]
  const actors = useMemo(() => [...new Set(changes.map((c) => c.actor))], [])
  const kinds = useMemo(() => [...new Set(changes.map((c) => c.targetKind))], [])

  const items = useMemo<Item[]>(() => {
    const cs: Item[] = changes
      .filter((c) => c.ts >= since)
      .filter((c) => actor === 'all' || c.actor === actor)
      .filter((c) => kind === 'all' || c.targetKind === kind)
      .filter((c) => effect === 'all' || c.effectTone === effect)
      .map((c) => ({ kind: 'change', ts: c.ts, c }))
    const filtering = actor !== 'all' || kind !== 'all' || effect !== 'all'
    const ts: Item[] = showTraffic && !filtering ? trafficEvents.filter((e) => e.ts >= since).map((e) => ({ kind: 'traffic', ts: e.ts, e })) : []
    return [...cs, ...ts].sort((a, b) => b.ts - a.ts)
  }, [since, actor, kind, effect, showTraffic])

  const hiddenByRange = changes.filter((c) => c.ts < since).length
  const filtering = actor !== 'all' || kind !== 'all' || effect !== 'all'

  return (
    <div className="flex flex-col">
      <PageHeader
        title="Activity"
        description="What changed, and what did it do? Config changes and traffic on one timeline, joined by actor and clock."
      >
        <div className="flex flex-wrap items-center gap-2">
          <SlidersHorizontal className="size-4 text-muted-foreground" aria-hidden="true" />
          <Pick label="Actor" value={actor} onChange={setActor} options={[{ value: 'all', label: 'Anyone' }, ...actors.map((a) => ({ value: a, label: a }))]} />
          <Pick label="Resource" value={kind} onChange={setKind} options={[{ value: 'all', label: 'Any' }, ...kinds.map((k) => ({ value: k, label: k }))]} />
          <Pick
            label="Effect"
            value={effect}
            onChange={setEffect}
            options={[
              { value: 'all', label: 'Any' },
              { value: 'good', label: 'Improved' },
              { value: 'bad', label: 'Regressed' },
              { value: 'neutral', label: 'Informational' },
            ]}
          />
          <label className="ml-2 inline-flex items-center gap-2 text-sm">
            <input type="checkbox" checked={showTraffic} onChange={(e) => setShowTraffic(e.target.checked)} className="size-4 accent-primary" disabled={filtering} />
            <span className={cn(filtering && 'text-muted-foreground')}>Show traffic events{filtering && ' (hidden while filtering changes)'}</span>
          </label>
          <span className="ml-auto text-xs text-muted-foreground">Showing {rangeLabel(range)}</span>
        </div>
      </PageHeader>

      {items.length === 0 ? (
        <div className="flex flex-col items-center gap-3 px-6 py-16 text-center">
          <p className="text-sm text-muted-foreground-strong">No config changes match in the {rangeLabel(range)}.</p>
          <Button variant="outline" size="sm" onClick={() => setRange('7d')}>
            Widen to last 7 days
          </Button>
        </div>
      ) : (
        <ol className="relative px-6 py-5" aria-label="Activity timeline">
          <span className="absolute top-5 bottom-5 left-[10.4375rem] w-px bg-border" aria-hidden="true" />
          {items.map((it) => (it.kind === 'change' ? <ChangeRow key={it.c.id} c={it.c} /> : <TrafficRow key={it.e.id} e={it.e} />))}
        </ol>
      )}

      {hiddenByRange > 0 && items.length > 0 && (
        <p className="border-t border-border px-6 py-3 text-xs text-muted-foreground">
          {hiddenByRange} earlier change{hiddenByRange === 1 ? '' : 's'} outside the {rangeLabel(range)}.{' '}
          <button type="button" className="underline underline-offset-4 hover:text-foreground" onClick={() => setRange('7d')}>
            Show last 7 days
          </button>
        </p>
      )}
    </div>
  )
}

function When({ ts }: { ts: number }) {
  return (
    <div className="w-[7.5rem] shrink-0 pt-0.5 text-right">
      <div className="num font-mono text-xs">{clock(ts).slice(0, 5)}</div>
      <div className="text-xs text-muted-foreground">{ago(ts, NOW)}</div>
    </div>
  )
}

function ChangeRow({ c }: { c: Change }) {
  const imp = impact[c.id]
  const s = imp ? around(c.ts, imp.series) : null
  const tone = c.effectTone ? effectTone[c.effectTone] : null
  return (
    <li className="relative flex gap-3 pb-6">
      <When ts={c.ts} />
      <span className="z-[1] mt-1 size-[1.375rem] shrink-0 rounded-full border-2 border-foreground bg-canvas" aria-hidden="true" />
      <div className="min-w-0 flex-1 border-b border-border pb-5">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="text-sm font-medium">{c.action}</span>
          <span className="font-mono text-sm">{c.target}</span>
          <span className="text-xs text-muted-foreground">· {c.targetKind}</span>
          <ProvenanceBadge provenance={c.source === 'git' ? 'git' : 'console'} source={c.source === 'git' ? 'github.com/acme/platform-gitops/commits/main' : undefined} />
        </div>
        <div className="mt-0.5 text-xs text-muted-foreground">by {c.actor}</div>

        {c.effect && (
          <div className="mt-3 flex flex-wrap items-center gap-x-6 gap-y-3">
            <div className="flex min-w-0 flex-col gap-1">
              <span className="flex items-center gap-2 text-sm">
                {tone && <StateChip tone={tone.tone}>{tone.label}</StateChip>}
                <span>{c.effect}</span>
              </span>
              {imp && (
                <span className="flex items-center gap-2 text-xs text-muted-foreground-strong">
                  <span>{imp.metric}</span>
                  <span className="num font-mono">{imp.before}</span>
                  <ArrowRight className="size-3" aria-label="to" />
                  <span className="num font-mono font-medium text-foreground">{imp.after}</span>
                </span>
              )}
            </div>
            {s?.ok && (
              <div className="flex flex-col items-start gap-0.5">
                <div className="relative">
                  <Sparkline values={s.values} width={132} height={28} label={`${imp!.series} requests around this change`} />
                  <span
                    className="absolute inset-y-0 w-px bg-foreground"
                    style={{ left: `${(s.split / Math.max(1, s.values.length - 1)) * 100}%` }}
                    aria-hidden="true"
                  />
                </div>
                <span className="text-[11px] text-muted-foreground">{imp!.series === 'total' ? 'requests' : `${imp!.series} requests`}, 2h either side</span>
              </div>
            )}
          </div>
        )}

        <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs">
          {imp && (
            <Link to={imp.resource} className="font-medium underline-offset-4 hover:underline">
              Open {c.targetKind.toLowerCase()}
            </Link>
          )}
          <Link to={`/traffic?since=${c.ts}`} className="font-medium underline-offset-4 hover:underline">
            Receipts after this change →
          </Link>
          <span className="text-muted-foreground">Audit record {c.id}-{String(c.ts).slice(-6)}</span>
        </div>
      </div>
    </li>
  )
}

function TrafficRow({ e }: { e: TrafficEvent }) {
  return (
    <li className="relative flex gap-3 pb-6">
      <When ts={e.ts} />
      <span className="z-[1] mt-1 flex size-[1.375rem] shrink-0 items-center justify-center rounded-full bg-canvas" aria-hidden="true">
        <Radio className="size-3.5 text-muted-foreground" />
      </span>
      <div className="min-w-0 flex-1 border-b border-dashed border-border pb-5">
        <div className="flex flex-wrap items-center gap-2">
          <StateChip tone={e.tone}>Traffic</StateChip>
          <span className="text-sm font-medium">{e.title}</span>
        </div>
        <p className="mt-0.5 text-sm text-muted-foreground-strong">{e.detail}</p>
        <Link to={e.to} className="mt-2 inline-block text-xs font-medium underline-offset-4 hover:underline">
          View receipts →
        </Link>
      </div>
    </li>
  )
}
