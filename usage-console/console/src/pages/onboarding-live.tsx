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
import { type ApiKey, api, type Backend, backends as seedBackends, createKey, type Receipt, type Session, session as seedSession, teams } from '@/data/catalog'
import { cn } from '@/lib/utils'
import { useReceipts } from '@/state/app-state'
import { useLive } from '@/state/live'
import { copy, FirstRequest, type Lang, readLang, Step, type StepInfo, StepMap } from './onboarding'

// §7.5.1 Onboarding against the real control plane. The backends are the
// ones the gateway config defines (adding one, with its credentials, isn't
// connected yet); the key is a real key; the first request is a real receipt
// for that key, from the caller's own app or the test request sent here.

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

  const backend = live.data.find((b) => b.name === name) ?? null
  const gatewayUrl = sess.gatewayUrl ?? ''

  const create = async () => {
    if (!backend) return
    setCreating(true)
    setError(null)
    try {
      const expires = new Date(Date.now() + 90 * 86_400_000).toISOString().slice(0, 10)
      const { key, secret } = await createKey({
        name: `onboarding-${backend.name}-${Math.random().toString(36).slice(2, 6)}`,
        team,
        project: 'onboarding',
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
          <Step n={1} title="Pick a backend the gateway serves" done={!!created}>
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
            <p className="mt-3 text-xs text-muted-foreground">
              Adding a provider and its credentials isn’t connected yet: backends come from the gateway’s config (
              <span className="font-mono">server/aigw/config.yaml</span>).
            </p>

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
                  <Select items={teams.map((t) => ({ value: t.id, label: t.name }))} value={team} onValueChange={(v) => setTeam((v as string) ?? team)}>
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
                <div className="flex flex-wrap items-center gap-3">
                  <Button onClick={create} loading={creating} loadingText="Creating key…" variant={created ? 'outline' : 'default'} disabled={!team}>
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

          {created && <KeyStep gatewayUrl={gatewayUrl} secret={created.secret} keyName={created.key.name} team={created.key.team} model={created.backend.models[0]} />}
          {created && (
            <FirstRequestLive gatewayUrl={gatewayUrl} keyId={created.key.id} secret={created.secret} model={created.backend.models[0]} landedId={firstId} onLand={setFirstId} />
          )}
        </div>
      </div>
    </div>
  )
}

function KeyStep({ gatewayUrl, secret, keyName, team, model }: { gatewayUrl: string; secret: string; keyName: string; team: string; model: string }) {
  const [lang, setLang] = useState<Lang>(readLang)
  const all = useMemo(() => snippets(gatewayUrl || '<gateway URL>', model), [gatewayUrl, model])
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
