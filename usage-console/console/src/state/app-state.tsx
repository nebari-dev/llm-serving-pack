import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { useSearchParams } from 'react-router-dom'
import { makeReceipt, type Receipt, seedReceipts } from '@/data/mock'

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
  private timer?: number
  byId = new Map<string, Receipt>(seedReceipts.map((r) => [r.id, r]))

  subscribe = (l: Listener) => {
    this.listeners.add(l)
    if (!this.timer) this.start()
    return () => {
      this.listeners.delete(l)
    }
  }

  getSnapshot = () => this.rows

  private emit() {
    for (const l of this.listeners) l()
  }

  private start() {
    const tick = () => {
      const streaming = Math.random() < 0.35
      const r = makeReceipt(Date.now(), Math.random, { inFlight: streaming })
      this.push(r)
      if (streaming) {
        // Tokens arrive last (§13): settle the row in place when the stream ends.
        window.setTimeout(() => this.settle(r.id), 1800 + Math.random() * 2600)
      }
      this.timer = window.setTimeout(tick, 700 + Math.random() * 1400)
    }
    this.timer = window.setTimeout(tick, 900)
  }

  private push(r: Receipt) {
    this.byId.set(r.id, r)
    this.rows = [r, ...this.rows].slice(0, 600)
    this.emit()
  }

  private settle(id: string) {
    const r = this.byId.get(id)
    if (!r) return
    const settled = { ...r, inFlight: false }
    this.byId.set(id, settled)
    this.rows = this.rows.map((x) => (x.id === id ? settled : x))
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
  const [env, setEnv] = usePersisted<Env>('gw:env', 'production')
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
