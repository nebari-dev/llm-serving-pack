import { Columns3, Link2, Pause, Play, Plus, Rows3, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Duration, Money, TokenCount } from '@/components/gw/numbers'
import { EmptyState } from '@/components/gw/page'
import { toneBar, toneText, verdictMeta } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuGroupLabel,
  DropdownMenuItem,
  DropdownMenuPortal,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/components/ui/toast'
import { backends, keys, models, type Receipt, teams, type Verdict } from '@/data/mock'
import { clock } from '@/lib/format'
import { cn } from '@/lib/utils'
import { type Density, rangeLabel, useApp, useReceipts } from '@/state/app-state'

// §7.5.3 Traffic — a dense table over a live stream.

type Dim = 'key' | 'team' | 'project' | 'model' | 'verdict' | 'provider' | 'backend' | 'reason'

const dims: { dim: Dim; label: string; values: { value: string; label: string }[] }[] = [
  { dim: 'key', label: 'Key', values: keys.map((k) => ({ value: k.name, label: k.name })) },
  { dim: 'team', label: 'Team', values: teams.map((t) => ({ value: t.id, label: t.name })) },
  { dim: 'project', label: 'Project', values: [...new Set(keys.map((k) => k.project))].map((p) => ({ value: p, label: p })) },
  { dim: 'model', label: 'Model', values: models.map((m) => ({ value: m.id, label: m.id })) },
  {
    dim: 'verdict',
    label: 'Verdict',
    values: (Object.keys(verdictMeta) as Verdict[]).map((v) => ({ value: v, label: verdictMeta[v].label })),
  },
  {
    dim: 'provider',
    label: 'Provider',
    values: ['OpenAI', 'Anthropic', 'Bedrock', 'Self-hosted', 'Azure'].map((p) => ({ value: p, label: p })),
  },
  { dim: 'backend', label: 'Backend', values: backends.map((b) => ({ value: b.name, label: b.name })) },
  {
    dim: 'reason',
    label: 'Route reason',
    values: ['explicit', 'alias', 'policy', 'fallback'].map((p) => ({ value: p, label: p })),
  },
]
const primaryDims: Dim[] = ['key', 'team', 'model', 'verdict']

function matches(r: Receipt, f: Record<Dim, string[]>, since: number | null, day: string | null) {
  if (since && r.ts < since) return false
  if (day && new Date(r.ts).toISOString().slice(5, 10) !== day) return false
  if (f.project.length && !f.project.includes(r.project)) return false
  if (f.key.length && !f.key.includes(r.keyName)) return false
  if (f.team.length && !f.team.includes(r.team)) return false
  if (f.model.length && !f.model.includes(r.resolvedModel) && !f.model.includes(r.requestedModel)) return false
  if (f.verdict.length && !f.verdict.includes(r.verdict)) return false
  if (f.provider.length && !f.provider.includes(r.provider)) return false
  if (f.backend.length && !f.backend.includes(r.backend)) return false
  if (f.reason.length && !f.reason.includes(r.routeReason)) return false
  return true
}

// Column config (§7.5.3): user-configurable, persisted; time + key are pinned.
type ColId = 'team' | 'model' | 'tokens' | 'cost' | 'ms' | 'backend' | 'status'
const allCols: { id: ColId; label: string; align?: 'right' }[] = [
  { id: 'team', label: 'Team' },
  { id: 'model', label: 'Model' },
  { id: 'backend', label: 'Backend' },
  { id: 'tokens', label: 'Tokens', align: 'right' },
  { id: 'cost', label: 'Cost', align: 'right' },
  { id: 'ms', label: 'Latency', align: 'right' },
  { id: 'status', label: 'Status', align: 'right' },
]
const defaultCols: ColId[] = ['team', 'model', 'tokens', 'cost', 'ms', 'status']

function loadCols(): ColId[] {
  try {
    const v = JSON.parse(localStorage.getItem('gw:traffic-cols') ?? 'null')
    return Array.isArray(v) ? v : defaultCols
  } catch {
    return defaultCols
  }
}

const SAMPLE_THRESHOLD = 40 // rows/min in this mockup; real threshold is server-side

