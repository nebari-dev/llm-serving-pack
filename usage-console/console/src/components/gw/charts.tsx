import { useState } from 'react'
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Line,
  LineChart,
  ReferenceArea,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { cn } from '@/lib/utils'

// §8 Sparkline / StackedArea / BarSeries, drawn with Recharts and themed on
// tokens. Mark specs follow the dataviz method: 2px lines, a light area wash,
// ≤24px bars with a 4px rounded data-end and a 2px surface gap between stacked
// segments, solid hairline grid, no animation ("live, but calm", §7.1). Every
// chart keeps a keyboard-reachable table equivalent (§7.7).

export interface Series {
  key: string
  label: string
  color: string
}

const MONO = "'IBM Plex Mono', ui-monospace, SFMono-Regular, monospace"
const tick = { fill: 'var(--muted-foreground)', fontSize: 11, fontFamily: MONO }
const grid = { stroke: 'var(--border)', strokeWidth: 1 }

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

/** Legend swatches mirror the mark: a rect for areas and bars. Text stays in text tokens. */
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

function ChartFrame({
  series,
  asTable,
  setAsTable,
  children,
}: {
  series: Series[]
  asTable: boolean
  setAsTable: (fn: (v: boolean) => boolean) => void
  children: React.ReactNode
}) {
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
      {children}
    </figure>
  )
}

/**
 * One tooltip, every series at that x. Values lead (strong), series names
 * follow (secondary), keyed by a short line in the series color.
 */
function SeriesTooltip({
  active,
  payload,
  label,
  series,
  valueFormat,
  labelFormat,
  footer,
}: {
  active?: boolean
  payload?: readonly { payload?: unknown }[]
  label?: string | number
  series: Series[]
  valueFormat: (n: number) => string
  labelFormat: (x: string | number) => string
  footer?: string
}) {
  if (!active || !payload?.length) return null
  const row = payload[0].payload as Record<string, number>
  const total = series.reduce((a, s) => a + (row[s.key] ?? 0), 0)
  return (
    <div className="min-w-44 rounded-md border border-border bg-popover p-2 text-xs shadow-lg">
      <div className="mb-1 font-mono text-muted-foreground">{labelFormat(label ?? '')}</div>
      {[...series].reverse().map((s) => (
        <div key={s.key} className="flex items-center justify-between gap-4 py-px">
          <span className="inline-flex items-center gap-1.5 text-muted-foreground-strong">
            <span className="h-0.5 w-3 rounded-full" style={{ background: s.color }} aria-hidden="true" />
            {s.label}
          </span>
          <span className="num font-mono font-medium text-foreground">{valueFormat(row[s.key] ?? 0)}</span>
        </div>
      ))}
      <div className="mt-1 flex justify-between border-t border-border pt-1">
        <span className="text-muted-foreground-strong">Total</span>
        <span className="num font-mono font-semibold text-foreground">{valueFormat(total)}</span>
      </div>
      {footer && <div className="mt-1 text-muted-foreground">{footer}</div>}
    </div>
  )
}

const compact = (n: number) => (Math.abs(n) >= 1000 ? `${+(n / 1000).toFixed(1)}k` : `${Math.round(n)}`)

/** Stacked area over time with a crosshair tooltip. Series stack bottom-up in the given order. */
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
  const [asTable, setAsTable] = useState(false)
  const rows = data.map((d) => ({ t: d.t, ...d.values }))
  return (
    <ChartFrame series={series} asTable={asTable} setAsTable={setAsTable}>
      {asTable ? (
        <ChartTable caption={caption} series={series} xLabel="Time" format={valueFormat} rows={data.map((d) => ({ x: xFormat(d.t), values: d.values }))} />
      ) : (
        <div role="img" aria-label={caption} style={{ height }}>
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={rows} margin={{ top: 22, right: 8, bottom: 0, left: 0 }}>
              <CartesianGrid vertical={false} {...grid} />
              <XAxis
                dataKey="t"
                type="number"
                scale="time"
                domain={['dataMin', 'dataMax']}
                tickFormatter={xFormat}
                tick={tick}
                tickLine={false}
                axisLine={{ stroke: 'var(--border-strong)' }}
                minTickGap={48}
              />
              <YAxis tick={tick} tickLine={false} axisLine={false} width={40} tickFormatter={compact} />
              <Tooltip
                cursor={{ stroke: 'var(--foreground)', strokeOpacity: 0.5, strokeWidth: 1 }}
                isAnimationActive={false}
                content={(p) => <SeriesTooltip active={p.active} payload={p.payload} label={p.label} series={series} valueFormat={valueFormat} labelFormat={(x) => xFormat(Number(x))} />}
              />
              {series.map((s) => (
                <Area
                  key={s.key}
                  dataKey={s.key}
                  name={s.label}
                  stackId="1"
                  type="monotone"
                  stroke={s.color}
                  strokeWidth={2}
                  fill={s.color}
                  fillOpacity={0.16}
                  activeDot={{ r: 4, stroke: 'var(--canvas)', strokeWidth: 2, fill: s.color }}
                  isAnimationActive={false}
                />
              ))}
              {annotations.map((a) => (
                <ReferenceLine
                  key={a.t}
                  x={a.t}
                  stroke="var(--foreground)"
                  strokeWidth={1}
                  label={{ value: a.label, position: 'top', fill: 'var(--foreground)', fontSize: 11, fontFamily: MONO }}
                />
              ))}
            </AreaChart>
          </ResponsiveContainer>
        </div>
      )}
    </ChartFrame>
  )
}

