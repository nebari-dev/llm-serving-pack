import { TriangleAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, dataMode, seedDegradations, type Degradation } from '@/data/catalog'

// §7.4: one banner slot, ranked by severity. Fail-open is persistent and
// non-dismissible while active (§7.6), so there is no close button.

const POLL_MS = 15_000

/** What's degraded now: the control plane's view in api mode, fixtures in mock mode. */
function useDegradations() {
  const [items, setItems] = useState<Degradation[]>(seedDegradations)
  useEffect(() => {
    if (dataMode !== 'api') return
    let live = true
    // A failed poll keeps the last answer rather than clearing the banner.
    const load = () =>
      api<Degradation[]>('/degradations')
        .then((d) => live && setItems(d))
        .catch(() => {})
    load()
    const t = setInterval(load, POLL_MS)
    return () => {
      live = false
      clearInterval(t)
    }
  }, [])
  return items
}

export function DegradationBanner() {
  const [top, ...rest] = [...useDegradations()].sort((a, b) => b.severity - a.severity)
  if (!top) return null
  return (
    <div role="status" className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-v-degraded-border bg-v-degraded-bg px-6 py-2 text-sm text-v-degraded-fg">
      <TriangleAlert className="size-4 shrink-0" aria-hidden="true" />
      <span className="font-medium">{top.title}</span>
      <span className="text-foreground/80">{top.detail}</span>
      <span className="ml-auto flex items-center gap-3">
        {rest.length > 0 && <span className="text-xs">+{rest.length} more degraded</span>}
        <Link to={top.to} className="font-medium underline underline-offset-4">
          {top.action}
        </Link>
      </span>
    </div>
  )
}
