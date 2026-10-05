import { Download, Pencil, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { Money } from '@/components/gw/numbers'
import { Section } from '@/components/gw/page'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { toast } from '@/components/ui/toast'
import {
  api,
  ApiError,
  cancelPairPrice,
  decidePriceProposal,
  type PairPrice,
  type PriceChange,
  type PriceSource,
  type PricingView,
  type RateName,
  setPairPrice,
  setPriceSource,
  syncPrices,
} from '@/data/catalog'
import { ago } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useLive } from '@/state/live'

// Models → Pricing in api mode (decisions §1). Prices are per (model,
// backend). LiteLLM's price file applies automatically through a daily sync;
// any rate can be overridden on its own, and a LiteLLM move on an overridden
// rate waits here as a proposal. A pair with no price shows "No price",
// never $0, and its receipts are costed once it gets one.

const th = 'px-3 py-2 font-medium'
const td = 'px-3 py-2'

export const rateNames: { key: RateName; label: string }[] = [
  { key: 'input', label: 'Input' },
  { key: 'cachedInput', label: 'Cached input' },
  { key: 'cacheWrite', label: 'Cache write' },
  { key: 'output', label: 'Output' },
  { key: 'reasoning', label: 'Reasoning' },
]
const rateLabel = Object.fromEntries(rateNames.map((r) => [r.key, r.label])) as Record<RateName, string>

const sourceLabel: Record<PriceSource, string> = { seed: 'Seed', litellm: 'LiteLLM', manual: 'Override' }

function SourceTag({ source }: { source?: PriceSource }) {
  if (!source) return null
  return (
    <span className={cn('ml-1 text-[11px]', source === 'manual' ? 'font-medium text-foreground' : 'text-muted-foreground')} title={source === 'manual' ? 'Set by hand: the sync proposes LiteLLM changes instead of applying them.' : undefined}>
      {sourceLabel[source]}
    </span>
  )
}

const fail = (e: unknown) => (e instanceof ApiError ? e.message : 'Something went wrong. Try again.')

