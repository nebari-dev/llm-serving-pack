import { History, Lock } from 'lucide-react'
import { useState } from 'react'
import { DiffView } from '@/components/gw/diff-view'
import { Section } from '@/components/gw/page'
import { StateChip } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { toast } from '@/components/ui/toast'
import { cn } from '@/lib/utils'
import { lineDiff } from './guardrails-model'

// §7.5.7: a published version is immutable, diffable against the previous,
// and rollback is one action. Rollback publishes a new version whose rules
// equal the chosen one; history is never rewritten.

const base = ['rule: no-pii-out', 'fail_mode: closed', 'when:', '  all:']
const versions = [
  {
    v: 7,
    by: 'priya@acme.dev',
    at: '2026-09-18 14:02',
    mode: 'enforce',
    note: 'Route matching traffic to eu-private',
    lines: [...base, '    - prompt contains entity [email, SSN]', '    - key.team is not [security]', 'then:', '  - redact [email, SSN] rehydrate', '  - reroute to eu-private'],
  },
  {
    v: 6,
    by: 'marco@acme.dev',
    at: '2026-09-02 09:41',
    mode: 'enforce',
    note: 'Exempt the security team',
    lines: [...base, '    - prompt contains entity [email, SSN]', '    - key.team is not [security]', 'then:', '  - redact [email, SSN] rehydrate'],
  },
  {
    v: 5,
    by: 'marco@acme.dev',
    at: '2026-08-21 16:10',
    mode: 'enforce',
    note: 'Promoted from monitor after 7 days',
    lines: [...base, '    - prompt contains entity [email, SSN]', 'then:', '  - redact [email, SSN] rehydrate'],
  },
  {
    v: 4,
    by: 'marco@acme.dev',
    at: '2026-08-14 11:25',
    mode: 'monitor',
    note: 'Add SSN',
    lines: [...base, '    - prompt contains entity [email, SSN]', 'then:', '  - redact [email, SSN] rehydrate'],
  },
  {
    v: 3,
    by: 'dana@acme.dev',
    at: '2026-08-01 10:03',
    mode: 'monitor',
    note: 'Turn on rehydration',
    lines: [...base, '    - prompt contains entity [email]', 'then:', '  - redact [email] rehydrate'],
  },
]

export function VersionsTab() {
  const [sel, setSel] = useState(7)
  const [confirm, setConfirm] = useState<number | null>(null)
  const [head, setHead] = useState(7)
  const [rolledTo, setRolledTo] = useState<number | null>(null)
  const cur = versions.find((x) => x.v === sel)!
  const prev = versions.find((x) => x.v === sel - 1)

  return (
    <div className="grid min-h-0 lg:grid-cols-[22rem_1fr]">
      <Section title="no-pii-out" description="Published versions are immutable." className="border-b lg:border-r lg:border-b-0">
        <ol className="flex flex-col divide-y divide-border border-y border-border">
          {head > 7 && (
            <li className="flex items-center gap-2 px-2 py-2 text-sm">
              <span className="num w-8 font-mono font-semibold">v{head}</span>
              <span className="flex-1 text-muted-foreground-strong">Rollback to v{rolledTo} · priya@acme.dev · just now</span>
              <StateChip tone="allowed">Live</StateChip>
            </li>
          )}
          {versions.map((x) => (
            <li key={x.v}>
              <button
                type="button"
                onClick={() => setSel(x.v)}
                aria-current={sel === x.v || undefined}
                className={cn(
                  'flex w-full items-start gap-2 px-2 py-2 text-left text-sm hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none focus-visible:ring-inset',
                  sel === x.v && 'bg-muted',
                )}
              >
                <span className="num w-8 font-mono font-semibold">v{x.v}</span>
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate">{x.note}</span>
                  <span className="text-xs text-muted-foreground">
                    {x.by} · {x.at}
                  </span>
                </span>
                {x.v === 7 && head === 7 && <StateChip tone="allowed">Live</StateChip>}
                {x.mode === 'monitor' && <StateChip tone="neutral">Monitor</StateChip>}
              </button>
            </li>
          ))}
        </ol>
      </Section>
      <Section
        title={`v${cur.v} against v${prev ? prev.v : '—'}`}
        description={
          <span className="inline-flex items-center gap-1.5">
            <Lock className="size-3.5" aria-hidden="true" /> Published {cur.at} by {cur.by}, in {cur.mode} mode.
          </span>
        }
        actions={
          cur.v < 7 && head === 7 && (
            <Button variant="outline" size="sm" onClick={() => setConfirm(cur.v)}>
              <History /> Roll back to v{cur.v}
            </Button>
          )
        }
      >
        {prev ? <DiffView diff={lineDiff(prev.lines, cur.lines)} title={`policy/no-pii-out  v${prev.v} → v${cur.v}`} /> : <p className="text-sm text-muted-foreground">First recorded version.</p>}
        {cur.v === 7 && <p className="mt-3 text-sm text-muted-foreground">To undo this change, pick v6 on the left and roll back to it.</p>}
      </Section>

      <Dialog open={confirm !== null} onOpenChange={(o) => !o && setConfirm(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Roll back no-pii-out to v{confirm}?</DialogTitle>
            <DialogDescription>
              This publishes v{head + 1} with v{confirm}'s rules. Traffic from agents-prod and support-bot stops routing to eu-private. v7 stays in history and you can return to it the same way.
            </DialogDescription>
          </DialogHeader>
          {confirm && <DiffView diff={lineDiff(versions[0].lines, versions.find((x) => x.v === confirm)!.lines)} title={`live v7 → v${head + 1}`} />}
          <DialogFooter>
            <DialogClose render={<Button variant="ghost" />}>Keep v7</DialogClose>
            <Button
              onClick={() => {
                setHead((h) => h + 1)
                setRolledTo(confirm)
                toast.add({ title: `Rolled back to v${confirm}`, description: `Published as v${head + 1}. Warden picks it up on the next snapshot.`, type: 'success' })
                setConfirm(null)
              }}
            >
              Roll back to v{confirm}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
