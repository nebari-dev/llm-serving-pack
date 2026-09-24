import { Check, X } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Section } from '@/components/gw/page'
import { Button } from '@/components/ui/button'
import { Slider } from '@/components/ui/slider'
import { toast } from '@/components/ui/toast'
import { detectors } from '@/data/mock'
import { int } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useApp } from '@/state/app-state'

// §7.5.7 detectors surface: built-in entity types, custom regex with a test
// box, per-detector thresholds, and a false-positive review queue fed from the
// receipt view. Real matched values are never shown — the test box only ever
// holds text the author typed.

const sample = `Ticket from jane.doe@example.com about account ACME-00412233.
Customer also mentioned ACME-1234 (too short) and acme-99887766 (lowercase).
Refund ref: ACME-55501234, card ending 4242.`

const initialQueue = [
  { id: 'fp1', detector: 'Person name', rule: 'no-pii-out v7', key: 'support-bot', reporter: 'dana@acme.dev', note: '"Jordan" was a product codename', ago: '12m ago' },
  { id: 'fp2', detector: 'API secret', rule: 'block-src v12', key: 'agents-prod', reporter: 'marco@acme.dev', note: 'Base64 image payload, not a secret', ago: '1h ago' },
  { id: 'fp3', detector: 'Source code', rule: 'block-src v12', key: 'support-bot', reporter: 'priya@acme.dev', note: 'Customer pasted a CSV snippet', ago: '3h ago' },
  { id: 'fp4', detector: 'Email address', rule: 'no-pii-out v7', key: 'web-chat', reporter: 'lee@acme.dev', note: 'support@acme.dev is our own public address', ago: '5h ago' },
]

