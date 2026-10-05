import { CircleCheck, CircleDashed, CircleX, GitBranch, Link2, MonitorCog, TriangleAlert } from 'lucide-react'
import type { Provenance, SyncState } from '@/data/catalog'
import { cn } from '@/lib/utils'
import { StateChip } from './verdict'

// §7.1 principle 3: provenance is rendered before interaction, with the exit
// route attached. §8 ProvenanceBadge + SyncStateIndicator.

export function ProvenanceBadge({ provenance, source, className }: { provenance: Provenance; source?: string; className?: string }) {
  if (provenance === 'git') {
    return (
      <a
        href={source ? `https://${source}` : undefined}
        target="_blank"
        rel="noreferrer"
        onClick={(e) => e.stopPropagation()}
        className={cn(
          'inline-flex h-5 items-center gap-1 rounded-full border border-border-strong bg-canvas px-2 text-xs font-medium text-foreground hover:underline',
          className,
        )}
        title={source ? `Managed in Git — ${source}` : 'Managed in Git'}
      >
        <GitBranch className="size-3" aria-hidden="true" />
        Git
        <span aria-hidden="true">↗</span>
      </a>
    )
  }
  return (
    <span
      className={cn(
        'inline-flex h-5 items-center gap-1 rounded-full border border-border bg-muted px-2 text-xs font-medium text-muted-foreground-strong',
        className,
      )}
      title={provenance === 'adopted' ? 'Adopted from Git into the console' : 'Owned by the console'}
    >
      {provenance === 'adopted' ? <Link2 className="size-3" aria-hidden="true" /> : <MonitorCog className="size-3" aria-hidden="true" />}
      {provenance === 'adopted' ? 'Adopted' : 'Console'}
    </span>
  )
}

const syncMeta: Record<SyncState, { label: string; tone: 'allowed' | 'neutral' | 'blocked' | 'degraded'; Icon: typeof CircleCheck }> = {
  synced: { label: 'Synced', tone: 'allowed', Icon: CircleCheck },
  applying: { label: 'Applying…', tone: 'neutral', Icon: CircleDashed },
  failed: { label: 'Reconcile failed', tone: 'blocked', Icon: CircleX },
  drift: { label: 'Drift detected', tone: 'degraded', Icon: TriangleAlert },
  not_reconciled: { label: 'No reconciler', tone: 'neutral', Icon: CircleDashed },
  pending: { label: 'Pending apply', tone: 'neutral', Icon: CircleDashed },
  no_endpoint: { label: 'No endpoint', tone: 'neutral', Icon: CircleDashed },
}

const syncTitle: Partial<Record<SyncState, string>> = {
  not_reconciled: 'There’s no gateway to apply to: nothing applies this to Agent Router or reports its state back.',
  pending: 'Saved, but the gateway doesn’t run it yet. Apply to put it in front of the gateway.',
  no_endpoint: 'The control plane has no endpoint for it, so the gateway can’t reach it and no route can target it.',
}

export function SyncStateIndicator({ state, className }: { state: SyncState; className?: string }) {
  const { label, tone, Icon } = syncMeta[state]
  // "Synced" is the absence of information (§7.5.2): render it quietly.
  if (state === 'synced') {
    return (
      <span className={cn('inline-flex items-center gap-1 text-xs text-muted-foreground', className)}>
        <Icon className="size-3.5 text-v-allowed-fg" aria-hidden="true" />
        {label}
      </span>
    )
  }
  return (
    <StateChip
      tone={tone}
      title={syncTitle[state]}
      className={cn((state === 'applying' || state === 'pending') && 'border-dashed', className)}
      icon={<Icon className={cn('size-3', state === 'applying' && 'motion-safe:animate-spin motion-safe:[animation-duration:var(--duration-loading)]')} aria-hidden="true" />}
    >
      {label}
    </StateChip>
  )
}
