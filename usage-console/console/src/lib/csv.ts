import type { PriceChange } from '@/data/catalog'

type Cell = string | number | boolean | null | undefined

/** RFC 4180 CSV. A text cell starting like a formula gets a leading ' so a spreadsheet shows it rather than running it. */
export function toCsv(rows: Cell[][]): string {
  const cell = (v: Cell) => {
    if (v == null) return ''
    let s = String(v)
    if (typeof v === 'string' && /^[=+\-@\t\r]/.test(s)) s = "'" + s
    return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  return rows.map((r) => r.map(cell).join(',')).join('\n') + '\n'
}

/** Pricing's change history, one row per rate change; an empty price is "no price". */
export function priceChangesCsv(changes: PriceChange[]): string {
  return toCsv([
    ['model', 'backend', 'rate', 'from_usd_per_1m', 'to_usd_per_1m', 'source', 'effective', 'scheduled'],
    ...changes.map((c) => [c.model, c.backend, c.field, c.from, c.to, c.source, c.effective, c.scheduled]),
  ])
}

/** Saves text as a file through a temporary link. */
export function downloadText(name: string, text: string, type = 'text/csv') {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([text], { type }))
  a.download = name
  a.click()
  URL.revokeObjectURL(a.href)
}
