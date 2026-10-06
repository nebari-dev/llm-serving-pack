import type { PriceChange, SpendView } from '@/data/catalog'

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

/**
 * Spend's breakdown. spend_usd leaves out each row's unpriced requests, and
 * reads "no price" when the row has some and no priced spend, never $0.
 */
export function spendCsv(view: SpendView): string {
  return toCsv([
    [view.by, 'detail', 'spend_usd', 'previous_spend_usd', 'requests', 'unpriced_requests', 'tokens'],
    ...view.rows.map((r) => [
      r.label,
      r.sub ?? '',
      r.unpriced && !r.spendUsd ? 'no price' : r.spendUsd.toFixed(2),
      r.prevSpendUsd.toFixed(2),
      r.requests,
      r.unpriced ?? 0,
      r.tokens,
    ]),
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
