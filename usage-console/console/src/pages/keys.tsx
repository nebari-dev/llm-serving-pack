import { ArrowLeft, Ban, Ellipsis, FolderKanban, Plus, RefreshCw, TriangleAlert } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Meter, Sparkline } from '@/components/gw/charts'
import { Duration, Money, TokenCount } from '@/components/gw/numbers'
import { EmptyState, PageHeader, Section } from '@/components/gw/page'
import { StateChip, VerdictBadge } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuPortal, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { type ApiKey, type WireKey, budgetLabel, budgets, can, canManageKey, dataMode, throttleRate, fromWire, governingBudget, keys as seedKeys, projects, revokeKey, teams } from '@/data/catalog'
import { clock, int, unpricedNote } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useApp, useReceipts } from '@/state/app-state'
import { useLive, useNow } from '@/state/live'
import { CreateKeyDialog, RevokeKeyDialog, RotateKeyDialog, RotationStatus, timeLeft } from './keys-dialogs'
import { ManageProjectsDialog } from './project-dialogs'
import { fmtDate, keySpend24h } from './spend-data'

// §7.5.8 Keys. List → key detail (?key=<id>), each key a miniature dashboard.

function expiryInfo(k: ApiKey) {
  if (k.expiresAt === null) return { label: 'Never', tone: 'degraded' as const, note: 'No expiry' }
  const days = Math.ceil((new Date(k.expiresAt).getTime() - Date.now()) / 86_400_000)
  const date = fmtDate(new Date(k.expiresAt))
  if (days < 0) return { label: date, tone: 'neutral' as const, note: 'Expired' }
  if (days <= 14) return { label: date, tone: 'degraded' as const, note: `Expires in ${days} day${days === 1 ? '' : 's'}` }
  return { label: date, tone: null, note: `in ${days} days` }
}

const spend24h = (k: ApiKey) => (k.status === 'revoked' ? 0 : (k.spend24hUsd ?? keySpend24h(k.id)))

