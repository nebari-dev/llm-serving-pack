import { ArrowRight, Radio, SlidersHorizontal } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Sparkline } from '@/components/gw/charts'
import { PageHeader } from '@/components/gw/page'
import { ProvenanceBadge } from '@/components/gw/provenance'
import { StateChip, type Tone } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  type ActivityChange,
  type ActivityMetric,
  type ActivityView,
  type Change,
  changes as catalogChanges,
  dataMode,
  now,
  seedActivityEvents,
  seedActivityReadouts,
  type TrafficEvent,
  trafficSeries,
} from '@/data/catalog'
import { ago, clock, int, perRequest } from '@/lib/format'
import { cn } from '@/lib/utils'
import { rangeLabel, type TimeRange, useApp } from '@/state/app-state'
import { useLive, useNow } from '@/state/live'

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

const effectTone: Record<NonNullable<Change['effectTone']>, { tone: Tone; label: string }> = {
  good: { tone: 'allowed', label: 'Improved' },
  bad: { tone: 'degraded', label: 'Regressed' },
  neutral: { tone: 'neutral', label: 'Informational' },
}

const NOW = now()

/** Slice trafficSeries around a timestamp: 4 points before, 4 after. */
function around(ts: number, metric: ActivityMetric) {
  let i = trafficSeries.findIndex((p) => p.t >= ts)
  if (i < 0) i = trafficSeries.length - 1
  const lo = Math.max(0, i - 4)
  const hi = Math.min(trafficSeries.length, i + 5)
  const pts = trafficSeries.slice(lo, hi)
  const val = (p: (typeof trafficSeries)[number]) =>
    metric === 'total' ? p.allowed + p.redacted + p.rerouted + p.blocked + p.truncated + p.throttled : p[metric]
  return { values: pts.map(val), split: i - lo, ok: pts.length > 2 }
}

/** What a change row shows under its effect: metric lines and a sparkline. */
interface Readout {
  lines: { metric: string; before: string; after: string }[]
  spark?: { values: number[]; split: number; label: string; caption: string }
  resource?: string
}

// Where "Open …" goes for each audit target kind, in api mode.
const kindResource: Record<string, string> = {
  Route: '/routing',
  Backend: '/routing?tab=backends',
  Policy: '/guardrails',
  Budget: '/spend',
  Export: '/spend',
  Key: '/keys',
}

const pct = (n: number) => `${(n * 100).toFixed(1)}%`

