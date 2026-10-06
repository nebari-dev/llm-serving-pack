import { Copy, TriangleAlert } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { DiffView } from '@/components/gw/diff-view'
import { Duration } from '@/components/gw/numbers'
import { PageHeader } from '@/components/gw/page'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { toast } from '@/components/ui/toast'
import { type ApiKey, api, ApiError, type Backend, backends as seedBackends, createKey, type LiveRoute, type Receipt, type RoutingChange, type RoutingPlan, type Session, session as seedSession, teams } from '@/data/catalog'
import { cn } from '@/lib/utils'
import { useReceipts, useStreamSampling } from '@/state/app-state'
import { useLive } from '@/state/live'
import { copy, FirstRequest, type Lang, readLang, Step, type StepInfo, StepMap } from './onboarding'
import { NEW_PROJECT, ProjectFields, useProjectChoice } from './project-dialogs'
import { ProviderForm } from './providers-live'

// §7.5.1 Onboarding against the real control plane. Pick a backend the
// control plane has, or connect a new provider (its key tested once, then
// sealed; one that fails isn't saved), route its models to it and apply
// (every pending change, listed first); the key is a real key; the first
// request is a real receipt for that key, from the caller's own app or the
// test request sent here.

type GatewayTest = { status: number; sessionId: string; reply?: string; error?: string; ms: number }

const LANG_KEY = 'gw:onboarding-lang'

function healthLine(b: Backend) {
  const n = b.requests1h ?? 0
  const served = `${n} ${n === 1 ? 'request' : 'requests'} in the last hour`
  switch (b.health) {
    case 'idle':
      return 'Idle: no requests in the last 15 minutes'
    case 'down':
      return `Down: every request in the last 15 minutes failed · ${served}`
    case 'degraded':
      return `Degraded: ${b.errorRate}% errors · ${served}`
    default:
      return `Healthy · ${served}`
  }
}

function snippets(gatewayUrl: string, model: string): Record<Lang, { diff: string; code: string; file: string }> {
  const body = `'{"model": "${model}", "messages": [{"role": "user", "content": "ping"}]}'`
  return {
    python: {
      file: 'app.py',
      diff: `
 from openai import OpenAI

 client = OpenAI(
-    base_url="https://api.openai.com/v1",
+    base_url="${gatewayUrl}",
-    api_key=os.environ["OPENAI_API_KEY"],
+    api_key=os.environ["NEBARI_GATEWAY_KEY"],
 )`,
      code: `from openai import OpenAI\n\nclient = OpenAI(\n    base_url="${gatewayUrl}",\n    api_key=os.environ["NEBARI_GATEWAY_KEY"],\n)`,
    },
    typescript: {
      file: 'client.ts',
      diff: `
 import OpenAI from 'openai'

 const client = new OpenAI({
-  baseURL: 'https://api.openai.com/v1',
+  baseURL: '${gatewayUrl}',
-  apiKey: process.env.OPENAI_API_KEY,
+  apiKey: process.env.NEBARI_GATEWAY_KEY,
 })`,
      code: `import OpenAI from 'openai'\n\nconst client = new OpenAI({\n  baseURL: '${gatewayUrl}',\n  apiKey: process.env.NEBARI_GATEWAY_KEY,\n})`,
    },
    curl: {
      file: 'shell',
      diff: `
-curl https://api.openai.com/v1/chat/completions \\
+curl ${gatewayUrl}/chat/completions \\
-  -H "Authorization: Bearer $OPENAI_API_KEY" \\
+  -H "Authorization: Bearer $NEBARI_GATEWAY_KEY" \\
   -H "Content-Type: application/json" \\
   -d ${body}`,
      code: `curl ${gatewayUrl}/chat/completions \\\n  -H "Authorization: Bearer $NEBARI_GATEWAY_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d ${body}`,
    },
  }
}

