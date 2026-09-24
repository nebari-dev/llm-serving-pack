import { ArrowRight, Download, Plus } from 'lucide-react'
import { Link, useSearchParams } from 'react-router-dom'
import { Money } from '@/components/gw/numbers'
import { PageHeader, Section } from '@/components/gw/page'
import { ProvenanceBadge } from '@/components/gw/provenance'
import { StateChip } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import { backends, modelById, models, type Provenance } from '@/data/mock'
import { cn } from '@/lib/utils'

// §7.4 Models → Catalog · Aliases · Pricing. §5.2 model_catalog,
// model_pricing, model_aliases. §13: reasoning tokens are a separate cost type.

type Tab = 'catalog' | 'aliases' | 'pricing'

const modalities: Record<string, string[]> = {
  'gpt-5-mini': ['text', 'image'],
  'gpt-5.5': ['text', 'image', 'audio'],
  'claude-sonnet-5': ['text', 'image'],
  'claude-opus-4-1': ['text', 'image'],
  'claude-haiku-4-5': ['text', 'image'],
  'llama-3.3-70b': ['text'],
}

const deprecated: Record<string, string> = { 'claude-opus-4-1': '2026-12-31' }

const aliases: {
  alias: string
  target: string
  conditions?: string
  provenance: Provenance
  requests24h: number
  note?: string
}[] = [
  { alias: 'default', target: 'claude-sonnet-5', provenance: 'console', requests24h: 31_204, note: 'Switched from gpt-5.5 at 14:02 by priya@acme.dev' },
  { alias: 'summarize-*', target: 'gpt-5-mini', conditions: 'input tokens < 64k', provenance: 'console', requests24h: 4_120 },
  { alias: 'summarize-*', target: 'llama-3.3-70b', conditions: 'header x-data-region = eu', provenance: 'git', requests24h: 612 },
  { alias: 'reasoning', target: 'gpt-5.5', conditions: 'key.team in [research, agents]', provenance: 'console', requests24h: 2_880 },
  { alias: 'fast', target: 'claude-haiku-4-5', provenance: 'console', requests24h: 9_411 },
]

const priceHistory = [
  { model: 'claude-sonnet-5', field: 'Output', from: 18.0, to: 15.0, effective: '2026-09-01', by: 'catalog sync (Anthropic list price)' },
  { model: 'gpt-5-mini', field: 'Cached input', from: 0.05, to: 0.025, effective: '2026-08-14', by: 'catalog sync (OpenAI list price)' },
]

const effectiveFrom: Record<string, string> = {
  'gpt-5-mini': '2026-08-14',
  'gpt-5.5': '2026-06-02',
  'claude-sonnet-5': '2026-09-01',
  'claude-opus-4-1': '2025-08-05',
  'claude-haiku-4-5': '2025-10-15',
  'llama-3.3-70b': '2026-01-01',
}

function ctx(n: number) {
  return n >= 1_000_000 ? `${n / 1_000_000}M` : `${Math.round(n / 1000)}k`
}

export function ModelsPage() {
  const [params, setParams] = useSearchParams()
  const tab = (params.get('tab') as Tab) ?? 'catalog'
  const setTab = (t: Tab) => {
    const next = new URLSearchParams(params)
    next.set('tab', t)
    setParams(next, { replace: true })
  }

  return (
    <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)} className="gap-0">
      <PageHeader
        title="Models"
        description="What clients can ask for, what it resolves to, and what it costs."
        actions={
          tab === 'aliases' ? (
            <Button>
              <Plus /> New alias
            </Button>
          ) : undefined
        }
      >
        <TabsList variant="underline" className="-mb-4">
          <TabsTab value="catalog">Catalog</TabsTab>
          <TabsTab value="aliases">Aliases</TabsTab>
          <TabsTab value="pricing">Pricing</TabsTab>
          <TabsIndicator />
        </TabsList>
      </PageHeader>

      <TabsPanel value="catalog">
        <CatalogTab />
      </TabsPanel>
      <TabsPanel value="aliases">
        <AliasesTab />
      </TabsPanel>
      <TabsPanel value="pricing">
        <PricingTab />
      </TabsPanel>
    </Tabs>
  )
}

