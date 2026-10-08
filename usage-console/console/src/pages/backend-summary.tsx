import { StateChip } from '@/components/gw/verdict'
import type { Backend } from '@/data/catalog'

// A connected backend at a glance: its provider, health and the models it
// serves. Connect an app lists backends this way, and Settings shows them as
// cards.

/** A backend row's models: the first three, then how many more, so one serving hundreds stays one line. */
export function modelChips(models: string[]): { shown: string[]; more: number } {
  return { shown: models.slice(0, 3), more: Math.max(0, models.length - 3) }
}

export const healthTone = { healthy: 'allowed', degraded: 'degraded', down: 'blocked', idle: 'neutral' } as const

/** One connected backend as a list row: name, then provider and health, then the models it serves. */
export function BackendRow({ b }: { b: Backend }) {
  return (
    <span className="mt-1 flex flex-col gap-1.5">
      <span className="flex flex-wrap items-center gap-2 text-sm">
        <span>{b.provider}</span>
        <HealthChip health={b.health} />
      </span>
      <ModelChips models={b.models} />
    </span>
  )
}

/** A backend's models as chips: three, then how many more. */
export function ModelChips({ models }: { models: string[] }) {
  const { shown, more } = modelChips(models)
  return (
    <span role="list" aria-label="Models" className="flex flex-wrap items-center gap-1">
      {shown.map((m) => (
        <span role="listitem" key={m} className="rounded-sm border border-border bg-muted px-1.5 font-mono text-xs leading-5">
          {m}
        </span>
      ))}
      {more > 0 && <span className="text-xs text-muted-foreground">+{more} more</span>}
    </span>
  )
}

/** A backend's health as a chip. */
export function HealthChip({ health }: { health: string }) {
  return <StateChip tone={healthTone[health as keyof typeof healthTone] ?? 'neutral'}>{health}</StateChip>
}
