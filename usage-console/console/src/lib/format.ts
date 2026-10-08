// Formatting helpers used by the Money / TokenCount / Duration primitives.

/** Dollars to `digits` places, except that spend under a cent shows to four places (never "$0.00" for real spend). */
export function money(usd: number, digits = 2) {
  const abs = Math.abs(usd)
  if (digits === 2 && abs > 0 && abs < 0.01) return abs < 0.0001 ? '<$0.0001' : (usd < 0 ? '-$' : '$') + abs.toFixed(4)
  return '$' + usd.toLocaleString('en-US', { minimumFractionDigits: digits, maximumFractionDigits: digits })
}

export function microMoney(usd: number) {
  if (usd === 0) return '$0.0000'
  return '$' + usd.toFixed(4)
}

/** Cost per request to 4 places; null is "no price" (none of the requests had one), never $0. */
export function perRequest(usd: number | null) {
  return usd === null ? 'no price' : `$${usd.toFixed(4)}`
}

/** "3 requests have no price and aren't in this total", for a total that leaves them out. */
export function unpricedNote(n: number, what = 'this total') {
  return `${n.toLocaleString('en-US')} request${n === 1 ? ' has' : 's have'} no price and ${n === 1 ? "isn't" : "aren't"} in ${what}`
}

export function tokens(n: number) {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 10_000) return Math.round(n / 1000) + 'k'
  if (n >= 1_000) return (n / 1000).toFixed(1) + 'k'
  return String(n)
}

export function int(n: number) {
  return n.toLocaleString('en-US')
}

export function clock(ts: number) {
  return new Date(ts).toLocaleTimeString('en-GB', { hour12: false })
}

/** Seconds as "4m 12s". */
export function age(sec: number) {
  const s = Math.round(sec)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

export function ago(ts: number, now = Date.now()) {
  const s = Math.round((now - ts) / 1000)
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  return `${Math.round(s / 86400)}d ago`
}
