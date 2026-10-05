import { ArrowDown, ArrowRight, ArrowUp, Download, FileCode, KeyRound, Pencil, Plus, Trash2, X } from 'lucide-react'
import { useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { DiffView } from '@/components/gw/diff-view'
import { Duration } from '@/components/gw/numbers'
import { PageHeader, Section } from '@/components/gw/page'
import { SyncStateIndicator } from '@/components/gw/provenance'
import { StateChip } from '@/components/gw/verdict'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { CodeBlock, CodeBlockBody, CodeBlockHeader } from '@/components/ui/code-block'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Drawer, DrawerBody, DrawerContent, DrawerDescription, DrawerFooter, DrawerHeader, DrawerTitle } from '@/components/ui/drawer'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import { api, ApiError, type Backend, backends as seedBackends, type LiveRoute, liveRoutes, type RouteTarget, type RoutingPlan } from '@/data/catalog'
import { ago } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useLive } from '@/state/live'
import { DeleteBackendDialog, KeyText, LastTest, ProviderDialog, ReplaceKeyDialog } from './providers-live'
import { CaptureMarker } from './routing'

// §7.5.6 Routing in api mode. Routes and backends are the control plane's
// desired state (§4.4); each route compiles to one rule of the gateway's
// AIGatewayRoute. Edits save at once, with an audit row and If-Match; the
// gateway changes only on "Apply", which shows the CRD diff against what it
// runs first. Drift, adopt and provenance wait on who owns the target's
// resources, so they aren't shown.

type Tab = 'routes' | 'backends' | 'fallback'

