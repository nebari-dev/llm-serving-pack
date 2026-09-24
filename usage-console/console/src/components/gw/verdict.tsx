import { Ban, Check, EyeOff, Scissors, Shuffle, TriangleAlert } from 'lucide-react'
import type { ComponentProps } from 'react'
import type { Verdict } from '@/data/mock'
import { cn } from '@/lib/utils'

// §7.7: verdict is never conveyed by color alone — every verdict carries a
// glyph and a text label. Truncated shares the degraded hue but has its own
// glyph and label so it never reads as "blocked" (§4.5 streaming).

export type Tone = 'allowed' | 'redacted' | 'rerouted' | 'blocked' | 'degraded' | 'neutral'

export const verdictMeta: Record<Verdict, { label: string; tone: Tone; Icon: typeof Check }> = {
  allowed: { label: 'Allowed', tone: 'allowed', Icon: Check },
  redacted: { label: 'Redacted', tone: 'redacted', Icon: EyeOff },
  rerouted: { label: 'Rerouted', tone: 'rerouted', Icon: Shuffle },
  blocked: { label: 'Blocked', tone: 'blocked', Icon: Ban },
  truncated: { label: 'Truncated', tone: 'degraded', Icon: Scissors },
}

export const toneText: Record<Tone, string> = {
  allowed: 'text-v-allowed-fg',
  redacted: 'text-v-redacted-fg',
  rerouted: 'text-v-rerouted-fg',
  blocked: 'text-v-blocked-fg',
  degraded: 'text-v-degraded-fg',
  neutral: 'text-muted-foreground',
}

export const toneChip: Record<Tone, string> = {
  allowed: 'bg-v-allowed-bg text-v-allowed-fg border-v-allowed-border',
  redacted: 'bg-v-redacted-bg text-v-redacted-fg border-v-redacted-border',
  rerouted: 'bg-v-rerouted-bg text-v-rerouted-fg border-v-rerouted-border',
  blocked: 'bg-v-blocked-bg text-v-blocked-fg border-v-blocked-border',
  degraded: 'bg-v-degraded-bg text-v-degraded-fg border-v-degraded-border',
  neutral: 'bg-muted text-muted-foreground-strong border-border',
}

export const toneBar: Record<Tone, string> = {
  allowed: 'bg-v-allowed-bar',
  redacted: 'bg-v-redacted-bar',
  rerouted: 'bg-v-rerouted-bar',
  blocked: 'bg-v-blocked-bar',
  degraded: 'bg-v-degraded-bar',
  neutral: 'bg-border-strong',
}

export const toneFill: Record<Tone, string> = {
  allowed: 'var(--v-allowed-bar)',
  redacted: 'var(--v-redacted-bar)',
  rerouted: 'var(--v-rerouted-bar)',
  blocked: 'var(--v-blocked-bar)',
  degraded: 'var(--v-degraded-bar)',
  neutral: 'var(--border-strong)',
}

/** Chip with glyph + label. Use `compact` inside dense tables (glyph only, label for AT). */
export function VerdictBadge({
  verdict,
  compact = false,
  className,
}: {
  verdict: Verdict
  compact?: boolean
  className?: string
}) {
  const { label, tone, Icon } = verdictMeta[verdict]
  return (
    <span
      className={cn(
        'inline-flex h-5 shrink-0 items-center gap-1 rounded-full border text-xs font-medium',
        compact ? 'w-5 justify-center' : 'px-2',
        toneChip[tone],
        className,
      )}
      title={compact ? label : undefined}
    >
      <Icon className="size-3" aria-hidden="true" />
      {compact ? <span className="sr-only">{label}</span> : label}
    </span>
  )
}

/** Generic state chip in one of the five state tones (or neutral). */
export function StateChip({
  tone,
  children,
  icon,
  className,
  ...props
}: ComponentProps<'span'> & { tone: Tone; icon?: React.ReactNode }) {
  return (
    <span
      className={cn(
        'inline-flex h-5 shrink-0 items-center gap-1 whitespace-nowrap rounded-full border px-2 text-xs font-medium',
        toneChip[tone],
        className,
      )}
      {...props}
    >
      {icon}
      {children}
    </span>
  )
}

export function DegradedIcon({ className }: { className?: string }) {
  return <TriangleAlert className={cn('size-4 text-v-degraded-fg', className)} aria-hidden="true" />
}
