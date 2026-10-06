import { ArrowRight, FileText } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Money } from '@/components/gw/numbers'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from '@/components/ui/toast'
import { apiFile, type LiveSavingsOpportunity, type SavingsView } from '@/data/catalog'
import { downloadBlob } from '@/lib/csv'
import { int } from '@/lib/format'
import { useLive } from '@/state/live'

// Api-mode Spend pieces (§7.5.5): the close report download and the savings
// analysis, both from the control plane.

/** This UTC month (to date) and the 12 before it, newest first. */
function closeMonths(now: number) {
  const d = new Date(now)
  let y = d.getUTCFullYear()
  let m = d.getUTCMonth()
  const out: { value: string; label: string }[] = []
  for (let i = 0; i < 13; i++) {
    const label = new Date(Date.UTC(y, m, 1)).toLocaleDateString('en-US', { month: 'long', year: 'numeric', timeZone: 'UTC' })
    out.push({ value: `${y}-${String(m + 1).padStart(2, '0')}`, label: i === 0 ? `${label} (to date)` : label })
    if (--m < 0) {
      m = 11
      y--
    }
  }
  return out
}

/** Month picker and download for GET /spend/close-report. Defaults to last month, the one being closed. */
export function CloseReportControl() {
  const months = useMemo(() => closeMonths(Date.now()), [])
  const [month, setMonth] = useState(months[1].value)
  const [busy, setBusy] = useState(false)
  const download = async () => {
    setBusy(true)
    try {
      const f = await apiFile(`/spend/close-report?month=${month}`)
      downloadBlob(f.name, f.blob)
      toast.add({ title: 'Close report downloaded', description: `${f.name}. The export is recorded on Activity.`, type: 'success' })
    } catch (e) {
      toast.add({ title: 'Close report not made', description: e instanceof Error ? e.message : String(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="flex items-center gap-2">
      <Select items={months} value={month} onValueChange={(v) => v && setMonth(v as string)}>
        <SelectTrigger className="w-48" aria-label="Close report month">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {months.map((m) => (
            <SelectItem key={m.value} value={m.value}>
              {m.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button
        variant="outline"
        onClick={download}
        loading={busy}
        loadingText="Making the report…"
        title="A PDF of the month's spend by team, project, key and model, budgets against their caps, and the prices behind it. Each download is recorded on Activity."
      >
        <FileText /> Download close report
      </Button>
    </div>
  )
}

const emptySavings: SavingsView = { from: 0, to: 0, firstAt: 0, days: 0, outputLimit: 0, served: 0, unpriced: 0, rerouted: 0, opportunities: [] }

function span(days: number) {
  return days >= 29.5 ? 'the last 30 days' : `the last ${days} day${days === 1 ? '' : 's'}`
}

/** "Not counted: 12 with longer answers, …", or '' when every request was counted. */
function notCounted(o: LiveSavingsOpportunity, outputLimit: number) {
  const x = o.excluded
  const parts = [
    x.longOutput && `${int(x.longOutput)} with answers over ${int(outputLimit)} tokens`,
    x.overContext && `${int(x.overContext)} too long for ${o.target}'s context`,
    x.unpriced && `${int(x.unpriced)} with no price`,
    x.targetUnpriced && `${int(x.targetUnpriced)} ${o.target} had no price for`,
    x.keyInactive && `${int(x.keyInactive)} from revoked or expired keys`,
  ].filter(Boolean)
  return parts.length ? `Not counted: ${parts.join(', ')}.` : ''
}

function receiptsHref(o: LiveSavingsOpportunity) {
  const q = new URLSearchParams({ model: o.model })
  for (const k of o.keys) q.append('key', k)
  return `/traffic?${q.toString()}`
}

/** GET /spend/savings, with its method stated. */
export function LiveSavings() {
  const { data: v, loaded } = useLive<SavingsView>('/spend/savings', emptySavings, 300_000)
  if (!loaded) return <Skeleton shape="block" className="h-[160px]" />
  const when = span(v.days)
  return (
    <div className="flex flex-col gap-3">
      <p className="max-w-4xl text-sm text-muted-foreground-strong">
        From {when} of receipts ({int(v.served)} served requests; older ones aren’t kept). A request counts when it had a price, its answer was at most{' '}
        {int(v.outputLimit)} output tokens, and its input and output fit the cheaper model’s context. The cheaper model is priced at the same token counts, at
        its rate on the backend named, in effect when each request started. Quality isn’t measured: try the cheaper model on part of the traffic before moving
        all of it.
        {v.unpriced > 0 && ` ${int(v.unpriced)} requests with no price aren’t counted.`}
        {v.rerouted > 0 && ` ${int(v.rerouted)} that a policy or fallback ran on another model aren’t either.`}
      </p>
      {v.opportunities.length === 0 ? (
        <p className="rounded-md border border-dashed border-border px-4 py-3 text-sm text-muted-foreground-strong">
          No savings found: no group of short requests has a cheaper, priced model in its family that would have cost less.
        </p>
      ) : (
        <ul className="divide-y divide-border rounded-md border border-border bg-card" aria-label="Savings opportunities">
          {v.opportunities.map((o) => (
            <li key={o.id} className="flex flex-wrap items-start justify-between gap-4 px-4 py-3">
              <div className="min-w-0 max-w-3xl">
                <div className="text-base">
                  <Money value={o.savedUsd} precision={o.savedUsd < 1 ? 'micro' : 'cents'} className="font-semibold" /> in {when} if{' '}
                  {o.alias ? (
                    <>
                      <span className="font-mono">{o.alias}</span> moved from <span className="font-mono">{o.model}</span>
                    </>
                  ) : (
                    <>
                      <span className="font-mono">{o.key}</span>’s short <span className="font-mono">{o.model}</span> calls moved
                    </>
                  )}{' '}
                  to <span className="font-mono">{o.target}</span>
                </div>
                <p className="mt-0.5 text-sm text-muted-foreground">
                  {int(o.requests)} of {int(o.served)} requests counted. They cost <Money value={o.actualUsd} precision={o.actualUsd < 1 ? 'micro' : 'cents'} />; on{' '}
                  <span className="font-mono">{o.target}</span> via {o.targetBackend} they’d have cost{' '}
                  <Money value={o.targetUsd} precision={o.targetUsd < 1 ? 'micro' : 'cents'} />. {notCounted(o, v.outputLimit)}{' '}
                  {o.alias
                    ? `Retargeting the alias moves all of its requests, not only the counted ones.`
                    : `The app asks for ${o.model} by name: it has to ask for ${o.target} instead, or go through an alias.`}
                </p>
                {o.notAllowedKeys.length > 0 && (
                  <p className="mt-1 text-sm text-v-degraded-fg">
                    {o.notAllowedKeys.join(', ')} {o.notAllowedKeys.length === 1 ? 'doesn’t' : 'don’t'} allow {o.target} yet: add it on Keys first, or{' '}
                    {o.notAllowedKeys.length === 1 ? 'its' : 'their'} requests would be refused.
                  </p>
                )}
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Button variant="ghost" size="sm" render={<Link to={receiptsHref(o)} />}>
                  View receipts
                </Button>
                {o.alias && (
                  <Button
                    variant="outline"
                    size="sm"
                    render={<Link to={`/models?${new URLSearchParams({ tab: 'aliases', edit: o.alias, target: o.target }).toString()}`} />}
                  >
                    Review alias change <ArrowRight />
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
