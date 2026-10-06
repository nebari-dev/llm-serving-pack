import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { api, dataMode, keys, type Receipt } from '@/data/catalog'
import { useLive } from './live'
import { receiptStream } from './app-state'

// The Traffic list (§7.5.3). In api mode the filters are the server query:
// GET /receipts pages older rows with ?before=, GET /stream/traffic brings new
// ones with the same filters, and GET /receipts/count gives the matching total
// from receipts_5m when the aggregate can answer. In mock mode it filters the
// simulated stream in the browser, as before.

export type Dim = 'key' | 'team' | 'project' | 'model' | 'verdict' | 'provider' | 'backend' | 'reason'
export type Filters = Record<Dim, string[]>

export interface TrafficWindow {
  /** Epoch ms, inclusive. */
  since: number
  /** Epoch ms, exclusive; null = open-ended (live). */
  before: number | null
}

export interface TrafficFeed {
  rows: Receipt[]
  /** False until the first page for this query arrives. */
  loaded: boolean
  loadingOlder: boolean
  /** No older rows in the window (or the hot window). */
  reachedEnd: boolean
  loadOlder: () => void
  /** Matching settled receipts in the window, or null when it can't be counted exactly. */
  count: number | null
  /** Receipts the server dropped from this stream because the console fell behind. */
  dropped: number
  /** Fetches the list again from the top, e.g. after drops. */
  reload: () => void
}

const PAGE = 200
// Rows kept in memory. Past this the oldest go, and scrolling fetches them again.
const MAX_ROWS = 10_000

function matches(r: Receipt, f: Filters, w: TrafficWindow) {
  if (r.ts < w.since) return false
  if (w.before !== null && r.ts >= w.before) return false
  // Projects filter by id (§5.1): two teams' "helpdesk" are two projects.
  if (f.project.length && !f.project.includes(r.projectId ?? '')) return false
  if (f.key.length && !f.key.includes(r.keyName)) return false
  if (f.team.length && !f.team.includes(r.team)) return false
  if (f.model.length && !f.model.includes(r.resolvedModel) && !f.model.includes(r.requestedModel)) return false
  if (f.verdict.length && !f.verdict.includes(r.verdict)) return false
  if (f.provider.length && !f.provider.includes(r.provider)) return false
  if (f.backend.length && !f.backend.includes(r.backend)) return false
  if (f.reason.length && !f.reason.includes(r.routeReason)) return false
  return true
}

/** The query string for /receipts, /receipts/count and /stream/traffic. The URL names keys; the server wants ids. */
function queryString(f: Filters, w: TrafficWindow | null) {
  const q = new URLSearchParams()
  if (w) {
    q.set('since', String(w.since))
    if (w.before !== null) q.set('before', String(w.before))
  }
  for (const [dim, vals] of Object.entries(f) as [Dim, string[]][]) {
    for (const v of vals) q.append(dim, dim === 'key' ? (keys.find((k) => k.name === v)?.id ?? v) : v)
  }
  return q
}

export function useTrafficFeed(filters: Filters, window: TrafficWindow): TrafficFeed {
  const api_ = useApiFeed(dataMode === 'api' ? filters : null, window)
  const mock = useMockFeed(dataMode === 'api' ? null : filters, window)
  return dataMode === 'api' ? api_ : mock
}

const none: Receipt[] = []
const noSubscribe = () => () => {}

function useMockFeed(filters: Filters | null, w: TrafficWindow): TrafficFeed {
  // Only subscribed in mock mode: in api mode the global stream would re-render the page on every receipt.
  const all = useSyncExternalStore(filters ? receiptStream.subscribe : noSubscribe, filters ? receiptStream.getSnapshot : () => none)
  const rows = useMemo(() => (filters ? all.filter((r) => matches(r, filters, w)) : []), [all, filters, w])
  return { rows, loaded: true, loadingOlder: false, reachedEnd: true, loadOlder: () => {}, count: rows.length, dropped: 0, reload: () => {} }
}