const healthTone = { healthy: 'allowed', degraded: 'degraded', down: 'blocked', idle: 'neutral' } as const
const healthLabel = { healthy: 'Healthy', degraded: 'Degraded', down: 'Down', idle: 'Idle' } as const

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`

function download(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'application/yaml' }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

function YamlDialog({ title, yaml, description, onClose }: { title: string; yaml: string; description: string; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex max-w-3xl flex-col">
        <DialogHeader>
          <DialogTitle>Generated YAML</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
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

const endpointText = (b: Backend) => (b.endpoint ? `${b.endpoint.host}:${b.endpoint.port}` : '—')

export function LiveRoutingPage() {
  const [params, setParams] = useSearchParams()
  const tab = (params.get('tab') as Tab) ?? 'routes'
  const setTab = (t: Tab) => {
    const next = new URLSearchParams(params)
    next.set('tab', t)
    setParams(next, { replace: true })
  }
  const routes = useLive<LiveRoute[]>('/routes', liveRoutes, 15_000)
  const plan = useLive<RoutingPlan | null>('/routing', null, 15_000)
  const backends = useLive<Backend[]>('/backends', seedBackends, 30_000)
  const reload = () => {
    routes.reload()
    plan.reload()
    backends.reload()
  }
  const [editing, setEditing] = useState<LiveRoute | 'new' | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const current = backends.data.find((b) => b.name === selected) ?? null

  return (
    <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)} className="gap-0">
      <PageHeader
        title="Routing"
        description="Where each request goes: routes match requests to backends, and the gateway runs them once applied. Every route compiles to a rule of the gateway’s AIGatewayRoute, so what you see here can be exported and run without the console."
        actions={
          <>
            <Button variant="outline" disabled={!plan.data} onClick={() => plan.data && download('routing.yaml', plan.data.yaml)}>
              <Download /> Export YAML
            </Button>
            <Button onClick={() => setAdding(true)}>
              <Plus /> Add provider
            </Button>
          </>
        }
      >
        <TabsList variant="underline" className="-mb-4">
          <TabsTab value="routes">Routes</TabsTab>
          <TabsTab value="backends">Backends</TabsTab>
          <TabsTab value="fallback">Fallback</TabsTab>
          <TabsIndicator />
        </TabsList>
      </PageHeader>

      <TabsPanel value="routes">
        <ApplyBar plan={plan.data} onApplied={reload} />
        <RoutesList routes={routes.data} loaded={routes.loaded} onNew={() => setEditing('new')} onEdit={setEditing} onDeleted={reload} />
      </TabsPanel>
      <TabsPanel value="backends">
        <BackendsTable items={backends.data} onOpen={setSelected} />
      </TabsPanel>
      <TabsPanel value="fallback">
        <FallbackView routes={routes.data} />
      </TabsPanel>

      {editing && (
        <RouteEditor
          route={editing === 'new' ? undefined : editing}
          backends={backends.data}
          onClose={() => {
            setEditing(null)
            // Even unsaved: the editor may have found someone else's change.
            reload()
          }}
        />
      )}
      {adding && (
        <ProviderDialog
          onClose={() => {
            setAdding(false)
            reload()
          }}
        />
      )}
      <Drawer open={!!current} onOpenChange={(o) => !o && setSelected(null)}>
        <DrawerContent style={{ ['--drawer-content-width' as string]: 'min(44rem, 100vw)' }}>
          {current && (
            <BackendDetail
              b={current}
              onChanged={(deleted) => {
                if (deleted) setSelected(null)
                reload()
              }}
            />
          )}
        </DrawerContent>
      </Drawer>
    </Tabs>
  )
}

// ---- Apply ------------------------------------------------------------------

function ApplyBar({ plan, onApplied }: { plan: RoutingPlan | null; onApplied: () => void }) {
  const [reviewing, setReviewing] = useState(false)
  if (!plan) return null
  const n = plan.changes.length
  const last = plan.lastApply
  const failed = last && !last.ok && n > 0
  return (
    <section aria-label="Apply to gateway" className="flex flex-col gap-3 border-b border-border px-6 py-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <p className="text-sm font-medium">{n ? `${plural(n, 'change')} not applied` : 'The gateway runs this routing.'}</p>
          <p className="text-xs text-muted-foreground">
            {n ? 'Saved routes are the desired state; the gateway runs the routing as of the last apply until you apply again.' : 'Every saved route is in the config the gateway runs.'}{' '}
            {last && (
              <>
                Last apply {ago(last.at)} by {last.actor}
                {last.ok ? '.' : ', which failed.'}
              </>
            )}
          </p>
        </div>
        <Button disabled={!n || !plan.canApply} title={plan.canApply ? undefined : plan.reason} onClick={() => setReviewing(true)}>
          {n ? `Review and apply ${plural(n, 'change')}` : 'Review and apply'}
        </Button>
      </div>
      {!plan.canApply && <p className="text-xs text-muted-foreground">Can’t apply: {plan.reason}</p>}
      {failed && (
        <Alert variant="destructive">
          <AlertTitle>The last apply failed</AlertTitle>
          <AlertDescription>
            <pre className="max-h-48 overflow-auto font-mono text-xs whitespace-pre-wrap">{last.error}</pre>
          </AlertDescription>
        </Alert>
      )}
      {reviewing && (
        <ApplyDialog
          plan={plan}
          onClose={(applied) => {
            setReviewing(false)
            onApplied()
            if (applied) toast.add({ title: 'Routing applied', description: `${plural(n, 'change')}. The gateway restarted on the new config.`, type: 'success' })
          }}
        />
      )}
    </section>
  )
}

function ApplyDialog({ plan, onClose }: { plan: RoutingPlan; onClose: (applied: boolean) => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<{ title: string; text: string } | null>(null)
  const apply = async () => {
    setBusy(true)
    setError(null)
    try {
      await api('/routing/apply', { method: 'POST', body: '{}', headers: { 'If-Match': plan.etag } })
      onClose(true)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) setError({ title: 'Routing changed since you opened this review', text: 'Close it and review the current changes.' })
      else if (e instanceof ApiError && e.status === 502) setError({ title: 'The gateway didn’t take the new config', text: e.message })
      else setError({ title: 'Not applied', text: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose(false)}>
      <DialogContent className="flex max-w-4xl flex-col">
        <DialogHeader>
          <DialogTitle>Apply {plural(plan.changes.length, 'change')} to the gateway</DialogTitle>
          <DialogDescription>
            What the gateway runs now, against the routing you saved. Applying writes {plan.target}; requests in flight while the gateway restarts can fail. If it doesn’t come back, the
            previous config is restored.
          </DialogDescription>
        </DialogHeader>
        <div className="flex max-h-[28rem] min-h-0 flex-col gap-3 overflow-y-auto">
          {plan.changes.map((c) => (
            <div key={`${c.kind}/${c.name}`} className="flex flex-col gap-1">
              <span className="text-xs text-muted-foreground">
                {c.change === 'added' ? 'Added' : c.change === 'removed' ? 'Removed' : c.change === 'key replaced' ? 'Key replaced: the gateway restarts with the new key. Only its version is in the config, never the key.' : 'Changed'}
              </span>
              <DiffView title={`${c.kind}/${c.name}`} diff={c.diff} />
            </div>
          ))}
        </div>
        {error && (
          <Alert variant="destructive">
            <AlertTitle>{error.title}</AlertTitle>
            <AlertDescription>
              <pre className="max-h-48 overflow-auto font-mono text-xs whitespace-pre-wrap">{error.text}</pre>
            </AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => onClose(false)}>
            {error ? 'Close' : 'Cancel'}
          </Button>
          <Button onClick={apply} loading={busy} loadingText="Applying… the gateway is restarting">
            {error?.title.startsWith('The gateway') ? 'Retry' : 'Apply to gateway'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---- Routes -----------------------------------------------------------------

function Models({ r }: { r: LiveRoute }) {
  return (
    <div className="flex flex-col gap-1 rounded-md border border-border bg-muted px-3 py-2 text-xs">
      <div className="flex flex-wrap items-center gap-1">
        <span className="text-muted-foreground">model</span>
        {r.match.models.map((m, i) => (
          <span key={m} className="inline-flex items-center gap-1">
            {i > 0 && <span className="text-muted-foreground">or</span>}
            <span className="font-mono">{m}</span>
          </span>
        ))}
      </div>
      {r.match.headers.map((h) => (
        <div key={h.name}>
          <span className="text-muted-foreground">and header</span> <span className="font-mono">{h.name}</span> <span className="text-muted-foreground">=</span>{' '}
          <span className="font-mono">{h.value}</span>
        </div>
      ))}
    </div>
  )
}

function Target({ t, share }: { t: RouteTarget; share?: number }) {
  return (
    <span className="truncate text-sm">
      {t.model ? <span className="font-mono">{t.model}</span> : <span className="text-muted-foreground">Requested model</span>} <span className="text-muted-foreground">via {t.backend}</span>
      {share !== undefined && <span className="num ml-2 font-mono text-xs">{share}%</span>}
    </span>
  )
}

function RoutesList({
  routes,
  loaded,
  onNew,
  onEdit,
  onDeleted,
}: {
  routes: LiveRoute[]
  loaded: boolean
  onNew: () => void
  onEdit: (r: LiveRoute) => void
  onDeleted: () => void
}) {
  const [yamlFor, setYamlFor] = useState<LiveRoute | null>(null)
  const [deleting, setDeleting] = useState<LiveRoute | null>(null)
  return (
    <>
      <div className="flex items-center justify-between px-6 pt-4">
        <p className="text-xs text-muted-foreground">
          {loaded ? plural(routes.length, 'route') : 'Loading routes…'}. The gateway tries the rule with the most header conditions first; among equals, the first in this list.
        </p>
        <Button size="sm" onClick={onNew}>
          <Plus /> New route
        </Button>
      </div>
      <ul className="divide-y divide-border">
        {routes.map((r) => {
          const total = r.targets.reduce((a, t) => a + (t.weight ?? 0), 0)
          return (
            <li key={r.name} className="px-6 py-4">
              <div className="mb-3 flex flex-wrap items-center gap-2">
                <h2 className="font-mono text-base font-semibold">{r.name}</h2>
                <SyncStateIndicator state={r.sync} />
                {r.captureContent && <CaptureMarker />}
                <div className="ml-auto flex gap-1">
                  <Button variant="ghost" size="xs" aria-label={`Edit ${r.name}`} onClick={() => onEdit(r)}>
                    <Pencil /> Edit route
                  </Button>
                  <Button variant="ghost" size="xs" onClick={() => setYamlFor(r)}>
                    <FileCode /> View YAML
                  </Button>
                  <Button variant="outline" size="xs" onClick={() => download(`${r.name}.yaml`, r.yaml)}>
                    <Download /> Export
                  </Button>
                  <Button variant="ghost" size="icon-xs" aria-label={`Delete ${r.name}`} onClick={() => setDeleting(r)}>
                    <Trash2 />
                  </Button>
                </div>
              </div>
              <div className="grid grid-cols-1 items-start gap-4 md:grid-cols-[minmax(0,1fr)_auto_minmax(0,1.3fr)_minmax(0,1fr)]">
                <div>
                  <div className="mb-1 text-xs text-muted-foreground">When</div>
                  <Models r={r} />
                </div>
                <ArrowRight className="mt-7 hidden size-4 text-muted-foreground md:block" aria-hidden="true" />
                <div>
                  <div className="mb-1 text-xs text-muted-foreground">Send to</div>
                  <ul className="flex flex-col gap-1.5">
                    {r.targets.map((t) => (
                      <li key={t.backend}>
                        <Target t={t} share={r.targets.length > 1 && total ? Math.round(((t.weight ?? 0) / total) * 100) : undefined} />
                      </li>
                    ))}
                  </ul>
                </div>
                <div>
                  <div className="mb-1 text-xs text-muted-foreground">If unavailable, fall back to</div>
                  {r.fallback.length ? (
                    <ol className="flex flex-col gap-1 text-sm">
                      {r.fallback.map((f, i) => (
                        <li key={f.backend} className="flex items-center gap-2">
                          <span className="num flex size-5 items-center justify-center rounded-full border border-border font-mono text-[11px]">{i + 1}</span>
                          <Target t={f} />
                        </li>
                      ))}
                    </ol>
                  ) : (
                    <p className="text-xs text-muted-foreground">No fallback. Retries stay on the targets.</p>
                  )}
                </div>
              </div>
            </li>
          )
        })}
      </ul>
      {yamlFor && (
        <YamlDialog
          title={yamlFor.name}
          yaml={yamlFor.yaml}
          description="The AIGatewayRoute rule this route compiles to. Export YAML on the page header has the whole routing config."
          onClose={() => setYamlFor(null)}
        />
      )}
      {deleting && (
        <DeleteRouteDialog
          route={deleting}
          onClose={(deleted) => {
            setDeleting(null)
            if (deleted) onDeleted()
          }}
        />
      )}
    </>
  )
}

interface Draft {
  name: string
  models: string
  headers: { name: string; value: string }[]
  targets: { backend: string; model: string; weight: string }[]
  fallback: { backend: string; model: string }[]
}

const toDraft = (r?: LiveRoute): Draft => ({
  name: r?.name ?? '',
  models: r?.match.models.join(', ') ?? '',
  headers: r?.match.headers.map((h) => ({ ...h })) ?? [],
  targets: r?.targets.map((t) => ({ backend: t.backend, model: t.model ?? '', weight: String(t.weight ?? 1) })) ?? [{ backend: '', model: '', weight: '1' }],
  fallback: r?.fallback.map((t) => ({ backend: t.backend, model: t.model ?? '' })) ?? [],
})

const fromDraft = (d: Draft) => ({
  name: d.name.trim(),
  match: { models: d.models.split(/[\s,]+/).filter(Boolean), headers: d.headers.map((h) => ({ name: h.name.trim(), value: h.value })) },
  targets: d.targets.map((t) => ({ backend: t.backend, model: t.model.trim() || undefined, weight: d.targets.length > 1 ? Number(t.weight) : undefined })),
  fallback: d.fallback.map((t) => ({ backend: t.backend, model: t.model.trim() || undefined })),
})

function BackendSelect({ label, value, backends, onChange }: { label: string; value: string; backends: Backend[]; onChange: (v: string) => void }) {
  return (
    <Select items={backends.map((b) => ({ value: b.name, label: b.name }))} value={value || null} onValueChange={(v) => onChange((v as string) ?? '')}>
      <SelectTrigger aria-label={label} className="font-mono">
        <SelectValue placeholder="Backend" />
      </SelectTrigger>
      <SelectContent>
        {backends.map((b) => (
          <SelectItem key={b.name} value={b.name} className="font-mono">
            {b.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** New route (no `route`), or an edit of one. Saving writes desired state; the gateway changes on apply. */
function RouteEditor({ route, backends, onClose }: { route?: LiveRoute; backends: Backend[]; onClose: (saved: boolean) => void }) {
  const [draft, setDraft] = useState(() => toDraft(route))
  const [etag, setEtag] = useState(route?.etag ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [stale, setStale] = useState<LiveRoute | null>(null)
  const [preview, setPreview] = useState<string | null>(null)
  // Only a backend the gateway can reach can be a target.
  const reachable = backends.filter((b) => b.endpoint)
  const set = (patch: Partial<Draft>) => {
    setDraft((d) => ({ ...d, ...patch }))
    setPreview(null)
  }
  const path = route ? `/routes/${encodeURIComponent(route.name)}` : '/routes'
  const send = (dryRun: boolean) =>
    api<LiveRoute & { yaml: string }>(path + (dryRun ? '?dryRun=true' : ''), {
      method: route ? 'PUT' : 'POST',
      body: JSON.stringify(fromDraft(draft)),
      headers: route ? { 'If-Match': etag } : {},
    })
  const fail = (e: unknown) => {
    if (e instanceof ApiError && e.status === 409 && e.current) setStale(e.current as LiveRoute)
    else setError(e instanceof ApiError && e.status === 409 ? `A route named ${draft.name.trim()} already exists.` : e instanceof Error ? e.message : String(e))
  }
  const run = async (dryRun: boolean) => {
    setBusy(true)
    setError(null)
    try {
      const out = await send(dryRun)
      if (dryRun) setPreview(out.yaml)
      else {
        toast.add({ title: route ? 'Route saved' : 'Route created', description: `${draft.name.trim()} is pending until you apply it.`, type: 'success' })
        onClose(true)
      }
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }
  const row = 'grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-2'

  return (
    <Dialog open onOpenChange={(o) => !o && onClose(false)}>
      <DialogContent className="flex max-w-4xl flex-col">
        <form
          className="flex min-h-0 flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void run(false)
          }}
        >
          <DialogHeader>
            <DialogTitle>{route ? `Edit route ${route.name}` : 'New route'}</DialogTitle>
            <DialogDescription>
              Conditions on the left, backends on the right, fallback as an ordered list. Saving changes the desired routing; the gateway runs it once you apply.
            </DialogDescription>
          </DialogHeader>
          <div className="grid max-h-[30rem] min-h-0 grid-cols-1 gap-6 overflow-y-auto md:grid-cols-[1fr_1.3fr]">
            <div className="flex flex-col gap-3">
              <Field>
                <FieldLabel>Name</FieldLabel>
                <Input value={draft.name} onChange={(e) => set({ name: e.target.value })} disabled={!!route} className="font-mono" autoComplete="off" spellCheck={false} />
              </Field>
              <Field>
                <FieldLabel>Models</FieldLabel>
                <Input value={draft.models} onChange={(e) => set({ models: e.target.value })} placeholder="gpt-5-mini, gpt-5.5" className="font-mono" autoComplete="off" spellCheck={false} />
                <FieldDescription>Exact model names, or one pattern: summarize-* by prefix, * for any model.</FieldDescription>
              </Field>
              <div className="flex flex-col gap-2">
                <h3 className="text-sm font-semibold">And every header</h3>
                {draft.headers.map((h, i) => (
                  <div key={i} className={row}>
                    <Input aria-label={`Header ${i + 1}`} value={h.name} placeholder="x-stargate-team" className="font-mono" onChange={(e) => set({ headers: draft.headers.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)) })} />
                    <Input aria-label={`Header ${i + 1} value`} value={h.value} placeholder="research" className="font-mono" onChange={(e) => set({ headers: draft.headers.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)) })} />
                    <Button type="button" variant="ghost" size="icon-xs" aria-label={`Remove header ${i + 1}`} onClick={() => set({ headers: draft.headers.filter((_, j) => j !== i) })}>
                      <X />
                    </Button>
                  </div>
                ))}
                <Button type="button" variant="ghost" size="xs" className="self-start" onClick={() => set({ headers: [...draft.headers, { name: '', value: '' }] })}>
                  <Plus /> Add condition
                </Button>
                <p className="text-xs text-muted-foreground">
                  Exact matches. The key check adds <span className="font-mono">x-stargate-team</span> and <span className="font-mono">x-stargate-project</span> from the caller’s key.
                </p>
              </div>
            </div>
            <div className="flex flex-col gap-4">
              <div className="flex flex-col gap-2">
                <h3 className="text-sm font-semibold">Send to</h3>
                {draft.targets.map((t, i) => (
                  <div key={i} className={cn(row, draft.targets.length > 1 && 'grid-cols-[minmax(0,1fr)_minmax(0,1fr)_4.5rem_auto]')}>
                    <BackendSelect label={`Target ${i + 1} backend`} value={t.backend} backends={reachable} onChange={(v) => set({ targets: draft.targets.map((x, j) => (j === i ? { ...x, backend: v } : x)) })} />
                    <Input aria-label={`Target ${i + 1} model`} value={t.model} placeholder="requested model" className="font-mono" onChange={(e) => set({ targets: draft.targets.map((x, j) => (j === i ? { ...x, model: e.target.value } : x)) })} />
                    {draft.targets.length > 1 && (
                      <Input aria-label={`Target ${i + 1} weight`} type="number" min={1} value={t.weight} className="num text-right font-mono" onChange={(e) => set({ targets: draft.targets.map((x, j) => (j === i ? { ...x, weight: e.target.value } : x)) })} />
                    )}
                    <Button type="button" variant="ghost" size="icon-xs" aria-label={`Remove target ${i + 1}`} disabled={draft.targets.length === 1} onClick={() => set({ targets: draft.targets.filter((_, j) => j !== i) })}>
                      <X />
                    </Button>
                  </div>
                ))}
                <Button type="button" variant="ghost" size="xs" className="self-start" onClick={() => set({ targets: [...draft.targets, { backend: '', model: '', weight: '1' }] })}>
                  <Plus /> Add target
                </Button>
                <p className="text-xs text-muted-foreground">Leave the model empty to pass the requested one through. With several targets, weights split the traffic.</p>
              </div>
              <div className="flex flex-col gap-2">
                <h3 className="text-sm font-semibold">If unavailable, fall back to</h3>
                {draft.fallback.map((f, i) => {
                  const move = (d: -1 | 1) => {
                    const next = [...draft.fallback]
                    ;[next[i], next[i + d]] = [next[i + d], next[i]]
                    set({ fallback: next })
                  }
                  return (
                    <div key={i} className="grid grid-cols-[1.25rem_minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-2">
                      <span className="num font-mono text-xs text-muted-foreground">{i + 1}</span>
                      <BackendSelect label={`Fallback ${i + 1} backend`} value={f.backend} backends={reachable} onChange={(v) => set({ fallback: draft.fallback.map((x, j) => (j === i ? { ...x, backend: v } : x)) })} />
                      <Input aria-label={`Fallback ${i + 1} model`} value={f.model} placeholder="requested model" className="font-mono" onChange={(e) => set({ fallback: draft.fallback.map((x, j) => (j === i ? { ...x, model: e.target.value } : x)) })} />
                      <span className="flex">
                        <Button type="button" variant="ghost" size="icon-xs" aria-label={`Move fallback ${i + 1} up`} disabled={i === 0} onClick={() => move(-1)}>
                          <ArrowUp />
                        </Button>
                        <Button type="button" variant="ghost" size="icon-xs" aria-label={`Move fallback ${i + 1} down`} disabled={i === draft.fallback.length - 1} onClick={() => move(1)}>
                          <ArrowDown />
                        </Button>
                        <Button type="button" variant="ghost" size="icon-xs" aria-label={`Remove fallback ${i + 1}`} onClick={() => set({ fallback: draft.fallback.filter((_, j) => j !== i) })}>
                          <X />
                        </Button>
                      </span>
                    </div>
                  )
                })}
                <Button type="button" variant="ghost" size="xs" className="self-start" onClick={() => set({ fallback: [...draft.fallback, { backend: '', model: '' }] })}>
                  <Plus /> Add fallback
                </Button>
              </div>
            </div>
          </div>
          {preview && (
            <CodeBlock code={preview} className="w-full">
              <CodeBlockHeader>AIGatewayRoute rule</CodeBlockHeader>
              <CodeBlockBody maxLines={12} />
            </CodeBlock>
          )}
          {stale && (
            <Alert variant="destructive">
              <AlertTitle>This route changed since you opened it</AlertTitle>
              <AlertDescription className="flex flex-col items-start gap-2">
                Saving would overwrite that change. Load the current version, then make your edit again.
                <Button
                  type="button"
                  variant="outline"
                  size="xs"
                  onClick={() => {
                    setDraft(toDraft(stale))
                    setEtag(stale.etag)
                    setStale(null)
                    setPreview(null)
                  }}
                >
                  Load the current version
                </Button>
              </AlertDescription>
            </Alert>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertTitle>Not saved</AlertTitle>
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter className="sm:justify-between">
            <Button type="button" variant="ghost" disabled={busy} onClick={() => void run(true)}>
              <FileCode /> Preview YAML
            </Button>
            <div className="flex gap-2">
              <Button variant="outline" type="button" onClick={() => onClose(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={busy} loadingText="Saving…" disabled={!!stale}>
                {route ? 'Save' : 'Create route'}
              </Button>
            </div>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function DeleteRouteDialog({ route, onClose }: { route: LiveRoute; onClose: (deleted: boolean) => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const remove = async () => {
    setBusy(true)
    setError(null)
    try {
      await api(`/routes/${encodeURIComponent(route.name)}`, { method: 'DELETE', headers: { 'If-Match': route.etag } })
      toast.add({ title: 'Route deleted', description: `${route.name} is pending removal until you apply.`, type: 'success' })
      onClose(true)
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? 'Someone else changed this route since the page loaded. Close and try again on the current version.' : e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose(false)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete route {route.name}?</DialogTitle>
          <DialogDescription>
            {route.sync === 'synced' ? 'The gateway keeps routing by it until the next apply. ' : ''}Requests for {route.match.models.join(', ')} then go to whichever route matches next, or are refused if
            none does.
          </DialogDescription>
        </DialogHeader>
        {error && (
          <Alert variant="destructive">
            <AlertTitle>Not deleted</AlertTitle>
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <Button variant="outline" type="button" onClick={() => onClose(false)}>
            Keep route
          </Button>
          <Button variant="destructive" onClick={remove} loading={busy} loadingText="Deleting…">
            Delete route
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---- Backends -----------------------------------------------------------------

function BackendsTable({ items, onOpen }: { items: Backend[]; onOpen: (name: string) => void }) {
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
              <th className="px-3 py-2 font-medium">Endpoint</th>
              <th className="px-3 py-2 font-medium">Sync</th>
              <th className="px-3 py-2 font-medium">Health</th>
              <th className="px-3 py-2 text-right font-medium">p50</th>
              <th className="py-2 pr-6 pl-3 text-right font-medium">Errors</th>
            </tr>
          </thead>
          <tbody>
            {items.map((b) => (
              <tr key={b.name} onClick={() => onOpen(b.name)} className="cursor-pointer border-b border-border hover:bg-muted/50">
                <td className="py-2.5 pr-3 pl-6">
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
                <td className="max-w-[16rem] truncate px-3 py-2.5 font-mono text-xs" title={endpointText(b)}>
                  {endpointText(b)}
                </td>
                <td className="px-3 py-2.5">
                  <SyncStateIndicator state={b.sync} />
                </td>
                <td className="px-3 py-2.5">
                  {b.health === 'healthy' ? <span className="text-xs text-muted-foreground">Healthy</span> : <StateChip tone={healthTone[b.health]}>{healthLabel[b.health]}</StateChip>}
                </td>
                <td className="px-3 py-2.5 text-right">{b.p50 ? <Duration ms={b.p50} /> : <span className="font-mono text-muted-foreground">—</span>}</td>
                <td className={cn('num py-2.5 pr-6 pl-3 text-right font-mono', b.errorRate > 2 && 'text-v-blocked-fg')}>{b.errorRate.toFixed(1)}%</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="px-6 py-3 text-xs text-muted-foreground">
        Backends are the control plane’s desired state: open one to edit it, replace its key or delete it. Synced means the gateway runs the backend as it is here. Health comes from the last 15 minutes of
        receipts: idle with no requests, down when every request failed, degraded past 5% errors. p50 and errors cover the last hour.
      </p>
    </Section>
  )
}

function BackendDetail({ b, onChanged }: { b: Backend; onChanged: (deleted: boolean) => void }) {
  const [yaml, setYaml] = useState(false)
  const [dialog, setDialog] = useState<'edit' | 'key' | 'delete' | null>(null)
  const [testing, setTesting] = useState(false)
  const e = b.endpoint
  const test = async () => {
    setTesting(true)
    try {
      await api(`/backends/${encodeURIComponent(b.name)}/test`, { method: 'POST' })
    } catch (err) {
      toast.add({ title: 'Not tested', description: err instanceof Error ? err.message : String(err), type: 'error' })
    } finally {
      setTesting(false)
      onChanged(false)
    }
  }
  return (
    <>
      <DrawerHeader className="flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <SyncStateIndicator state={b.sync} />
          {b.captureContent && <CaptureMarker />}
        </div>
        <DrawerTitle className="font-mono">{b.name}</DrawerTitle>
        <DrawerDescription>
          {b.provider} · {b.region} · serves {b.models.join(', ')}
        </DrawerDescription>
      </DrawerHeader>
      <DrawerBody className="gap-4">
        <dl className="grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1.5 text-sm">
          {e ? (
            <>
              <dt className="text-muted-foreground">Base URL</dt>
              <dd className="font-mono text-xs break-all">{e.baseUrl}</dd>
              <dt className="text-muted-foreground">Endpoint</dt>
              <dd className="font-mono text-xs break-all">
                {e.host}:{e.port}
                {e.tls && <span className="ml-2 font-sans text-muted-foreground">TLS</span>}
              </dd>
              <dt className="text-muted-foreground">API</dt>
              <dd className="font-mono text-xs break-all">
                {e.schema} {e.prefix}
              </dd>
              <dt className="text-muted-foreground">Provider key</dt>
              <dd className="text-xs">
                <KeyText b={b} />
                {e.apiKeyEnv && b.key && <span className="ml-1 font-mono text-muted-foreground">(${e.apiKeyEnv})</span>}
              </dd>
              <dt className="text-muted-foreground">Last test</dt>
              <dd className="text-xs">
                <LastTest t={b.lastTest} />
              </dd>
              <dd className="col-span-2 text-xs text-muted-foreground">{'${VAR:-default}'} values come from the gateway’s environment when it loads its config.</dd>
            </>
          ) : (
            <dd className="col-span-2 text-sm text-muted-foreground">No endpoint: the control plane doesn’t know where this backend is, so the gateway can’t reach it and no route can send to it.</dd>
          )}
          <dt className="text-muted-foreground">Health</dt>
          <dd>
            {healthLabel[b.health]}{' '}
            <span className="text-xs text-muted-foreground">{b.health === 'idle' ? '(No requests in the last 15 minutes)' : '(from the last 15 minutes of receipts)'}</span>
          </dd>
          <dt className="text-muted-foreground">Requests</dt>
          <dd className="num font-mono text-xs">{b.requests1h ?? 0} in the last hour</dd>
          <dt className="text-muted-foreground">p50</dt>
          <dd>{b.p50 ? <Duration ms={b.p50} /> : '—'}</dd>
          <dt className="text-muted-foreground">Errors</dt>
          <dd className="num font-mono text-xs">{b.errorRate.toFixed(1)}%</dd>
        </dl>
      </DrawerBody>
      <DrawerFooter className="flex-wrap">
        <Button variant="outline" onClick={() => setDialog('edit')}>
          <Pencil /> Edit provider
        </Button>
        {e && (
          <>
            <Button variant="outline" onClick={() => setDialog('key')}>
              <KeyRound /> Replace key
            </Button>
            <Button variant="outline" onClick={test} loading={testing} loadingText="Testing…">
              Test connection
            </Button>
          </>
        )}
        {b.yaml && (
          <Button variant="outline" onClick={() => setYaml(true)}>
            <FileCode /> View generated YAML
          </Button>
        )}
        <Button variant="ghost" onClick={() => setDialog('delete')}>
          <Trash2 /> Delete provider
        </Button>
      </DrawerFooter>
      {dialog === 'edit' && (
        <ProviderDialog
          backend={b}
          onClose={(saved) => {
            setDialog(null)
            if (saved) onChanged(false)
          }}
        />
      )}
      {dialog === 'key' && (
        <ReplaceKeyDialog
          backend={b}
          onClose={(replaced) => {
            setDialog(null)
            if (replaced) onChanged(false)
          }}
        />
      )}
      {dialog === 'delete' && (
        <DeleteBackendDialog
          backend={b}
          onClose={(deleted) => {
            setDialog(null)
            if (deleted) onChanged(true)
          }}
        />
      )}
      {yaml && b.yaml && (
        <YamlDialog title={b.name} yaml={b.yaml} description="The gateway resources this backend compiles to: its Backend and AIServiceBackend, and a provider key’s policy and Secret." onClose={() => setYaml(false)} />
      )}
    </>
  )
}

// ---- Fallback -----------------------------------------------------------------

function FallbackView({ routes }: { routes: LiveRoute[] }) {
  return (
    <>
      <Section title="Fallback chains" description="The gateway retries a 5xx on the same backends until passive health checks eject them, then moves to the next in order.">
        <ul className="flex flex-col divide-y divide-border rounded-md border border-border">
          {routes.map((r) => (
            <li key={r.name} className="flex flex-wrap items-center gap-2 px-3 py-2 text-sm">
              <span className="w-40 font-mono font-medium">{r.name}</span>
              {[...r.targets, ...r.fallback].map((t, i, arr) => (
                <span key={t.backend} className="inline-flex items-center gap-2">
                  <span className={cn('rounded-sm border px-1.5 font-mono text-xs leading-5', i < r.targets.length ? 'border-border-strong bg-card' : 'border-border bg-muted')}>{t.backend}</span>
                  {i < arr.length - 1 && <ArrowRight className="size-3.5 text-muted-foreground" aria-hidden="true" />}
                </span>
              ))}
              {r.fallback.length === 0 && <span className="text-xs text-muted-foreground">no fallback</span>}
            </li>
          ))}
        </ul>
      </Section>
      <Section title="Fallbacks in traffic" description="Receipts record each fallback; there’s no failover log apart from them.">
        <Link to="/traffic?reason=fallback" className="text-sm underline-offset-4 hover:underline">
          Requests that fell back →
        </Link>
      </Section>
    </>
  )
}
