import { useCallback, useEffect, useState } from 'react'
import { api, dataMode } from '@/data/catalog'

/**
 * A control-plane read kept fresh by polling. In mock mode, or with a null
 * path, it returns `initial` untouched. A failed poll keeps the last answer.
 * `loaded` is false until the first answer for the current path arrives.
 */
export function useLive<T>(path: string | null, initial: T, pollMs = 30_000): { data: T; loaded: boolean; reload: () => void } {
  const [state, setState] = useState<{ path: string | null; data: T; loaded: boolean }>({ path, data: initial, loaded: dataMode !== 'api' })
  // Bumped by reload() to fetch now, after a write, instead of on the next poll.
  const [nonce, setNonce] = useState(0)
  const reload = useCallback(() => setNonce((n) => n + 1), [])
  useEffect(() => {
    if (dataMode !== 'api' || !path) return
    let live = true
    const load = () =>
      api<T>(path)
        .then((data) => live && setState({ path, data, loaded: true }))
        .catch(() => {})
    load()
    const t = setInterval(load, pollMs)
    return () => {
      live = false
      clearInterval(t)
    }
  }, [path, pollMs, nonce])
  // A new path shows `initial` until its first answer, not the old path's data.
  if (state.path !== path && dataMode === 'api') return { data: initial, loaded: false, reload }
  return { data: state.data, loaded: state.loaded, reload }
}

/** The current time, re-read every `ms`, for "3s ago" labels that stay true. */
export function useNow(ms = 5_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms)
    return () => clearInterval(t)
  }, [ms])
  return now
}
