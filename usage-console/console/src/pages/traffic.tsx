import { Columns3, FileSignature, Link2, Pause, Play, Plus, RotateCw, Rows3, X } from 'lucide-react'
import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
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
import { toast } from '@/components/ui/toast'
import { ApiError, backends, dataMode, downloadSignedExport, keys, type LiveRoute, liveRoutes, models, projects, type Receipt, teams, type Verdict } from '@/data/catalog'
import { clock } from '@/lib/format'
import { cn } from '@/lib/utils'
import { type Density, rangeLabel, rangeMs, useApp } from '@/state/app-state'
import { type Dim, exportQuery, type Filters, type TrafficWindow, useTrafficFeed } from '@/state/traffic-feed'
import { useLive } from '@/state/live'

// §7.5.3 Traffic — a dense virtualized table over a live stream.

const dims: { dim: Dim; label: string; values: { value: string; label: string }[] }[] = [
  { dim: 'key', label: 'Key', values: keys.map((k) => ({ value: k.name, label: k.name })) },
  { dim: 'team', label: 'Team', values: teams.map((t) => ({ value: t.id, label: t.name })) },
  // By id, labelled with the team, since names repeat across teams.
  {
    dim: 'project',
    label: 'Project',
    values: projects.map((p) => ({ value: p.id, label: `${p.name} · ${teams.find((t) => t.id === p.team)?.name ?? p.team}` })),
  },
  { dim: 'model', label: 'Model', values: models.map((m) => ({ value: m.id, label: m.id })) },
  {
    dim: 'verdict',
    label: 'Verdict',
    values: (Object.keys(verdictMeta) as Verdict[]).map((v) => ({ value: v, label: verdictMeta[v].label })),
  },
  {
    dim: 'provider',
    label: 'Provider',
    values: [...new Set(backends.map((b) => b.provider))].sort().map((p) => ({ value: p, label: p })),
  },
  { dim: 'backend', label: 'Backend', values: backends.map((b) => ({ value: b.name, label: b.name })) },
  {
    dim: 'reason',
    label: 'Route reason',
    values: ['explicit', 'alias', 'policy', 'fallback'].map((p) => ({ value: p, label: p })),
  },
]
const primaryDims: Dim[] = ['key', 'team', 'model', 'verdict']

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

const HOT_WINDOW_MS = 30 * 86_400_000
const BUCKET_MS = 5 * 60_000
const OVERSCAN = 12

/** A short date and time for the window's edges, e.g. "Sep 24, 14:05". */
const edge = (ts: number) => new Date(ts).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false })

/**
 * The list's window: the shared range, narrowed by ?since= (Overview links)
 * and ?day=MM-DD (Spend's bars, UTC days). It starts on a 5-minute bucket so
 * the count from receipts_5m covers the same span as the list, and it's fixed
 * while the page is open so the list doesn't reload under the reader.
 */
function useWindow(range: Parameters<typeof rangeMs>[0], sinceParam: number | null, day: string | null, untilParam: number | null): TrafficWindow {
  return useMemo(() => {
    const now = Date.now()
    let since = Math.floor((now - Math.min(rangeMs(range), HOT_WINDOW_MS)) / BUCKET_MS) * BUCKET_MS
    let before: number | null = null
    if (sinceParam) since = Math.max(since, sinceParam)
    if (untilParam) before = untilParam
    if (day && /^\d{2}-\d{2}$/.test(day)) {
      const [m, d] = day.split('-').map(Number)
      const y = new Date(now).getUTCFullYear()
      let start = Date.UTC(y, m - 1, d)
      if (start > now) start = Date.UTC(y - 1, m - 1, d)
      since = Math.max(since, start)
      before = start + 86_400_000
    }
    return { since, before }
  }, [range, sinceParam, day, untilParam])
}

