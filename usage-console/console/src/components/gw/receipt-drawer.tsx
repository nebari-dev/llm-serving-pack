import { ArrowRight, Copy, Download, Link2, Printer, ShieldAlert } from 'lucide-react'
import { Link, useLocation } from 'react-router-dom'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Drawer, DrawerBody, DrawerContent, DrawerDescription, DrawerFooter, DrawerHeader, DrawerTitle } from '@/components/ui/drawer'
import { toast } from '@/components/ui/toast'
import { dataMode, type Receipt } from '@/data/catalog'
import { ago, clock } from '@/lib/format'
import { cn } from '@/lib/utils'
import { receiptStream, useApp, useReceipts } from '@/state/app-state'
import { useLive } from '@/state/live'
import { Duration, Money, TokenCount } from './numbers'
import { failedAfterAllowed, VerdictBadge } from './verdict'

// Request receipt. A drawer, deep-linkable (?receipt=id), printable and
// exportable as JSON.

// The alert at the top says what stopped the request.
function failure(r: Receipt) {
  return { title: r.verdict === 'blocked' ? 'This request was blocked.' : 'This request failed.', refused: r.verdict === 'blocked' }
}

const api = dataMode === 'api'

/** Downloads the receipt as JSON, as the console received it. */
function exportJson(r: Receipt) {
  const body = JSON.stringify(r, null, 2)
  const url = URL.createObjectURL(new Blob([body + '\n'], { type: 'application/json' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `receipt-${r.id}.json`
  a.click()
  URL.revokeObjectURL(url)
}

function Sub({ title, children, className }: { title: string; children: React.ReactNode; className?: string }) {
  return (
    <section className={cn('border-b border-border px-5 py-4 last:border-b-0', className)}>
      <h3 className="mb-3 text-base leading-5 font-semibold">{title}</h3>
      {children}
    </section>
  )
}

function KV({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{k}</dt>
      <dd className="min-w-0 truncate">{children}</dd>
    </>
  )
}

export function ReceiptDrawer() {
  const { receiptId, closeReceipt } = useApp()
  useReceipts() // re-render when an in-flight receipt settles
  const r = receiptId ? receiptStream.byId.get(receiptId) : undefined
  return (
    <Drawer open={!!receiptId} onOpenChange={(o) => !o && closeReceipt()} side="right">
      <DrawerContent data-print-receipt style={{ ['--drawer-content-width' as string]: 'min(46rem, 100vw)' }}>
        {r ? <ReceiptBody r={r} /> : <NotFound id={receiptId} />}
      </DrawerContent>
    </Drawer>
  )
}

function NotFound({ id }: { id: string | null }) {
  return (
    <>
      <DrawerHeader>
        <DrawerTitle>Receipt not found</DrawerTitle>
      </DrawerHeader>
      <DrawerBody>
        <p className="text-sm text-muted-foreground">
          No receipt <span className="font-mono">{id}</span> in the hot window (30 days). Older receipts keep aggregates only.
        </p>
      </DrawerBody>
    </>
  )
}

function ReceiptBody({ r }: { r: Receipt }) {
  const { pathname } = useLocation()
  const { sameKey, sameSession } = useRelated(r)

  const copy = (text: string, what: string) => {
    void navigator.clipboard?.writeText(text)
    toast.add({ title: `${what} copied`, type: 'success' })
  }

  const cacheWrites = r.cacheWriteTokens ?? 0
  const inputBilled = Math.max(r.inputTokens - r.cachedInputTokens - cacheWrites, 0)
  // Output tokens include reasoning (as OpenAI and Agent Router count them).
  const lines = [
    { label: 'Input', tok: inputBilled },
    { label: 'Cached input', tok: r.cachedInputTokens },
    ...(cacheWrites > 0 ? [{ label: 'Cache write', tok: cacheWrites }] : []),
    { label: 'Output', tok: Math.max(r.outputTokens - r.reasoningTokens, 0) },
    { label: 'Reasoning', tok: r.reasoningTokens },
  ]
  // Refused before the upstream call: blocked, or throttled for now.
  const blocked = r.verdict === 'blocked' || r.verdict === 'throttled'
  const unpriced = r.costUsd === null && !r.inFlight

  return (
    <>
      {/* 1. Header */}
      <DrawerHeader className="flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <VerdictBadge verdict={r.verdict} />
          {failedAfterAllowed(r) ? (
            // Allowed, then failed upstream: say so, not just the code.
            <span className="rounded-sm border border-v-degraded-border bg-v-degraded-bg px-1.5 text-xs leading-5 font-medium text-v-degraded-fg">
              Failed · <span className="font-mono">{r.status}</span>
            </span>
          ) : (
            <span className={cn('rounded-sm border px-1.5 font-mono text-xs leading-5', r.status >= 400 ? 'border-v-blocked-border text-v-blocked-fg' : 'border-border text-muted-foreground-strong')}>
              {r.status}
            </span>
          )}
          {r.inFlight && <span className="text-xs text-muted-foreground">Streaming: usage arrives at end of stream</span>}
        </div>
        <DrawerTitle className="font-mono text-lg">
          {r.requestedModel}
          {r.requestedModel !== r.resolvedModel && (
            <>
              <ArrowRight className="mx-1.5 inline size-4 text-muted-foreground" aria-label="resolved to" />
              {r.resolvedModel}
            </>
          )}
        </DrawerTitle>
        <DrawerDescription render={<div />} className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
          <span>
            <Money value={r.costUsd} precision="micro" unknown={r.inFlight} className="text-foreground" /> total
          </span>
          <span>
            <Duration ms={r.durationMs} unknown={r.inFlight} className="text-foreground" /> total
          </span>
          <span className="text-muted-foreground">
            {clock(r.ts)} · {ago(r.ts)}
          </span>
        </DrawerDescription>
        <button
          type="button"
          onClick={() => copy(r.traceId, 'Trace ID')}
          className="group inline-flex max-w-full items-center gap-1.5 self-start rounded-sm font-mono text-xs text-muted-foreground hover:text-foreground"
        >
          trace {r.traceId}
          <Copy className="size-3 shrink-0 opacity-60 group-hover:opacity-100" aria-hidden="true" />
          <span className="sr-only">Copy trace ID</span>
        </button>
      </DrawerHeader>

      <DrawerBody className="p-0">
        {r.errorDetail && (
          <div className="px-5 pt-4">
            <Alert variant={failure(r).refused ? 'destructive' : 'warning'}>
              <ShieldAlert />
              <AlertTitle>{failure(r).title}</AlertTitle>
              <AlertDescription>{r.errorDetail}</AlertDescription>
            </Alert>
          </div>
        )}

        {/* 2. Usage and cost */}
        <Sub title="Usage and cost">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border text-xs text-muted-foreground">
                <th className="py-1 text-left font-medium">Type</th>
                <th className="py-1 text-right font-medium">Tokens</th>
                <th className="py-1 text-right font-medium">Cost</th>
              </tr>
            </thead>
            <tbody>
              {lines.map((l) => (
                <tr key={l.label} className="border-b border-border">
                  <td className="py-1.5">{l.label}</td>
                  <td className="py-1.5 text-right">
                    <TokenCount value={l.tok} exact unknown={r.inFlight && l.label !== 'Input' && l.label !== 'Cached input'} />
                  </td>
                  <td />
                </tr>
              ))}
              <tr className="font-medium">
                <td className="py-1.5">Total</td>
                <td className="py-1.5 text-right">
                  <TokenCount value={r.inputTokens + r.outputTokens} exact unknown={r.inFlight} />
                </td>
                <td className="py-1.5 text-right">
                  <Money value={r.costUsd} precision="micro" unknown={r.inFlight} />
                </td>
              </tr>
            </tbody>
          </table>
          <p className="mt-2 text-xs text-muted-foreground">
            {unpriced
              ? `${r.resolvedModel} on ${r.backend} has no price, so this request has no cost and isn't in spend. It's costed once OpenMeter has a price for it.`
              : blocked
                ? `${r.verdict === 'throttled' ? 'Throttled' : 'Blocked'} before the upstream call: nothing was billed.`
                : r.inFlight
                  ? 'Priced when the stream ends and usage arrives.'
                  : "Priced at OpenMeter's prices in effect now, not the price when the request ran: OpenMeter keeps no price history."}
          </p>
        </Sub>

        {/* 3. Related */}
        <Sub title="Related">
          <dl className="mb-3 grid grid-cols-[6rem_1fr] gap-x-3 gap-y-1 text-sm">
            <KV k="Key">
              <Link to={`/keys?key=${r.keyId}`} className="font-mono hover:underline">
                {r.keyName}
              </Link>{' '}
              <span className="text-muted-foreground">
                · {r.team} / {r.project}
              </span>
            </KV>
            <KV k="Backend">
              <span className="font-mono">{r.backend}</span>
            </KV>
            {r.actor && <KV k="Actor">{r.actor}</KV>}
            {r.sessionId && (
              <KV k="Session">
                <span className="font-mono">{r.sessionId}</span>
              </KV>
            )}
          </dl>
          <RelatedList title={sameSession.length ? 'Same session' : 'Same key, the hour before'} items={sameSession.length ? sameSession : sameKey} />
        </Sub>
      </DrawerBody>

      <DrawerFooter className="flex-col items-stretch gap-2" data-print-hide>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap gap-2">
            <Button variant="ghost" size="sm" onClick={() => copy(window.location.href, 'Link')}>
              <Link2 /> Copy link
            </Button>
            {/* Opened over another page (Overview, Keys…): Traffic shows it among the requests around it. */}
            {pathname !== '/traffic' && (
              <Button variant="ghost" size="sm" render={<Link to={`/traffic?receipt=${r.id}`} />}>
                <ArrowRight /> Open in Traffic
              </Button>
            )}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={() => window.print()}>
              <Printer /> Print
            </Button>
            <Button variant="outline" size="sm" onClick={() => exportJson(r)}>
              <Download /> Export JSON
            </Button>
          </div>
        </div>
      </DrawerFooter>
    </>
  )
}

const HOUR = 3_600_000

/** Same session, and same key in the hour before. In api mode, asked of the server, not the loaded rows. */
function useRelated(r: Receipt) {
  const all = useReceipts()
  const keyQ = `/receipts?limit=5&key=${encodeURIComponent(r.keyId)}&since=${r.ts - HOUR}&before=${r.ts}`
  const sessQ = r.sessionId ? `/receipts?limit=4&session=${encodeURIComponent(r.sessionId)}` : null
  const liveKey = useLive<Receipt[]>(api ? keyQ : null, [], 60_000).data
  const liveSess = useLive<Receipt[]>(api ? sessQ : null, [], 60_000).data
  if (api) {
    return { sameKey: liveKey.filter((x) => x.id !== r.id).slice(0, 4), sameSession: liveSess.filter((x) => x.id !== r.id).slice(0, 3) }
  }
  return {
    sameKey: all.filter((x) => x.keyId === r.keyId && x.id !== r.id && x.ts < r.ts && x.ts >= r.ts - HOUR).slice(0, 4),
    sameSession: r.sessionId ? all.filter((x) => x.sessionId === r.sessionId && x.id !== r.id).slice(0, 3) : [],
  }
}

function RelatedList({ title, items }: { title: string; items: Receipt[] }) {
  const { openReceipt } = useApp()
  if (!items.length) return null
  return (
    <div>
      <div className="mb-1 text-xs text-muted-foreground">{title}</div>
      <ul className="divide-y divide-border rounded-md border border-border">
        {items.map((x) => (
          <li key={x.id}>
            <button type="button" onClick={() => openReceipt(x.id)} className="flex w-full items-center gap-3 px-3 py-1.5 text-left text-xs hover:bg-muted">
              <VerdictBadge verdict={x.verdict} compact />
              <span className="num font-mono">{clock(x.ts)}</span>
              <span className="font-mono">{x.resolvedModel}</span>
              <span className="ml-auto">
                <Money value={x.costUsd} precision="micro" unknown={x.inFlight} />
              </span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}