function readoutOf(c: Change | ActivityChange): Readout | null {
  if ('impact' in c) {
    const im = c.impact
    if (im.windowMinutes === 0) return { lines: [], resource: kindResource[c.targetKind] }
    const { before: b, after: a } = im
    return {
      lines: [
        { metric: 'requests', before: int(b.requests), after: int(a.requests) },
        { metric: 'cost/request', before: perRequest(b.costPerRequestUsd), after: perRequest(a.costPerRequestUsd) },
        // Cost/request is over priced requests; say how many it leaves out.
        ...(b.unpriced || a.unpriced ? [{ metric: 'requests with no price', before: int(b.unpriced ?? 0), after: int(a.unpriced ?? 0) }] : []),
        { metric: 'error rate', before: pct(b.errorRate), after: pct(a.errorRate) },
        { metric: 'blocked + redacted', before: pct(b.blockedRedactedShare), after: pct(a.blockedRedactedShare) },
      ],
      spark: {
        values: im.bins,
        split: im.split,
        label: 'requests around this change',
        caption: `requests, ${im.windowMinutes}m either side, across the tenant, from the 5-minute aggregates`,
      },
      resource: kindResource[c.targetKind],
    }
  }
  const imp = seedActivityReadouts[c.id]
  if (!imp) return null
  const s = around(c.ts, imp.series)
  return {
    lines: [{ metric: imp.metric, before: imp.before, after: imp.after }],
    spark: s.ok
      ? { values: s.values, split: s.split, label: `${imp.series} requests around this change`, caption: `${imp.series === 'total' ? 'requests' : `${imp.series} requests`}, 2h either side` }
      : undefined,
    resource: imp.resource,
  }
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

type Item = { kind: 'change'; ts: number; c: Change | ActivityChange } | { kind: 'traffic'; ts: number; e: TrafficEvent }

export function ActivityPage() {
  const { range, setRange } = useApp()
  const [actor, setActor] = useState('all')
  const [kind, setKind] = useState('all')
  const [effect, setEffect] = useState('all')
  const [showTraffic, setShowTraffic] = useState(true)
  const { data: view, loaded } = useLive<ActivityView | null>(dataMode === 'api' ? `/activity?range=${range}` : null, null, 60_000)
  const liveNow = useNow(30_000)
  const clockNow = dataMode === 'api' ? liveNow : NOW

  const since = view?.since ?? NOW - rangeMs[range]
  const changes: (Change | ActivityChange)[] = dataMode === 'api' ? (view?.changes ?? []) : catalogChanges
  const trafficEvents = dataMode === 'api' ? (view?.events ?? []) : seedActivityEvents
  const actors = useMemo(() => [...new Set(changes.map((c) => c.actor))], [changes])
  const kinds = useMemo(() => [...new Set(changes.map((c) => c.targetKind))], [changes])

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
  }, [changes, trafficEvents, since, actor, kind, effect, showTraffic])

  // Api mode's view holds only the range; the hydrated list knows what's older.
  const hiddenByRange = catalogChanges.filter((c) => c.ts < since).length
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

      {!loaded ? (
        <p className="px-6 py-16 text-center text-sm text-muted-foreground">Loading activity…</p>
      ) : items.length === 0 ? (
        <div className="flex flex-col items-center gap-3 px-6 py-16 text-center">
          <p className="text-sm text-muted-foreground-strong">No config changes match in the {rangeLabel(range)}.</p>
          <Button variant="outline" size="sm" onClick={() => setRange('7d')}>
            Widen to last 7 days
          </Button>
        </div>
      ) : (
        <ol className="relative px-6 py-5" aria-label="Activity timeline">
          <span className="absolute top-5 bottom-5 left-[10.4375rem] w-px bg-border" aria-hidden="true" />
          {items.map((it) => (it.kind === 'change' ? <ChangeRow key={it.c.id} c={it.c} now={clockNow} /> : <TrafficRow key={it.e.id} e={it.e} now={clockNow} />))}
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

function When({ ts, now }: { ts: number; now: number }) {
  return (
    <div className="w-[7.5rem] shrink-0 pt-0.5 text-right">
      <div className="num font-mono text-xs">{clock(ts).slice(0, 5)}</div>
      <div className="text-xs text-muted-foreground">{ago(ts, now)}</div>
    </div>
  )
}

function ChangeRow({ c, now }: { c: Change | ActivityChange; now: number }) {
  const r = readoutOf(c)
  const tone = c.effectTone ? effectTone[c.effectTone] : null
  return (
    <li className="relative flex gap-3 pb-6">
      <When ts={c.ts} now={now} />
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
              {r?.lines.map((l) => (
                <span key={l.metric} className="flex items-center gap-2 text-xs text-muted-foreground-strong">
                  <span>{l.metric}</span>
                  <span className="num font-mono">{l.before}</span>
                  <ArrowRight className="size-3" aria-label="to" />
                  <span className="num font-mono font-medium text-foreground">{l.after}</span>
                </span>
              ))}
            </div>
            {r?.spark && (
              <div className="flex flex-col items-start gap-0.5">
                <div className="relative">
                  <Sparkline values={r.spark.values} width={132} height={28} label={r.spark.label} />
                  <span
                    className="absolute inset-y-0 w-px bg-foreground"
                    style={{ left: `${(r.spark.split / Math.max(1, r.spark.values.length - 1)) * 100}%` }}
                    aria-hidden="true"
                  />
                </div>
                <span className="text-[11px] text-muted-foreground">{r.spark.caption}</span>
              </div>
            )}
          </div>
        )}

        <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs">
          {r?.resource && (
            <Link to={r.resource} className="font-medium underline-offset-4 hover:underline">
              Open {c.targetKind.toLowerCase()}
            </Link>
          )}
          <Link to={`/traffic?since=${c.ts}`} className="font-medium underline-offset-4 hover:underline">
            Receipts after this change →
          </Link>
          <span className="text-muted-foreground">Audit record {dataMode === 'api' ? c.id : `${c.id}-${String(c.ts).slice(-6)}`}</span>
        </div>
      </div>
    </li>
  )
}

function TrafficRow({ e, now }: { e: TrafficEvent; now: number }) {
  return (
    <li className="relative flex gap-3 pb-6">
      <When ts={e.ts} now={now} />
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
          {e.to.startsWith('/spend') ? 'Open spend →' : 'View receipts →'}
        </Link>
      </div>
    </li>
  )
}
