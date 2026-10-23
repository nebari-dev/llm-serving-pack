import { ArrowLeft } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Sparkline } from '@/components/gw/charts'
import { Duration, Money, TokenCount } from '@/components/gw/numbers'
import { EmptyState, PageHeader, Section } from '@/components/gw/page'
import { VerdictBadge } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { type ApiKey, type WireKey, dataMode, fromWire, keys as seedKeys, teams } from '@/data/catalog'
import { clock, int, unpricedNote } from '@/lib/format'
import { useApp, useReceipts } from '@/state/app-state'
import { useLive } from '@/state/live'
import { keySpend24h } from './spend-data'

// Keys. List → key detail (?key=<id>), each key a miniature dashboard.

const spend24h = (k: ApiKey) => k.spend24hUsd ?? keySpend24h(k.id)

/** 24h spend as a cell shows it: no price, not $0, when it's all requests with no price (api mode). */
function Spend24h({ k, className }: { k: ApiKey; className?: string }) {
  const spend = spend24h(k)
  const unpriced = k.unpriced24h ?? 0
  return (
    <>
      <Money value={unpriced && !spend ? null : spend} className={className} />
      {!!unpriced && !!spend && (
        <div className="text-xs text-muted-foreground" title={unpricedNote(unpriced)}>
          + {int(unpriced)} no price
        </div>
      )}
    </>
  )
}

