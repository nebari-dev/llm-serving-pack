import { ArrowRight, Info } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { VerdictBadge } from '@/components/gw/verdict'
import { Skeleton } from '@/components/ui/skeleton'
import type { Receipt, Verdict } from '@/data/mock'
import { clock, int } from '@/lib/format'
import { cn } from '@/lib/utils'
import { type TimeRange, timeRanges, useApp, useReceipts } from '@/state/app-state'
import { type Draft, type Group, hashDraft } from './guardrails-model'

// §7.5.7 replay: runs the compiled rule against stored receipts with Warden's
// evaluator. Results are always framed as change from current behavior.
// (Mockup: counts are simulated deterministically from the draft.)

const volume: Record<TimeRange, number> = {
  '15m': 1_046,
  '1h': 4_182,
  '6h': 25_094,
  '24h': 98_410,
  '7d': 688_870,
  '30d': 2_952_300,
}

function teamsIn(g: Group): string[] {
  return g.children.flatMap((n) => (n.kind === 'group' ? teamsIn(n) : n.field === 'key.team' && n.op === 'is' ? n.value : []))
}
function excludedTeams(g: Group): string[] {
  return g.children.flatMap((n) => (n.kind === 'group' ? excludedTeams(n) : n.field === 'key.team' && n.op === 'is not' ? n.value : []))
}

export interface ReplayResult {
  total: number
  withContent: number
  deltas: Record<'blocked' | 'redacted' | 'rerouted', number>
  current: Record<Verdict, number>
  affected: { r: Receipt; from: Verdict; to: Verdict }[]
  team: string
}

function simulate(draft: Draft, baseline: Draft | null, range: TimeRange, receipts: Receipt[]): ReplayResult {
  const total = volume[range]
  const withContent = Math.round(total * 0.9328)
  const scale = total / 4_182
  const h = hashDraft(draft)
  const same = baseline && hashDraft(baseline) === h
  const has = (t: string) => draft.then.some((a) => a.type === t)
  const had = (t: string) => !!baseline?.then.some((a) => a.type === t)
  const d = (on: boolean, was: boolean, base: number, spread: number) => {
    if (same) return 0
    if (on && !was) return Math.round((base + (h % spread)) * scale)
    if (!on && was) return -Math.round((base + (h % spread)) * scale)
    return on ? Math.round(((h >> 3) % 7) * scale) : 0
  }
  const deltas = {
    blocked: d(has('block'), had('block'), 2, 3),
    redacted: d(has('redact'), had('redact'), 34, 12),
    rerouted: d(has('reroute'), had('reroute'), 18, 9),
  }
  const current: Record<Verdict, number> = {
    redacted: Math.round(total * 0.0098),
    rerouted: Math.round(total * 0.041),
    blocked: Math.round(total * 0.0005),
    truncated: Math.round(total * 0.0004),
    allowed: 0,
  }
  current.allowed = total - current.redacted - current.rerouted - current.blocked - current.truncated

  const inc = teamsIn(draft.when)
  const exc = excludedTeams(draft.when)
  const team = inc[0] ?? (exc.includes('support') ? 'agents' : 'support')
  const pool = receipts.filter((r) => r.verdict === 'allowed' && !r.inFlight && (inc.length ? inc.includes(r.team) : !exc.includes(r.team)))
  const affected: ReplayResult['affected'] = []
  const take = (n: number, to: Verdict) => {
    for (const r of pool) {
      if (affected.length >= 12 || n <= 0) break
      if (affected.some((a) => a.r.id === r.id)) continue
      affected.push({ r, from: 'allowed', to })
      n--
    }
  }
  take(Math.max(0, deltas.blocked), 'blocked')
  take(Math.min(6, Math.max(0, deltas.redacted)), 'redacted')
  take(Math.min(4, Math.max(0, deltas.rerouted)), 'rerouted')
  return { total, withContent, deltas, current, affected, team }
}

function Change({ n }: { n: number }) {
  if (n === 0) return <span className="text-muted-foreground">no change</span>
  return (
    <span className={cn('font-medium', n > 0 ? 'text-foreground' : 'text-muted-foreground-strong')}>
      {n > 0 ? '+' : '−'}
      {int(Math.abs(n))}
    </span>
  )
}

