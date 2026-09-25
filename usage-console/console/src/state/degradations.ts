import { useSyncExternalStore } from 'react'
import { api, dataMode, seedDegradations, type Degradation } from '@/data/catalog'

// One poll of GET /degradations shared by everything that shows them (the
// banner, the notification bell). Mock mode serves the fixtures.

const POLL_MS = 15_000

let items: Degradation[] = seedDegradations
const listeners = new Set<() => void>()
let timer: ReturnType<typeof setInterval> | undefined

function load() {
  // A failed poll keeps the last answer rather than clearing the banner.
  api<Degradation[]>('/degradations')
    .then((d) => {
      items = d
      listeners.forEach((l) => l())
    })
    .catch(() => {})
}

function subscribe(l: () => void) {
  listeners.add(l)
  if (dataMode === 'api' && !timer) {
    load()
    timer = setInterval(load, POLL_MS)
  }
  return () => {
    listeners.delete(l)
    if (listeners.size === 0 && timer) {
      clearInterval(timer)
      timer = undefined
    }
  }
}

/** What's degraded now, worst first. */
export function useDegradations(): Degradation[] {
  return useSyncExternalStore(subscribe, () => items)
}