export function TrafficPage() {
  const { range, openReceipt, density, setDensity } = useApp()
  const [params, setParams] = useSearchParams()
  const [live, setLive] = useState(true)
  const [hovering, setHovering] = useState(false)
  const [focusWithin, setFocusWithin] = useState(false)
  const [frozenTop, setFrozenTop] = useState<number | null>(null)
  const [cols, setCols] = useState<ColId[]>(loadCols)
  const mountedAt = useRef(Date.now())
  const [announce, setAnnounce] = useState('')

  const filterKey = dims.map((d) => d.dim + '=' + params.getAll(d.dim).join(',')).join('&')
  const filters = useMemo(() => {
    const f = {} as Filters
    for (const d of dims) f[d.dim] = params.getAll(d.dim)
    return f
    // filterKey is the filters' identity; params also changes for ?receipt=.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filterKey])
  const since = params.get('since') ? Number(params.get('since')) : null
  const day = params.get('day')
  const until = params.get('until') ? Number(params.get('until')) : null
  const window_ = useWindow(range, since, day, until)
  const feed = useTrafficFeed(filters, window_)
  const capturing = useLive<LiveRoute[]>(dataMode === 'api' ? '/routes' : null, liveRoutes, 60_000).data.filter((r) => r.captureContent)
  const rows = feed.rows

  const frozen = !live || hovering || focusWithin
  // Freeze on interaction: remember the newest visible row and hold the view there.
  useEffect(() => {
    if (frozen && frozenTop === null) setFrozenTop(rows[0]?.ts ?? Date.now())
    if (!frozen && frozenTop !== null) setFrozenTop(null)
  }, [frozen, frozenTop, rows])

  const visible = useMemo(() => (frozenTop !== null ? rows.filter((r) => r.ts <= frozenTop) : rows), [rows, frozenTop])
  const newCount = frozenTop !== null ? rows.length - visible.length : 0

  // Screen-reader announcements are throttled, and silent while paused (§7.7).
  const lastTop = useRef(rows[0]?.id)
  const rowsRef = useRef(rows)
  rowsRef.current = rows
  useEffect(() => {
    const id = window.setInterval(() => {
      const cur = rowsRef.current
      const at = cur.findIndex((r) => r.id === lastTop.current)
      const delta = at === -1 ? cur.length : at
      lastTop.current = cur[0]?.id
      if (!frozen && delta > 0) setAnnounce(`${delta} new requests`)
    }, 15_000)
    return () => window.clearInterval(id)
  }, [frozen])

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
  const shareView = () => {
    // The link carries the range too, so it opens on the same window (§7.4).
    const url = new URL(window.location.href)
    url.searchParams.delete('receipt')
    url.searchParams.set('range', range)
    void navigator.clipboard?.writeText(url.toString())
    toast.add({ title: 'Link to this view copied', description: 'Filters and the time range are part of the link.', type: 'success' })
  }

  const [exporting, setExporting] = useState(false)
  // Every matching receipt in the window, from the database: never the sampled live rows.
  const exportSigned = () => {
    setExporting(true)
    downloadSignedExport(`/receipts/export?${exportQuery(filters, window_)}`)
      .then((ex) =>
        toast.add({
          title: `Exported ${ex.count.toLocaleString()} ${ex.count === 1 ? 'receipt' : 'receipts'}, signed`,
          description: `${ex.filename}: the README inside says how to verify it. The export is recorded in the audit log.`,
          type: 'success',
        }),
      )
      .catch((e: unknown) =>
        toast.add({ title: "The receipts couldn't be exported", description: e instanceof ApiError ? e.message : `${String(e)}. Try again.`, type: 'error' }),
      )
      .finally(() => setExporting(false))
  }

  const activeDims = dims.filter((d) => filters[d.dim].length > 0 || primaryDims.includes(d.dim))
  const extraDims = dims.filter((d) => !primaryDims.includes(d.dim) && filters[d.dim].length === 0)
  const shownCols = allCols.filter((c) => cols.includes(c.id))
  const colIds = useMemo(() => shownCols.map((c) => c.id), [shownCols.map((c) => c.id).join()]) // eslint-disable-line react-hooks/exhaustive-deps
  const hasFilters = Object.values(filters).some((v) => v.length) || since !== null || day !== null
  const hotEdge = Date.now() - HOT_WINDOW_MS

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
            <Button variant="outline" size="sm" onClick={shareView}>
              <Link2 /> Share this view
            </Button>
            {dataMode === 'api' && (
              <Button
                variant="outline"
                size="sm"
                onClick={exportSigned}
                disabled={exporting}
                title="Every receipt matching these filters in this window, as JSON Lines with an Ed25519 signature anyone can verify."
              >
                <FileSignature /> {exporting ? 'Exporting…' : 'Export signed'}
              </Button>
            )}
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
                  {extraDims
                    .filter((d) => d.values.length > 0)
                    .map((d) => (
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
          <span className="ml-auto text-xs text-muted-foreground">
            {feed.count !== null ? (
              <span className="num font-mono">{feed.count.toLocaleString()} matching</span>
            ) : (
              <span
                className="num font-mono"
                title={`Project, model, provider and route reason filters can't be counted from the 5-minute aggregate, so this is what has loaded so far.${feed.wasSampled ? ' Live rows were sampled, so it is less than what happened.' : ''}`}
              >
                {rows.length.toLocaleString()} loaded
              </span>
            )}
          </span>
        </div>

        {/* §7.6: content capture on gets a persistent marker wherever its route's traffic appears. */}
        {capturing.length > 0 && (
          <div role="status" aria-label="Content capture" className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border border-v-degraded-border bg-v-degraded-bg px-3 py-1.5 text-sm text-v-degraded-fg">
            <span className="font-medium">Content capture on</span>
            <span className="text-foreground/80">
              for {capturing.map((r) => r.name).join(', ')}: those requests’ prompts and responses are kept, masked, for 30 days.
            </span>
          </div>
        )}
        {/* §7.5.3 backpressure: never silently drop. Only the live rows are sampled; counts come from the database. */}
        {feed.sampling ? (
          <div role="status" aria-label="Stream sampling" className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border border-v-degraded-border bg-v-degraded-bg px-3 py-1.5 text-sm text-v-degraded-fg">
            <span className="font-medium">
              Sampling 1 in {feed.sampling.oneIn}. Add a filter to see everything matching.
            </span>
            <span className="text-foreground/80">
              About {Math.round(feed.sampling.ratePerSec).toLocaleString()} requests a second match, more than the {feed.sampling.thresholdPerSec} a second this list shows in full. Only new live rows are
              sampled: counts and totals come from the database and stay exact.
            </span>
          </div>
        ) : (
          feed.wasSampled && (
            <div role="status" aria-label="Stream sampling" className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted px-3 py-1.5 text-sm text-muted-foreground-strong">
              <span>The live rows were sampled for a while, so this list has gaps. Every receipt is stored.</span>
              <Button variant="outline" size="xs" className="ml-auto" onClick={feed.reload}>
                <RotateCw /> Reload the list
              </Button>
            </div>
          )
        )}

        {feed.dropped > 0 && (
          <div role="status" className="flex flex-wrap items-center gap-2 rounded-md border border-v-degraded-border bg-v-degraded-bg px-3 py-1.5 text-sm text-v-degraded-fg">
            <span className="font-medium">Missed {feed.dropped.toLocaleString()} receipts while the stream was behind.</span>
            <span className="text-foreground/80">They're stored, but not in this list. Reload it to see everything, or add a filter to slow the stream.</span>
            <Button variant="outline" size="xs" className="ml-auto" onClick={feed.reload}>
              <RotateCw /> Reload the list
            </Button>
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
              {newCount > 0
                ? `${newCount} new${feed.sampling ? ` (sampled 1 in ${feed.sampling.oneIn})` : ''} · click to resume`
                : live
                  ? 'Paused while you look'
                  : 'Paused · click to resume'}
            </button>
          </div>
        )}
        <div aria-live="polite" className="sr-only">
          {announce}
        </div>

        {feed.loaded && visible.length === 0 ? (
          <EmptyState
            title={hasFilters ? 'No requests match these filters in this window.' : `No traffic ${rangeLabel(range)}. Point an app at the gateway →`}
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
          <VirtualTable
            rows={visible}
            loaded={feed.loaded}
            total={feed.count}
            cols={shownCols}
            colIds={colIds}
            mountedAt={mountedAt.current}
            onOpen={openReceipt}
            onNearEnd={feed.loadOlder}
            onHover={setHovering}
            onFocusWithin={setFocusWithin}
            footer={
              feed.loadingOlder
                ? 'Loading older receipts…'
                : !feed.reachedEnd
                  ? 'Older receipts load as you scroll.'
                  : window_.since <= hotEdge + BUCKET_MS
                    ? `That's everything back to the edge of the 30-day hot window (${edge(hotEdge)}). Older traffic keeps aggregates only.`
                    : `That's everything since ${edge(window_.since)}. Widen the time range to see older requests.`
            }
          />
        )}
      </div>
    </div>
  )
}

/**
 * The table, rendering only the rows in view (§10: 2,000 rows/min). Rows are
 * one fixed height per density, so the window is arithmetic, and a spacer row
 * above and below stands in for the rest.
 */
function VirtualTable({
  rows,
  loaded,
  total,
  cols,
  colIds,
  mountedAt,
  onOpen,
  onNearEnd,
  onHover,
  onFocusWithin,
  footer,
}: {
  rows: Receipt[]
  loaded: boolean
  total: number | null
  cols: typeof allCols
  colIds: ColId[]
  mountedAt: number
  onOpen: (id: string) => void
  onNearEnd: () => void
  onHover: (h: boolean) => void
  onFocusWithin: (f: boolean) => void
  footer: string
}) {
  const scroller = useRef<HTMLDivElement>(null)
  const [view, setView] = useState({ top: 0, height: 800 })
  const [rowH, setRowH] = useState(28)
  const focusNext = useRef<number | null>(null)

  // Viewport size, and the row height from the density's --row-h.
  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    const ro = new ResizeObserver(() => setView({ top: el.scrollTop, height: el.clientHeight }))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  useLayoutEffect(() => {
    const h = scroller.current?.querySelector<HTMLElement>('tbody tr[data-index]')?.offsetHeight
    if (h && h !== rowH) setRowH(h)
  })

  // New rows on top don't push what the reader is looking at down: when
  // scrolled, keep the same first row in view.
  const firstId = useRef<string | undefined>(undefined)
  useLayoutEffect(() => {
    const el = scroller.current
    const prev = firstId.current
    firstId.current = rows[0]?.id
    if (!el || !prev || el.scrollTop === 0 || rows[0]?.id === prev) return
    const shift = rows.findIndex((r) => r.id === prev)
    if (shift > 0) el.scrollTop += shift * rowH
  }, [rows, rowH])

  const start = Math.max(0, Math.floor(view.top / rowH) - OVERSCAN)
  const end = Math.min(rows.length, Math.ceil((view.top + view.height) / rowH) + OVERSCAN)

  useEffect(() => {
    if (loaded && end >= rows.length - OVERSCAN) onNearEnd()
  }, [loaded, end, rows.length, onNearEnd])

  // Arrow keys move through every row, not just the rendered ones.
  useLayoutEffect(() => {
    const n = focusNext.current
    if (n === null) return
    const tr = scroller.current?.querySelector<HTMLElement>(`tr[data-index="${n}"]`)
    if (tr) {
      focusNext.current = null
      tr.focus()
    }
  })
  const move = (from: number, by: number) => {
    const el = scroller.current
    const n = from + by
    if (!el || n < 0 || n >= rows.length) return
    const head = el.querySelector('thead')?.clientHeight ?? 0
    const y = n * rowH
    if (y < el.scrollTop) el.scrollTop = y
    else if (y + rowH > el.scrollTop + el.clientHeight - head) el.scrollTop = y + rowH - el.clientHeight + head
    focusNext.current = n
    setView({ top: el.scrollTop, height: el.clientHeight })
  }

  return (
    <div
      ref={scroller}
      className="h-full overflow-auto"
      onScroll={(e) => setView({ top: e.currentTarget.scrollTop, height: e.currentTarget.clientHeight })}
      onMouseEnter={() => onHover(true)}
      onMouseLeave={() => onHover(false)}
      onFocus={() => onFocusWithin(true)}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) onFocusWithin(false)
      }}
    >
      <table className="w-full border-separate border-spacing-0 text-sm" aria-label="Live traffic" aria-rowcount={total !== null ? Math.max(total, rows.length) + 1 : -1} aria-busy={!loaded}>
        <thead className="sticky top-0 z-10 bg-header text-xs text-muted-foreground-strong">
          <tr aria-rowindex={1}>
            <th className="sticky left-0 z-10 w-2 border-b border-border bg-header" aria-label="Verdict marker" />
            <th className="sticky left-2 z-10 border-b border-border bg-header px-(--cell-px) py-2 text-left font-medium">Time</th>
            <th className="sticky left-[6.5rem] z-10 border-b border-r border-border bg-header px-(--cell-px) py-2 text-left font-medium">Key</th>
            {cols.map((c) => (
              <th key={c.id} className={cn('border-b border-border px-(--cell-px) py-2 font-medium', c.align === 'right' ? 'text-right' : 'text-left')}>
                {c.label}
              </th>
            ))}
            <th className="border-b border-border px-(--cell-px) py-2 text-left font-medium">Verdict</th>
          </tr>
        </thead>
        <tbody>
          {!loaded
            ? // Skeleton rows at the final row height, so nothing shifts when data lands (§7.6).
              Array.from({ length: 12 }, (_, i) => (
                <tr key={i} data-index={i} aria-hidden="true">
                  <td colSpan={cols.length + 4} className="h-(--row-h) border-b border-border px-(--cell-px)">
                    <span className="block h-2.5 w-full max-w-3xl animate-pulse rounded-sm bg-muted motion-reduce:animate-none" />
                  </td>
                </tr>
              ))
            : (
              <>
                {start > 0 && <tr aria-hidden="true" style={{ height: start * rowH }} />}
                {rows.slice(start, end).map((r, i) => (
                  <TrafficRow key={r.id} r={r} index={start + i} cols={colIds} isNew={r.ts > mountedAt} onOpen={onOpen} onMove={move} />
                ))}
                {end < rows.length && <tr aria-hidden="true" style={{ height: (rows.length - end) * rowH }} />}
              </>
            )}
        </tbody>
      </table>
      {loaded && <p className="px-6 py-3 text-xs text-muted-foreground">{footer}</p>}
    </div>
  )
}

const TrafficRow = memo(function TrafficRow({
  r,
  index,
  cols,
  isNew,
  onOpen,
  onMove,
}: {
  r: Receipt
  index: number
  cols: ColId[]
  isNew: boolean
  onOpen: (id: string) => void
  onMove: (from: number, by: number) => void
}) {
  const meta = verdictMeta[r.verdict]
  const cell = 'h-(--row-h) border-b border-border px-(--cell-px) whitespace-nowrap'
  const pinnedBg = 'bg-canvas group-hover:bg-muted group-focus-visible:bg-muted'
  const blocked = r.verdict === 'blocked' || r.verdict === 'throttled' // refused before the upstream call
  return (
    <tr
      tabIndex={0}
      data-index={index}
      aria-rowindex={index + 2}
      onClick={() => onOpen(r.id)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onOpen(r.id)
        } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
          e.preventDefault()
          onMove(index, e.key === 'ArrowDown' ? 1 : -1)
        }
      }}
      aria-label={`${clock(r.ts)} ${r.keyName} ${r.resolvedModel} ${r.inFlight ? 'streaming' : meta.label}`}
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
                  // Input may be known while streaming; output arrives at the end.
                  <span className="text-muted-foreground" title="Output tokens arrive at the end of the stream">
                    <TokenCount value={r.inputTokens} unknown={!r.inputTokens} />
                    {r.inputTokens > 0 && <span aria-label=" input so far">↑</span>}
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
                  <span className="text-muted-foreground">
                    ttft <Duration ms={r.ttftMs ?? 0} unknown={r.ttftMs == null} />
                  </span>
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
})

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
