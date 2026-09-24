import {
  ArrowDown,
  ArrowRight,
  ArrowUp,
  CircleX,
  Download,
  FileCode,
  GitBranch,
  Pencil,
  Plus,
  RotateCcw,
  ShieldAlert,
  TriangleAlert,
  Undo2,
} from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { DiffView } from '@/components/gw/diff-view'
import { Duration } from '@/components/gw/numbers'
import { PageHeader, Section } from '@/components/gw/page'
import { ProvenanceBadge, SyncStateIndicator } from '@/components/gw/provenance'
import { StateChip } from '@/components/gw/verdict'
import { Alert, AlertAction, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { CodeBlock, CodeBlockBody, CodeBlockHeader } from '@/components/ui/code-block'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Drawer, DrawerBody, DrawerContent, DrawerDescription, DrawerFooter, DrawerHeader, DrawerTitle } from '@/components/ui/drawer'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import { type Backend, backends as seedBackends, modelById, type Provenance, type Route, routes, type SyncState } from '@/data/mock'
import { cn } from '@/lib/utils'
import { backendDiff, type BackendSpec, backendSpecs, backendYaml, routeYaml } from './routing-yaml'

// §7.5.6 Models, routing, and backends — one list surface with provenance as a
// first-class column. §4.4 provenance + reconciliation, §7.6 cross-cutting states.

type Tab = 'routes' | 'backends' | 'fallback'

interface BackendState extends Backend {
  spec: BackendSpec
  /** Proposed spec awaiting reconcile (§7.1 principle 4: pending is a real state). */
  pending?: BackendSpec
}

const healthTone = { healthy: 'allowed', degraded: 'degraded', down: 'blocked' } as const
const healthLabel = { healthy: 'Healthy', degraded: 'Degraded', down: 'Down' } as const

const reconcileError =
  'admission webhook "vaiservicebackend.aigateway.envoyproxy.io" denied the request: spec.backendRef: Backend.gateway.envoyproxy.io "azure-openai-eu" not found in namespace "nebari-gateway"'

/** §9.2: persistent marker on every surface where a content-capturing route appears. */
export function CaptureMarker({ className }: { className?: string }) {
  return (
    <StateChip
      tone="degraded"
      className={cn('font-semibold', className)}
      icon={<ShieldAlert className="size-3" aria-hidden="true" />}
      title="Raw prompt and response content is stored for this route"
    >
      Content capture on
    </StateChip>
  )
}

function download(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'application/yaml' }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
  toast.add({ title: `Exported ${filename}`, description: 'Export recorded in the audit log.', type: 'success' })
}

