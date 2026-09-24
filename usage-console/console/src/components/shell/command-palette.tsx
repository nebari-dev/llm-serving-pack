import { Boxes, FileText, KeyRound, Route, ShieldCheck } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { backends, keys, models, rules } from '@/data/mock'
import { cn } from '@/lib/utils'
import { receiptStream, useApp } from '@/state/app-state'

// §7.4 CommandPalette (⌘K) with a resolver registry. Pasting a trace ID
// anywhere resolves to its receipt.

type Result = { id: string; label: string; hint: string; icon: typeof KeyRound; run: () => void }

export function CommandPalette() {
  const { paletteOpen, setPaletteOpen, openReceipt } = useApp()
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const [active, setActive] = useState(0)

  const results = useMemo<Result[]>(() => {
    const needle = q.trim().toLowerCase()
    const out: Result[] = []
    // Trace-ID resolver: any 8+ hex chars that prefix-match a receipt id or trace id.
    if (/^[0-9a-f-]{6,}$/.test(needle)) {
      for (const r of receiptStream.byId.values()) {
        if (r.traceId.startsWith(needle) || r.id.startsWith(needle)) {
          out.push({
            id: 'r' + r.id,
            label: `Receipt ${r.id}`,
            hint: `${r.keyName} · ${r.resolvedModel} · ${r.verdict}`,
            icon: FileText,
            run: () => openReceipt(r.id),
          })
          if (out.length > 4) break
        }
      }
    }
    const add = (items: Result[]) => out.push(...items.filter((i) => !needle || i.label.toLowerCase().includes(needle) || i.hint.toLowerCase().includes(needle)))
    add(keys.map((k) => ({ id: 'k' + k.id, label: k.name, hint: `Key · ${k.prefix}… · ${k.team}`, icon: KeyRound, run: () => navigate(`/keys?key=${k.id}`) })))
    add(models.map((m) => ({ id: 'm' + m.id, label: m.id, hint: `Model · ${m.provider}`, icon: Boxes, run: () => navigate(`/traffic?model=${m.id}`) })))
    add(rules.map((r) => ({ id: 'p' + r.id, label: r.name, hint: `Rule · v${r.version} · ${r.mode}`, icon: ShieldCheck, run: () => navigate(`/guardrails?rule=${r.id}`) })))
    add(backends.map((b) => ({ id: 'b' + b.name, label: b.name, hint: `Backend · ${b.provider}`, icon: Route, run: () => navigate('/routing') })))
    return out.slice(0, 12)
  }, [q, navigate, openReceipt])

  const run = (r: Result) => {
    setPaletteOpen(false)
    setQ('')
    r.run()
  }

  return (
    <Dialog open={paletteOpen} onOpenChange={setPaletteOpen}>
      <DialogContent className="max-w-xl gap-0 p-0" viewportClassName="items-start pt-[12vh]" showCloseButton={false}>
        <DialogTitle className="sr-only">Jump to</DialogTitle>
        <input
          autoFocus
          value={q}
          onChange={(e) => {
            setQ(e.target.value)
            setActive(0)
          }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              setActive((a) => Math.min(results.length - 1, a + 1))
            } else if (e.key === 'ArrowUp') {
              e.preventDefault()
              setActive((a) => Math.max(0, a - 1))
            } else if (e.key === 'Enter' && results[active]) {
              run(results[active])
            }
          }}
          placeholder="Jump to a key, model, rule — or paste a trace ID"
          className="h-12 w-full border-b border-border bg-transparent px-4 text-sm outline-none placeholder:text-muted-foreground"
          role="combobox"
          aria-expanded="true"
          aria-controls="palette-results"
          aria-activedescendant={results[active] ? `pal-${results[active].id}` : undefined}
        />
        <ul id="palette-results" role="listbox" className="max-h-80 overflow-y-auto p-1">
          {results.length === 0 && <li className="px-3 py-6 text-center text-sm text-muted-foreground">Nothing matches “{q}”.</li>}
          {results.map((r, i) => (
            <li
              key={r.id}
              id={`pal-${r.id}`}
              role="option"
              aria-selected={i === active}
              onMouseEnter={() => setActive(i)}
              onClick={() => run(r)}
              className={cn('flex cursor-pointer items-center gap-3 rounded-sm px-3 py-2 text-sm', i === active && 'bg-muted')}
            >
              <r.icon className="size-4 text-muted-foreground" aria-hidden="true" />
              <span className="font-mono">{r.label}</span>
              <span className="ml-auto truncate text-xs text-muted-foreground">{r.hint}</span>
            </li>
          ))}
        </ul>
        <div className="border-t border-border px-4 py-2 text-xs text-muted-foreground">↑↓ to move · Enter to open · Esc to close</div>
      </DialogContent>
    </Dialog>
  )
}
