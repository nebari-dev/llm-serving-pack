import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { useSearchParams } from 'react-router-dom'
import { API_BASE, api, dataMode, type Receipt, seedReceipts, session } from '@/data/catalog'
import { makeReceipt } from '@/data/mock'

// Global console state (§7.4): one time range shared across surfaces, the
// tenant/environment, density, and the live receipt stream.

export type TimeRange = '15m' | '1h' | '6h' | '24h' | '7d' | '30d'
export const timeRanges: { value: TimeRange; label: string }[] = [
  { value: '15m', label: 'Last 15 minutes' },
  { value: '1h', label: 'Last 1 hour' },
  { value: '6h', label: 'Last 6 hours' },
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
]

export type Env = 'production' | 'staging'
export type Density = 'comfortable' | 'compact' | 'dense'

function usePersisted<T extends string>(key: string, initial: T): [T, (v: T) => void] {
  const [value, setValue] = useState<T>(() => {
    try {
      return (localStorage.getItem(key) as T) ?? initial
    } catch {
      return initial
    }
  })
  const set = useCallback(
    (v: T) => {
      setValue(v)
      try {
        localStorage.setItem(key, v)
      } catch {
        /* storage unavailable */
      }
    },
    [key],
  )
  return [value, set]
}

// ---- receipt stream store ------------------------------------------------

type Listener = () => void

class ReceiptStream {
  private rows: Receipt[] = seedReceipts
  private listeners = new Set<Listener>()
  private started = false
  private pending = new Set<string>()
  byId = new Map<string, Receipt>(seedReceipts.map((r) => [r.id, r]))

  subscribe = (l: Listener) => {
    this.listeners.add(l)
    if (!this.started) {
      this.started = true
      if (dataMode === 'api') this.connect()
      else this.simulate()
    }
    return () => {
      this.listeners.delete(l)
    }
  }

  getSnapshot = () => this.rows

  private emit() {
    for (const l of this.listeners) l()
  }

  // Live receipts from the control plane (§6 /stream/traffic). The same id
  // arrives twice for streamed requests: in flight, then settled. EventSource
  // reconnects on its own after a drop.
  private connect() {
    const es = new EventSource(`${API_BASE}/stream/traffic`)
    es.addEventListener('receipt', (e) => this.upsert(JSON.parse((e as MessageEvent<string>).data) as Receipt))
  }

  /** Loads a receipt outside the live window (deep links) into byId. */
  ensure(id: string) {
    if (dataMode !== 'api' || this.byId.has(id) || this.pending.has(id)) return
    this.pending.add(id)
    api<Receipt>(`/receipts/${encodeURIComponent(id)}`)
      .then((r) => {
        this.byId.set(r.id, r)
        // Not added to the live rows, but subscribers compare snapshots by
        // identity, so hand out a new array to make readers of byId re-render.
        this.rows = this.rows.slice()
        this.emit()
      })
      .catch(() => {})
      .finally(() => this.pending.delete(id))
  }

  private simulate() {
    const tick = () => {
      const streaming = Math.random() < 0.35
      const r = makeReceipt(Date.now(), Math.random, { inFlight: streaming })
      this.upsert(r)
      if (streaming) {
        // Tokens arrive last (§13): settle the row in place when the stream ends.
        window.setTimeout(() => {
          const cur = this.byId.get(r.id)
          if (cur) this.upsert({ ...cur, inFlight: false })
        }, 1800 + Math.random() * 2600)
      }
      window.setTimeout(tick, 700 + Math.random() * 1400)
    }
    window.setTimeout(tick, 900)
  }

  private upsert(r: Receipt) {
    const known = this.byId.has(r.id)
    this.byId.set(r.id, r)
    this.rows = known ? this.rows.map((x) => (x.id === r.id ? r : x)) : [r, ...this.rows].slice(0, 600)
    this.emit()
  }
}

export const receiptStream = new ReceiptStream()

export function useReceipts() {
  return useSyncExternalStore(receiptStream.subscribe, receiptStream.getSnapshot)
}

// ---- context -------------------------------------------------------------

interface AppState {
  range: TimeRange
  setRange: (r: TimeRange) => void
  env: Env
  setEnv: (e: Env) => void
  density: Density
  setDensity: (d: Density) => void
  openReceipt: (id: string) => void
  closeReceipt: () => void
  receiptId: string | null
  paletteOpen: boolean
  setPaletteOpen: (o: boolean) => void
}

const Ctx = createContext<AppState | null>(null)

export function AppStateProvider({ children }: { children: ReactNode }) {
  const [range, setRange] = usePersisted<TimeRange>('gw:range', '24h')
  const [envChoice, setEnv] = usePersisted<Env>('gw:env', 'production')
  // Against the control plane there's no choice: it serves one environment,
  // and anything but production gets the staging treatment.
  const env: Env = dataMode === 'api' ? (session.environment === 'production' ? 'production' : 'staging') : envChoice
  const [density, setDensity] = usePersisted<Density>('gw:density', 'dense')
  const [params, setParams] = useSearchParams()
  const [paletteOpen, setPaletteOpen] = useState(false)
  const paramsRef = useRef(params)
  paramsRef.current = params

  // Receipt drawer is deep-linkable (§7.5.4): ?receipt=<id>
  const receiptId = params.get('receipt')
  const openReceipt = useCallback(
    (id: string) => {
      const next = new URLSearchParams(paramsRef.current)
      next.set('receipt', id)
      setParams(next)
    },
    [setParams],
  )
  const closeReceipt = useCallback(() => {
    const next = new URLSearchParams(paramsRef.current)
    next.delete('receipt')
    setParams(next)
  }, [setParams])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const value = useMemo(
    () => ({
      range,
      setRange,
      env,
      setEnv,
      density,
      setDensity,
      openReceipt,
      closeReceipt,
      receiptId,
      paletteOpen,
      setPaletteOpen,
    }),
    [range, setRange, env, setEnv, density, setDensity, openReceipt, closeReceipt, receiptId, paletteOpen],
  )
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function useApp() {
  const v = useContext(Ctx)
  if (!v) throw new Error('useApp must be used within AppStateProvider')
  return v
}

export function rangeLabel(r: TimeRange) {
  return timeRanges.find((x) => x.value === r)?.label.replace('Last ', 'last ') ?? r
}
