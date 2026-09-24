import { cn } from '@/lib/utils'
import { microMoney, money, tokens } from '@/lib/format'

// §8 Money / TokenCount / Duration: tabular numerals, monospace, consistent
// precision within a column. Unknown (in-flight) values render as a reserved
// placeholder so rows never change width when they settle (§7.5.3, §13).

type NumProps = { className?: string; unknown?: boolean }

const base = 'num font-mono whitespace-nowrap'

function Unknown({ className, label }: { className?: string; label: string }) {
  return (
    <span className={cn(base, 'text-muted-foreground', className)} aria-label={label}>
      —
    </span>
  )
}

export function Money({
  value,
  precision = 'cents',
  className,
  unknown,
}: NumProps & { value: number; precision?: 'cents' | 'micro' | 'whole' }) {
  if (unknown) return <Unknown className={className} label="cost pending" />
  const text = precision === 'micro' ? microMoney(value) : money(value, precision === 'whole' ? 0 : 2)
  return <span className={cn(base, className)}>{text}</span>
}

export function TokenCount({ value, className, unknown, exact }: NumProps & { value: number; exact?: boolean }) {
  if (unknown) return <Unknown className={className} label="tokens pending" />
  return (
    <span className={cn(base, className)} title={`${value.toLocaleString()} tokens`}>
      {exact ? value.toLocaleString('en-US') : tokens(value)}
    </span>
  )
}

export function Duration({ ms, className, unknown }: NumProps & { ms: number }) {
  if (unknown) return <Unknown className={className} label="duration pending" />
  const text = ms >= 10_000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms).toLocaleString('en-US')}ms`
  return <span className={cn(base, className)}>{text}</span>
}

export function Mono({ children, className }: { children: React.ReactNode; className?: string }) {
  return <span className={cn('font-mono text-[0.8125rem]', className)}>{children}</span>
}

/** Trend delta, e.g. "+12.4%". `goodWhen` decides the tone; color is never the only signal. */
export function Delta({ pct, goodWhen = 'down', className }: { pct: number; goodWhen?: 'up' | 'down'; className?: string }) {
  const up = pct >= 0
  const good = goodWhen === 'up' ? up : !up
  return (
    <span className={cn(base, 'text-xs', good ? 'text-v-allowed-fg' : 'text-v-blocked-fg', className)}>
      {up ? '▲' : '▼'} {Math.abs(pct).toFixed(1)}%
    </span>
  )
}