function useApiFeed(filters: Filters | null, w: TrafficWindow): TrafficFeed {
  const key = filters ? queryString(filters, w).toString() : null
  const [state, setState] = useState<{ key: string | null; rows: Receipt[]; loaded: boolean; reachedEnd: boolean }>({
    key: null,
    rows: [],
    loaded: false,
    reachedEnd: false,
  })
  const [loadingOlder, setLoadingOlder] = useState(false)
  const [dropped, setDropped] = useState(0)
  const [nonce, setNonce] = useState(0)
  const keyRef = useRef(key)
  keyRef.current = key

  // First page, then the live stream, whenever the query changes.
  useEffect(() => {
    if (!filters || key === null) return
    let live = true
    setDropped(0)
    setState({ key, rows: [], loaded: false, reachedEnd: false })
    api<Receipt[]>(`/receipts?limit=${PAGE}&${key}`)
      .then((rows) => {
        if (!live) return
        for (const r of rows) receiptStream.remember(r)
        setState((s) => (s.key === key ? { ...s, rows: mergeNewer(s.rows, rows), loaded: true, reachedEnd: rows.length < PAGE } : s))
      })
      .catch(() => live && setState((s) => (s.key === key ? { ...s, loaded: true } : s)))

    // A day in the past has no live tail.
    if (w.before !== null && w.before <= Date.now()) return () => void (live = false)
    // The tab's one receipt stream, narrowed to these filters and already
    // batched. It ignores the window, so a window that ends later (today, a
    // Spend bar) stops taking rows once it closes.
    const untail = receiptStream.tail(queryString(filters, null).toString(), {
      onReceipts: (batch) => {
        const inWindow = batch.filter((r) => r.ts >= w.since && (w.before === null || r.ts < w.before))
        if (!live || !inWindow.length) return
        setState((s) => {
          if (s.key !== key) return s
          const rows = applyLive(s.rows, inWindow)
          // Trimmed rows can be fetched again by scrolling.
          return { ...s, rows, reachedEnd: s.reachedEnd && rows.length < MAX_ROWS }
        })
      },
      onDropped: (count) => live && setDropped((d) => d + count),
    })
    return () => {
      live = false
      untail()
    }
    // key covers filters and the window.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, nonce])

  const current = state.key === key ? state : { rows: [], loaded: false, reachedEnd: false }
  const oldest = current.rows.at(-1)?.ts
  const loadOlder = useCallback(() => {
    if (!filters || loadingOlder || current.reachedEnd || !current.loaded || oldest === undefined) return
    const k = key
    setLoadingOlder(true)
    // before is exclusive, so ask from oldest + 1ms and drop what we have:
    // rows sharing the oldest millisecond aren't skipped.
    const q = queryString(filters, { since: w.since, before: oldest + 1 })
    api<Receipt[]>(`/receipts?limit=${PAGE}&${q}`)
      .then((rows) => {
        for (const r of rows) receiptStream.remember(r)
        setState((s) => {
          if (s.key !== k) return s
          const seen = new Set(s.rows.map((r) => r.id))
          const older = rows.filter((r) => !seen.has(r.id))
          return { ...s, rows: [...s.rows, ...older], reachedEnd: rows.length < PAGE || older.length === 0 }
        })
      })
      .catch(() => {})
      .finally(() => keyRef.current === k && setLoadingOlder(false))
  }, [filters, loadingOlder, current.reachedEnd, current.loaded, oldest, key, w.since])

  const count = useLive<{ count: number | null } | null>(key !== null ? `/receipts/count?${key}` : null, null, 15_000).data?.count ?? null

  return {
    rows: current.rows,
    loaded: current.loaded,
    loadingOlder,
    reachedEnd: current.reachedEnd,
    loadOlder,
    count,
    dropped,
    reload: () => setNonce((n) => n + 1),
  }
}

/** The first page can race the stream; keep whichever copy is already there. */
function mergeNewer(have: Receipt[], page: Receipt[]) {
  if (!have.length) return page
  const seen = new Set(have.map((r) => r.id))
  return [...have, ...page.filter((r) => !seen.has(r.id))].sort((a, b) => b.ts - a.ts)
}

/** Settles known rows in place and puts new ones on top, newest first. */
function applyLive(rows: Receipt[], batch: Receipt[]) {
  if (!batch.length) return rows
  const updates = new Map<string, Receipt>()
  for (const r of batch) updates.set(r.id, r)
  let next = rows.map((r) => {
    const u = updates.get(r.id)
    if (!u) return r
    updates.delete(r.id)
    return u
  })
  const fresh = [...updates.values()].sort((a, b) => b.ts - a.ts)
  if (fresh.length) next = [...fresh, ...next]
  return next.length > MAX_ROWS ? next.slice(0, MAX_ROWS) : next
}
