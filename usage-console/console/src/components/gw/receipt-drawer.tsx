import { ArrowRight, Copy, Download, Eye, Link2, Lock, Printer, ShieldAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Drawer, DrawerBody, DrawerContent, DrawerDescription, DrawerFooter, DrawerHeader, DrawerTitle } from '@/components/ui/drawer'
import { toast } from '@/components/ui/toast'
import { API_BASE, changes, dataMode, type PriceSource, type Receipt } from '@/data/catalog'
import { ago, clock } from '@/lib/format'
import { cn } from '@/lib/utils'
import { receiptStream, useApp, useReceipts } from '@/state/app-state'
import { useLive } from '@/state/live'
import { DecisionTrace } from './decision-trace'
import { Duration, Money, TokenCount } from './numbers'
import { VerdictBadge } from './verdict'

// §7.5.4 Request receipt. A drawer, deep-linkable (?receipt=id), printable,
// exportable as signed JSON. Sections in the spec's order.

// The alert above the trace names what stopped the request, from its error
// code: a rule, a budget, the key's allowlist, Warden, routing or the provider.
const failures: Record<string, { title: string; refused: boolean }> = {
  policy_blocked: { title: 'This request was blocked by a rule.', refused: true },
  budget_exceeded: { title: 'This request was blocked by a budget.', refused: true },
  budget_throttled: { title: 'A budget over its cap throttled this request. The caller may retry after the Retry-After delay.', refused: true },
  model_not_allowed: { title: "This key isn't allowed to call this model.", refused: true },
  policy_unavailable: { title: "Policy couldn't be evaluated, so the request failed closed.", refused: true },
  policy_deadline: { title: "A rule couldn't be evaluated in time, so the request failed closed.", refused: true },
  model_not_found: { title: 'No model by that name.', refused: true },
  no_route: { title: 'No route serves this model.', refused: true },
  no_healthy_backend: { title: 'No healthy backend serves this model.', refused: true },
  upstream_error: { title: 'The provider returned an error.', refused: false },
  upstream_rate_limited: { title: 'The provider rate-limited this request.', refused: false },
  client_disconnected: { title: 'The caller disconnected before the response finished.', refused: false },
}

function failure(r: Receipt) {
  return failures[r.errorCode ?? ''] ?? { title: r.verdict === 'blocked' ? 'This request was blocked.' : 'This request failed.', refused: r.verdict === 'blocked' }
}

const inbound: Record<Receipt['inboundVerdict'], string> = {
  allowed: 'clean',
  stripped: 'stripped',
  blocked: 'blocked',
  skipped: 'not inspected',
}

// What policyMode means for this request, when it isn't the normal case.
const sourceLabel: Record<PriceSource, string> = { seed: 'seed', litellm: 'LiteLLM', manual: 'override' }

const modes: Record<NonNullable<Receipt['policyMode']>, { label: string; note?: string }> = {
  enforced: { label: 'enforced' },
  passthrough: { label: 'passthrough', note: "Warden's kill switch was on: rules were recorded but not enforced." },
  'fail-open': { label: 'fail-open', note: "Policy couldn't be evaluated, and the rule's fail mode let the request through unpoliced." },
  'fail-closed': { label: 'fail-closed', note: "Policy couldn't be evaluated, and the rule's fail mode refused the request." },
}

const api = dataMode === 'api'

