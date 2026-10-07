import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { type PolicyContent, type ReplayPart, replayPolicy, type ReplayView, type ReplayWindow } from '@/data/catalog'
import { ago, int } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useApp } from '@/state/app-state'

// §7.5.7 in api mode: the builder's rules replayed over recorded requests
// with Warden's evaluator, as a change from what the policies do now.
// Requests whose route captured content replay exactly; the rest on
// metadata only, and every number says which.

const windows: { value: ReplayWindow; label: string; phrase: string }[] = [
  { value: '1h', label: 'Last hour', phrase: 'the last hour' },
  { value: '24h', label: 'Last 24 hours', phrase: 'the last 24 hours' },
  { value: '7d', label: 'Last 7 days', phrase: 'the last 7 days' },
  { value: '30d', label: 'Last 30 days', phrase: 'the last 30 days' },
]
const phrase = (w: ReplayWindow) => windows.find((x) => x.value === w)!.phrase

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** "Would newly block 2 · stop redacting 3", or "No change." */
export function changeWords(p: ReplayPart): string {
  const parts = (
    [
      [p.newlyBlocked, 'newly block'],
      [p.noLongerBlocked, 'stop blocking'],
      [p.newlyRedacted, 'newly redact'],
      [p.noLongerRedacted, 'stop redacting'],
      [p.newlyRerouted, 'newly reroute'],
      [p.noLongerRerouted, 'stop rerouting'],
    ] as const
  )
    .filter(([n]) => n > 0)
    .map(([n, w]) => `${w} ${int(n)}`)
  if (!parts.length) return p.changed ? `Would change ${int(p.changed)} (which rule decides, not the outcome)` : 'No change.'
  return `Would ${parts.join(' · ')}`
}

/** The replay's headline, part by part, and the requests it would change. */
export function ReplaySummary({ replay, maxAffected = 50 }: { replay: ReplayView; maxAffected?: number }) {
  // Each changed request opens its receipt over the page, keeping the draft and this result.
  const { openReceipt } = useApp()
  if (replay.total === 0) {
    return <p className="text-sm">No requests in {phrase(replay.window)} reached the policies, so there’s nothing to replay. Try a longer window.</p>
  }
  const shown = replay.affected.slice(0, maxAffected)
  return (
    <div className="flex flex-col gap-3 text-sm">
      <p>
        Replayed against {int(replay.total)} {replay.total === 1 ? 'request' : 'requests'} from {phrase(replay.window)}. {int(replay.exact.requests)} had content available;{' '}
        {int(replay.metadata.requests)} evaluated on metadata only.
        {replay.limited && <span className="text-muted-foreground"> The window held more; these are the newest {int(replay.limit)}.</span>}
      </p>
      <section aria-label="Exact" className="rounded-md border border-border p-3">
        <h3 className="text-xs font-medium text-muted-foreground">Exact · {int(replay.exact.requests)} with captured content</h3>
        <p className="mt-1">{replay.exact.requests ? changeWords(replay.exact) : 'No captured content in this window.'}</p>
      </section>
      <section aria-label="Metadata only" className="rounded-md border border-border p-3">
        <h3 className="text-xs font-medium text-muted-foreground">Metadata only · {int(replay.metadata.requests)} without content</h3>
        <p className="mt-1">{replay.metadata.requests ? changeWords(replay.metadata) : 'Every request had content.'}</p>
        {replay.metadata.requests > 0 && replay.skippedRules.length > 0 && (
          <p className="mt-1 text-xs text-muted-foreground">
            Rules that read the prompt didn’t run on these, before or after: <span className="font-mono">{replay.skippedRules.join(', ')}</span>.
          </p>
        )}
      </section>
      {shown.length > 0 && (
        <div>
          <h3 className="mb-1 text-xs font-medium text-muted-foreground">
            {replay.affected.length > shown.length ? `First ${shown.length} of the requests it would change` : 'Requests it would change'}
          </h3>
          <ul className="flex flex-col gap-1">
            {shown.map((a) => (
              <li key={a.id}>
                <button type="button" onClick={() => openReceipt(a.id)} className="flex w-full flex-wrap items-baseline gap-x-2 rounded px-1 py-0.5 text-left hover:bg-muted">
                  <span className="text-xs text-muted-foreground">{ago(a.ts)}</span>
                  <span className="font-mono text-xs">{a.key}</span>
                  <span>
                    {a.from} → {a.to}
                  </span>
                  <span className={cn('rounded border px-1 text-[11px]', a.kind === 'exact' ? 'border-border' : 'border-dashed border-border text-muted-foreground')}>
                    {a.kind === 'exact' ? 'Exact' : 'Metadata only'}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

/** The Replay pane beside the builder. `content` is null while the rules can't be sent (problems to fix). */
export function ReplayPane({ policyId, content, blocked }: { policyId: string | null; content: PolicyContent | null; blocked: string }) {
  const [window, setWindow] = useState<ReplayWindow>('1h')
  const [result, setResult] = useState<{ replay: ReplayView; of: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const key = JSON.stringify(content)
  const why = !policyId ? 'Save the draft to replay it.' : !content ? blocked || 'Fix the rules marked to replay them.' : ''

  const run = async () => {
    if (!policyId || !content) return
    setBusy(true)
    setError('')
    try {
      setResult({ replay: await replayPolicy(policyId, content, window), of: key })
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <aside aria-label="Replay" className="min-w-0 px-5 py-4 lg:col-span-2 xl:col-span-1">
      <h2 className="text-base font-semibold">Replay</h2>
      <p className="mt-1 text-sm text-muted-foreground">Runs these rules over recorded requests, as if enforced, and shows what they’d change from what the policies do now.</p>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Select items={windows.map(({ value, label }) => ({ value, label }))} value={window} onValueChange={(v) => setWindow((v as ReplayWindow) ?? '1h')}>
          <SelectTrigger aria-label="Replay window" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {windows.map((w) => (
              <SelectItem key={w.value} value={w.value}>
                {w.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button size="sm" onClick={() => void run()} disabled={!!why || busy} title={why || undefined} loading={busy} loadingText="Replaying…">
          Replay draft
        </Button>
      </div>
      {why && <p className="mt-1 text-xs text-muted-foreground">{why}</p>}
      <p className="mt-2 text-xs text-muted-foreground">
        Replay reads raw receipts, kept 30 days. It’s exact only for requests on routes that capture content
        {result ? (result.replay.captureRoutes.length ? ` (now: ${result.replay.captureRoutes.join(', ')})` : ' (none do now)') : ''}; the rest replay on metadata only.
      </p>
      {error && <p className="mt-3 text-sm text-destructive-foreground">{error}</p>}
      {result && (
        <div className="mt-4">
          {result.of !== key && <p className="mb-2 text-xs text-warning-foreground">The rules changed since this replay. Replay again to see the change.</p>}
          <ReplaySummary replay={result.replay} />
        </div>
      )}
    </aside>
  )
}