/** 24h spend as a cell shows it: no price, not $0, when it's all requests with no price (api mode). */
function Spend24h({ k, className }: { k: ApiKey; className?: string }) {
  const spend = spend24h(k)
  const unpriced = k.status === 'revoked' ? 0 : (k.unpriced24h ?? 0)
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

function StatusCell({ k }: { k: ApiKey }) {
  const now = useNow(60_000)
  if (k.status === 'revoked') return <StateChip tone="blocked" icon={<Ban className="size-3" aria-hidden="true" />}>Revoked</StateChip>
  if (k.status === 'rotating') {
    const r = k.rotation
    const detail = r?.split ? ` · ${Math.round(r.split.newShare * 100)}%` : r?.endsAt ? ` · ${timeLeft(r.endsAt, now)} left` : ''
    return (
      <StateChip tone="neutral" className="border-dashed" icon={<RefreshCw className="size-3" aria-hidden="true" />}>
        Rotating{detail}
      </StateChip>
    )
  }
  return <span className="text-xs text-muted-foreground">Active</span>
}

export function KeysPage() {
  const [params, setParams] = useSearchParams()
  const [list, setList] = useState<ApiKey[]>(seedKeys)
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<ApiKey | null>(null)
  const [rotating, setRotating] = useState<ApiKey | null>(null)
  const [managingProjects, setManagingProjects] = useState(false)
  // Api mode re-reads /keys, which also picks up this page's own writes.
  const { data: live, reload } = useLive<WireKey[] | null>(dataMode === 'api' ? '/keys' : null, null)
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
  const update = (id: string, patch: Partial<ApiKey>) => setList((l) => l.map((k) => (k.id === id ? { ...k, ...patch } : k)))

  const dialogs = (
    <>
      <CreateKeyDialog open={creating} onOpenChange={setCreating} onCreate={(k) => setList((l) => [k, ...l])} />
      <RevokeKeyDialog
        apiKey={revoking}
        onOpenChange={(o) => !o && setRevoking(null)}
        onRevoke={async (k) => {
          update(k.id, await revokeKey(k))
          setRevoking(null)
        }}
      />
      <RotateKeyDialog apiKey={rotating} onOpenChange={(o) => !o && setRotating(null)} onRotate={(k) => update(k.id, k)} />
      <ManageProjectsDialog
        open={managingProjects}
        onOpenChange={setManagingProjects}
        onChanged={() => {
          // A rename shows on its keys at once; api mode re-reads them too.
          setList((l) => l.map((k) => ({ ...k, project: projects.find((p) => p.id === k.projectId)?.name ?? k.project })))
          reload()
        }}
      />
    </>
  )

  if (selectedId) {
    return (
      <>
        {selected ? (
          <KeyDetail k={selected} onBack={() => openKey(null)} onRevoke={() => setRevoking(selected)} onRotate={() => setRotating(selected)} />
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
        {dialogs}
      </>
    )
  }

  const active = list.filter((k) => k.status !== 'revoked')
  const attention = active.filter((k) => {
    const e = expiryInfo(k)
    return e.tone === 'degraded'
  }).length

  return (
    <div className="flex flex-col">
      <PageHeader
        title="Keys"
        description="Gateway keys that apps use instead of provider credentials. Each key is scoped to a team and project and an allow-list of models and regions. Budgets on its team, project or the key itself apply to it."
        actions={
          <>
            <Button variant="outline" onClick={() => setManagingProjects(true)}>
              <FolderKanban /> Projects
            </Button>
            <Button onClick={() => setCreating(true)} disabled={!can('keys.own').ok} title={can('keys.own').reason}>
              <Plus /> Create key
            </Button>
          </>
        }
      />
      <Section
        title={`${active.length} active keys`}
        description={attention ? `${attention} need attention: expiring within 14 days or set to never expire.` : undefined}
      >
        <Table aria-label="Gateway keys">
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Team / project</TableHead>
              <TableHead className="text-right">Models</TableHead>
              <TableHead>Budget</TableHead>
              <TableHead>Expires</TableHead>
              <TableHead>Last used</TableHead>
              <TableHead className="text-right">Requests 24h</TableHead>
              <TableHead className="text-right">Spend 24h</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.map((k) => {
              const e = expiryInfo(k)
              const budget = governingBudget(k)
              const revoked = k.status === 'revoked'
              return (
                <TableRow key={k.id} className={cn('cursor-pointer', revoked && 'text-muted-foreground')} onClick={() => openKey(k.id)}>
                  <TableCell className="h-11 py-1.5">
                    <button
                      type="button"
                      className={cn('font-mono text-[0.8125rem] font-medium hover:underline', revoked && 'line-through')}
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
                  <TableCell className="num py-1.5 text-right font-mono" title={k.allowedModels.join(', ')}>
                    {k.allowedModels.length}
                  </TableCell>
                  <TableCell className="py-1.5 text-sm">
                    {budget ? (
                      <span className="font-mono text-xs">
                        {budgetLabel(budget)}
                        {budget.currentUsd > budget.capUsd && <span className="ml-1.5 font-sans text-v-blocked-fg">over cap</span>}
                      </span>
                    ) : (
                      <span className="text-xs text-muted-foreground">None</span>
                    )}
                  </TableCell>
                  <TableCell className="py-1.5">
                    {revoked ? (
                      <span className="text-xs">—</span>
                    ) : e.tone === 'degraded' ? (
                      <StateChip tone="degraded" icon={<TriangleAlert className="size-3" aria-hidden="true" />}>
                        {e.note === 'No expiry' ? 'Never expires' : `${e.label} · ${e.note.toLowerCase()}`}
                      </StateChip>
                    ) : (
                      <span className="text-sm">
                        {e.label} <span className="text-xs text-muted-foreground">{e.note}</span>
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="py-1.5 text-sm whitespace-nowrap">{k.lastUsed}</TableCell>
                  <TableCell className="num py-1.5 text-right font-mono">{int(k.requests24h)}</TableCell>
                  <TableCell className="py-1.5 text-right">
                    <Spend24h k={k} />
                  </TableCell>
                  <TableCell className="py-1.5">
                    <StatusCell k={k} />
                  </TableCell>
                  <TableCell className="py-1.5" onClick={(ev) => ev.stopPropagation()}>
                    {!revoked && (
                      <DropdownMenu modal={false}>
                        <DropdownMenuTrigger variant="ghost" className="w-8 px-0" aria-label={`Actions for ${k.name}`}>
                          <Ellipsis />
                        </DropdownMenuTrigger>
                        <DropdownMenuPortal>
                          <DropdownMenuContent align="end" className="min-w-48">
                            <DropdownMenuItem onClick={() => openKey(k.id)}>View key</DropdownMenuItem>
                            <DropdownMenuItem
                              onClick={() => setRotating(k)}
                              disabled={k.status !== 'rotating' && !canManageKey(k).ok}
                              title={k.status !== 'rotating' ? canManageKey(k).reason : undefined}
                            >
                              {k.status === 'rotating' ? 'View rotation' : 'Rotate secret'}
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem variant="destructive" onClick={() => setRevoking(k)} disabled={!canManageKey(k).ok} title={canManageKey(k).reason}>
                              Revoke key…
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenuPortal>
                      </DropdownMenu>
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </Section>
      {dialogs}
    </div>
  )
}

// ---- detail --------------------------------------------------------------

/**
 * What the key's budget does at its cap. Api mode says only what the gateway
 * does, as on Spend: block returns 429, throttle holds the key to a few
 * requests a minute, warn admits and marks.
 */
function budgetWords(b: (typeof budgets)[number]) {
  const over = b.currentUsd > b.capUsd
  const cap = `$${int(b.capUsd)}`
  if (dataMode === 'api') {
    if (over)
      return b.onExceed === 'block'
        ? 'Over cap · blocking new requests'
        : b.onExceed === 'throttle'
          ? `Over cap · throttled to ${throttleRate(b)} requests a minute (429, retry later)`
          : 'Over cap · requests admitted and marked'
    return b.onExceed === 'block' ? `Blocks new requests at ${cap}` : b.onExceed === 'throttle' ? `Throttles to ${throttleRate(b)} requests a minute at ${cap}` : `Marks requests over ${cap}`
  }
  if (over) return `Over cap · ${b.onExceed === 'throttle' ? 'throttling' : b.onExceed === 'block' ? 'blocking' : 'warning'} new requests`
  return b.onExceed === 'block' ? `Blocks new requests at ${cap}` : b.onExceed === 'throttle' ? `Throttles at ${cap}` : `Warns at ${cap}`
}

function KeyDetail({ k, onBack, onRevoke, onRotate }: { k: ApiKey; onBack: () => void; onRevoke: () => void; onRotate: () => void }) {
  const { openReceipt } = useApp()
  const all = useReceipts()
  const receipts = useMemo(() => all.filter((r) => r.keyId === k.id), [all, k.id])
  const budget = governingBudget(k)
  const e = expiryInfo(k)
  const revoked = k.status === 'revoked'
  const manage = canManageKey(k)

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
            <StatusCell k={k} />
            <span>
              Owner <span className="font-mono">{k.owner}</span>
            </span>
          </span>
        }
        actions={
          <>
            <Button variant="ghost" onClick={onBack}>
              <ArrowLeft /> All keys
            </Button>
            {!revoked && (
              <>
                <Button variant="outline" onClick={onRotate} disabled={k.status !== 'rotating' && !manage.ok} title={k.status !== 'rotating' ? manage.reason : undefined}>
                  <RefreshCw /> {k.status === 'rotating' ? 'View rotation' : 'Rotate secret'}
                </Button>
                <Button variant="destructive" onClick={onRevoke} disabled={!manage.ok} title={manage.reason}>
                  <Ban /> Revoke key…
                </Button>
              </>
            )}
          </>
        }
      >
        <dl className="grid grid-cols-2 gap-y-3 border-t border-border pt-3 md:grid-cols-5 md:divide-x md:divide-border">
          <div className="pr-4">
            <dt className="text-xs text-muted-foreground">Requests, last 24h</dt>
            <dd className="flex items-center gap-3">
              <span className="num font-mono text-xl font-semibold">{int(k.requests24h)}</span>
              {!revoked && <Sparkline values={k.hourly24h} label={`Hourly requests for ${k.name}, last 24 hours`} />}
            </dd>
          </div>
          <div className="md:px-4">
            <dt className="text-xs text-muted-foreground">Spend, last 24h</dt>
            <dd>
              <Spend24h k={k} className="text-xl font-semibold" />
            </dd>
          </div>
          <div className="md:px-4">
            <dt className="text-xs text-muted-foreground">Last used</dt>
            <dd className="text-xl font-semibold">{k.lastUsed}</dd>
          </div>
          <div className="md:px-4">
            <dt className="text-xs text-muted-foreground">Expires</dt>
            <dd className={cn('text-xl font-semibold', e.tone === 'degraded' && 'text-v-degraded-fg')}>{e.label}</dd>
            <dd className="text-xs text-muted-foreground">{e.note !== 'No expiry' ? e.note : dataMode === 'api' ? 'Set to never expire' : 'Set to never expire — rotation reminders every 90 days'}</dd>
          </div>
          <div className="md:pl-4">
            <dt className="text-xs text-muted-foreground">Budget</dt>
            {budget ? (
              <dd className="flex flex-col gap-1.5 pt-1">
                <span className="text-sm">
                  <Money value={budget.currentUsd} /> of <Money value={budget.capUsd} precision="whole" />{' '}
                  <span className="font-mono text-xs text-muted-foreground">{budgetLabel(budget)}</span>
                </span>
                {!!budget.unpricedRequests && (
                  <span className="text-xs text-muted-foreground">{unpricedNote(budget.unpricedRequests, 'what it has spent')}</span>
                )}
                <Meter value={budget.currentUsd} cap={budget.capUsd} projected={budget.projectedUsd} />
                <Link to="/spend#budgets" className="text-xs text-muted-foreground hover:text-foreground hover:underline">
                  {budgetWords(budget)}
                </Link>
              </dd>
            ) : (
              <dd className="pt-1 text-sm text-muted-foreground">No budget covers this key</dd>
            )}
          </div>
        </dl>
      </PageHeader>

      {k.status === 'rotating' && (
        <Section title="Rotation in progress" description="Traffic moves from the old secret to the new one as apps pick it up.">
          <RotationStatus apiKey={k} className="max-w-2xl" />
        </Section>
      )}

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
          <EmptyState
            title={revoked ? 'This key is revoked and receives no traffic.' : 'No requests on this key yet.'}
            action={
              !revoked && (
                <Button variant="outline" render={<Link to="/onboarding" />}>
                  Point an app at the gateway →
                </Button>
              )
            }
          />
        )}
      </Section>

      <Section title="Recent blocks" description="Requests the gateway stopped, throttled or cut short for this key. Open one to see which rule or budget, and what to change.">
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
                    {r.verdict === 'blocked'
                      ? (r.rules.find((x) => x.matched)?.name ?? r.errorCode)
                      : r.verdict === 'throttled'
                        ? 'throttled by a budget over its cap · retry later'
                        : 'stream cut by inbound inspection'}
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

      <Section title="Access">
        <dl className="grid max-w-3xl grid-cols-[10rem_1fr] gap-x-4 gap-y-2 text-sm">
          <dt className="text-muted-foreground">Allowed models</dt>
          <dd className="flex flex-wrap gap-1.5">
            {k.allowedModels.map((m) => (
              <span key={m} className="rounded-sm border border-border bg-muted px-1.5 font-mono text-xs leading-5">
                {m}
              </span>
            ))}
          </dd>
          <dt className="text-muted-foreground">Allowed regions</dt>
          <dd className="flex flex-wrap gap-1.5">
            {k.allowedRegions.map((r) => (
              <span key={r} className="rounded-sm border border-border bg-muted px-1.5 font-mono text-xs leading-5">
                {r}
              </span>
            ))}
          </dd>
          <dt className="text-muted-foreground">Requests outside these</dt>
          <dd className="text-muted-foreground-strong">
            Rejected before the upstream call with <span className="font-mono">403 model_not_allowed</span> or <span className="font-mono">403 region_not_permitted</span>.
          </dd>
        </dl>
      </Section>
    </div>
  )
}
