import { ArrowRight, Copy, Download, Eye, Link2, Lock, Printer, ShieldAlert } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Drawer, DrawerBody, DrawerContent, DrawerDescription, DrawerFooter, DrawerHeader, DrawerTitle } from '@/components/ui/drawer'
import { toast } from '@/components/ui/toast'
import { changes, modelById, type Receipt } from '@/data/mock'
import { ago, clock } from '@/lib/format'
import { cn } from '@/lib/utils'
import { receiptStream, useApp, useReceipts } from '@/state/app-state'
import { DecisionTrace } from './decision-trace'
import { Duration, Money, TokenCount } from './numbers'
import { VerdictBadge } from './verdict'

// §7.5.4 Request receipt. A drawer, deep-linkable (?receipt=id), printable,
// exportable as signed JSON. Sections in the spec's order.

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
      <DrawerContent style={{ ['--drawer-content-width' as string]: 'min(46rem, 100vw)' }}>
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
  const [revealed, setRevealed] = useState(false)
  const model = modelById[r.resolvedModel]
  const precedingChange = changes.find((c) => c.ts < r.ts)
  const all = useReceipts()
  const sameKey = all.filter((x) => x.keyId === r.keyId && x.id !== r.id).slice(0, 4)
  const sameSession = r.sessionId ? all.filter((x) => x.sessionId === r.sessionId && x.id !== r.id).slice(0, 3) : []

  const copy = (text: string, what: string) => {
    void navigator.clipboard?.writeText(text)
    toast.add({ title: `${what} copied`, type: 'success' })
  }

  const inputBilled = r.inputTokens - r.cachedInputTokens
  const lines = [
    { label: 'Input', tok: inputBilled, rate: model.inPerM },
    { label: 'Cached input', tok: r.cachedInputTokens, rate: model.cachedPerM },
    { label: 'Output', tok: r.outputTokens, rate: model.outPerM },
    { label: 'Reasoning', tok: r.reasoningTokens, rate: model.reasoningPerM },
  ]
  const blocked = r.verdict === 'blocked'

  return (
    <>
      {/* 1. Header */}
      <DrawerHeader className="flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <VerdictBadge verdict={r.verdict} />
          <span className={cn('rounded-sm border px-1.5 font-mono text-xs leading-5', r.status >= 400 ? 'border-v-blocked-border text-v-blocked-fg' : 'border-border text-muted-foreground-strong')}>
            {r.status}
          </span>
          {r.inFlight && <span className="text-xs text-muted-foreground">Streaming — usage arrives at end of stream</span>}
          {r.contentCaptured && (
            <span className="inline-flex items-center gap-1 rounded-sm border border-v-degraded-border bg-v-degraded-bg px-1.5 text-xs leading-5 font-medium text-v-degraded-fg">
              <ShieldAlert className="size-3" /> Content capture on
            </span>
          )}
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
          {r.ttftMs && (
            <span>
              <Duration ms={r.ttftMs} className="text-foreground" /> to first token
            </span>
          )}
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
            <Alert variant="destructive">
              <ShieldAlert />
              <AlertTitle>This request was blocked by a rule.</AlertTitle>
              <AlertDescription>{r.errorDetail}</AlertDescription>
            </Alert>
          </div>
        )}

        {/* 2. Decision trace */}
        <Sub title="Decision trace">
          <DecisionTrace steps={r.trace} totalMs={r.durationMs} />
        </Sub>

        {/* 3. Policy detail */}
        <Sub title="Policy">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs text-muted-foreground">
                <th className="py-1 pr-2 font-medium">#</th>
                <th className="py-1 pr-2 font-medium">Rule</th>
                <th className="py-1 pr-2 font-medium">Matched</th>
                <th className="py-1 pr-2 font-medium">Action</th>
                <th className="py-1 text-right font-medium">Time</th>
              </tr>
            </thead>
            <tbody>
              {r.rules.map((x, i) => (
                <tr key={x.ruleId} className={cn('border-b border-border last:border-0', !x.matched && 'text-muted-foreground')}>
                  <td className="num py-1.5 pr-2 font-mono text-xs">{i + 1}</td>
                  <td className="py-1.5 pr-2">
                    <Link to={`/guardrails?rule=${x.ruleId}`} className="font-mono text-xs hover:underline">
                      {x.name} v{x.version}
                    </Link>
                  </td>
                  <td className="py-1.5 pr-2 text-xs">{x.matched ? 'Yes' : 'No'}</td>
                  <td className={cn('py-1.5 pr-2 text-xs', x.matched && x.action === 'block' && 'font-medium text-v-blocked-fg')}>{x.action}</td>
                  <td className="num py-1.5 text-right font-mono text-xs">{x.ms.toFixed(2)}ms</td>
                </tr>
              ))}
            </tbody>
          </table>
          {blocked && <p className="mt-2 text-xs text-muted-foreground">First block wins and short-circuits; later rules were not evaluated.</p>}
          {r.redactions.length > 0 && (
            <div className="mt-3 flex flex-wrap items-center gap-2 text-sm">
              <span className="text-muted-foreground">Redacted before egress:</span>
              {r.redactions.map((x) => (
                <span key={x.type} className="rounded-sm border border-v-redacted-border bg-v-redacted-bg px-1.5 font-mono text-xs leading-5 text-v-redacted-fg">
                  {x.count}× {x.type}
                </span>
              ))}
              <span className="text-xs text-muted-foreground">Types and counts only — matched values are never stored.</span>
            </div>
          )}
          <div className="mt-3 flex items-center gap-2 text-xs text-muted-foreground">
            Inbound inspection: <span className="font-medium text-foreground">{r.inboundVerdict === 'allowed' ? 'clean' : r.inboundVerdict}</span>
            {r.redactions.length > 0 && (
              <button type="button" className="ml-auto underline underline-offset-4 hover:text-foreground" onClick={() => toast.add({ title: 'Sent to the false-positive review queue', type: 'info' })}>
                Mark a redaction as incorrect
              </button>
            )}
          </div>
        </Sub>

        {/* 4. Usage and cost */}
        <Sub title="Usage and cost">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border text-xs text-muted-foreground">
                <th className="py-1 text-left font-medium">Type</th>
                <th className="py-1 text-right font-medium">Tokens</th>
                <th className="py-1 text-right font-medium">Rate / 1M</th>
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
                  <td className="py-1.5 text-right">
                    <Money value={l.rate} className="text-muted-foreground" />
                  </td>
                  <td className="py-1.5 text-right">
                    <Money value={blocked ? 0 : (l.tok * l.rate) / 1e6} precision="micro" unknown={r.inFlight} />
                  </td>
                </tr>
              ))}
              <tr className="font-medium">
                <td className="py-1.5">Total</td>
                <td className="py-1.5 text-right">
                  <TokenCount value={r.inputTokens + r.outputTokens + r.reasoningTokens} exact unknown={r.inFlight} />
                </td>
                <td />
                <td className="py-1.5 text-right">
                  <Money value={r.costUsd} precision="micro" unknown={r.inFlight} />
                </td>
              </tr>
            </tbody>
          </table>
          <p className="mt-2 text-xs text-muted-foreground">
            Priced with the {model.display} price snapshot in effect at {clock(r.ts)} (model_pricing row effective 2026-09-01, {model.provider} list price).
            {blocked && ' Blocked before the upstream call: nothing was billed.'}
          </p>
        </Sub>

        {/* 5. Content */}
        <Sub title="Content">
          {r.contentCaptured ? (
            revealed ? (
              <div className="flex flex-col gap-2">
                <p className="text-xs text-v-degraded-fg">Reveal recorded in the audit log as priya@acme.dev at {clock(Date.now())}.</p>
                <pre className="max-h-48 overflow-auto rounded-md border border-border bg-muted p-3 font-mono text-xs whitespace-pre-wrap text-muted-foreground-strong">
                  {`[system] You are the Acme support assistant…\n[user] My account ⟦ACCOUNT_1⟧ was charged twice, please refund to ⟦PERSON_1⟧.\n[assistant] I've opened a refund request for ⟦ACCOUNT_1⟧…`}
                </pre>
              </div>
            ) : (
              <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-dashed border-border-strong p-3">
                <p className="text-sm text-muted-foreground-strong">
                  Content was captured for route <span className="font-mono">eu-private</span>. Revealing it writes an audit record.
                </p>
                <Button variant="outline" size="sm" onClick={() => setRevealed(true)}>
                  <Eye /> Reveal content
                </Button>
              </div>
            )
          ) : (
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <Lock className="size-4" aria-hidden="true" /> Not captured for this route. Hashes only.
            </p>
          )}
          <dl className="mt-3 grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1 font-mono text-xs">
            <KV k="request_hash">{r.requestHash}</KV>
            <KV k="response_hash">{r.responseHash}</KV>
          </dl>
        </Sub>

        {/* 6. Related */}
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
              <span className="font-mono">{r.backend}</span> <span className="text-muted-foreground">· {r.provider} · {r.region}</span>
            </KV>
            <KV k="Route reason">
              {r.routeReason}
              {r.fallbackFrom && <span className="text-muted-foreground"> from {r.fallbackFrom}</span>}
            </KV>
            {r.actor && <KV k="Actor">{r.actor}</KV>}
            {r.sessionId && (
              <KV k="Session">
                <span className="font-mono">{r.sessionId}</span>
              </KV>
            )}
          </dl>
          {precedingChange && (
            <p className="mb-3 rounded-md border border-border bg-muted p-2 text-xs text-muted-foreground-strong">
              Most recent config change before this request: <span className="font-medium text-foreground">{precedingChange.action}</span>{' '}
              <span className="font-mono">{precedingChange.target}</span> by {precedingChange.actor}, {ago(precedingChange.ts, r.ts)} earlier.
            </p>
          )}
          <RelatedList title={sameSession.length ? 'Same session' : 'Same key, last hour'} items={sameSession.length ? sameSession : sameKey} />
        </Sub>
      </DrawerBody>

      <DrawerFooter className="justify-between">
        <Button variant="ghost" size="sm" onClick={() => copy(window.location.href, 'Link')}>
          <Link2 /> Copy link
        </Button>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => toast.add({ title: 'Print is disabled in this mockup', type: 'info' })}>
            <Printer /> Print
          </Button>
          <Button variant="outline" size="sm" onClick={() => toast.add({ title: 'Signed receipt exported', description: 'Export recorded in the audit log.', type: 'success' })}>
            <Download /> Export signed JSON
          </Button>
        </div>
      </DrawerFooter>
    </>
  )
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