/** Downloads the receipt as JSON: in api mode, exactly what the control plane returns. */
async function exportJson(r: Receipt) {
  let body = JSON.stringify(r, null, 2)
  if (api) {
    const res = await fetch(`${API_BASE}/receipts/${encodeURIComponent(r.id)}`)
    if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
    body = JSON.stringify(await res.json(), null, 2)
  }
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
  useEffect(() => {
    if (receiptId && !r) receiptStream.ensure(receiptId)
  }, [receiptId, r])
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
  const [revealed, setRevealed] = useState(false)
  const basis = r.costBasis
  const precedingChange = changes.find((c) => c.ts < r.ts)
  const { sameKey, sameSession } = useRelated(r)
  const mode = r.policyMode ? modes[r.policyMode] : undefined

  const copy = (text: string, what: string) => {
    void navigator.clipboard?.writeText(text)
    toast.add({ title: `${what} copied`, type: 'success' })
  }

  const cacheWrites = r.cacheWriteTokens ?? 0
  const inputBilled = Math.max(r.inputTokens - r.cachedInputTokens - cacheWrites, 0)
  // Priced from the snapshot stored with the receipt (§5.1), never today's prices.
  // Older snapshots have no cache-write rate and no sources.
  const lines = [
    { label: 'Input', tok: inputBilled, rate: basis?.inPerM, source: basis?.sources?.input },
    { label: 'Cached input', tok: r.cachedInputTokens, rate: basis?.cachedPerM, source: basis?.sources?.cachedInput },
    ...(cacheWrites > 0 || basis?.cacheWritePerM != null ? [{ label: 'Cache write', tok: cacheWrites, rate: basis?.cacheWritePerM, source: basis?.sources?.cacheWrite }] : []),
    { label: 'Output', tok: r.outputTokens, rate: basis?.outPerM, source: basis?.sources?.output },
    { label: 'Reasoning', tok: r.reasoningTokens, rate: basis?.reasoningPerM, source: basis?.sources?.reasoning },
  ]
  const blocked = r.verdict === 'blocked'
  const lineTotal = lines.reduce((sum, l) => sum + (l.rate == null ? 0 : (l.tok * l.rate) / 1e6), 0)
  // The recorded total is the number of record; say so if the lines disagree with it.
  const mismatch = basis && r.costUsd !== null && !r.inFlight && !blocked && Math.abs(lineTotal - r.costUsd) > 1e-6
  const unpriced = r.costUsd === null && !r.inFlight

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
          {mode?.note && (
            <span className="inline-flex items-center gap-1 rounded-sm border border-v-degraded-border bg-v-degraded-bg px-1.5 text-xs leading-5 font-medium text-v-degraded-fg">
              Policy {mode.label}
            </span>
          )}
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
          {r.overheadUs != null && (
            <span title="The gateway's own time before calling the provider: key check, Warden and Agent Router.">
              <span className="num font-mono text-foreground">{(r.overheadUs / 1000).toFixed(1)}ms</span> gateway overhead
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
            <Alert variant={failure(r).refused ? 'destructive' : 'warning'}>
              <ShieldAlert />
              <AlertTitle>{failure(r).title}</AlertTitle>
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
                  {x.rehydrated ? ` · ${x.rehydrated} restored in the response` : ''}
                </span>
              ))}
              <span className="text-xs text-muted-foreground">Types and counts only — matched values are never stored.</span>
            </div>
          )}
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
            <span>
              Policy mode: <span className="font-medium text-foreground">{mode?.label ?? 'not evaluated'}</span>
            </span>
            <span>
              Inbound inspection: <span className="font-medium text-foreground">{inbound[r.inboundVerdict] ?? r.inboundVerdict}</span>
            </span>
            {r.redactions.length > 0 &&
              (api ? (
                <span className="ml-auto" data-print-hide>
                  Reporting an incorrect redaction isn't connected yet: there's no false-positive review queue.
                </span>
              ) : (
                <button type="button" data-print-hide className="ml-auto underline underline-offset-4 hover:text-foreground" onClick={() => toast.add({ title: 'Sent to the false-positive review queue', type: 'info' })}>
                  Mark a redaction as incorrect
                </button>
              ))}
          </div>
          {mode?.note && <p className="mt-2 text-xs text-v-degraded-fg">{mode.note}</p>}
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
                  <td className="py-1.5">
                    {l.label}
                    {l.source && <span className="ml-1.5 text-xs text-muted-foreground">{sourceLabel[l.source]}</span>}
                  </td>
                  <td className="py-1.5 text-right">
                    <TokenCount value={l.tok} exact unknown={r.inFlight && l.label !== 'Input' && l.label !== 'Cached input'} />
                  </td>
                  <td className="py-1.5 text-right">
                    <Money value={l.rate ?? 0} unknown={l.rate == null} className="text-muted-foreground" />
                  </td>
                  <td className="py-1.5 text-right">
                    <Money value={blocked ? 0 : ((l.rate ?? 0) * l.tok) / 1e6} precision="micro" unknown={r.inFlight || l.rate == null} />
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
            {basis
              ? basis.backend
                ? `Priced with the ${basis.display} rates on ${basis.backend} ${basis.pricedLater ? 'that were set after this request arrived' : `recorded with this receipt at ${clock(r.ts)}`}, not today's prices.`
                : `Priced with the ${basis.display} (${basis.provider}) rates recorded with this receipt at ${clock(r.ts)}, not today's prices.`
              : unpriced
                ? `${r.resolvedModel} on ${r.backend} had no price when this request arrived, so it has no cost and isn't in spend or budgets yet. It's costed once someone sets a price for that pair on Models → Pricing.`
                : blocked
                ? 'Blocked before the upstream call: nothing was billed.'
                : r.inFlight
                  ? 'Priced when the stream ends and usage arrives.'
                  : r.costUsd === 0
                    ? "Nothing was priced: the request didn't complete upstream."
                    : "No price snapshot was recorded with this receipt, so the rates behind its total can't be shown."}
            {basis && blocked && ' Blocked before the upstream call: nothing was billed.'}
          </p>
          {mismatch && (
            <p className="mt-1 text-xs text-v-degraded-fg">
              These lines add up to <Money value={lineTotal} precision="micro" />, but the receipt records <Money value={r.costUsd} precision="micro" />. The recorded total is the one used for spend and budgets.
            </p>
          )}
        </Sub>

        {/* 5. Content */}
        <Sub title="Content">
          {r.contentCaptured ? (
            api ? (
              <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-dashed border-border-strong p-3">
                <p className="text-sm text-muted-foreground-strong">
                  Content was captured for backend <span className="font-mono">{r.backend}</span>. Revealing it isn't connected yet: a reveal has to write its own audit record first.
                </p>
                <Button variant="outline" size="sm" disabled data-print-hide>
                  <Eye /> Reveal content
                </Button>
              </div>
            ) : revealed ? (
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
              <span className="font-mono">{precedingChange.target}</span> by {precedingChange.actor}, {ago(precedingChange.ts, r.ts).replace(/ ago$/, '')} earlier.
            </p>
          )}
          <RelatedList title={sameSession.length ? 'Same session' : 'Same key, the hour before'} items={sameSession.length ? sameSession : sameKey} />
        </Sub>
      </DrawerBody>

      <DrawerFooter className="flex-col items-stretch gap-2" data-print-hide>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Button variant="ghost" size="sm" onClick={() => copy(window.location.href, 'Link')}>
            <Link2 /> Copy link
          </Button>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={() => window.print()}>
              <Printer /> Print
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                exportJson(r).catch((e: Error) => toast.add({ title: "The receipt couldn't be exported", description: `${e.message}. Try again.`, type: 'error' }))
              }
            >
              <Download /> Export JSON
            </Button>
            {api ? (
              <Button variant="outline" size="sm" disabled aria-describedby="sign-reason">
                <Download /> Export signed JSON
              </Button>
            ) : (
              <Button variant="outline" size="sm" onClick={() => toast.add({ title: 'Signed receipt exported', description: 'Export recorded in the audit log.', type: 'success' })}>
                <Download /> Export signed JSON
              </Button>
            )}
          </div>
        </div>
        {api && (
          <p id="sign-reason" className="text-right text-xs text-muted-foreground">
            Signed export isn't connected yet: the control plane has no receipt signing key. Export JSON is unsigned.
          </p>
        )}
      </DrawerFooter>
    </>
  )
}

const HOUR = 3_600_000

/** Same session, and same key in the hour before (§7.5.4). In api mode, asked of the server, not the loaded rows. */
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
