import { useId, useState } from 'react'
import { cn } from '@/lib/utils'

// §8 Sparkline / StackedArea / BarSeries. Plain SVG, themed on tokens, each
// with a keyboard-reachable table equivalent (§7.7).

export interface Series {
  key: string
  label: string
  color: string
}

function ChartTable({
  caption,
  series,
  rows,
  xLabel,
  format,
}: {
  caption: string
  series: Series[]
  rows: { x: string; values: Record<string, number> }[]
  xLabel: string
  format: (n: number) => string
}) {
  return (
    <div className="max-h-64 overflow-auto rounded-md border border-border">
      <table className="w-full text-sm">
        <caption className="sr-only">{caption}</caption>
        <thead className="sticky top-0 bg-header">
          <tr>
            <th className="px-3 py-1.5 text-left font-medium">{xLabel}</th>
            {series.map((s) => (
              <th key={s.key} className="px-3 py-1.5 text-right font-medium">
                {s.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.x} className="border-t border-border">
              <td className="num px-3 py-1 font-mono text-xs">{r.x}</td>
              {series.map((s) => (
                <td key={s.key} className="num px-3 py-1 text-right font-mono text-xs">
                  {format(r.values[s.key] ?? 0)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

export function Legend({ series, className }: { series: Series[]; className?: string }) {
  return (
    <ul className={cn('flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground-strong', className)}>
      {series.map((s) => (
        <li key={s.key} className="inline-flex items-center gap-1.5">
          <span className="size-2.5 rounded-[2px]" style={{ background: s.color }} aria-hidden="true" />
          {s.label}
        </li>
      ))}
    </ul>
  )
}

/** Stacked area over time. Hover shows a crosshair readout; "View as table" toggles the equivalent. */
export function StackedArea({
  data,
  series,
  height = 180,
  xFormat,
  valueFormat = (n) => n.toLocaleString('en-US'),
  caption,
  annotations = [],
}: {
  data: { t: number; values: Record<string, number> }[]
  series: Series[]
  height?: number
  xFormat: (t: number) => string
  valueFormat?: (n: number) => string
  caption: string
  annotations?: { t: number; label: string }[]
}) {
  const [hover, setHover] = useState<number | null>(null)
  const [asTable, setAsTable] = useState(false)
  const W = 1000
  const H = height
  const padB = 20
  const totals = data.map((d) => series.reduce((a, s) => a + (d.values[s.key] ?? 0), 0))
  const max = Math.max(...totals) * 1.08 || 1
  const x = (i: number) => (i / Math.max(1, data.length - 1)) * W
  const y = (v: number) => H - padB - (v / max) * (H - padB - 6)

  const stacks: { s: Series; path: string }[] = []
  const acc = data.map(() => 0)
  for (const s of series) {
    const lower = [...acc]
    data.forEach((d, i) => (acc[i] += d.values[s.key] ?? 0))
    const top = data.map((_, i) => `${x(i)},${y(acc[i])}`)
    const bottom = data.map((_, i) => `${x(i)},${y(lower[i])}`).reverse()
    stacks.push({ s, path: `M${top.join('L')}L${bottom.join('L')}Z` })
  }

  const t0 = data[0]?.t ?? 0
  const t1 = data[data.length - 1]?.t ?? 1
  const tx = (t: number) => ((t - t0) / (t1 - t0 || 1)) * W

  return (
    <figure className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-4">
        <Legend series={series} />
        <button
          type="button"
          className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
          onClick={() => setAsTable((v) => !v)}
        >
          {asTable ? 'View as chart' : 'View as table'}
        </button>
      </div>
      {asTable ? (
        <ChartTable
          caption={caption}
          series={series}
          xLabel="Time"
          format={valueFormat}
          rows={data.map((d) => ({ x: xFormat(d.t), values: d.values }))}
        />
      ) : (
        <div className="relative">
          <svg
            viewBox={`0 0 ${W} ${H}`}
            preserveAspectRatio="none"
            className="block h-auto w-full"
            style={{ height: H }}
            role="img"
            aria-label={caption}
            onMouseLeave={() => setHover(null)}
            onMouseMove={(e) => {
              const rect = e.currentTarget.getBoundingClientRect()
              const i = Math.round(((e.clientX - rect.left) / rect.width) * (data.length - 1))
              setHover(Math.max(0, Math.min(data.length - 1, i)))
            }}
          >
            {[0.25, 0.5, 0.75].map((f) => (
              <line key={f} x1={0} x2={W} y1={y(max * f)} y2={y(max * f)} stroke="var(--border)" strokeDasharray="2 4" vectorEffect="non-scaling-stroke" />
            ))}
            <line x1={0} x2={W} y1={H - padB} y2={H - padB} stroke="var(--border-strong)" vectorEffect="non-scaling-stroke" />
            {stacks.map(({ s, path }) => (
              <path key={s.key} d={path} fill={s.color} fillOpacity={0.9} stroke="var(--canvas)" strokeWidth={0.5} vectorEffect="non-scaling-stroke" />
            ))}
            {annotations.map((a) => (
              <g key={a.t}>
                <line x1={tx(a.t)} x2={tx(a.t)} y1={0} y2={H - padB} stroke="var(--foreground)" strokeDasharray="3 3" vectorEffect="non-scaling-stroke" />
              </g>
            ))}
            {hover !== null && (
              <line x1={x(hover)} x2={x(hover)} y1={0} y2={H - padB} stroke="var(--foreground)" strokeOpacity={0.5} vectorEffect="non-scaling-stroke" />
            )}
          </svg>
          {annotations.map((a) => (
            <span
              key={a.t}
              className="pointer-events-none absolute top-0 -translate-x-1/2 rounded-sm border border-border bg-card px-1.5 py-0.5 font-mono text-[11px] whitespace-nowrap text-foreground"
              style={{ left: `${(tx(a.t) / W) * 100}%` }}
            >
              {a.label}
            </span>
          ))}
          <div className="flex justify-between pt-0.5 font-mono text-[11px] text-muted-foreground">
            <span>{xFormat(t0)}</span>
            <span>{xFormat(data[Math.floor(data.length / 2)]?.t ?? t0)}</span>
            <span>{xFormat(t1)}</span>
          </div>
          {hover !== null && (
            <div
              className="pointer-events-none absolute top-6 z-10 min-w-40 rounded-md border border-border bg-popover p-2 text-xs shadow-lg"
              style={{ left: `min(calc(${(x(hover) / W) * 100}% + 12px), calc(100% - 11rem))` }}
            >
              <div className="mb-1 font-mono text-muted-foreground">{xFormat(data[hover].t)}</div>
              {[...series].reverse().map((s) => (
                <div key={s.key} className="flex items-center justify-between gap-4">
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-2 rounded-[2px]" style={{ background: s.color }} />
                    {s.label}
                  </span>
                  <span className="num font-mono">{valueFormat(data[hover].values[s.key] ?? 0)}</span>
                </div>
              ))}
              <div className="mt-1 flex justify-between border-t border-border pt-1 font-medium">
                <span>Total</span>
                <span className="num font-mono">{valueFormat(totals[hover])}</span>
              </div>
            </div>
          )}
        </div>
      )}
    </figure>
  )
}

/** Stacked bars by category, e.g. daily spend by team. */
export function StackedBars({
  data,
  series,
  height = 200,
  valueFormat,
  caption,
  onBarClick,
  highlightFrom,
}: {
  data: { x: string; values: Record<string, number> }[]
  series: Series[]
  height?: number
  valueFormat: (n: number) => string
  caption: string
  onBarClick?: (x: string) => void
  highlightFrom?: number
}) {
  const [hover, setHover] = useState<number | null>(null)
  const [asTable, setAsTable] = useState(false)
  const totals = data.map((d) => series.reduce((a, s) => a + (d.values[s.key] ?? 0), 0))
  const max = Math.max(...totals) * 1.08 || 1
  return (
    <figure className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-4">
        <Legend series={series} />
        <button
          type="button"
          className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
          onClick={() => setAsTable((v) => !v)}
        >
          {asTable ? 'View as chart' : 'View as table'}
        </button>
      </div>
      {asTable ? (
        <ChartTable caption={caption} series={series} xLabel="Day" format={valueFormat} rows={data} />
      ) : (
        <div className="relative">
          <div className="flex items-end gap-[3px] border-b border-border-strong" style={{ height }} role="img" aria-label={caption}>
            {data.map((d, i) => (
              <button
                type="button"
                key={d.x}
                className={cn(
                  'group relative flex h-full flex-1 flex-col-reverse outline-none focus-visible:ring-2 focus-visible:ring-ring',
                  highlightFrom !== undefined && i >= highlightFrom && 'bg-v-degraded-bg',
                )}
                onMouseEnter={() => setHover(i)}
                onMouseLeave={() => setHover(null)}
                onFocus={() => setHover(i)}
                onBlur={() => setHover(null)}
                onClick={() => onBarClick?.(d.x)}
                aria-label={`${d.x}: ${valueFormat(totals[i])}`}
              >
                {series.map((s) => (
                  <span
                    key={s.key}
                    className="block w-full group-hover:opacity-80"
                    style={{ height: `${((d.values[s.key] ?? 0) / max) * 100}%`, background: s.color }}
                  />
                ))}
              </button>
            ))}
          </div>
          <div className="flex justify-between pt-0.5 font-mono text-[11px] text-muted-foreground">
            <span>{data[0]?.x}</span>
            <span>{data[Math.floor(data.length / 2)]?.x}</span>
            <span>{data[data.length - 1]?.x}</span>
          </div>
          {hover !== null && (
            <div
              className="pointer-events-none absolute top-2 z-10 min-w-44 rounded-md border border-border bg-popover p-2 text-xs shadow-lg"
              style={{ left: `min(calc(${((hover + 0.5) / data.length) * 100}% + 12px), calc(100% - 12rem))` }}
            >
              <div className="mb-1 font-mono text-muted-foreground">{data[hover].x}</div>
              {[...series].reverse().map((s) => (
                <div key={s.key} className="flex items-center justify-between gap-4">
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-2 rounded-[2px]" style={{ background: s.color }} />
                    {s.label}
                  </span>
                  <span className="num font-mono">{valueFormat(data[hover].values[s.key] ?? 0)}</span>
                </div>
              ))}
              <div className="mt-1 flex justify-between border-t border-border pt-1 font-medium">
                <span>Total</span>
                <span className="num font-mono">{valueFormat(totals[hover])}</span>
              </div>
              {onBarClick && <div className="mt-1 text-muted-foreground">Click to open receipts</div>}
            </div>
          )}
        </div>
      )}
    </figure>
  )
}

export function Sparkline({
  values,
  color = 'var(--muted-foreground-strong)',
  width = 96,
  height = 24,
  label,
}: {
  values: number[]
  color?: string
  width?: number
  height?: number
  label: string
}) {
  const id = useId()
  const max = Math.max(...values) || 1
  const min = Math.min(...values)
  const pts = values.map((v, i) => `${(i / (values.length - 1)) * width},${height - 2 - ((v - min) / (max - min || 1)) * (height - 4)}`)
  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label} className="shrink-0">
      <defs>
        <linearGradient id={id} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0" stopColor={color} stopOpacity={0.18} />
          <stop offset="1" stopColor={color} stopOpacity={0} />
        </linearGradient>
      </defs>
      <path d={`M0,${height}L${pts.join('L')}L${width},${height}Z`} fill={`url(#${id})`} />
      <polyline points={pts.join(' ')} fill="none" stroke={color} strokeWidth={1.25} />
    </svg>
  )
}

/** Horizontal meter with explicit cap marker; never the only carrier of meaning. */
export function Meter({ value, cap, projected, className }: { value: number; cap: number; projected?: number; className?: string }) {
  const scale = Math.max(cap, value, projected ?? 0) * 1.05
  const over = value > cap
  const warn = value / cap >= 0.8
  return (
    <div className={cn('relative h-2 w-full rounded-full bg-muted', className)} aria-hidden="true">
      {projected !== undefined && (
        <div
          className="absolute inset-y-0 left-0 rounded-full border border-dashed border-border-strong"
          style={{ width: `${(projected / scale) * 100}%` }}
        />
      )}
      <div
        className={cn('absolute inset-y-0 left-0 rounded-full', over ? 'bg-v-blocked-bar' : warn ? 'bg-v-degraded-bar' : 'bg-foreground/60')}
        style={{ width: `${(Math.min(value, scale) / scale) * 100}%` }}
      />
      <div className="absolute -inset-y-1 w-0.5 bg-foreground" style={{ left: `${(cap / scale) * 100}%` }} />
    </div>
  )
}