export function TrafficPage() {
  const all = useReceipts()
  const { range, openReceipt, density, setDensity } = useApp()
  const [params, setParams] = useSearchParams()
  const [live, setLive] = useState(true)
  const [hovering, setHovering] = useState(false)
  const [focusWithin, setFocusWithin] = useState(false)
  const [frozenTop, setFrozenTop] = useState<number | null>(null)
  const [simulateBurst, setSimulateBurst] = useState(false)
  const [cols, setCols] = useState<ColId[]>(loadCols)
  const mountedAt = useRef(Date.now())
  const [announce, setAnnounce] = useState('')

  const filters = useMemo(() => {
    const f = {} as Record<Dim, string[]>
    for (const d of dims) f[d.dim] = params.getAll(d.dim)
    return f
  }, [params])
  const since = params.get('since') ? Number(params.get('since')) : null
  const day = params.get('day')

  const frozen = !live || hovering || focusWithin
  // Freeze on interaction: remember the newest visible row and hold the view there.
  useEffect(() => {
    if (frozen && frozenTop === null) setFrozenTop(all[0]?.ts ?? Date.now())
    if (!frozen && frozenTop !== null) setFrozenTop(null)
  }, [frozen, frozenTop, all])

  const matching = useMemo(() => all.filter((r) => matches(r, filters, since, day)), [all, filters, since, day])
  const sampling = simulateBurst && Object.values(filters).every((v) => v.length === 0)
  const visible = useMemo(() => {
    let rows = frozenTop !== null ? matching.filter((r) => r.ts <= frozenTop) : matching
    if (sampling) rows = rows.filter((_, i) => i % 20 === 0)
    return rows.slice(0, 300)
  }, [matching, frozenTop, sampling])
  const newCount = frozenTop !== null ? matching.filter((r) => r.ts > frozenTop).length : 0

  // Screen-reader announcements are throttled, and silent while paused (§7.7).
  const lastCount = useRef(matching.length)
  useEffect(() => {
    const id = window.setInterval(() => {
      if (frozen) return
      const delta = matching.length - lastCount.current
      lastCount.current = matching.length
      if (delta > 0) setAnnounce(`${delta} new requests`)
    }, 15_000)
    return () => window.clearInterval(id)
  }, [frozen, matching.length])

  const setFilter = (dim: Dim | 'since' | 'day', values: string[]) => {
    const next = new URLSearchParams(params)
    next.delete(dim)
    for (const v of values) next.append(dim, v)
    setParams(next, { replace: true })
  }
  const clearAll = () => {
    const next = new URLSearchParams()
    const receipt = params.get('receipt')
    if (receipt) next.set('receipt', receipt)
    setParams(next, { replace: true })
  }
  const toggleCol = (id: ColId) => {
    const next = cols.includes(id) ? cols.filter((c) => c !== id) : allCols.map((c) => c.id).filter((c) => c === id || cols.includes(c))
    setCols(next)
    try {
      localStorage.setItem('gw:traffic-cols', JSON.stringify(next))
    } catch {
      /* storage unavailable */
    }
  }

  const perMin = Math.round((matching.filter((r) => r.ts > Date.now() - 60_000).length || 38) * (simulateBurst ? 22 : 1))
  const activeDims = dims.filter((d) => filters[d.dim].length > 0 || primaryDims.includes(d.dim))
  const extraDims = dims.filter((d) => !primaryDims.includes(d.dim) && filters[d.dim].length === 0)
  const shownCols = allCols.filter((c) => cols.includes(c.id))
  const hasFilters = Object.values(filters).some((v) => v.length) || since !== null || day !== null

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-col gap-2 border-b border-border px-6 pt-5 pb-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-[28px] leading-9 font-semibold tracking-[-0.2px]">Traffic</h1>
            <p className="text-sm text-muted-foreground">
              Every request through the gateway, {rangeLabel(range)}. Click a row to open its receipt.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                void navigator.clipboard?.writeText(window.location.href)
                toast.add({ title: 'Link to this view copied', description: 'Filters are part of the URL.', type: 'success' })
              }}
            >
              <Link2 /> Share this view
            </Button>
            <DensityPicker density={density} setDensity={setDensity} />
            <DropdownMenu modal={false}>
              <DropdownMenuTrigger variant="outline" className="h-7 px-2.5 text-xs" aria-label="Columns">
                <Columns3 className="size-3.5" /> Columns
              </DropdownMenuTrigger>
              <DropdownMenuPortal>
                <DropdownMenuContent align="end" className="w-52">
                  <DropdownMenuGroup>
                    <DropdownMenuGroupLabel className="text-xs tracking-normal normal-case">Time and key are pinned</DropdownMenuGroupLabel>
                    {allCols.map((c) => (
                      <DropdownMenuCheckboxItem key={c.id} checked={cols.includes(c.id)} onCheckedChange={() => toggleCol(c.id)} closeOnClick={false}>
                        {c.label}
                      </DropdownMenuCheckboxItem>
                    ))}
                  </DropdownMenuGroup>
                </DropdownMenuContent>
              </DropdownMenuPortal>
            </DropdownMenu>
          </div>
        </div>

        {/* Filter bar — filters are the query and serialize to the URL. */}
        <div className="flex flex-wrap items-center gap-2" role="toolbar" aria-label="Traffic filters">
          <button
            type="button"
            onClick={() => setLive((v) => !v)}
            aria-pressed={live}
            className={cn(
              'inline-flex h-7 items-center gap-1.5 rounded-md border px-2.5 text-xs font-medium',
              live ? 'border-border-strong bg-card' : 'border-dashed border-border-strong text-muted-foreground-strong',
            )}
          >
            {live ? (
              <>
                <span className="size-2 rounded-full bg-v-allowed-bar" aria-hidden="true" /> Live <Pause className="size-3" aria-hidden="true" />
              </>
            ) : (
              <>
                <Play className="size-3" aria-hidden="true" /> Paused
              </>
            )}
          </button>
          <span className="text-xs text-muted-foreground">{rangeLabel(range)}</span>
          <span className="h-4 w-px bg-border" aria-hidden="true" />
          {activeDims.map((d) => (
            <FilterMenu key={d.dim} label={d.label} values={d.values} selected={filters[d.dim]} onChange={(v) => setFilter(d.dim, v)} />
          ))}
          {extraDims.length > 0 && (
            <DropdownMenu modal={false}>
              <DropdownMenuTrigger variant="ghost" className="h-7 px-2 text-xs">
                <Plus className="size-3.5" /> Filter
              </DropdownMenuTrigger>
              <DropdownMenuPortal>
                <DropdownMenuContent className="w-48">
                  {extraDims.map((d) => (
                    <DropdownMenuItem key={d.dim} onClick={() => setFilter(d.dim, [d.values[0].value])}>
                      {d.label}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenuPortal>
            </DropdownMenu>
          )}
          {since !== null && (
            <span className="inline-flex h-7 items-center gap-1 rounded-md border border-border-strong bg-card px-2 text-xs">
              Since <span className="num font-mono">{clock(since)}</span>
              <button type="button" aria-label="Remove since filter" onClick={() => setFilter('since', [])} className="rounded-sm hover:bg-muted">
                <X className="size-3" />
              </button>
            </span>
          )}
          {day !== null && (
            <span className="inline-flex h-7 items-center gap-1 rounded-md border border-border-strong bg-card px-2 text-xs">
              Day <span className="num font-mono">{day}</span>
              <button type="button" aria-label="Remove day filter" onClick={() => setFilter('day', [])} className="rounded-sm hover:bg-muted">
                <X className="size-3" />
              </button>
            </span>
          )}
          {hasFilters && (
            <Button variant="link" size="xs" onClick={clearAll}>
              Clear filters
            </Button>
          )}
          <span className="ml-auto flex items-center gap-3 text-xs text-muted-foreground">
            <span className="num font-mono">{matching.length.toLocaleString()} matching</span>
            <label className="inline-flex items-center gap-2">
              <Switch checked={simulateBurst} onCheckedChange={setSimulateBurst} aria-label="Simulate traffic burst" />
              Simulate burst
            </label>
          </span>
        </div>

        {sampling && (
          <div role="status" className="flex items-center gap-2 rounded-md border border-v-degraded-border bg-v-degraded-bg px-3 py-1.5 text-sm text-v-degraded-fg">
            <span className="font-medium">Sampling 1 in 20.</span>
            <span className="text-foreground/80">
              {perMin.toLocaleString()} requests/min exceeds the live threshold of {SAMPLE_THRESHOLD}/min. Add a filter to see everything matching.
            </span>
          </div>
        )}
      </div>

      <div className="relative min-h-0 flex-1">
        {/* "N new" resume affordance: rows never move under the pointer. */}
        {frozenTop !== null && (
          <div className="pointer-events-none absolute inset-x-0 top-9 z-20 flex justify-center">
            <button
              type="button"
              onClick={() => {
                setLive(true)
                setHovering(false)
                setFocusWithin(false)
                setFrozenTop(null)
              }}
              className="pointer-events-auto inline-flex h-7 items-center gap-1.5 rounded-full border border-border-strong bg-popover px-3 text-xs font-medium shadow-md hover:bg-muted"
            >
              {live ? <Pause className="size-3" aria-hidden="true" /> : <Play className="size-3" aria-hidden="true" />}
              {newCount > 0 ? `${newCount} new · click to resume` : live ? 'Paused while you look' : 'Paused · click to resume'}
            </button>
          </div>
        )}
        <div aria-live="polite" className="sr-only">
          {announce}
        </div>

        {visible.length === 0 ? (
          <EmptyState
            title={hasFilters ? 'No requests match these filters in this window.' : 'No traffic yet. Point an app at the gateway →'}
            action={
              hasFilters ? (
                <Button variant="outline" size="sm" onClick={clearAll}>
                  Clear filters
                </Button>
              ) : (
                <Button size="sm" render={<a href="/onboarding" />}>
                  Connect an app
                </Button>
              )
            }
          />
        ) : (
          <div
            className="h-full overflow-auto"
            onMouseEnter={() => setHovering(true)}
            onMouseLeave={() => setHovering(false)}
            onFocus={() => setFocusWithin(true)}
            onBlur={(e) => {
              if (!e.currentTarget.contains(e.relatedTarget as Node)) setFocusWithin(false)
            }}
          >
            <table className="w-full border-separate border-spacing-0 text-sm" aria-label="Live traffic" aria-rowcount={matching.length}>
              <thead className="sticky top-0 z-10 bg-header text-xs text-muted-foreground-strong">
                <tr>
                  <th className="sticky left-0 z-10 w-2 border-b border-border bg-header" aria-label="Verdict marker" />
                  <th className="sticky left-2 z-10 border-b border-border bg-header px-(--cell-px) py-2 text-left font-medium">Time</th>
                  <th className="sticky left-[6.5rem] z-10 border-b border-r border-border bg-header px-(--cell-px) py-2 text-left font-medium">Key</th>
                  {shownCols.map((c) => (
                    <th key={c.id} className={cn('border-b border-border px-(--cell-px) py-2 font-medium', c.align === 'right' ? 'text-right' : 'text-left')}>
                      {c.label}
                    </th>
                  ))}
                  <th className="border-b border-border px-(--cell-px) py-2 text-left font-medium">Verdict</th>
                </tr>
              </thead>
              <tbody>
                {visible.map((r) => (
                  <TrafficRow key={r.id} r={r} cols={shownCols.map((c) => c.id)} isNew={r.ts > mountedAt.current} onOpen={() => openReceipt(r.id)} />
                ))}
              </tbody>
            </table>
            <p className="px-6 py-3 text-xs text-muted-foreground">
              Showing the newest {visible.length} of {matching.length.toLocaleString()} matching. Older receipts load as you scroll; the hot window keeps 30 days.
            </p>
          </div>
        )}
      </div>
    </div>
  )
}

function TrafficRow({ r, cols, isNew, onOpen }: { r: Receipt; cols: ColId[]; isNew: boolean; onOpen: () => void }) {
  const meta = verdictMeta[r.verdict]
  const cell = 'h-(--row-h) border-b border-border px-(--cell-px) whitespace-nowrap'
  const pinnedBg = 'bg-canvas group-hover:bg-muted group-focus-visible:bg-muted'
  const blocked = r.verdict === 'blocked'
  return (
    <tr
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onOpen()
        } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
          e.preventDefault()
          const sib = e.key === 'ArrowDown' ? e.currentTarget.nextElementSibling : e.currentTarget.previousElementSibling
          ;(sib as HTMLElement | null)?.focus()
        }
      }}
      aria-label={`${clock(r.ts)} ${r.keyName} ${r.resolvedModel} ${meta.label}`}
      className={cn('group cursor-pointer outline-none hover:bg-muted focus-visible:bg-muted', isNew && 'motion-safe:animate-row-arrive')}
    >
      {/* Verdict is the leftmost signal: a color bar at the row edge. */}
      <td className={cn('sticky left-0 z-[1] w-2 border-b border-border p-0', pinnedBg)}>
        <span
          className={cn('mx-auto block h-[calc(var(--row-h)-6px)] w-1 rounded-full', r.inFlight ? 'border border-dashed border-border-strong' : toneBar[meta.tone])}
          aria-hidden="true"
        />
        {/* Reduced motion: the arrival highlight degrades to a static left-edge marker. */}
        {isNew && <span className="absolute inset-y-0 left-0 hidden w-0.5 bg-primary motion-reduce:block" aria-hidden="true" />}
      </td>
      <td className={cn(cell, 'sticky left-2 z-[1] num font-mono text-xs', pinnedBg)}>{clock(r.ts)}</td>
      <td className={cn(cell, 'sticky left-[6.5rem] z-[1] border-r font-mono text-xs', pinnedBg)}>{r.keyName}</td>
      {cols.map((c) => {
        switch (c) {
          case 'team':
            return (
              <td key={c} className={cn(cell, 'text-xs text-muted-foreground-strong')}>
                {r.team}
              </td>
            )
          case 'model':
            return (
              <td key={c} className={cn(cell, 'font-mono text-xs')}>
                {r.resolvedModel}
                {r.fallbackFrom && (
                  <span className="ml-1 text-v-degraded-fg" title={`Fell back from ${r.fallbackFrom}`}>
                    ↓<span className="sr-only"> fell back from {r.fallbackFrom}</span>
                  </span>
                )}
                {r.requestedModel !== r.resolvedModel && !r.fallbackFrom && (
                  <span className="ml-1 text-muted-foreground" title={`Requested ${r.requestedModel}`}>
                    ← {r.requestedModel}
                  </span>
                )}
              </td>
            )
          case 'backend':
            return (
              <td key={c} className={cn(cell, 'font-mono text-xs text-muted-foreground-strong')}>
                {r.backend}
              </td>
            )
          case 'tokens':
            return (
              <td key={c} className={cn(cell, 'text-right text-xs')}>
                {r.inFlight ? (
                  <span className="num font-mono text-muted-foreground">
                    {(r.inputTokens / 1000).toFixed(1)}k<span title="Output tokens arrive at end of stream">↑</span>
                  </span>
                ) : (
                  <TokenCount value={r.inputTokens + r.outputTokens + r.reasoningTokens} unknown={blocked} />
                )}
              </td>
            )
          case 'cost':
            return (
              <td key={c} className={cn(cell, 'text-right text-xs')}>
                <Money value={r.costUsd} precision="micro" unknown={r.inFlight || blocked} />
              </td>
            )
          case 'ms':
            return (
              <td key={c} className={cn(cell, 'text-right text-xs')}>
                {r.inFlight ? (
                  <span className="num font-mono text-muted-foreground">ttft {r.ttftMs ?? '—'}</span>
                ) : (
                  <Duration ms={r.durationMs} />
                )}
              </td>
            )
          case 'status':
            return (
              <td key={c} className={cn(cell, 'num text-right font-mono text-xs', r.status >= 400 && 'text-v-blocked-fg')}>
                {r.inFlight ? '…' : r.status}
              </td>
            )
        }
      })}
      <td className={cn(cell, 'text-xs')}>
        {r.inFlight ? (
          <span className="inline-flex items-center gap-1 text-muted-foreground">
            <span aria-hidden="true">⋯</span> Streaming
          </span>
        ) : (
          <span className={cn('inline-flex items-center gap-1 font-medium', toneText[meta.tone])}>
            <meta.Icon className="size-3" aria-hidden="true" />
            {meta.label}
          </span>
        )}
      </td>
    </tr>
  )
}

