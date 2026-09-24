import { cn } from '@/lib/utils'

// §8 DiffView — unified, YAML-aware (line-level). Used for CRD diffs,
// drift, and policy versions. Kept intentionally simple for the mockup.

export type DiffLine = { kind: ' ' | '+' | '-'; text: string }

export function parseDiff(src: string): DiffLine[] {
  return src
    .replace(/^\n/, '')
    .split('\n')
    .map((l) => {
      const k = l[0]
      if (k === '+' || k === '-') return { kind: k, text: l.slice(1) }
      return { kind: ' ', text: l.startsWith(' ') ? l.slice(1) : l }
    })
}

export function DiffView({ diff, title, className }: { diff: string; title?: string; className?: string }) {
  const lines = parseDiff(diff)
  let oldN = 0
  let newN = 0
  const adds = lines.filter((l) => l.kind === '+').length
  const dels = lines.filter((l) => l.kind === '-').length
  return (
    <div className={cn('overflow-hidden rounded-md border border-border bg-card', className)}>
      {title && (
        <div className="flex items-center justify-between border-b border-border bg-header px-3 py-1.5 text-xs">
          <span className="font-mono">{title}</span>
          <span className="num font-mono">
            <span className="text-v-allowed-fg">+{adds}</span> <span className="text-v-blocked-fg">−{dels}</span>
          </span>
        </div>
      )}
      <div className="overflow-x-auto">
        <table className="w-full border-collapse font-mono text-xs leading-5">
          <tbody>
            {lines.map((l, i) => {
              if (l.kind !== '+') oldN++
              if (l.kind !== '-') newN++
              return (
                <tr
                  key={i}
                  className={cn(l.kind === '+' && 'bg-v-allowed-bg', l.kind === '-' && 'bg-v-blocked-bg')}
                >
                  <td className="num w-8 select-none px-2 text-right text-muted-foreground">{l.kind === '+' ? '' : oldN}</td>
                  <td className="num w-8 select-none px-2 text-right text-muted-foreground">{l.kind === '-' ? '' : newN}</td>
                  <td
                    className={cn(
                      'w-4 select-none text-center',
                      l.kind === '+' && 'text-v-allowed-fg',
                      l.kind === '-' && 'text-v-blocked-fg',
                    )}
                  >
                    {l.kind === ' ' ? '' : l.kind === '-' ? '−' : '+'}
                  </td>
                  <td className="whitespace-pre pr-4">{l.text}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}