export function LiveOnboardingPage() {
  const live = useLive<Backend[]>('/backends', seedBackends, 15_000)
  const sess = useLive<Session>('/session', seedSession, 60_000).data
  const [name, setName] = useState<string | null>(null)
  const [team, setTeam] = useState(teams[0]?.id ?? '')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [created, setCreated] = useState<{ key: ApiKey; secret: string; backend: Backend } | null>(null)
  const [firstId, setFirstId] = useState<string | null>(null)
  // Connecting a new provider: the form, then the provider it saved until it's routed and applied.
  const [adding, setAdding] = useState(false)
  const [fresh, setFresh] = useState<Backend | null>(null)

  const backend = live.data.find((b) => b.name === name) ?? null
  const gatewayUrl = sess.gatewayUrl ?? ''

  // The key goes in one of the team's projects. Until someone picks, it's the
  // team's "onboarding" project, or a new one by that name, made with the key.
  const project = useProjectChoice(team, true)
  const [tried, setTried] = useState(false)
  const { choice, setChoice, setNewName, teamProjects } = project
  const loaded = project.live.loaded
  useEffect(() => {
    if (!loaded || choice) return
    const existing = teamProjects.find((p) => p.name === 'onboarding')
    if (existing) setChoice(existing.id)
    else {
      setChoice(NEW_PROJECT)
      setNewName('onboarding')
    }
  }, [loaded, choice, teamProjects, setChoice, setNewName])

  const create = async () => {
    setTried(true)
    if (!backend || project.error) return
    setCreating(true)
    setError(null)
    try {
      const expires = new Date(Date.now() + 90 * 86_400_000).toISOString().slice(0, 10)
      const { key, secret } = await createKey({
        name: `onboarding-${backend.name}-${Math.random().toString(36).slice(2, 6)}`,
        team,
        projectId: await project.resolve(),
        allowedModels: backend.models,
        allowedRegions: [backend.region],
        expiresAt: expires,
      })
      setCreated({ key, secret, backend })
      setFirstId(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setCreating(false)
    }
  }

  const steps: StepInfo[] = [
    { n: 1, title: 'Pick a backend', hint: created ? created.backend.name : 'One the gateway serves', status: created ? 'done' : 'current' },
    { n: 2, title: 'Swap two lines', hint: 'Base URL and key', status: !created ? 'upcoming' : firstId ? 'done' : 'current' },
    { n: 3, title: 'First request', hint: firstId ? 'Receipt ready' : created ? 'Waiting…' : 'Lands live here', status: !created ? 'upcoming' : firstId ? 'done' : 'waiting' },
  ]

  return (
    <div data-density="comfortable" className="flex flex-col">
      <PageHeader title="Connect a provider" description="Point one app at the gateway. It's a base URL and a key swap — about a minute." />
      <div className="mx-auto grid w-full max-w-4xl gap-8 px-6 py-8 md:grid-cols-[12rem_minmax(0,1fr)] md:gap-10">
        <StepMap steps={steps} />
        <div className="flex min-w-0 flex-col gap-6">
          <Step n={1} title="Pick a backend the gateway serves, or connect a provider" done={!!created}>
            {live.loaded && live.data.length === 0 && <p className="text-sm text-muted-foreground">The control plane has no backends.</p>}
            <RadioGroup
              aria-label="Backend"
              value={name}
              onValueChange={(v) => {
                setName(v as string)
                setCreated(null)
                setFirstId(null)
              }}
              className="grid grid-cols-2 gap-2 sm:grid-cols-3"
              orientation="vertical"
            >
              {live.data.map((b) => (
                <RadioGroupItem
                  key={b.name}
                  value={b.name}
                  variant="box"
                  description={`${b.provider} · ${b.health === 'idle' ? 'idle' : b.health}`}
                  className={(s) => cn(s.checked && 'border-primary bg-card')}
                >
                  <span className="font-mono">{b.name}</span>
                </RadioGroupItem>
              ))}
            </RadioGroup>
            {!adding && !fresh && (
              <Button
                variant="outline"
                size="sm"
                className="mt-3"
                onClick={() => {
                  setAdding(true)
                  setName(null)
                  setCreated(null)
                }}
              >
                Connect a new provider
              </Button>
            )}
            {adding && (
              <div className="mt-4 rounded-md border border-border p-4">
                <h3 className="mb-3 text-sm font-semibold">Connect your first provider</h3>
                <ProviderForm
                  label="New provider"
                  onCancel={() => setAdding(false)}
                  onSaved={({ backend: b }) => {
                    setAdding(false)
                    setFresh(b)
                    live.reload()
                  }}
                />
              </div>
            )}
            {fresh && (
              <ConnectFresh
                b={fresh}
                onDone={() => {
                  setName(fresh.name)
                  setFresh(null)
                  live.reload()
                }}
              />
            )}

            {backend && (
              <div className="mt-5 flex flex-col gap-4">
                <dl className="grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1.5 text-sm">
                  <dt className="text-muted-foreground">Serves</dt>
                  <dd className="font-mono">{backend.models.join(', ')}</dd>
                  <dt className="text-muted-foreground">Region</dt>
                  <dd className="font-mono">{backend.region}</dd>
                  <dt className="text-muted-foreground">Observed</dt>
                  <dd>
                    {healthLine(backend)}
                    {backend.p50 > 0 && (
                      <>
                        {' · p50 '}
                        <Duration ms={backend.p50} />
                      </>
                    )}
                  </dd>
                </dl>
                <Field>
                  <FieldLabel>Team</FieldLabel>
                  <Select
                    items={teams.map((t) => ({ value: t.id, label: t.name }))}
                    value={team}
                    onValueChange={(v) => {
                      setTeam((v as string) ?? team)
                      project.reset()
                    }}
                  >
                    <SelectTrigger aria-label="Team" className="w-60">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {teams.map((t) => (
                        <SelectItem key={t.id} value={t.id}>
                          {t.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <FieldDescription>The key’s spend and receipts count against this team. It may call only {backend.name}’s models.</FieldDescription>
                </Field>
                <div className="flex w-60 flex-col gap-4">
                  <ProjectFields c={project} team={team} tried={tried} when="when you create the key" />
                </div>
                <div className="flex flex-wrap items-center gap-3">
                  <Button onClick={create} loading={creating} loadingText="Creating key…" variant={created ? 'outline' : 'default'} disabled={!team || !loaded}>
                    {created ? 'Create another key' : `Create a key for ${backend.name}`}
                  </Button>
                </div>
                {error && (
                  <p role="alert" className="flex items-start gap-2 text-sm text-destructive-foreground">
                    <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
                    {error}
                  </p>
                )}
              </div>
            )}
          </Step>

          {created && <KeyStep gatewayUrl={gatewayUrl} secret={created.secret} keyName={created.key.name} team={created.key.team} model={created.backend.models[0]} anthropic={created.backend.provider === 'Anthropic'} />}
          {created && (
            <FirstRequestLive gatewayUrl={gatewayUrl} keyId={created.key.id} secret={created.secret} model={created.backend.models[0]} landedId={firstId} onLand={setFirstId} />
          )}
        </div>
      </div>
    </div>
  )
}

/**
 * A provider saved here isn't in the gateway yet: it needs a route for its
 * models (saved like any route), then an apply. The applier applies the whole
 * desired routing, one config and one restart, so applying here applies every
 * pending change, not only this provider's: the button says how many, and
 * they're listed above it. Applying sends the listed plan's etag, so a change
 * made meanwhile is refused rather than applied unseen.
 */
function ConnectFresh({ b, onDone }: { b: Backend; onDone: () => void }) {
  const plan = useLive<RoutingPlan | null>('/routing', null, 5_000)
  const routes = useLive<LiveRoute[] | null>('/routes', null, 10_000)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<{ title: string; text: string } | null>(null)
  const routed = routes.data?.some((r) => r.targets.some((t) => t.backend === b.name)) ?? false
  const changes = plan.data?.changes ?? []
  const n = changes.length
  const ours = (c: RoutingChange) => c.name === b.name || c.name === `${b.name}-key` || c.kind === 'AIGatewayRoute'

  const route = async () => {
    setBusy(true)
    setError(null)
    try {
      await api('/routes', { method: 'POST', body: JSON.stringify({ name: b.name, match: { models: b.models, headers: [] }, targets: [{ backend: b.name }], fallback: [] }) })
      routes.reload()
      plan.reload()
    } catch (e) {
      setError({ title: 'Not routed', text: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }
  const apply = async () => {
    if (!plan.data) return
    setBusy(true)
    setError(null)
    try {
      await api('/routing/apply', { method: 'POST', body: '{}', headers: { 'If-Match': plan.data.etag } })
      toast.add({ title: 'Changes applied', description: `The gateway sends ${b.models.join(', ')} to ${b.name}.`, type: 'success' })
      onDone()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) setError({ title: 'Routing changed since this list', text: 'Review the pending changes again, then apply.' })
      else if (e instanceof ApiError && e.status === 502) setError({ title: 'The gateway didn’t take the new config', text: e.message })
      else setError({ title: 'Not applied', text: e instanceof Error ? e.message : String(e) })
      plan.reload()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mt-4 flex flex-col items-start gap-3 rounded-md border border-border p-4">
      <p className="text-sm">
        <span className="font-mono">{b.name}</span> is saved{b.key ? <> with key <span className="font-mono">{b.key.prefix}…</span>, which passed its test</> : null}. The gateway
        sends to it once a route matches its models and routing is applied.
      </p>
      {b.lastTest && !b.lastTest.ok && <p className="text-sm text-destructive-foreground">Its connection test failed: {b.lastTest.message}</p>}
      {routes.loaded && !routed && (
        <Button onClick={route} loading={busy} loadingText="Saving the route…">
          Route {b.models.join(', ')} to {b.name}
        </Button>
      )}
      {routed && plan.data && n === 0 && (
        <>
          <p className="text-sm text-muted-foreground">Nothing is pending: the gateway already sends to {b.name}.</p>
          <Button onClick={onDone}>Continue</Button>
        </>
      )}
      {routed && plan.data && n > 0 && (
        <>
          <p className="text-sm">
            Applying restarts the gateway with every pending routing change, not only this provider’s: the gateway runs one config, so it can’t take part of one.
            {changes.some((c) => !ours(c)) ? ' Some of these aren’t about this provider.' : ''} Review the diffs on{' '}
            <Link to="/routing" className="underline underline-offset-4">
              Routing
            </Link>{' '}
            if you’re unsure.
          </p>
          <ul aria-label="Pending routing changes" className="flex w-full flex-col gap-0.5 text-sm">
            {changes.map((c) => (
              <li key={`${c.kind}/${c.name}`} className="flex gap-2">
                <span className="w-28 shrink-0 text-muted-foreground">{c.change === 'key replaced' ? 'Key replaced' : c.change[0].toUpperCase() + c.change.slice(1)}</span>
                <span className="font-mono">
                  {c.kind}/{c.name}
                </span>
                {!ours(c) && <span className="text-muted-foreground">not this provider</span>}
              </li>
            ))}
          </ul>
          <Button onClick={apply} loading={busy} loadingText="Applying… the gateway is restarting" disabled={!plan.data.canApply} title={plan.data.canApply ? undefined : plan.data.reason}>
            {n === 1 ? 'Apply 1 change' : `Apply all ${n} changes`}
          </Button>
        </>
      )}
      {error && (
        <Alert variant="destructive">
          <AlertTitle>{error.title}</AlertTitle>
          <AlertDescription>
            <pre className="max-h-48 overflow-auto font-mono text-xs whitespace-pre-wrap">{error.text}</pre>
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}

function KeyStep({ gatewayUrl, secret, keyName, team, model, anthropic }: { gatewayUrl: string; secret: string; keyName: string; team: string; model: string; anthropic?: boolean }) {
  const [lang, setLang] = useState<Lang>(readLang)
  const all = useMemo(() => snippets(gatewayUrl || '<gateway URL>', model), [gatewayUrl, model])
  // Anthropic's SDK posts to {base}/v1/messages; the gateway serves that under /anthropic.
  const anthropicUrl = gatewayUrl ? gatewayUrl.replace(/\/v1\/?$/, '') + '/anthropic' : ''
  const changeLang = (v: Lang) => {
    setLang(v)
    try {
      localStorage.setItem(LANG_KEY, v)
    } catch {
      /* storage unavailable */
    }
  }
  return (
    <Step n={2} title="Swap two lines in your app" done>
      <dl className="grid grid-cols-[7rem_1fr] items-center gap-x-3 gap-y-2 text-sm">
        <dt className="text-muted-foreground">Base URL</dt>
        <dd className="flex items-center gap-2">
          <span className="font-mono">{gatewayUrl || 'not reported by the control plane'}</span>
          {gatewayUrl && (
            <Button size="xs" variant="ghost" onClick={() => copy(gatewayUrl, 'Base URL')}>
              <Copy /> Copy
            </Button>
          )}
        </dd>
        {anthropic && anthropicUrl && (
          <>
            <dt className="text-muted-foreground">Anthropic SDK</dt>
            <dd className="flex flex-wrap items-center gap-x-2">
              <span className="font-mono">{anthropicUrl}</span>
              <Button size="xs" variant="ghost" onClick={() => copy(anthropicUrl, 'Anthropic base URL')}>
                <Copy /> Copy
              </Button>
              <span className="w-full text-xs text-muted-foreground">Its base_url, with the same key as its api_key: requests go to Anthropic’s own Messages API.</span>
            </dd>
          </>
        )}
      </dl>
      <div className="mt-4 flex flex-col gap-1.5">
        <div className="text-sm font-medium">Your gateway key</div>
        <div className="flex items-center gap-2 rounded-md border border-border bg-muted px-3 py-2">
          <span className="min-w-0 flex-1 truncate font-mono text-sm" title="Shown in full once. Store it as NEBARI_GATEWAY_KEY.">
            {secret.slice(0, 13)}
            <span className="text-muted-foreground-strong">{secret.slice(13)}</span>
          </span>
          <Button size="sm" variant="outline" onClick={() => copy(secret, 'Gateway key')}>
            <Copy /> Copy key
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          Shown in full once: the control plane keeps only its hash. Key <span className="font-mono">{keyName}</span>, team{' '}
          <span className="font-mono">{team}</span>, expires in 90 days. Revoke or rotate it under{' '}
          <Link to="/keys" className="underline underline-offset-4">
            Keys
          </Link>
          .
        </p>
      </div>
      <Tabs value={lang} onValueChange={(v) => changeLang(v as Lang)} className="mt-5 gap-3">
        <div className="flex items-end justify-between gap-2">
          <TabsList variant="underline" aria-label="Language">
            <TabsTab value="python">Python</TabsTab>
            <TabsTab value="typescript">TypeScript</TabsTab>
            <TabsTab value="curl">curl</TabsTab>
            <TabsIndicator />
          </TabsList>
          <Button size="sm" variant="ghost" onClick={() => copy(all[lang].code, 'Snippet')}>
            <Copy /> Copy snippet
          </Button>
        </div>
        {(Object.keys(all) as Lang[]).map((l) => (
          <TabsPanel key={l} value={l}>
            <DiffView diff={all[l].diff} title={all[l].file} />
          </TabsPanel>
        ))}
      </Tabs>
    </Step>
  )
}

/**
 * Waits for the key's first receipt: from the live stream (the caller's own
 * app), or the one the test request will produce, fetched until it lands.
 */
function FirstRequestLive({
  gatewayUrl,
  keyId,
  secret,
  model,
  landedId,
  onLand,
}: {
  gatewayUrl: string
  keyId: string
  secret: string
  model: string
  landedId: string | null
  onLand: (id: string) => void
}) {
  const rows = useReceipts()
  const sampling = useStreamSampling()
  const [sending, setSending] = useState(false)
  const [test, setTest] = useState<GatewayTest | null>(null)
  const [sendError, setSendError] = useState<string | null>(null)
  const [landed, setLanded] = useState<Receipt | null>(null)

  // The caller's own traffic, from the tab's receipt stream.
  useEffect(() => {
    if (landed) return
    const r = rows.find((x) => x.keyId === keyId && !x.inFlight)
    if (r) {
      setLanded(r)
      onLand(r.id)
    }
  }, [rows, keyId, landed])

  // A sampled stream (§7.5.3) may skip this key's first request, so ask for it too.
  useEffect(() => {
    if (!sampling || landed) return
    let stop = false
    const timer = window.setInterval(() => {
      api<Receipt[]>(`/receipts?key=${encodeURIComponent(keyId)}&limit=1`)
        .then(([r]) => {
          if (!stop && r && !r.inFlight) {
            setLanded(r)
            onLand(r.id)
          }
        })
        .catch(() => {})
    }, 3000)
    return () => {
      stop = true
      window.clearInterval(timer)
    }
  }, [sampling, landed, keyId])

  // The test request's receipt, by the session the control plane tagged it with.
  useEffect(() => {
    if (!test || landed) return
    let stop = false
    const poll = async (tries: number) => {
      try {
        const [r] = await api<Receipt[]>(`/receipts?session=${encodeURIComponent(test.sessionId)}&limit=1`)
        if (!stop && r && !r.inFlight) {
          setLanded(r)
          onLand(r.id)
          return
        }
      } catch {
        /* not ingested yet */
      }
      if (!stop && tries > 0) window.setTimeout(() => poll(tries - 1), 1000)
    }
    void poll(30)
    return () => {
      stop = true
    }
  }, [test, landed])

  const send = async () => {
    setSending(true)
    setSendError(null)
    try {
      setTest(await api<GatewayTest>('/gateway/test', { method: 'POST', body: JSON.stringify({ secret, model }) }))
    } catch (e) {
      setSendError(e instanceof Error ? e.message : String(e))
    } finally {
      setSending(false)
    }
  }

  const answer = test && (
    <p className={cn('text-sm', test.status === 200 ? 'text-muted-foreground-strong' : 'text-destructive-foreground')}>
      {test.status === 200 ? (
        <>
          <span className="font-mono">{model}</span> answered in <Duration ms={test.ms} />: “{test.reply?.trim()}”
        </>
      ) : (
        <>
          The gateway answered {test.status}: {test.error}
        </>
      )}
    </p>
  )

  if (!landed || landed.id !== landedId) {
    return (
      <Step n={3} title="Send your first request">
        <div role="status" className="flex flex-col items-start gap-3">
          <p className="flex items-center gap-2.5 text-sm">
            <span className="relative flex size-3 items-center justify-center" aria-hidden="true">
              <span className="absolute size-3 rounded-full border-2 border-dashed border-muted-foreground motion-safe:animate-spin motion-safe:[animation-duration:var(--duration-loading)]" />
            </span>
            Waiting for the first request with this key…
          </p>
          <p className="text-sm text-muted-foreground">
            Run your app with the new base URL{gatewayUrl && <> (<span className="font-mono">{gatewayUrl}</span>)</>}. This panel turns into that request’s
            receipt the moment it lands.
          </p>
          <Button variant="outline" size="sm" onClick={send} loading={sending} loadingText="Sending through the gateway…">
            Send a test request for me
          </Button>
          {answer}
          {sendError && (
            <p role="alert" className="flex items-start gap-2 text-sm text-destructive-foreground">
              <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
              {sendError}
            </p>
          )}
        </div>
      </Step>
    )
  }
  return (
    <>
      {answer}
      <FirstRequest r={landed} />
    </>
  )
}