function FilterMenu({
  label,
  values,
  selected,
  onChange,
}: {
  label: string
  values: { value: string; label: string }[]
  selected: string[]
  onChange: (v: string[]) => void
}) {
  const active = selected.length > 0
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger
        variant={active ? 'outline' : 'ghost'}
        className={cn('h-7 gap-1 px-2 text-xs', active && 'border-border-strong bg-card')}
        showExpandIcon
      >
        {label}
        {active && (
          <span className="max-w-40 truncate font-mono text-foreground">
            : {selected.length === 1 ? values.find((v) => v.value === selected[0])?.label : `${selected.length} selected`}
          </span>
        )}
      </DropdownMenuTrigger>
      <DropdownMenuPortal>
        <DropdownMenuContent className="w-56">
          {values.map((v) => (
            <DropdownMenuCheckboxItem
              key={v.value}
              checked={selected.includes(v.value)}
              closeOnClick={false}
              onCheckedChange={(checked) => onChange(checked ? [...selected, v.value] : selected.filter((s) => s !== v.value))}
            >
              <span className="font-mono text-xs">{v.label}</span>
            </DropdownMenuCheckboxItem>
          ))}
          {active && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => onChange([])}>Clear {label.toLowerCase()}</DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenuPortal>
    </DropdownMenu>
  )
}

function DensityPicker({ density, setDensity }: { density: Density; setDensity: (d: Density) => void }) {
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger variant="outline" className="h-7 px-2.5 text-xs" aria-label={`Density: ${density}`}>
        <Rows3 className="size-3.5" /> {density[0].toUpperCase() + density.slice(1)}
      </DropdownMenuTrigger>
      <DropdownMenuPortal>
        <DropdownMenuContent align="end" className="w-44">
          {(['comfortable', 'compact', 'dense'] as Density[]).map((d) => (
            <DropdownMenuCheckboxItem key={d} checked={density === d} onCheckedChange={() => setDensity(d)}>
              {d[0].toUpperCase() + d.slice(1)}
            </DropdownMenuCheckboxItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenuPortal>
    </DropdownMenu>
  )
}