const th = 'px-3 py-2 font-medium'
const td = 'px-3 py-2'

function CatalogTab() {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[52rem] text-sm">
        <caption className="sr-only">Model catalog</caption>
        <thead className="bg-header text-left text-xs text-muted-foreground-strong">
          <tr className="border-b border-border">
            <th className={cn(th, 'pl-6')}>Model</th>
            <th className={th}>Provider</th>
            <th className={th}>Family</th>
            <th className={cn(th, 'text-right')}>Context</th>
            <th className={th}>Modalities</th>
            <th className={th}>Served by</th>
            <th className={cn(th, 'pr-6')}>Status</th>
          </tr>
        </thead>
        <tbody>
          {models.map((m) => (
            <tr key={m.id} className="border-b border-border hover:bg-muted/50">
              <td className={cn(td, 'pl-6')}>
                <Link to={`/traffic?model=${m.id}`} className="font-mono font-medium hover:underline">
                  {m.id}
                </Link>
                <div className="text-xs text-muted-foreground">{m.display}</div>
              </td>
              <td className={td}>{m.provider}</td>
              <td className={cn(td, 'font-mono text-xs')}>{m.family}</td>
              <td className={cn(td, 'num text-right font-mono')}>{ctx(m.context)}</td>
              <td className={td}>
                <span className="flex gap-1">
                  {modalities[m.id].map((x) => (
                    <span key={x} className="rounded-sm border border-border px-1.5 text-xs leading-5 text-muted-foreground-strong">
                      {x}
                    </span>
                  ))}
                </span>
              </td>
              <td className={cn(td, 'font-mono text-xs')}>
                {backends
                  .filter((b) => b.models.includes(m.id))
                  .map((b) => b.name)
                  .join(', ')}
              </td>
              <td className={cn(td, 'pr-6')}>
                {deprecated[m.id] ? (
                  <StateChip tone="degraded">Deprecated {deprecated[m.id]}</StateChip>
                ) : (
                  <span className="text-xs text-muted-foreground">Available</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function AliasesTab() {
  return (
    <>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[52rem] text-sm">
          <caption className="sr-only">Model aliases</caption>
          <thead className="bg-header text-left text-xs text-muted-foreground-strong">
            <tr className="border-b border-border">
              <th className={cn(th, 'pl-6')}>Client asks for</th>
              <th className={th}>
                <span className="sr-only">resolves to</span>
              </th>
              <th className={th}>Runs</th>
              <th className={th}>When</th>
              <th className={th}>Owner</th>
              <th className={cn(th, 'text-right')}>Requests, 24h</th>
              <th className={cn(th, 'pr-6 text-right')}>Blended cost / 1M</th>
            </tr>
          </thead>
          <tbody>
            {aliases.map((a) => {
              const m = modelById[a.target]
              return (
                <tr key={a.alias + a.target} className="border-b border-border hover:bg-muted/50">
                  <td className={cn(td, 'pl-6 font-mono font-medium')}>{a.alias}</td>
                  <td className={td}>
                    <ArrowRight className="size-4 text-muted-foreground" aria-label="resolves to" />
                  </td>
                  <td className={td}>
                    <span className="font-mono">{a.target}</span>
                    {a.note && <div className="text-xs text-muted-foreground">{a.note}</div>}
                  </td>
                  <td className={cn(td, 'font-mono text-xs')}>{a.conditions ?? <span className="font-sans text-muted-foreground">always</span>}</td>
                  <td className={td}>
                    <ProvenanceBadge provenance={a.provenance} source="github.com/acme/platform-gitops/blob/main/gateway/aliases.yaml" />
                  </td>
                  <td className={cn(td, 'num text-right font-mono')}>{a.requests24h.toLocaleString('en-US')}</td>
                  <td className={cn(td, 'pr-6 text-right')}>
                    <Money value={(m.inPerM * 3 + m.outPerM) / 4} />
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <Section>
        <p className="text-sm text-muted-foreground">
          Conditions evaluate top to bottom; the first match wins. Savings suggestions from Spend open here as a <em>draft</em> alias change — nothing is applied until you review the diff.{' '}
          <Link to="/spend" className="text-foreground underline underline-offset-4">
            See savings analysis
          </Link>
        </p>
      </Section>
    </>
  )
}

function PricingTab() {
  return (
    <>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[56rem] text-sm">
          <caption className="sr-only">Model pricing, per million tokens</caption>
          <thead className="bg-header text-xs text-muted-foreground-strong">
            <tr className="border-b border-border">
              <th className={cn(th, 'pl-6 text-left')}>Model</th>
              <th className={cn(th, 'text-right')}>Input</th>
              <th className={cn(th, 'text-right')}>Cached input</th>
              <th className={cn(th, 'text-right')}>Output</th>
              <th className={cn(th, 'text-right')}>Reasoning</th>
              <th className={cn(th, 'text-left')}>Effective from</th>
              <th className={cn(th, 'pr-6 text-left')}>Source</th>
            </tr>
          </thead>
          <tbody>
            {models.map((m) => (
              <tr key={m.id} className="border-b border-border hover:bg-muted/50">
                <td className={cn(td, 'pl-6 font-mono font-medium')}>{m.id}</td>
                <td className={cn(td, 'text-right')}>
                  <Money value={m.inPerM} precision="micro" />
                </td>
                <td className={cn(td, 'text-right')}>
                  <Money value={m.cachedPerM} precision="micro" />
                </td>
                <td className={cn(td, 'text-right')}>
                  <Money value={m.outPerM} precision="micro" />
                </td>
                <td className={cn(td, 'text-right')}>
                  <Money value={m.reasoningPerM} precision="micro" />
                </td>
                <td className={cn(td, 'num font-mono text-xs')}>{effectiveFrom[m.id]}</td>
                <td className={cn(td, 'pr-6 text-xs text-muted-foreground')}>{m.provider === 'Self-hosted' ? 'Internal chargeback rate' : `${m.provider} list price`}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Section
        title="Price changes"
        description="Every receipt snapshots the price row in effect when it was written. A March receipt reconciles to March prices, not today's."
        actions={
          <Button variant="outline" size="sm" onClick={() => toast.add({ title: 'Exported model-pricing.csv', type: 'success' })}>
            <Download /> Export CSV
          </Button>
        }
      >
        <table className="w-full text-sm">
          <thead className="text-left text-xs text-muted-foreground">
            <tr className="border-b border-border">
              <th className="py-1.5 pr-3 font-medium">Effective</th>
              <th className="py-1.5 pr-3 font-medium">Model</th>
              <th className="py-1.5 pr-3 font-medium">Rate</th>
              <th className="py-1.5 pr-3 text-right font-medium">Was</th>
              <th className="py-1.5 pr-3 text-right font-medium">Now</th>
              <th className="py-1.5 font-medium">Changed by</th>
            </tr>
          </thead>
          <tbody>
            {priceHistory.map((p) => (
              <tr key={p.model + p.field} className="border-b border-border last:border-0">
                <td className="num py-2 pr-3 font-mono text-xs">{p.effective}</td>
                <td className="py-2 pr-3 font-mono text-xs">{p.model}</td>
                <td className="py-2 pr-3 text-xs">{p.field} / 1M</td>
                <td className="py-2 pr-3 text-right text-muted-foreground line-through">
                  <Money value={p.from} precision="micro" />
                </td>
                <td className="py-2 pr-3 text-right">
                  <Money value={p.to} precision="micro" />
                </td>
                <td className="py-2 text-xs text-muted-foreground">{p.by}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="mt-3 text-xs text-muted-foreground">Reasoning tokens are priced and budgeted separately from output tokens, so reasoning models don’t under-report.</p>
      </Section>
    </>
  )
}