function YamlDialog({ title, yaml, open, onOpenChange }: { title: string; yaml: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>Generated YAML</DialogTitle>
          <DialogDescription>
            What the console applies to the cluster for <span className="font-mono">{title}</span>. Hand it to Git and the gateway keeps working without the console.
          </DialogDescription>
        </DialogHeader>
        <CodeBlock code={yaml} showLineNumbers className="w-full">
          <CodeBlockHeader>{title}.yaml</CodeBlockHeader>
          <CodeBlockBody maxLines={22} />
        </CodeBlock>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Close</DialogClose>
          <Button onClick={() => download(`${title}.yaml`, yaml)}>
            <Download /> Export YAML
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function RoutingPage() {
  const [params, setParams] = useSearchParams()
  const tab = (params.get('tab') as Tab) ?? 'backends'
  const setTab = (t: Tab) => {
    const next = new URLSearchParams(params)
    next.set('tab', t)
    setParams(next, { replace: true })
  }

  const [items, setItems] = useState<BackendState[]>(() => seedBackends.map((b) => ({ ...b, spec: { ...backendSpecs[b.name] } })))
  const [selected, setSelected] = useState<string | null>(null)
  const [yamlFor, setYamlFor] = useState<BackendState | null>(null)
  const timers = useRef<number[]>([])
  useEffect(() => () => timers.current.forEach(clearTimeout), [])

  const update = (name: string, patch: Partial<BackendState>) => setItems((xs) => xs.map((x) => (x.name === name ? { ...x, ...patch } : x)))

  const propose = (name: string, spec: BackendSpec) => {
    update(name, { pending: spec, sync: 'applying' })
    toast.add({ title: 'Changes applied', description: `${name} is reconciling. The previous config stays live until it syncs.`, type: 'success' })
    timers.current.push(
      window.setTimeout(() => {
        setItems((xs) => xs.map((x) => (x.name === name && x.pending ? { ...x, spec: x.pending, pending: undefined, sync: 'synced' } : x)))
      }, 6000),
    )
  }

  const current = items.find((b) => b.name === selected) ?? null

  return (
    <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)} className="gap-0">
      <PageHeader
        title="Routing"
        description="Where each request goes: routes match requests to model targets, backends are the providers behind them. Console-owned resources are editable; Git-managed ones are read-only here and link to their source."
        actions={
          <Button render={<Link to="/onboarding" />}>
            <Plus /> Add provider
          </Button>
        }
      >
        <TabsList variant="underline" className="-mb-4">
          <TabsTab value="routes">Routes</TabsTab>
          <TabsTab value="backends">Backends</TabsTab>
          <TabsTab value="fallback">Fallback</TabsTab>
          <TabsIndicator />
        </TabsList>
      </PageHeader>

      <TabsPanel value="backends">
        <BackendsList items={items} onOpen={setSelected} onYaml={setYamlFor} onCancel={(n) => update(n, { pending: undefined, sync: 'synced' })} />
      </TabsPanel>
      <TabsPanel value="routes">
        <RoutesList />
      </TabsPanel>
      <TabsPanel value="fallback">
        <FallbackView />
      </TabsPanel>

      <Drawer open={!!current} onOpenChange={(o) => !o && setSelected(null)}>
        <DrawerContent style={{ ['--drawer-content-width' as string]: 'min(44rem, 100vw)' }}>
          {current && (
            <BackendDetail
              b={current}
              onPropose={(spec) => propose(current.name, spec)}
              onCancelPending={() => update(current.name, { pending: undefined, sync: 'synced' })}
              onAdopt={() => {
                update(current.name, { provenance: 'adopted' as Provenance })
                toast.add({ title: `${current.name} adopted into the console`, description: 'Audit record written. Remove it from the Git repo to stop Argo reporting it out of sync.', type: 'success' })
              }}
              onResolveDrift={(mode) => {
                if (mode === 'revert') {
                  update(current.name, { sync: 'applying' })
                  toast.add({ title: 'Reverting to desired state', description: 'replicas → 4', type: 'info' })
                  timers.current.push(window.setTimeout(() => update(current.name, { sync: 'synced' }), 4000))
                } else {
                  update(current.name, { sync: 'synced', spec: { ...current.spec, replicas: 2 } })
                  toast.add({ title: 'Accepted as new desired state', description: 'replicas = 2 is now the desired value.', type: 'success' })
                }
              }}
              onRetry={() => {
                update(current.name, { sync: 'applying' as SyncState })
                timers.current.push(
                  window.setTimeout(() => {
                    update(current.name, { sync: 'failed' })
                    toast.add({ title: 'Reconcile failed again', description: 'The Backend object is still missing.', type: 'error' })
                  }, 3000),
                )
              }}
              onYaml={() => setYamlFor(current)}
            />
          )}
        </DrawerContent>
      </Drawer>

      {yamlFor && <YamlDialog title={yamlFor.name} yaml={backendYaml(yamlFor, yamlFor.spec)} open onOpenChange={(o) => !o && setYamlFor(null)} />}
    </Tabs>
  )
}

// ---- Backends -------------------------------------------------------------