export function PricingLive() {
  const { data: pricing, loaded, reload } = useLive<PricingView | null>('/pricing', null, 60_000)
  const [editing, setEditing] = useState<PairPrice | null>(null)
  const [syncing, setSyncing] = useState(false)
  const [busy, setBusy] = useState<string | null>(null)

  const syncNow = async () => {
    setSyncing(true)
    try {
      const v = await syncPrices()
      toast.add({ title: 'Synced from LiteLLM', description: `${v.sync.applied} applied, ${v.sync.proposed} proposed, ${v.sync.retired} retired.`, type: 'success' })
    } catch (e) {
      toast.add({ title: 'LiteLLM sync failed', description: fail(e), type: 'error' })
    } finally {
      setSyncing(false)
      reload()
    }
  }

  const act = async (id: string, run: () => Promise<unknown>, done: string) => {
    setBusy(id)
    try {
      await run()
      toast.add({ title: done, type: 'success' })
    } catch (e) {
      toast.add({ title: 'Not saved', description: fail(e), type: 'error' })
    } finally {
      setBusy(null)
      reload()
    }
  }

  const sync = pricing?.sync
  const prices = pricing?.prices ?? []
  const changes = pricing?.changes ?? []
  const proposals = pricing?.proposals ?? []

  return (
    <>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-border px-6 py-3 text-sm">
        <span className="font-medium">LiteLLM sync</span>
        <span className="text-muted-foreground">
          {!sync
            ? 'Loading…'
            : sync.lastOkAt
              ? `Last synced ${ago(sync.lastOkAt)} · next ${new Date(sync.nextRunAt).toLocaleString('en-US', { dateStyle: 'medium', timeStyle: 'short' })}`
              : 'Never synced'}
          {sync?.lastRunAt ? ` · last run: ${sync.applied} applied, ${sync.proposed} proposed, ${sync.retired} retired` : ''}
        </span>
        {sync && (
          <a href={sync.source} target="_blank" rel="noreferrer" className="text-xs text-muted-foreground underline underline-offset-4">
            Source file
          </a>
        )}
        <Button variant="outline" size="sm" className="ml-auto" onClick={syncNow} disabled={syncing}>
          <RefreshCw className={cn(syncing && 'animate-spin')} /> Sync now
        </Button>
      </div>
      {sync?.error && (
        <Alert variant="destructive" className="mx-6 mt-3 w-auto">
          <AlertTitle>The last sync failed</AlertTitle>
          <AlertDescription>
            {sync.error} Prices stay as they were; it retries within the hour.
          </AlertDescription>
        </Alert>
      )}

      {proposals.length > 0 && (
        <Section title="LiteLLM changes waiting on you" description="LiteLLM's price moved on a rate someone overrode, so the sync didn't apply it. Accept to follow LiteLLM again from now; dismiss to keep the override.">
          <ul className="divide-y divide-border text-sm">
            {proposals.map((p) => (
              <li key={p.id} className="flex flex-wrap items-center gap-3 py-2">
                <span className="font-mono text-xs">
                  {p.model} on {p.backend}
                </span>
                <span>{rateLabel[p.rate]}</span>
                <span className="text-muted-foreground">
                  override <Money value={p.current} precision="micro" />, LiteLLM now <Money value={p.proposed} precision="micro" /> / 1M
                </span>
                <span className="ml-auto flex gap-2">
                  <Button size="sm" variant="outline" disabled={busy !== null} onClick={() => act(`d${p.id}`, () => decidePriceProposal(p.id, 'dismiss'), 'Kept the override')}>
                    Dismiss
                  </Button>
                  <Button size="sm" disabled={busy !== null} onClick={() => act(`a${p.id}`, () => decidePriceProposal(p.id, 'accept'), 'Following LiteLLM again')}>
                    Accept
                  </Button>
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}

      <div className="overflow-x-auto">
        <table className="w-full min-w-[76rem] text-sm">
          <caption className="sr-only">Prices per model and backend, per million tokens</caption>
          <thead className="bg-header text-xs text-muted-foreground-strong">
            <tr className="border-b border-border">
              <th className={cn(th, 'pl-6 text-left')}>Model</th>
              <th className={cn(th, 'text-left')}>Backend</th>
              {rateNames.map((r) => (
                <th key={r.key} className={cn(th, 'text-right')}>
                  {r.label}
                </th>
              ))}
              <th className={cn(th, 'text-left')}>Effective from</th>
              <th className={cn(th, 'text-left')}>LiteLLM entry</th>
              <th className={cn(th, 'pr-6')}>
                <span className="sr-only">Edit</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {prices.map((p) => (
              <tr key={p.model + '@' + p.backend} className="border-b border-border hover:bg-muted/50">
                <td className={cn(td, 'pl-6 font-mono font-medium whitespace-nowrap')}>{p.model}</td>
                <td className={cn(td, 'font-mono text-xs whitespace-nowrap')}>{p.backend}</td>
                {rateNames.map((r) => {
                  const rate = p.rates[r.key]
                  return (
                    <td key={r.key} className={cn(td, 'text-right whitespace-nowrap')}>
                      <Money value={rate ? rate.perM : null} precision="micro" />
                      <SourceTag source={rate?.source} />
                    </td>
                  )
                })}
                <td className={cn(td, 'num font-mono text-xs whitespace-nowrap')}>{p.effectiveFrom ?? '—'}</td>
                <td className={cn(td, 'font-mono text-xs')}>{p.litellmKey ?? <span className="font-sans text-muted-foreground">None</span>}</td>
                <td className={cn(td, 'pr-6 text-right')}>
                  <Button variant="ghost" size="sm" onClick={() => setEditing(p)} aria-label={`Edit price for ${p.model} on ${p.backend}`}>
                    <Pencil /> Edit
                  </Button>
                </td>
              </tr>
            ))}
            {loaded && prices.length === 0 && (
              <tr>
                <td colSpan={10} className="px-6 py-6 text-center text-sm text-muted-foreground">
                  No backend serves a model yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
        <p className="px-6 py-3 text-xs text-muted-foreground">
          Rates are per 1M tokens. Where LiteLLM has no separate cached, cache-write or reasoning rate, those tokens bill at its input or output rate. Requests on a pair with no price
          have no cost until it gets one; then they're costed at that price.
        </p>
      </div>

      <Section
        title="Price changes"
        description="Every receipt snapshots the price row in effect when it was written, so later changes never reprice history."
        actions={
          <Button variant="outline" size="sm" disabled title="CSV export isn't connected yet.">
            <Download /> Export CSV
          </Button>
        }
      >
        <ChangesTable changes={changes} loaded={loaded} busy={busy} onCancel={(c) => act(`c${c.effectiveAt}`, () => cancelPairPrice(c), 'Scheduled change cancelled')} />
      </Section>

      {editing && (
        <PriceDialog
          pair={editing}
          onClose={(saved) => {
            setEditing(null)
            if (saved) reload()
          }}
        />
      )}
    </>
  )
}

function ChangesTable({ changes, loaded, busy, onCancel }: { changes: PriceChange[]; loaded: boolean; busy: string | null; onCancel: (c: PriceChange) => void }) {
  // One cancel per scheduled row, not per rate it changes.
  const cancelShown = new Set<string>()
  return (
    <table className="w-full text-sm">
      <thead className="text-left text-xs text-muted-foreground">
        <tr className="border-b border-border">
          <th className="py-1.5 pr-3 font-medium">Effective</th>
          <th className="py-1.5 pr-3 font-medium">Model</th>
          <th className="py-1.5 pr-3 font-medium">Backend</th>
          <th className="py-1.5 pr-3 font-medium">Rate</th>
          <th className="py-1.5 pr-3 text-right font-medium">Was</th>
          <th className="py-1.5 pr-3 text-right font-medium">Now</th>
          <th className="py-1.5 pr-3 font-medium">Source</th>
          <th className="py-1.5">
            <span className="sr-only">Cancel</span>
          </th>
        </tr>
      </thead>
      <tbody>
        {changes.map((c) => {
          const row = `${c.model}@${c.backend}@${c.effectiveAt}`
          const showCancel = c.scheduled && !cancelShown.has(row)
          if (showCancel) cancelShown.add(row)
          return (
            <tr key={row + c.field} className="border-b border-border last:border-0">
              <td className="num py-2 pr-3 font-mono text-xs">
                {c.effective}
                {c.scheduled && (
                  <Badge variant="outline" className="ml-2">
                    Scheduled
                  </Badge>
                )}
              </td>
              <td className="py-2 pr-3 font-mono text-xs">{c.model}</td>
              <td className="py-2 pr-3 font-mono text-xs">{c.backend}</td>
              <td className="py-2 pr-3 text-xs">{c.field} / 1M</td>
              <td className="py-2 pr-3 text-right text-muted-foreground">
                <Money value={c.from} precision="micro" />
              </td>
              <td className="py-2 pr-3 text-right">
                <Money value={c.to} precision="micro" />
              </td>
              <td className="py-2 pr-3 text-xs text-muted-foreground">{c.source ? sourceLabel[c.source] : 'Ended'}</td>
              <td className="py-2 text-right">
                {showCancel && (
                  <Button variant="ghost" size="sm" disabled={busy !== null} onClick={() => onCancel(c)}>
                    Cancel
                  </Button>
                )}
              </td>
            </tr>
          )
        })}
        {loaded && changes.length === 0 && (
          <tr>
            <td colSpan={8} className="py-4 text-center text-sm text-muted-foreground">
              No price changes yet.
            </td>
          </tr>
        )}
      </tbody>
    </table>
  )
}

type RateDraft = { mode: 'litellm' | 'manual' | 'none'; value: string }

function initialDraft(p: PairPrice): Record<RateName, RateDraft> {
  return Object.fromEntries(
    rateNames.map(({ key }) => {
      const r = p.rates[key]
      const mode = r?.source === 'manual' ? 'manual' : r ? 'litellm' : p.litellm?.[key] !== undefined ? 'litellm' : 'none'
      return [key, { mode, value: r ? String(r.perM) : '' }]
    }),
  ) as Record<RateName, RateDraft>
}

/** Edit one pair: per rate, follow LiteLLM or override it; when; and which LiteLLM entry. */
function PriceDialog({ pair, onClose }: { pair: PairPrice; onClose: (saved: boolean) => void }) {
  const [draft, setDraft] = useState(() => initialDraft(pair))
  const [key, setKey] = useState(pair.litellmKey ?? '')
  const [when, setWhen] = useState<'now' | 'later'>('now')
  const [at, setAt] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const start = initialDraft(pair)

  const set = (k: RateName, d: Partial<RateDraft>) => setDraft((x) => ({ ...x, [k]: { ...x[k], ...d } }))

  // Only what changed is sent: a rate left alone keeps its source.
  const edits: Partial<Record<RateName, number | null>> = {}
  let invalid: string | null = null
  for (const { key: k, label } of rateNames) {
    const d = draft[k]
    const was = start[k]
    if (d.mode === 'manual') {
      const v = Number(d.value)
      if (d.value.trim() === '' || !Number.isFinite(v) || v < 0) invalid = `${label}: enter a rate of 0 or more`
      else if (was.mode !== 'manual' || Number(was.value) !== v) edits[k] = v
    } else if (d.mode === 'litellm' && was.mode !== 'litellm') edits[k] = null
  }
  const keyChanged = key.trim() !== (pair.litellmKey ?? '')
  const unchanged = Object.keys(edits).length === 0 && !keyChanged

  const save = async () => {
    setError(null)
    if (invalid) return setError(invalid)
    if (when === 'later' && !at) return setError('Pick when the price takes effect.')
    setSaving(true)
    try {
      if (keyChanged) await setPriceSource(pair, key.trim())
      if (Object.keys(edits).length) {
        // A source change moves the etag; the sync it runs may have changed rates too.
        const fresh = keyChanged ? await api<PricingView>('/pricing') : null
        const target = fresh?.prices.find((p) => p.model === pair.model && p.backend === pair.backend) ?? pair
        await setPairPrice(target, edits, when === 'later' ? new Date(at).toISOString() : undefined)
      }
      toast.add({ title: `Saved ${pair.model} on ${pair.backend}`, type: 'success' })
      onClose(true)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) setError('This price changed since you opened it, maybe by the LiteLLM sync. Close and reopen to see the current rates.')
      else setError(fail(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose(false)}>
      <DialogContent className="flex max-h-[90vh] flex-col sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            Price for <span className="font-mono">{pair.model}</span> on <span className="font-mono">{pair.backend}</span>
          </DialogTitle>
          <DialogDescription>
            Each rate follows LiteLLM unless you override it. An override stays until you put it back; when LiteLLM moves, you're asked. The catalog is shared, so this reprices every tenant's traffic on this backend.
          </DialogDescription>
        </DialogHeader>
        <div className="-mx-6 flex-1 space-y-4 overflow-y-auto px-6">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr className="border-b border-border">
                <th className="py-1.5 pr-3 font-medium">Rate / 1M</th>
                <th className="py-1.5 pr-3 font-medium">LiteLLM</th>
                <th className="py-1.5 pr-3 font-medium">Use</th>
                <th className="py-1.5 font-medium">Override</th>
              </tr>
            </thead>
            <tbody>
              {rateNames.map(({ key: k, label }) => {
                const lite = pair.litellm?.[k]
                const d = draft[k]
                return (
                  <tr key={k} className="border-b border-border last:border-0">
                    <td className="py-2 pr-3">{label}</td>
                    <td className="py-2 pr-3">
                      <Money value={lite ?? null} precision="micro" />
                    </td>
                    <td className="py-2 pr-3">
                      <select
                        aria-label={`${label}: source`}
                        className="h-8 rounded-md border border-input bg-background px-2 text-sm"
                        value={d.mode}
                        onChange={(e) => set(k, { mode: e.target.value as RateDraft['mode'] })}
                      >
                        <option value="litellm" disabled={lite === undefined}>
                          Follow LiteLLM
                        </option>
                        <option value="manual">Override</option>
                        {start[k].mode === 'none' && <option value="none">No price</option>}
                      </select>
                    </td>
                    <td className="py-2">
                      <Input
                        aria-label={`${label}: override per 1M`}
                        inputMode="decimal"
                        className="h-8 w-28 font-mono"
                        disabled={d.mode !== 'manual'}
                        value={d.mode === 'manual' ? d.value : ''}
                        placeholder={d.mode === 'manual' ? '0.00' : ''}
                        onChange={(e) => set(k, { value: e.target.value })}
                      />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
          {!pair.priced && (
            <p className="text-xs text-muted-foreground">
              This pair has no full price. Requests that use a rate it lacks have no cost; once those rates are set, they're costed at them.
            </p>
          )}
          <Field>
            <FieldLabel htmlFor="litellm-key">LiteLLM entry</FieldLabel>
            <Input id="litellm-key" className="font-mono" value={key} placeholder="none" onChange={(e) => setKey(e.target.value)} />
            <FieldDescription>
              The key in LiteLLM's price file for this model on this backend, like <code className="font-mono">eu.anthropic.claude-sonnet-5</code>. Empty for none. Saving checks the file
              and syncs at once.
            </FieldDescription>
          </Field>
          <Field>
            <FieldLabel>Takes effect</FieldLabel>
            <div className="flex flex-wrap items-center gap-3 text-sm">
              <label className="flex items-center gap-1.5">
                <input type="radio" name="when" checked={when === 'now'} onChange={() => setWhen('now')} /> Now
              </label>
              <label className="flex items-center gap-1.5">
                <input type="radio" name="when" checked={when === 'later'} onChange={() => setWhen('later')} /> Later
              </label>
              {when === 'later' && <Input aria-label="Takes effect at" type="datetime-local" className="h-8 w-56" value={at} onChange={(e) => setAt(e.target.value)} />}
            </div>
            <FieldDescription>Receipts already written keep the price they were costed with.</FieldDescription>
          </Field>
          {error && <FieldError>{error}</FieldError>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onClose(false)}>
            Cancel
          </Button>
          <Button onClick={save} disabled={saving || unchanged}>
            {saving ? 'Saving…' : 'Save price'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
