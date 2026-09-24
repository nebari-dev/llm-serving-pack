// Formatting helpers used by the Money / TokenCount / Duration primitives.

export function money(usd: number, digits = 2) {
  return '$' + usd.toLocaleString('en-US', { minimumFractionDigits: digits, maximumFractionDigits: digits })
}

export function microMoney(usd: number) {
  if (usd === 0) return '$0.0000'
  return '$' + usd.toFixed(4)
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

export function ago(ts: number, now = Date.now()) {
  const s = Math.round((now - ts) / 1000)
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  return `${Math.round(s / 86400)}d ago`
}