function BackendsList({
  items,
  onOpen,
  onYaml,
  onCancel,
}: {
  items: BackendState[]
  onOpen: (name: string) => void
  onYaml: (b: BackendState) => void
  onCancel: (name: string) => void
}) {
  return (
    <Section className="px-0 py-0">
      <div className="overflow-x-auto">
        <table className="w-full min-w-[60rem] text-sm">
          <caption className="sr-only">Backends</caption>
          <thead className="bg-header text-left text-xs text-muted-foreground-strong">
            <tr className="border-b border-border">
              <th className="py-2 pr-3 pl-6 font-medium">Backend</th>
              <th className="px-3 py-2 font-medium">Provider</th>
              <th className="px-3 py-2 font-medium">Region</th>
              <th className="px-3 py-2 font-medium">Owner</th>
              <th className="px-3 py-2 font-medium">Sync</th>
              <th className="px-3 py-2 font-medium">Health</th>
              <th className="px-3 py-2 text-right font-medium">p50</th>
              <th className="px-3 py-2 text-right font-medium">Errors</th>
              <th className="py-2 pr-6 pl-3 text-right font-medium">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {items.map((b) => (
              <tr
                key={b.name}
                onClick={() => onOpen(b.name)}
                className={cn(
                  'cursor-pointer border-b border-border hover:bg-muted/50',
                  b.pending && 'bg-muted/40 [&>td:not(:last-child)]:opacity-60',
                )}
              >
                <td className={cn('relative py-2.5 pr-3 pl-6', b.pending && 'before:absolute before:inset-y-1 before:left-2 before:w-0.5 before:rounded-full before:border-l-2 before:border-dashed before:border-border-strong')}>
                  <div className="flex flex-wrap items-center gap-2">
                    <button type="button" className="font-mono font-medium hover:underline" onClick={(e) => (e.stopPropagation(), onOpen(b.name))}>
                      {b.name}
                    </button>
                    {b.captureContent && <CaptureMarker />}
                  </div>
                  <div className="text-xs text-muted-foreground">{b.models.join(', ')}</div>
                </td>
                <td className="px-3 py-2.5">{b.provider}</td>
                <td className="px-3 py-2.5 font-mono text-xs">{b.region}</td>
                <td className="px-3 py-2.5">
                  <ProvenanceBadge provenance={b.provenance} source={b.source} />
                </td>
                <td className="px-3 py-2.5">
                  {b.pending ? (
                    <span className="inline-flex items-center gap-2">
                      <StateChip tone="neutral" className="border-dashed">
                        Pending apply
                      </StateChip>
                    </span>
                  ) : (
                    <SyncStateIndicator state={b.sync} />
                  )}
                </td>
                <td className="px-3 py-2.5">
                  {b.health === 'healthy' ? (
                    <span className="text-xs text-muted-foreground">Healthy</span>
                  ) : (
                    <StateChip tone={healthTone[b.health]}>{healthLabel[b.health]}</StateChip>
                  )}
                </td>
                <td className="px-3 py-2.5 text-right">{b.p50 ? <Duration ms={b.p50} /> : <span className="font-mono text-muted-foreground">—</span>}</td>
                <td className={cn('num px-3 py-2.5 text-right font-mono', b.errorRate > 2 && 'text-v-blocked-fg')}>{b.errorRate.toFixed(1)}%</td>
                <td className="py-2.5 pr-6 pl-3" onClick={(e) => e.stopPropagation()}>
                  <div className="flex justify-end gap-1">
                    {b.pending && (
                      <Button variant="ghost" size="xs" onClick={() => onCancel(b.name)}>
                        <Undo2 /> Cancel
                      </Button>
                    )}
                    <Button variant="ghost" size="xs" onClick={() => onYaml(b)}>
                      <FileCode /> View YAML
                    </Button>
                    <Button variant="outline" size="xs" onClick={() => download(`${b.name}.yaml`, backendYaml(b, b.spec))}>
                      <Download /> Export
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="px-6 py-3 text-xs text-muted-foreground">
        Console-owned backends reconcile from the console database; drift is reverted on the next loop and reported here. Git-managed backends are mirrored read-only from the cluster.
      </p>
    </Section>
  )
}

function BackendDetail({
  b,
  onPropose,
  onCancelPending,
  onAdopt,
  onResolveDrift,
  onRetry,
  onYaml,
}: {
  b: BackendState
  onPropose: (spec: BackendSpec) => void
  onCancelPending: () => void
  onAdopt: () => void
  onResolveDrift: (mode: 'revert' | 'accept') => void
  onRetry: () => void
  onYaml: () => void
}) {
  const [draft, setDraft] = useState<BackendSpec>(b.spec)
  const [reviewing, setReviewing] = useState(false)
  const [adoptOpen, setAdoptOpen] = useState(false)
  const editable = b.provenance !== 'git'
  const dirty = JSON.stringify(draft) !== JSON.stringify(b.spec)

  return (
    <>
      <DrawerHeader className="flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <ProvenanceBadge provenance={b.provenance} source={b.source} />
          {b.pending ? <StateChip tone="neutral" className="border-dashed">Pending apply</StateChip> : <SyncStateIndicator state={b.sync} />}
          {b.captureContent && <CaptureMarker />}
        </div>
        <DrawerTitle className="font-mono">{b.name}</DrawerTitle>
        <DrawerDescription>
          {b.provider} · {b.region} · serves {b.models.join(', ')}
        </DrawerDescription>
      </DrawerHeader>

      <DrawerBody className="gap-5">
        {b.sync === 'failed' && (
          <Alert variant="destructive">
            <CircleX />
            <AlertTitle>The cluster rejected the last apply.</AlertTitle>
            <AlertDescription>
              <p className="mb-2">API server response, verbatim:</p>
              <pre className="rounded-sm border border-destructive-foreground/30 bg-canvas p-2 font-mono text-xs whitespace-pre-wrap text-foreground">{reconcileError}</pre>
              <p className="mt-2">Create the missing Backend endpoint, or edit this backend to point at an existing one, then retry.</p>
            </AlertDescription>
            <AlertAction>
              <Button size="sm" variant="outline" onClick={onRetry}>
                <RotateCcw /> Retry
              </Button>
            </AlertAction>
          </Alert>
        )}

        {b.sync === 'drift' && (
          <Alert variant="warning">
            <TriangleAlert />
            <AlertTitle>Drift: the cluster no longer matches the desired state.</AlertTitle>
            <AlertDescription>
              <p className="mb-2">
                <span className="font-mono">argocd</span> changed one field 3h 36m ago. Since then p95 latency on <span className="font-mono">eu-private</span> is up 610ms.
              </p>
              <DiffView title="spec.replicas" diff={`-  replicas: 4\n+  replicas: 2`} className="mb-2" />
              <div className="flex flex-wrap gap-2">
                <Button size="sm" variant="outline" onClick={() => onResolveDrift('revert')}>
                  <Undo2 /> Revert to desired
                </Button>
                <Button size="sm" variant="outline" onClick={() => onResolveDrift('accept')}>
                  Accept as new desired
                </Button>
              </div>
            </AlertDescription>
          </Alert>
        )}

        {b.provenance === 'git' ? (
          <section className="flex flex-col gap-3">
            <div className="rounded-md border border-border bg-muted p-3 text-sm text-muted-foreground-strong">
              <p className="flex items-center gap-2 font-medium text-foreground">
                <GitBranch className="size-4" aria-hidden="true" /> Managed in Git — read-only here
              </p>
              <p className="mt-1">
                Argo CD owns this backend. Change it with a pull request to{' '}
                <a className="font-mono text-foreground underline underline-offset-4" href={`https://${b.source}`} target="_blank" rel="noreferrer">
                  {b.source?.split('/blob/main/')[1]} ↗
                </a>
                , or adopt it so the console owns it from now on.
              </p>
            </div>
            <dl className="grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1.5 text-sm">
              <dt className="text-muted-foreground">Endpoint</dt>
              <dd className="font-mono text-xs">{b.spec.endpoint}</dd>
              <dt className="text-muted-foreground">Request timeout</dt>
              <dd className="font-mono text-xs">{b.spec.timeout}</dd>
              <dt className="text-muted-foreground">Retries</dt>
              <dd className="font-mono text-xs">{b.spec.maxRetries}</dd>
              {b.spec.replicas !== undefined && (
                <>
                  <dt className="text-muted-foreground">Replicas</dt>
                  <dd className="font-mono text-xs">{b.spec.replicas}</dd>
                </>
              )}
            </dl>
            <div>
              <Button variant="outline" onClick={() => setAdoptOpen(true)}>
                Adopt into console
              </Button>
            </div>
          </section>
        ) : reviewing ? (
          <section className="flex flex-col gap-3">
            <h3 className="text-base font-semibold">Review changes</h3>
            <p className="text-sm text-muted-foreground">Nothing changes in the cluster until you apply. This is the exact diff the reconciler will send.</p>
            <DiffView title={`AIServiceBackend/${b.name}`} diff={backendDiff(b, b.spec, draft)} />
          </section>
        ) : (
          <section className="flex flex-col gap-4">
            <h3 className="text-base font-semibold">Configuration</h3>
            <Field>
              <FieldLabel>Endpoint</FieldLabel>
              <Input className="font-mono" value={draft.endpoint} onChange={(e) => setDraft({ ...draft, endpoint: e.target.value })} disabled={!!b.pending} />
            </Field>
            <div className="grid grid-cols-2 gap-4">
              <Field>
                <FieldLabel>Request timeout</FieldLabel>
                <Input className="font-mono" value={draft.timeout} onChange={(e) => setDraft({ ...draft, timeout: e.target.value })} disabled={!!b.pending} />
              </Field>
              <Field>
                <FieldLabel>Retries</FieldLabel>
                <Input
                  className="font-mono"
                  type="number"
                  min={0}
                  max={5}
                  value={draft.maxRetries}
                  onChange={(e) => setDraft({ ...draft, maxRetries: Number(e.target.value) })}
                  disabled={!!b.pending}
                />
              </Field>
            </div>
            <Field>
              <FieldLabel>Provider key</FieldLabel>
              <Input value="•••••••••••••••• (sealed)" disabled />
              <FieldDescription>Tested once, then sealed. Never returned to callers. Replace it from Settings → Providers.</FieldDescription>
            </Field>
            {b.pending && (
              <p className="flex items-center gap-2 text-sm text-muted-foreground">
                <StateChip tone="neutral" className="border-dashed">
                  Pending apply
                </StateChip>
                Your change is reconciling.
                <Button variant="link" size="xs" className="px-0" onClick={onCancelPending}>
                  Cancel
                </Button>
              </p>
            )}
          </section>
        )}

        <section className="flex flex-col gap-2 border-t border-border pt-4">
          <h3 className="text-base font-semibold">Recent reconcile events</h3>
          <ul className="flex flex-col gap-1 font-mono text-xs text-muted-foreground-strong">
            <li>13:58:02 observed generation 14 · {b.sync === 'failed' ? 'apply rejected by admission webhook' : 'status Accepted'}</li>
            <li>12:41:17 field manager {b.provenance === 'git' ? 'argocd-controller' : 'nebari-gateway-console'} applied generation 14</li>
            <li>09:03:55 informer resync · no changes</li>
          </ul>
        </section>
      </DrawerBody>

      <DrawerFooter className="justify-between">
        <div className="flex gap-2">
          <Button variant="outline" onClick={onYaml}>
            <FileCode /> View generated YAML
          </Button>
          <Button variant="outline" onClick={() => download(`${b.name}.yaml`, backendYaml(b, b.spec))}>
            <Download /> Export
          </Button>
        </div>
        {editable &&
          (reviewing ? (
            <div className="flex gap-2">
              <Button variant="ghost" onClick={() => setReviewing(false)}>
                Back to edit
              </Button>
              <Button
                onClick={() => {
                  onPropose(draft)
                  setReviewing(false)
                }}
              >
                Apply changes
              </Button>
            </div>
          ) : (
            <Button disabled={!dirty || !!b.pending} onClick={() => setReviewing(true)}>
              Review changes
            </Button>
          ))}
      </DrawerFooter>

      <Dialog open={adoptOpen} onOpenChange={setAdoptOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Adopt {b.name} into the console?</DialogTitle>
            <DialogDescription render={<div />} className="flex flex-col gap-2">
              <p>
                Git will no longer control this backend. Your next Argo sync will show it as out of sync unless you remove it from the repo:
              </p>
              <p className="font-mono text-xs break-all">{b.source}</p>
              <p>The console becomes the field manager and reconciles it from here on. This writes an audit record under your name. You can release it back to Git later by exporting its YAML.</p>
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>Keep it in Git</DialogClose>
            <Button
              onClick={() => {
                onAdopt()
                setAdoptOpen(false)
              }}
            >
              Adopt into console
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

// ---- Routes ---------------------------------------------------------------

const captureBackends = new Set(seedBackends.filter((b) => b.captureContent).map((b) => b.name))

function RoutesList() {
  const [editing, setEditing] = useState<Route | null>(null)
  const [yamlFor, setYamlFor] = useState<Route | null>(null)
  return (
    <>
      <ul className="divide-y divide-border">
        {routes.map((r) => {
          const captures = r.captureContent || r.targets.some((t) => captureBackends.has(t.backend))
          return (
            <li key={r.name} className="px-6 py-4">
              <div className="mb-3 flex flex-wrap items-center gap-2">
                <h2 className="font-mono text-base font-semibold">{r.name}</h2>
                <ProvenanceBadge provenance={r.provenance} />
                <SyncStateIndicator state={r.sync} />
                {captures && <CaptureMarker />}
                <div className="ml-auto flex gap-1">
                  {r.provenance !== 'git' ? (
                    <Button variant="ghost" size="xs" onClick={() => setEditing(r)}>
                      <Pencil /> Edit route
                    </Button>
                  ) : (
                    <span className="self-center pr-2 text-xs text-muted-foreground">Read-only · edit in Git</span>
                  )}
                  <Button variant="ghost" size="xs" onClick={() => setYamlFor(r)}>
                    <FileCode /> View YAML
                  </Button>
                  <Button variant="outline" size="xs" onClick={() => download(`${r.name}.yaml`, routeYaml(r))}>
                    <Download /> Export
                  </Button>
                </div>
              </div>
              <div className="grid grid-cols-1 items-start gap-4 md:grid-cols-[minmax(0,1fr)_auto_minmax(0,1.3fr)_minmax(0,1fr)]">
                <div>
                  <div className="mb-1 text-xs text-muted-foreground">When</div>
                  <div className="rounded-md border border-border bg-muted px-3 py-2 font-mono text-xs">{r.match}</div>
                </div>
                <ArrowRight className="mt-7 hidden size-4 text-muted-foreground md:block" aria-hidden="true" />
                <div>
                  <div className="mb-1 text-xs text-muted-foreground">Send to</div>
                  <ul className="flex flex-col gap-1.5">
                    {r.targets.map((t) => (
                      <li key={t.backend} className="grid grid-cols-[1fr_auto] items-center gap-x-3 gap-y-1">
                        <span className="truncate text-sm">
                          <span className="font-mono">{t.model}</span> <span className="text-muted-foreground">via {t.backend}</span>
                        </span>
                        <span className="num font-mono text-xs">{t.weight}%</span>
                        <span className="col-span-2 h-1 rounded-full bg-muted" aria-hidden="true">
                          <span className="block h-full rounded-full bg-foreground/60" style={{ width: `${t.weight}%` }} />
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
                <div>
                  <div className="mb-1 text-xs text-muted-foreground">If unavailable, fall back to</div>
                  {r.fallback.length ? (
                    <ol className="flex flex-col gap-1 text-sm">
                      {r.fallback.map((f, i) => (
                        <li key={f} className="flex items-center gap-2">
                          <span className="num flex size-5 items-center justify-center rounded-full border border-border font-mono text-[11px]">{i + 1}</span>
                          <span className="font-mono text-xs">{f}</span>
                          {captureBackends.has(f) && <CaptureMarker />}
                        </li>
                      ))}
                    </ol>
                  ) : (
                    <p className="text-xs text-muted-foreground">No fallback. Requests fail if all targets are down.</p>
                  )}
                </div>
              </div>
            </li>
          )
        })}
      </ul>
      <p className="border-t border-border px-6 py-3 text-xs text-muted-foreground">
        The visual editor compiles to an AIGatewayRoute. Routes it can’t express — regex header matches, per-rule filters — open in a YAML editor with schema validation instead.
      </p>
      {editing && <RouteEditor route={editing} onClose={() => setEditing(null)} />}
      {yamlFor && <YamlDialog title={yamlFor.name} yaml={routeYaml(yamlFor)} open onOpenChange={(o) => !o && setYamlFor(null)} />}
    </>
  )
}

function RouteEditor({ route, onClose }: { route: Route; onClose: () => void }) {
  const [targets, setTargets] = useState(route.targets)
  const [fallback, setFallback] = useState(route.fallback)
  const [reviewing, setReviewing] = useState(false)
  const total = targets.reduce((a, t) => a + t.weight, 0)
  const move = (i: number, d: -1 | 1) => {
    const next = [...fallback]
    ;[next[i], next[i + d]] = [next[i + d], next[i]]
    setFallback(next)
  }
  const after = { ...route, targets, fallback }
  const before = routeYaml(route).split('\n')
  const next = routeYaml(after).split('\n')
  const diff = next
    .map((l, i) => (before[i] === l ? ' ' + l : (before[i] !== undefined ? '-' + before[i] + '\n' : '') + '+' + l))
    .join('\n')

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-4xl">
        <DialogHeader>
          <DialogTitle>
            Edit route <span className="font-mono">{route.name}</span>
          </DialogTitle>
          <DialogDescription>Request conditions on the left, model targets on the right, fallback as an ordered list.</DialogDescription>
        </DialogHeader>
        {reviewing ? (
          <div className="max-h-[26rem] overflow-y-auto">
            <DiffView title={`AIGatewayRoute/${route.name}`} diff={diff} />
          </div>
        ) : (
          <div className="grid max-h-[26rem] grid-cols-1 gap-6 overflow-y-auto md:grid-cols-[1fr_1.4fr]">
            <div className="flex flex-col gap-2">
              <h3 className="text-sm font-semibold">When</h3>
              <div className="flex items-center gap-2 rounded-md border border-border p-2 text-sm">
                <span className="text-muted-foreground">model</span>
                <span className="rounded-sm bg-muted px-1.5 text-xs">matches</span>
                <span className="font-mono text-xs">{route.match.split('= ')[1] ?? route.match}</span>
              </div>
              <Button variant="ghost" size="xs" className="self-start">
                <Plus /> Add condition
              </Button>
            </div>
            <div className="flex flex-col gap-4">
              <div className="flex flex-col gap-2">
                <h3 className="text-sm font-semibold">Send to</h3>
                {targets.map((t, i) => (
                  <div key={t.backend} className="grid grid-cols-[1fr_5rem] items-center gap-2">
                    <div className="truncate rounded-md border border-border px-2 py-1.5 text-sm">
                      <span className="font-mono">{t.model}</span> <span className="text-xs text-muted-foreground">via {t.backend} · {modelById[t.model]?.provider}</span>
                    </div>
                    <Input
                      aria-label={`Weight for ${t.backend}`}
                      className="num text-right font-mono"
                      type="number"
                      min={0}
                      max={100}
                      value={t.weight}
                      onChange={(e) => setTargets(targets.map((x, j) => (j === i ? { ...x, weight: Number(e.target.value) } : x)))}
                      aria-invalid={total !== 100 || undefined}
                    />
                  </div>
                ))}
                {total !== 100 && <p className="text-xs text-destructive-foreground">Weights add up to {total}%. They must total 100%.</p>}
              </div>
              <div className="flex flex-col gap-2">
                <h3 className="text-sm font-semibold">If unavailable, fall back to</h3>
                <ol className="flex flex-col gap-1">
                  {fallback.map((f, i) => (
                    <li key={f} className="flex items-center gap-2 rounded-md border border-border px-2 py-1">
                      <span className="num font-mono text-xs text-muted-foreground">{i + 1}</span>
                      <span className="font-mono text-xs">{f}</span>
                      {captureBackends.has(f) && <CaptureMarker />}
                      <span className="ml-auto flex">
                        <Button variant="ghost" size="icon-xs" aria-label={`Move ${f} up`} disabled={i === 0} onClick={() => move(i, -1)}>
                          <ArrowUp />
                        </Button>
                        <Button variant="ghost" size="icon-xs" aria-label={`Move ${f} down`} disabled={i === fallback.length - 1} onClick={() => move(i, 1)}>
                          <ArrowDown />
                        </Button>
                      </span>
                    </li>
                  ))}
                </ol>
              </div>
            </div>
          </div>
        )}
        <DialogFooter className="sm:justify-between">
          <p className="self-center text-xs text-muted-foreground">Can’t express it here? Open in YAML editor — validated against the AIGatewayRoute schema.</p>
          <div className="flex gap-2">
            {reviewing ? (
              <>
                <Button variant="ghost" onClick={() => setReviewing(false)}>
                  Back to edit
                </Button>
                <Button
                  onClick={() => {
                    toast.add({ title: 'Changes applied', description: `Route ${route.name} is reconciling.`, type: 'success' })
                    onClose()
                  }}
                >
                  Apply changes
                </Button>
              </>
            ) : (
              <>
                <DialogClose render={<Button variant="outline" />}>Discard</DialogClose>
                <Button disabled={total !== 100} onClick={() => setReviewing(true)}>
                  Review changes
                </Button>
              </>
            )}
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---- Fallback -------------------------------------------------------------

const failovers = [
  { ts: '14:02:10', from: 'anthropic-prod', to: 'bedrock-eu', route: 'default', reason: '529 overloaded', requests: 312 },
  { ts: '13:51:44', from: 'anthropic-prod', to: 'bedrock-eu', route: 'research-frontier', reason: '529 overloaded', requests: 41 },
  { ts: '11:20:03', from: 'openai-prod', to: 'vllm-internal', route: 'cheap-summarize', reason: 'timeout after 60s', requests: 7 },
  { ts: '08:14:37', from: 'azure-openai-eu', to: '—', route: 'eu-private', reason: 'backend not reconciled', requests: 0 },
]

function FallbackView() {
  return (
    <>
      <Section title="Fallback chains" description="Tried in order when the primary target returns a retryable error or times out.">
        <ul className="flex flex-col divide-y divide-border rounded-md border border-border">
          {routes.map((r) => (
            <li key={r.name} className="flex flex-wrap items-center gap-2 px-3 py-2 text-sm">
              <span className="w-40 font-mono font-medium">{r.name}</span>
              {[...new Set([...r.targets.map((t) => t.backend), ...r.fallback])].map((b, i, arr) => (
                <span key={b} className="inline-flex items-center gap-2">
                  <span className={cn('rounded-sm border px-1.5 font-mono text-xs leading-5', i === 0 ? 'border-border-strong bg-card' : 'border-border bg-muted')}>{b}</span>
                  {captureBackends.has(b) && <CaptureMarker />}
                  {i < arr.length - 1 && <ArrowRight className="size-3.5 text-muted-foreground" aria-hidden="true" />}
                </span>
              ))}
              {r.fallback.length === 0 && <span className="text-xs text-muted-foreground">no fallback</span>}
            </li>
          ))}
        </ul>
      </Section>
      <Section title="Recent failovers" description="Each links to the receipts that fell back.">
        <table className="w-full text-sm">
          <thead className="text-left text-xs text-muted-foreground">
            <tr className="border-b border-border">
              <th className="py-1.5 pr-3 font-medium">Time</th>
              <th className="py-1.5 pr-3 font-medium">From</th>
              <th className="py-1.5 pr-3 font-medium">To</th>
              <th className="py-1.5 pr-3 font-medium">Route</th>
              <th className="py-1.5 pr-3 font-medium">Reason</th>
              <th className="py-1.5 text-right font-medium">Requests</th>
            </tr>
          </thead>
          <tbody>
            {failovers.map((f) => (
              <tr key={f.ts} className="border-b border-border last:border-0">
                <td className="num py-2 pr-3 font-mono text-xs">{f.ts}</td>
                <td className="py-2 pr-3 font-mono text-xs">{f.from}</td>
                <td className="py-2 pr-3 font-mono text-xs">{f.to}</td>
                <td className="py-2 pr-3 font-mono text-xs">{f.route}</td>
                <td className={cn('py-2 pr-3 text-xs', f.to === '—' && 'text-v-blocked-fg')}>{f.reason}</td>
                <td className="py-2 text-right">
                  {f.requests ? (
                    <Link to={`/traffic?backend=${f.to}&route_reason=fallback`} className="num font-mono text-xs underline-offset-4 hover:underline">
                      {f.requests.toLocaleString()} →
                    </Link>
                  ) : (
                    <span className="font-mono text-xs text-muted-foreground">0</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
    </>
  )
}