export function ReplayPane({ draft, baseline, nonce }: { draft: Draft; baseline: Draft | null; nonce: number }) {
  const { range, openReceipt } = useApp()
  const receipts = useReceipts()
  const key = `${hashDraft(draft)}:${range}:${nonce}`
  const [settledKey, setSettledKey] = useState(key)
  const [frozen, setFrozen] = useState(() => simulate(draft, baseline, range, receipts))
  const pending = settledKey !== key

  // Re-run when the rule changes; results replace in place (no layout shift).
  useEffect(() => {
    if (settledKey === key) return
    const t = window.setTimeout(() => {
      setFrozen(simulate(draft, baseline, range, receipts))
      setSettledKey(key)
    }, 650)
    return () => window.clearTimeout(t)
    // receipts intentionally excluded: a replay is a snapshot, not a live view
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  const res = frozen
  const rangeText = useMemo(() => timeRanges.find((t) => t.value === range)?.label.toLowerCase() ?? range, [range])
  const meta = res.total - res.withContent
  const rows: { v: Verdict; delta: number }[] = [
    { v: 'allowed', delta: -(res.deltas.blocked + res.deltas.redacted + res.deltas.rerouted) },
    { v: 'redacted', delta: res.deltas.redacted },
    { v: 'rerouted', delta: res.deltas.rerouted },
    { v: 'blocked', delta: res.deltas.blocked },
  ]
  const headlines: string[] = []
  if (res.deltas.blocked > 0) headlines.push(`Would newly block ${int(res.deltas.blocked)} request${res.deltas.blocked === 1 ? '' : 's'} from ${res.team}`)
  if (res.deltas.redacted > 0) headlines.push(`Would newly redact ${int(res.deltas.redacted)}`)
  if (res.deltas.rerouted > 0) headlines.push(`Would newly reroute ${int(res.deltas.rerouted)}`)
  if (res.deltas.redacted < 0) headlines.push(`Would stop redacting ${int(-res.deltas.redacted)}`)
  if (res.deltas.blocked < 0) headlines.push(`Would stop blocking ${int(-res.deltas.blocked)}`)

  return (
    <div className="flex flex-col gap-4" aria-busy={pending}>
      <div>
        <div className="flex items-baseline justify-between gap-2">
          <h3 className="text-base font-semibold">Replay</h3>
          <span className="text-xs text-muted-foreground">Against: {rangeText}</span>
        </div>
        <p className="mt-1 min-h-10 text-xs text-muted-foreground-strong">
          {pending ? (
            <span className="flex flex-col gap-1.5 pt-0.5">
              <Skeleton className="h-3 w-11/12" />
              <Skeleton className="h-3 w-2/3" />
            </span>
          ) : (
            <>
              Replayed against <span className="num font-mono text-foreground">{int(res.total)}</span> requests.{' '}
              <span className="num font-mono">{int(res.withContent)}</span> had content available;{' '}
              <span className="num font-mono">{int(meta)}</span> evaluated on metadata only.
            </>
          )}
        </p>
      </div>

      <div className="min-h-14 border-y border-border py-3" aria-live="polite">
        {pending ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-4 w-1/2" />
          </div>
        ) : headlines.length ? (
          <ul className="flex flex-col gap-1 text-sm font-medium">
            {headlines.map((h) => (
              <li key={h}>{h}</li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-muted-foreground-strong">No change from current behavior. The published version already does this.</p>
        )}
      </div>

      <table className="w-full text-sm">
        <caption className="sr-only">Verdict counts, current versus with this rule</caption>
        <thead>
          <tr className="border-b border-border text-xs text-muted-foreground">
            <th className="py-1 text-left font-medium">Verdict</th>
            <th className="py-1 text-right font-medium">Now</th>
            <th className="py-1 text-right font-medium">Change</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(({ v, delta }) => (
            <tr key={v} className="h-8 border-b border-border last:border-0">
              <td>
                <VerdictBadge verdict={v} />
              </td>
              <td className="num text-right font-mono text-xs">{pending ? <Skeleton className="ml-auto h-3 w-12" /> : int(res.current[v])}</td>
              <td className="num w-24 text-right font-mono text-xs">{pending ? <Skeleton className="ml-auto h-3 w-10" /> : <Change n={delta} />}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <div>
        <h4 className="mb-1 text-sm font-semibold">Requests this would change</h4>
        <ul className="flex min-h-24 flex-col divide-y divide-border rounded-md border border-border">
          {pending ? (
            Array.from({ length: Math.max(3, Math.min(6, res.affected.length)) }, (_, i) => (
              <li key={i} className="flex h-8 items-center gap-2 px-2">
                <Skeleton className="h-3 w-full" />
              </li>
            ))
          ) : res.affected.length === 0 ? (
            <li className="px-3 py-4 text-center text-xs text-muted-foreground">No recorded requests would change.</li>
          ) : (
            res.affected.slice(0, 6).map(({ r, from, to }) => (
              <li key={r.id}>
                <button
                  type="button"
                  onClick={() => openReceipt(r.id)}
                  className="flex h-8 w-full items-center gap-2 px-2 text-left text-xs hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none focus-visible:ring-inset"
                >
                  <span className="num font-mono text-muted-foreground">{clock(r.ts)}</span>
                  <span className="truncate font-mono">{r.keyName}</span>
                  <span className="ml-auto flex items-center gap-1">
                    <VerdictBadge verdict={from} compact />
                    <ArrowRight className="size-3 text-muted-foreground" aria-label="would become" />
                    <VerdictBadge verdict={to} compact />
                  </span>
                  <span className="text-muted-foreground underline-offset-4">View</span>
                </button>
              </li>
            ))
          )}
        </ul>
        {!pending && res.affected.length > 6 && <p className="mt-1 text-xs text-muted-foreground">and {res.affected.length - 6} more in the receipt list</p>}
      </div>

      <p className="flex gap-2 text-xs text-muted-foreground">
        <Info className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
        <span>
          Replay reads raw receipts, so it can only look back over the hot window (30 days). Runs Warden's own evaluator, not an approximation. Metadata-only receipts can't be
          checked for content conditions, so content-based counts are a lower bound.
        </span>
      </p>
    </div>
  )
}