export function DetectorsTab({ receiptIds }: { receiptIds: string[] }) {
  const { openReceipt } = useApp()
  const [thresholds, setThresholds] = useState<Record<string, number>>(() => Object.fromEntries(detectors.map((d) => [d.id, d.threshold])))
  const [pattern, setPattern] = useState('ACME-\\d{8}')
  const [text, setText] = useState(sample)
  const [queue, setQueue] = useState(initialQueue)

  const { parts, count, error } = useMemo(() => {
    try {
      const re = new RegExp(pattern, 'g')
      const out: { t: string; m: boolean }[] = []
      let last = 0
      let n = 0
      for (const m of text.matchAll(re)) {
        if (m[0] === '') break
        out.push({ t: text.slice(last, m.index), m: false }, { t: m[0], m: true })
        last = (m.index ?? 0) + m[0].length
        n++
      }
      out.push({ t: text.slice(last), m: false })
      return { parts: out, count: n, error: null as string | null }
    } catch (e) {
      return { parts: [{ t: text, m: false }], count: 0, error: (e as Error).message }
    }
  }, [pattern, text])

  const resolve = (id: string, accepted: boolean) => {
    const item = queue.find((q) => q.id === id)
    setQueue((q) => q.filter((x) => x.id !== id))
    toast.add({
      title: accepted ? 'Marked as false positive' : 'Report dismissed',
      description: accepted ? `${item?.detector}: added to the tuning set. Threshold changes stay a draft until you publish.` : 'The redaction stands. The reporter is notified.',
      type: accepted ? 'success' : 'default',
    })
  }

  return (
    <div>
      <Section title="Built-in detectors" description="Confidence below the threshold is ignored. Raising it trades recall for fewer false positives.">
        <div className="overflow-x-auto">
          <table className="w-full min-w-[44rem] text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs text-muted-foreground">
                <th className="py-1.5 pr-3 font-medium">Detector</th>
                <th className="py-1.5 pr-3 font-medium">Method</th>
                <th className="w-64 py-1.5 pr-3 font-medium">Threshold</th>
                <th className="py-1.5 pr-3 text-right font-medium">Hits, 24h</th>
                <th className="py-1.5 pr-3 text-right font-medium">False positives</th>
                <th className="py-1.5 text-right font-medium">FP rate</th>
              </tr>
            </thead>
            <tbody>
              {detectors.map((d) => {
                const rate = d.hits24h ? (d.fp / d.hits24h) * 100 : 0
                return (
                  <tr key={d.id} className="h-11 border-b border-border">
                    <td className="pr-3 font-medium">{d.name}</td>
                    <td className="pr-3 font-mono text-xs text-muted-foreground-strong">{d.kind}</td>
                    <td className="pr-3">
                      <div className="flex items-center gap-3">
                        <Slider
                          value={thresholds[d.id]}
                          min={0.5}
                          max={1}
                          step={0.01}
                          onValueChange={(v) => setThresholds((t) => ({ ...t, [d.id]: v as number }))}
                          getThumbAriaLabel={() => `${d.name} confidence threshold`}
                          showValueTooltip={false}
                          disabled={d.kind.includes('checksum') || d.kind.includes('Luhn')}
                        />
                        <span className="num w-10 text-right font-mono text-xs">{thresholds[d.id].toFixed(2)}</span>
                      </div>
                    </td>
                    <td className="num pr-3 text-right font-mono text-xs">{int(d.hits24h)}</td>
                    <td className="num pr-3 text-right font-mono text-xs">{int(d.fp)}</td>
                    <td className={cn('num text-right font-mono text-xs', rate > 5 && 'font-medium text-v-degraded-fg')}>{rate.toFixed(1)}%</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
        <p className="mt-2 text-xs text-muted-foreground">Checksum and Luhn detectors are deterministic, so they have no threshold to tune.</p>
      </Section>

      <Section title="Custom entity: Acme account ID" description="Regex detector. Test it against sample text you type here. Recorded traffic is never shown.">
        <div className="grid gap-4 lg:grid-cols-2">
          <div className="flex flex-col gap-3">
            <label className="flex flex-col gap-1">
              <span className="text-xs text-muted-foreground">Pattern</span>
              <input
                value={pattern}
                onChange={(e) => setPattern(e.target.value)}
                aria-invalid={!!error || undefined}
                className="h-8 rounded-md border border-input bg-background px-2 font-mono text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring aria-invalid:border-destructive-foreground"
              />
              {error && <span className="text-xs text-destructive-foreground">{error}</span>}
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-xs text-muted-foreground">Test text</span>
              <textarea
                value={text}
                onChange={(e) => setText(e.target.value)}
                rows={5}
                className="rounded-md border border-input bg-background p-2 font-mono text-xs outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring"
              />
            </label>
          </div>
          <div className="flex flex-col gap-1">
            <span className="text-xs text-muted-foreground" aria-live="polite">
              {error ? 'Fix the pattern to see matches' : `${count} match${count === 1 ? '' : 'es'}`}
            </span>
            <pre className="min-h-32 flex-1 rounded-md border border-border bg-muted p-3 font-mono text-xs whitespace-pre-wrap text-muted-foreground-strong">
              {parts.map((p, i) =>
                p.m ? (
                  <mark key={i} className="rounded-[2px] bg-v-redacted-bg px-0.5 text-v-redacted-fg ring-1 ring-v-redacted-border">
                    {p.t}
                  </mark>
                ) : (
                  <span key={i}>{p.t}</span>
                ),
              )}
            </pre>
          </div>
        </div>
      </Section>

      <Section
        title="False-positive review"
        description="Reported from the receipt view with “Mark a redaction as incorrect”. Reports carry the detector and rule, never the matched value."
      >
        {queue.length === 0 ? (
          <p className="py-6 text-center text-sm text-muted-foreground">Queue is clear. New reports land here from receipts.</p>
        ) : (
          <ul className="divide-y divide-border border-y border-border">
            {queue.map((q, i) => (
              <li key={q.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5 text-sm">
                <span className="w-28 font-medium">{q.detector}</span>
                <span className="font-mono text-xs text-muted-foreground-strong">{q.rule}</span>
                <span className="font-mono text-xs">{q.key}</span>
                <span className="min-w-48 flex-1 text-muted-foreground-strong">“{q.note.replace(/"/g, '')}”</span>
                <span className="text-xs text-muted-foreground">
                  {q.reporter} · {q.ago}
                </span>
                <span className="flex gap-1">
                  {receiptIds[i] && (
                    <Button variant="ghost" size="xs" onClick={() => openReceipt(receiptIds[i])}>
                      Open receipt
                    </Button>
                  )}
                  <Button variant="outline" size="xs" onClick={() => resolve(q.id, true)}>
                    <Check /> Confirm false positive
                  </Button>
                  <Button variant="ghost" size="xs" onClick={() => resolve(q.id, false)}>
                    <X /> Keep redaction
                  </Button>
                </span>
              </li>
            ))}
          </ul>
        )}
      </Section>
    </div>
  )
}