export function KeysPage() {
  const [params, setParams] = useSearchParams()
  const [list, setList] = useState<ApiKey[]>(seedKeys)
  // Api mode re-reads /keys.
  const { data: live } = useLive<WireKey[] | null>(dataMode === 'api' ? '/keys' : null, null)
  useEffect(() => {
    if (live) setList(live.map(fromWire))
  }, [live])

  const selectedId = params.get('key')
  const selected = list.find((k) => k.id === selectedId)

  const openKey = (id: string | null) => {
    const next = new URLSearchParams(params)
    if (id) next.set('key', id)
    else next.delete('key')
    setParams(next)
  }
  if (selectedId) {
    return (
      <>
        {selected ? (
          <KeyDetail k={selected} onBack={() => openKey(null)} />
        ) : (
          <>
            <PageHeader title="Key not found" />
            <EmptyState
              title={`No key with id ${selectedId} in this tenant.`}
              action={
                <Button variant="outline" onClick={() => openKey(null)}>
                  Back to all keys
                </Button>
              }
            />
          </>
        )}
      </>
    )
  }

  return (
    <div className="flex flex-col">
      <PageHeader
        title="Keys"
        description="Gateway keys that apps use instead of provider credentials, with each key's team, project and owner."
      />
      <Section title={`${list.length} keys`}>
        <Table aria-label="Gateway keys">
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Team / project</TableHead>
              <TableHead>Last used</TableHead>
              <TableHead className="text-right">Requests 24h</TableHead>
              <TableHead className="text-right">Spend 24h</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.map((k) => {
              return (
                <TableRow key={k.id} className="cursor-pointer" onClick={() => openKey(k.id)}>
                  <TableCell className="h-11 py-1.5">
                    <button
                      type="button"
                      className="font-mono text-[0.8125rem] font-medium hover:underline"
                      onClick={(ev) => {
                        ev.stopPropagation()
                        openKey(k.id)
                      }}
                    >
                      {k.name}
                    </button>
                    <div className="font-mono text-xs text-muted-foreground">{k.prefix}…</div>
                  </TableCell>
                  <TableCell className="py-1.5 text-sm">
                    {teams.find((t) => t.id === k.team)?.name ?? k.team}
                    <span className="text-muted-foreground"> / </span>
                    <span className="text-xs">{k.project}</span>
                  </TableCell>
                  <TableCell className="py-1.5 text-sm whitespace-nowrap">{k.lastUsed}</TableCell>
                  <TableCell className="num py-1.5 text-right font-mono">{int(k.requests24h)}</TableCell>
                  <TableCell className="py-1.5 text-right">
                    <Spend24h k={k} />
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </Section>
    </div>
  )
}

// ---- detail --------------------------------------------------------------

function KeyDetail({ k, onBack }: { k: ApiKey; onBack: () => void }) {
  const { openReceipt } = useApp()
  const all = useReceipts()
  const receipts = useMemo(() => all.filter((r) => r.keyId === k.id), [all, k.id])

  const topModels = useMemo(() => {
    const m = new Map<string, { model: string; n: number; tokens: number; cost: number; unpriced: number; ms: number[] }>()
    for (const r of receipts) {
      if (r.inFlight) continue
      const g = m.get(r.resolvedModel) ?? { model: r.resolvedModel, n: 0, tokens: 0, cost: 0, unpriced: 0, ms: [] }
      g.n++
      g.tokens += r.inputTokens + r.outputTokens + r.reasoningTokens
      // Unpriced requests aren't in spend either; the cell says how many.
      if (r.costUsd === null) g.unpriced++
      else g.cost += r.costUsd
      g.ms.push(r.durationMs)
      m.set(r.resolvedModel, g)
    }
    return [...m.values()]
      .map((g) => ({ ...g, p50: [...g.ms].sort((a, b) => a - b)[Math.floor(g.ms.length / 2)] ?? 0 }))
      .sort((a, b) => b.cost - a.cost)
  }, [receipts])
  const blocks = receipts.filter((r) => r.verdict === 'blocked' || r.verdict === 'truncated' || r.verdict === 'throttled').slice(0, 8)
  const recentWindow = receipts.length ? receipts[receipts.length - 1].ts : Date.now()

  return (
    <div className="flex flex-col">
      <PageHeader
        title={k.name}
        description={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="font-mono">{k.prefix}…</span>
            <span>
              {teams.find((t) => t.id === k.team)?.name} / <span className="font-mono">{k.project}</span>
            </span>
            <span>
              Owner <span className="font-mono">{k.owner}</span>
            </span>
          </span>
        }
        actions={
          <Button variant="ghost" onClick={onBack}>
            <ArrowLeft /> All keys
          </Button>
        }
      >
        <dl className="grid grid-cols-2 gap-y-3 border-t border-border pt-3 md:grid-cols-3 md:divide-x md:divide-border">
          <div className="pr-4">
            <dt className="text-xs text-muted-foreground">Requests, last 24h</dt>
            <dd className="flex items-center gap-3">
              <span className="num font-mono text-xl font-semibold">{int(k.requests24h)}</span>
              <Sparkline values={k.hourly24h} label={`Hourly requests for ${k.name}, last 24 hours`} />
            </dd>
          </div>
          <div className="md:px-4">
            <dt className="text-xs text-muted-foreground">Spend, last 24h</dt>
            <dd>
              <Spend24h k={k} className="text-xl font-semibold" />
            </dd>
          </div>
          <div className="md:pl-4">
            <dt className="text-xs text-muted-foreground">Last used</dt>
            <dd className="text-xl font-semibold">{k.lastUsed}</dd>
          </div>
        </dl>
      </PageHeader>

      <Section
        title="Top models"
        description={`From the ${int(receipts.length)} receipts on this key since ${clock(recentWindow)}.`}
        actions={
          <Button variant="outline" size="sm" render={<Link to={`/traffic?key=${k.name}`} />}>
            Open in Traffic
          </Button>
        }
      >
        {topModels.length ? (
          <Table aria-label={`Top models for ${k.name}`}>
            <TableHeader>
              <TableRow>
                <TableHead>Model</TableHead>
                <TableHead className="text-right">Requests</TableHead>
                <TableHead className="text-right">Tokens</TableHead>
                <TableHead className="text-right">Cost</TableHead>
                <TableHead className="text-right">p50</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {topModels.map((m) => (
                <TableRow key={m.model}>
                  <TableCell className="h-9 py-1 font-mono text-[0.8125rem]">
                    <Link to={`/traffic?key=${k.name}&model=${m.model}`} className="hover:underline">
                      {m.model}
                    </Link>
                  </TableCell>
                  <TableCell className="num h-9 py-1 text-right font-mono">{int(m.n)}</TableCell>
                  <TableCell className="h-9 py-1 text-right">
                    <TokenCount value={m.tokens} />
                  </TableCell>
                  <TableCell className="h-9 py-1 text-right">
                    <Money value={m.unpriced && !m.cost ? null : m.cost} precision="micro" />
                    {!!m.unpriced && !!m.cost && <div className="text-xs text-muted-foreground">+ {int(m.unpriced)} no price</div>}
                  </TableCell>
                  <TableCell className="h-9 py-1 text-right">
                    <Duration ms={m.p50} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : (
          <EmptyState title="No requests on this key yet." />
        )}
      </Section>

      <Section title="Recent blocks" description="Requests the gateway refused, throttled or cut short for this key. Open one to see why.">
        {blocks.length ? (
          <ul className="divide-y divide-border rounded-md border border-border bg-card">
            {blocks.map((r) => (
              <li key={r.id}>
                <button
                  type="button"
                  onClick={() => openReceipt(r.id)}
                  className="flex w-full flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2 text-left text-sm hover:bg-muted"
                >
                  <VerdictBadge verdict={r.verdict} />
                  <span className="num font-mono text-xs">{clock(r.ts)}</span>
                  <span className="font-mono text-xs">{r.requestedModel}</span>
                  <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                    {r.verdict === 'blocked' ? r.errorCode : r.verdict === 'throttled' ? 'throttled · retry later' : 'cut short, no token counts'}
                  </span>
                  <span className="text-xs text-muted-foreground">Open receipt →</span>
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-muted-foreground">No blocked requests on this key in the current stream window.</p>
        )}
      </Section>

    </div>
  )
}
