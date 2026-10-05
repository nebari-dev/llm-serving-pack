import { ArrowRight, Download, Plus } from 'lucide-react'
import { Link, useSearchParams } from 'react-router-dom'
import { Money } from '@/components/gw/numbers'
import { PageHeader, Section } from '@/components/gw/page'
import { ProvenanceBadge } from '@/components/gw/provenance'
import { StateChip } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import { backends, dataMode, modelById, models, seedAliases, seedDeprecations, seedModalities, seedPricing, seedRates, type AliasView, type MockPricingView, type PricingView } from '@/data/catalog'
import { cn } from '@/lib/utils'
import { useLive } from '@/state/live'
import { PricingLive } from './pricing-live'

// §7.4 Models → Catalog · Aliases · Pricing. §5.2 model_catalog,
// model_pricing, model_aliases. §13: reasoning tokens are a separate cost type.

type Tab = 'catalog' | 'aliases' | 'pricing'

const live = dataMode === 'api'

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
            <Button disabled={live} title={live ? "Alias writes aren't connected yet: the control plane can't create aliases." : undefined}>
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
        {live ? <PricingLive /> : <PricingTab />}
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
                  {seedModalities?.[m.id]?.map((x) => (
                    <span key={x} className="rounded-sm border border-border px-1.5 text-xs leading-5 text-muted-foreground-strong">
                      {x}
                    </span>
                  )) ?? <span className="text-xs text-muted-foreground">—</span>}
                </span>
              </td>
              <td className={cn(td, 'font-mono text-xs')}>
                {backends
                  .filter((b) => b.models.includes(m.id))
                  .map((b) => b.name)
                  .join(', ')}
              </td>
              <td className={cn(td, 'pr-6')}>
                {!seedDeprecations ? (
                  <span className="text-xs text-muted-foreground">—</span>
                ) : seedDeprecations[m.id] ? (
                  <StateChip tone="degraded">Deprecated {seedDeprecations[m.id]}</StateChip>
                ) : (
                  <span className="text-xs text-muted-foreground">Available</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {!seedModalities && (
        <p className="px-6 py-3 text-xs text-muted-foreground">Not connected yet: the catalog doesn’t store modalities or deprecation dates.</p>
      )}
    </div>
  )
}

// Blended is a 3:1 input:output mix per 1M tokens.
const blended = (inPerM: number, outPerM: number) => (inPerM * 3 + outPerM) / 4

/** Api mode: a model costs what its backend charges, so show the range over
 * the backends that serve it, or no price. */
function LiveBlendedCost({ model, pricing }: { model: string; pricing: PricingView | null }) {
  if (!pricing) return <Money value={0} unknown />
  const costs = pricing.prices
    .filter((p) => p.model === model && p.rates.input && p.rates.output)
    .map((p) => blended(p.rates.input!.perM, p.rates.output!.perM))
    .sort((a, b) => a - b)
  if (costs.length === 0) return <Money value={null} />
  if (costs[0] === costs.at(-1)) return <Money value={costs[0]} />
  return (
    <span title="Depends on the backend that serves it">
      <Money value={costs[0]} />–<Money value={costs.at(-1)!} />
    </span>
  )
}

function AliasesTab() {
  const { data: aliases, loaded } = useLive<AliasView[]>(live ? '/aliases' : null, seedAliases, 60_000)
  const { data: pricing } = useLive<PricingView | null>(live ? '/pricing' : null, null, 300_000)
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
              const m = modelById[a.target] as (typeof models)[number] | undefined
              const rates = seedRates?.[a.target]
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
                    {a.provenance ? (
                      <ProvenanceBadge provenance={a.provenance} source="github.com/acme/platform-gitops/blob/main/gateway/aliases.yaml" />
                    ) : (
                      <span className="text-xs text-muted-foreground">Not recorded</span>
                    )}
                  </td>
                  <td className={cn(td, 'num text-right font-mono')}>{a.requests24h.toLocaleString('en-US')}</td>
                  <td className={cn(td, 'pr-6 text-right')}>
                    {!m ? (
                      <span className="text-xs text-muted-foreground">Not in catalog</span>
                    ) : live ? (
                      <LiveBlendedCost model={a.target} pricing={pricing} />
                    ) : (
                      rates && <Money value={blended(rates.inPerM, rates.outPerM)} />
                    )}
                  </td>
                </tr>
              )
            })}
            {loaded && aliases.length === 0 && (
              <tr>
                <td colSpan={7} className="px-6 py-6 text-center text-sm text-muted-foreground">
                  No aliases yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <Section>
        {live ? (
          <p className="text-sm text-muted-foreground">
            An exact alias wins over a <code className="font-mono">*</code> pattern. Requests count what the client asked for, including ones a policy or fallback later sent elsewhere.
          </p>
        ) : (
          <p className="text-sm text-muted-foreground">
            Conditions evaluate top to bottom; the first match wins. Savings suggestions from Spend open here as a <em>draft</em> alias change — nothing is applied until you review the diff.{' '}
            <Link to="/spend" className="text-foreground underline underline-offset-4">
              See savings analysis
            </Link>
          </p>
        )}
      </Section>
    </>
  )
}

/** Mock mode's pricing: the fixtures' one price per model. */
function PricingTab() {
  const pricing: MockPricingView | null = seedPricing
  const loaded = true
  const changes = pricing?.changes ?? []
  const showBy = changes.some((p) => p.by)
  const since = Object.values(pricing?.effectiveFrom ?? {}).sort()[0]
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
            {models.flatMap((x) => (seedRates?.[x.id] ? [seedRates[x.id]] : [])).map((m) => (
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
                <td className={cn(td, 'num font-mono text-xs')}>{pricing?.effectiveFrom[m.id] ?? '—'}</td>
                <td className={cn(td, 'pr-6 text-xs text-muted-foreground')}>{pricing?.source?.[m.id] ?? 'Seed price'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Section
        title="Price changes"
        description="Every receipt snapshots the price row in effect when it was written. A March receipt reconciles to March prices, not today's."
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => toast.add({ title: 'Exported model-pricing.csv', type: 'success' })}
          >
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
              {showBy && <th className="py-1.5 font-medium">Changed by</th>}
            </tr>
          </thead>
          <tbody>
            {changes.map((p) => (
              <tr key={p.model + p.field + p.effective} className="border-b border-border last:border-0">
                <td className="num py-2 pr-3 font-mono text-xs">{p.effective}</td>
                <td className="py-2 pr-3 font-mono text-xs">{p.model}</td>
                <td className="py-2 pr-3 text-xs">{p.field} / 1M</td>
                <td className="py-2 pr-3 text-right text-muted-foreground line-through">
                  <Money value={p.from} precision="micro" />
                </td>
                <td className="py-2 pr-3 text-right">
                  <Money value={p.to} precision="micro" />
                </td>
                {showBy && <td className="py-2 text-xs text-muted-foreground">{p.by}</td>}
              </tr>
            ))}
            {loaded && changes.length === 0 && (
              <tr>
                <td colSpan={5} className="py-4 text-center text-sm text-muted-foreground">
                  No price changes since {since ?? 'pricing began'}.
                </td>
              </tr>
            )}
          </tbody>
        </table>
        <p className="mt-3 text-xs text-muted-foreground">Reasoning tokens are priced and budgeted separately from output tokens, so reasoning models don’t under-report.</p>
      </Section>
    </>
  )
}
