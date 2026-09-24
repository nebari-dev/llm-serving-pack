import { Check, CircleSlash, TriangleAlert, X } from 'lucide-react'
import type { TraceStep } from '@/data/mock'
import { cn } from '@/lib/utils'

// §7.5.4 / §8 DecisionTrace — "the most important component in the product".
// An ordered vertical sequence: each step shows input, outcome, and its
// latency contribution, with a proportional bar so the slow step is obvious.

const stateMeta = {
  ok: { Icon: Check, dot: 'bg-v-allowed-bg text-v-allowed-fg border-v-allowed-border', label: 'OK' },
  warn: { Icon: TriangleAlert, dot: 'bg-v-degraded-bg text-v-degraded-fg border-v-degraded-border', label: 'Attention' },
  fail: { Icon: X, dot: 'bg-v-blocked-bg text-v-blocked-fg border-v-blocked-border', label: 'Stopped here' },
  skip: { Icon: CircleSlash, dot: 'bg-muted text-muted-foreground border-border', label: 'Not reached' },
} as const

export function DecisionTrace({ steps, totalMs }: { steps: TraceStep[]; totalMs: number }) {
  const max = Math.max(totalMs, 1)
  return (
    <ol className="relative flex flex-col" aria-label="Decision trace">
      {steps.map((s, i) => {
        const { Icon, dot, label } = stateMeta[s.state]
        const last = i === steps.length - 1
        return (
          <li key={s.step} className={cn('relative grid grid-cols-[1.5rem_1fr_auto] gap-x-3 pb-4', last && 'pb-0')}>
            {!last && <span className="absolute top-6 bottom-0 left-[0.6875rem] w-px bg-border" aria-hidden="true" />}
            <span className={cn('z-[1] mt-0.5 flex size-6 items-center justify-center rounded-full border', dot)}>
              <Icon className="size-3.5" aria-hidden="true" />
              <span className="sr-only">{label}</span>
            </span>
            <div className={cn('min-w-0', s.state === 'skip' && 'text-muted-foreground')}>
              <div className="text-sm font-medium">{s.step}</div>
              <dl className="mt-0.5 grid grid-cols-[4.5rem_1fr] gap-x-2 text-xs">
                <dt className="text-muted-foreground">Input</dt>
                <dd className="truncate font-mono" title={s.input}>
                  {s.input}
                </dd>
                <dt className="text-muted-foreground">Outcome</dt>
                <dd
                  className={cn(
                    'font-mono break-words',
                    s.state === 'fail' && 'text-v-blocked-fg',
                    s.state === 'warn' && 'text-v-degraded-fg',
                  )}
                >
                  {s.outcome}
                </dd>
              </dl>
            </div>
            <div className="flex w-28 flex-col items-end gap-1 pt-0.5">
              <span className="num font-mono text-xs">{s.state === 'skip' ? '—' : s.ms < 10 ? `${s.ms.toFixed(1)}ms` : `${Math.round(s.ms)}ms`}</span>
              <span className="h-1 w-full rounded-full bg-muted" aria-hidden="true">
                <span
                  className="block h-full rounded-full bg-foreground/50"
                  style={{ width: `${Math.max(s.state === 'skip' ? 0 : 2, (s.ms / max) * 100)}%` }}
                />
              </span>
            </div>
          </li>
        )
      })}
    </ol>
  )
}