/**
 * Stacked columns by category, e.g. daily spend by team. Each column caps at
 * 24px; only the topmost non-zero segment gets the 4px rounded data-end, and
 * segments are separated by a 2px surface gap rather than an outline.
 */
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
  const [asTable, setAsTable] = useState(false)
  const rows = data.map((d) => ({ x: d.x, ...d.values }))
  const topKey = (row: Record<string, unknown>) => [...series].reverse().find((s) => Number(row[s.key] ?? 0) > 0)?.key
  const bottomKey = (row: Record<string, unknown>) => series.find((s) => Number(row[s.key] ?? 0) > 0)?.key

  return (
    <ChartFrame series={series} asTable={asTable} setAsTable={setAsTable}>
      {asTable ? (
        <ChartTable caption={caption} series={series} xLabel="Day" format={valueFormat} rows={data} />
      ) : (
        <div role="img" aria-label={caption} style={{ height }}>
          <ResponsiveContainer width="100%" height="100%">
            <BarChart
              data={rows}
              margin={{ top: 8, right: 8, bottom: 0, left: 0 }}
              barCategoryGap="20%"
              onClick={(state) => {
                const x = state?.activeLabel
                if (onBarClick && x != null) onBarClick(String(x))
              }}
              style={{ cursor: onBarClick ? 'pointer' : undefined }}
            >
              <CartesianGrid vertical={false} {...grid} />
              {highlightFrom !== undefined && data[highlightFrom] && (
                <ReferenceArea x1={data[highlightFrom].x} x2={data[data.length - 1].x} fill="var(--v-degraded-bg)" fillOpacity={1} ifOverflow="extendDomain" />
              )}
              <XAxis dataKey="x" tick={tick} tickLine={false} axisLine={{ stroke: 'var(--border-strong)' }} minTickGap={32} />
              <YAxis tick={tick} tickLine={false} axisLine={false} width={48} tickFormatter={(n: number) => `$${compact(n)}`} />
              <Tooltip
                cursor={{ fill: 'var(--muted)', opacity: 0.6 }}
                isAnimationActive={false}
                content={(p) => (
                  <SeriesTooltip active={p.active} payload={p.payload} label={p.label} series={series} valueFormat={valueFormat} labelFormat={String} footer={onBarClick ? 'Click to open receipts' : undefined} />
                )}
              />
              {series.map((s) => (
                <Bar
                  key={s.key}
                  dataKey={s.key}
                  name={s.label}
                  stackId="1"
                  fill={s.color}
                  maxBarSize={24}
                  isAnimationActive={false}
                  shape={(props: { x?: number; y?: number; width?: number; height?: number; payload?: Record<string, unknown> }) => {
                    const { x = 0, y = 0, width = 0, height: h = 0, payload = {} } = props
                    if (h <= 0) return <g />
                    // 2px surface gap under every segment except the one on the baseline.
                    const gap = bottomKey(payload) === s.key ? 0 : 2
                    const hh = Math.max(0, h - gap)
                    const r = topKey(payload) === s.key ? Math.min(4, width / 2, hh) : 0
                    const path = r
                      ? `M${x},${y + hh}V${y + r}Q${x},${y} ${x + r},${y}H${x + width - r}Q${x + width},${y} ${x + width},${y + r}V${y + hh}Z`
                      : `M${x},${y}H${x + width}V${y + hh}H${x}Z`
                    return <path d={path} fill={s.color} />
                  }}
                />
              ))}
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}
    </ChartFrame>
  )
}

/** Trend accent for tiles and rows: a 2px line in the de-emphasis hue with an end-dot. */
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
  const rows = values.map((v, i) => ({ i, v }))
  const last = rows.length - 1
  return (
    <div role="img" aria-label={label} className="shrink-0" style={{ width, height }}>
      <LineChart width={width} height={height} data={rows} margin={{ top: 4, right: 5, bottom: 4, left: 1 }}>
        <YAxis hide domain={['dataMin', 'dataMax']} />
        <Line
          dataKey="v"
          type="monotone"
          stroke={color}
          strokeWidth={2}
          strokeLinecap="round"
          strokeLinejoin="round"
          isAnimationActive={false}
          dot={(p: { cx?: number; cy?: number; index?: number }) =>
            p.index === last ? <circle key="end" cx={p.cx} cy={p.cy} r={3} fill={color} stroke="var(--canvas)" strokeWidth={1.5} /> : <g key={p.index} />
          }
          activeDot={false}
        />
      </LineChart>
    </div>
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
        <div className="absolute inset-y-0 left-0 rounded-full border border-dashed border-border-strong" style={{ width: `${(projected / scale) * 100}%` }} />
      )}
      <div
        className={cn('absolute inset-y-0 left-0 rounded-full', over ? 'bg-v-blocked-bar' : warn ? 'bg-v-degraded-bar' : 'bg-foreground/60')}
        style={{ width: `${(Math.min(value, scale) / scale) * 100}%` }}
      />
      <div className="absolute -inset-y-1 w-0.5 bg-foreground" style={{ left: `${(cap / scale) * 100}%` }} />
    </div>
  )
}
