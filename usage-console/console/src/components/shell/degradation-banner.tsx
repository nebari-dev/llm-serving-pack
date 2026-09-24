import { TriangleAlert } from 'lucide-react'
import { Link } from 'react-router-dom'

// §7.4: one banner slot, ranked by severity. Fail-open is persistent and
// non-dismissible while active (§7.6), so there is no close button.

const conditions = [
  {
    severity: 2,
    title: 'Policy cost-guard-opus is running fail-open.',
    body: 'Warden cannot reach the control plane for anthropic-prod routing hints; requests pass without the cost guard. Cache age 4m 12s.',
    to: '/guardrails',
    action: 'Review policy',
  },
  {
    severity: 1,
    title: 'anthropic-prod is failing over to bedrock-eu.',
    body: '8% of Claude traffic since 13:51. Upstream is returning 529 overloaded.',
    to: '/routing',
    action: 'View backend',
  },
]

export function DegradationBanner() {
  const [top, ...rest] = [...conditions].sort((a, b) => b.severity - a.severity)
  if (!top) return null
  return (
    <div role="status" className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-v-degraded-border bg-v-degraded-bg px-6 py-2 text-sm text-v-degraded-fg">
      <TriangleAlert className="size-4 shrink-0" aria-hidden="true" />
      <span className="font-medium">{top.title}</span>
      <span className="text-foreground/80">{top.body}</span>
      <span className="ml-auto flex items-center gap-3">
        {rest.length > 0 && <span className="text-xs">+{rest.length} more degraded</span>}
        <Link to={top.to} className="font-medium underline underline-offset-4">
          {top.action}
        </Link>
      </span>
    </div>
  )
}
