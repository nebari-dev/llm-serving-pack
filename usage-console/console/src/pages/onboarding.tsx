import { ArrowRight, Check, CircleCheck, Copy, TriangleAlert } from 'lucide-react'
import { type ReactNode, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { DecisionTrace } from '@/components/gw/decision-trace'
import { DiffView } from '@/components/gw/diff-view'
import { Duration, Money, TokenCount } from '@/components/gw/numbers'
import { PageHeader } from '@/components/gw/page'
import { VerdictBadge } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import { cn } from '@/lib/utils'
import { receiptStream, useApp, useReceipts } from '@/state/app-state'

// §7.5.1 Onboarding. "The migration is a base URL and a key swap. The
// onboarding must be shorter than the migration." One page; each step reveals
// in place beneath the last. The waiting state resolves itself into the
// user's first receipt — their own traffic, not a tour.

type ProviderId = 'openai' | 'anthropic' | 'bedrock' | 'azure' | 'vertex' | 'self-hosted'

const providers: {
  id: ProviderId
  label: string
  description: string
  auth: 'key' | 'oidc' | 'url'
  models: number
  baseUrl: string
  envKey: string
}[] = [
  { id: 'openai', label: 'OpenAI', description: 'API key', auth: 'key', models: 12, baseUrl: 'https://api.openai.com/v1', envKey: 'OPENAI_API_KEY' },
  { id: 'anthropic', label: 'Anthropic', description: 'API key', auth: 'key', models: 6, baseUrl: 'https://api.anthropic.com/v1', envKey: 'ANTHROPIC_API_KEY' },
  { id: 'bedrock', label: 'Bedrock', description: 'Cloud identity (OIDC)', auth: 'oidc', models: 18, baseUrl: 'https://bedrock-runtime.eu-central-1.amazonaws.com', envKey: 'AWS_ACCESS_KEY_ID' },
  { id: 'azure', label: 'Azure OpenAI', description: 'API key', auth: 'key', models: 9, baseUrl: 'https://acme.openai.azure.com/openai/v1', envKey: 'AZURE_OPENAI_API_KEY' },
  { id: 'vertex', label: 'Vertex', description: 'Cloud identity (OIDC)', auth: 'oidc', models: 11, baseUrl: 'https://europe-west4-aiplatform.googleapis.com/v1', envKey: 'GOOGLE_APPLICATION_CREDENTIALS' },
  { id: 'self-hosted', label: 'Self-hosted', description: 'vLLM, TGI, Ollama', auth: 'url', models: 1, baseUrl: 'http://vllm.internal:8000/v1', envKey: 'VLLM_API_KEY' },
]

const GATEWAY_URL = 'https://gw.acme.dev/v1'
const GATEWAY_KEY = 'ngw_live_7f3a91c4e0b2d8f6a1c5e7b9d3f02a64'
const LANG_KEY = 'gw:onboarding-lang'

type Lang = 'python' | 'typescript' | 'curl'

type TestState =
  | { kind: 'idle' }
  | { kind: 'testing' }
  | { kind: 'ok'; ms: number }
  | { kind: 'error'; message: string }

function Step({
  n,
  title,
  done,
  children,
}: {
  n: number
  title: string
  done?: boolean
  children: ReactNode
}) {
  return (
    <section
      aria-labelledby={`step-${n}`}
      className="rounded-md border border-border bg-card p-6 motion-safe:animate-slide-up-fade"
    >
      <h2 id={`step-${n}`} className="mb-4 flex items-center gap-2.5 text-lg leading-6 font-semibold">
        <span
          className={cn(
            'flex size-6 items-center justify-center rounded-full border text-xs font-semibold',
            done ? 'border-v-allowed-border bg-v-allowed-bg text-v-allowed-fg' : 'border-border-strong text-muted-foreground-strong',
          )}
          aria-hidden="true"
        >
          {done ? <Check className="size-3.5" /> : n}
        </span>
        {title}
        {done && <span className="sr-only">(done)</span>}
      </h2>
      {children}
    </section>
  )
}

function snippets(p: (typeof providers)[number]): Record<Lang, { diff: string; code: string; file: string }> {
  return {
    python: {
      file: 'app.py',
      diff: `
 from openai import OpenAI

 client = OpenAI(
-    base_url="${p.baseUrl}",
+    base_url="${GATEWAY_URL}",
-    api_key=os.environ["${p.envKey}"],
+    api_key=os.environ["NEBARI_GATEWAY_KEY"],
 )`,
      code: `from openai import OpenAI\n\nclient = OpenAI(\n    base_url="${GATEWAY_URL}",\n    api_key=os.environ["NEBARI_GATEWAY_KEY"],\n)`,
    },
    typescript: {
      file: 'client.ts',
      diff: `
 import OpenAI from 'openai'

 const client = new OpenAI({
-  baseURL: '${p.baseUrl}',
+  baseURL: '${GATEWAY_URL}',
-  apiKey: process.env.${p.envKey},
+  apiKey: process.env.NEBARI_GATEWAY_KEY,
 })`,
      code: `import OpenAI from 'openai'\n\nconst client = new OpenAI({\n  baseURL: '${GATEWAY_URL}',\n  apiKey: process.env.NEBARI_GATEWAY_KEY,\n})`,
    },
    curl: {
      file: 'shell',
      diff: `
-curl ${p.baseUrl}/chat/completions \\
+curl ${GATEWAY_URL}/chat/completions \\
-  -H "Authorization: Bearer $${p.envKey}" \\
+  -H "Authorization: Bearer $NEBARI_GATEWAY_KEY" \\
   -H "Content-Type: application/json" \\
   -d '{"model": "gpt-5-mini", "messages": [{"role": "user", "content": "ping"}]}'`,
      code: `curl ${GATEWAY_URL}/chat/completions \\\n  -H "Authorization: Bearer $NEBARI_GATEWAY_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model": "gpt-5-mini", "messages": [{"role": "user", "content": "ping"}]}'`,
    },
  }
}

function copy(text: string, what: string) {
  void navigator.clipboard?.writeText(text)
  toast.add({ title: `${what} copied`, type: 'success' })
}

export function OnboardingPage() {
  const [providerId, setProviderId] = useState<ProviderId>('openai')
  const [secret, setSecret] = useState('')
  const [test, setTest] = useState<TestState>({ kind: 'idle' })
  const provider = providers.find((p) => p.id === providerId)!

  const runTest = () => {
    setTest({ kind: 'testing' })
    window.setTimeout(() => {
      if (!secret.trim()) {
        setTest({
          kind: 'error',
          message:
            provider.auth === 'oidc'
              ? `Enter the ${provider.id === 'bedrock' ? 'IAM role ARN' : 'service account'} the gateway should assume, then test again.`
              : provider.auth === 'url'
                ? 'Enter the base URL of your inference server, then test again.'
                : `Paste a ${provider.label} API key, then test again.`,
        })
      } else if (secret.trim().toLowerCase().startsWith('bad')) {
        setTest({
          kind: 'error',
          message:
            provider.auth === 'key'
              ? `${provider.label} rejected this key (401 invalid_api_key). Check it's a project key with model access, then test again.`
              : provider.auth === 'oidc'
                ? `${provider.label} refused to issue credentials (403 AccessDenied). Add the gateway's service account to the role's trust policy, then test again.`
                : `Couldn't reach ${secret.trim()} from the cluster (connect timeout after 5s). Check the service is reachable from namespace nebari-gateway, then test again.`,
        })
      } else {
        setTest({ kind: 'ok', ms: 140 + Math.round(Math.random() * 90) })
      }
    }, 1100)
  }

  const connected = test.kind === 'ok'

  return (
    <div data-density="comfortable" className="flex flex-col">
      <PageHeader
        title="Connect a provider"
        description="Point one app at the gateway. It's a base URL and a key swap — about a minute."
      />
      <div className="mx-auto flex w-full max-w-2xl flex-col gap-6 px-6 py-8">
        <Step n={1} title="Connect your first provider" done={connected}>
          <RadioGroup
            aria-label="Provider"
            value={providerId}
            onValueChange={(v) => {
              setProviderId(v as ProviderId)
              setSecret('')
              setTest({ kind: 'idle' })
            }}
            className="grid grid-cols-2 gap-2 sm:grid-cols-3"
            orientation="vertical"
          >
            {providers.map((p) => (
              <RadioGroupItem
                key={p.id}
                value={p.id}
                variant="box"
                description={p.description}
                className={(s) => cn(s.checked && 'border-primary bg-card')}
              >
                {p.label}
              </RadioGroupItem>
            ))}
          </RadioGroup>

          <div className="mt-5 flex flex-col gap-4">
            {provider.auth === 'key' && (
              <Field>
                <FieldLabel>{provider.label} API key</FieldLabel>
                <Input
                  type="password"
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="sk-…"
                  value={secret}
                  onChange={(e) => setSecret(e.target.value)}
                  aria-invalid={test.kind === 'error' || undefined}
                  className="font-mono"
                />
                <FieldDescription>Tested once, then sealed. Never returned to callers.</FieldDescription>
              </Field>
            )}
            {provider.auth === 'oidc' && (
              <Field>
                <FieldLabel>{provider.id === 'bedrock' ? 'IAM role to assume' : 'Service account to impersonate'}</FieldLabel>
                <Input
                  spellCheck={false}
                  placeholder={
                    provider.id === 'bedrock'
                      ? 'arn:aws:iam::123456789012:role/nebari-gateway'
                      : 'nebari-gateway@acme-prod.iam.gserviceaccount.com'
                  }
                  value={secret}
                  onChange={(e) => setSecret(e.target.value)}
                  aria-invalid={test.kind === 'error' || undefined}
                  className="font-mono"
                />
                <FieldDescription>
                  No long-lived key. The gateway exchanges its cluster identity for short-lived credentials on each refresh.
                </FieldDescription>
              </Field>
            )}
            {provider.auth === 'url' && (
              <Field>
                <FieldLabel>Base URL</FieldLabel>
                <Input
                  spellCheck={false}
                  placeholder="http://vllm.internal:8000/v1"
                  value={secret}
                  onChange={(e) => setSecret(e.target.value)}
                  aria-invalid={test.kind === 'error' || undefined}
                  className="font-mono"
                />
                <FieldDescription>Any OpenAI-compatible server reachable from the cluster. Traffic stays in-cluster.</FieldDescription>
              </Field>
            )}

            <div className="flex flex-wrap items-center gap-3">
              <Button onClick={runTest} loading={test.kind === 'testing'} loadingText="Testing connection…" variant={connected ? 'outline' : 'default'}>
                {connected ? 'Test again' : 'Test connection'}
              </Button>
              <div aria-live="polite" className="min-w-0 text-sm">
                {test.kind === 'ok' && (
                  <span className="inline-flex items-center gap-1.5 text-v-allowed-fg">
                    <CircleCheck className="size-4" aria-hidden="true" />
                    Connected to {provider.label} · {provider.models} {provider.models === 1 ? 'model' : 'models'} available ·{' '}
                    <Duration ms={test.ms} />
                  </span>
                )}
              </div>
            </div>
            {test.kind === 'error' && (
              <p role="alert" className="flex items-start gap-2 text-sm text-destructive-foreground">
                <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
                {test.message}
              </p>
            )}
            {test.kind === 'idle' && provider.auth === 'key' && (
              <p className="text-xs text-muted-foreground">Mockup tip: any key works; one starting with “bad” shows the failure state.</p>
            )}
          </div>
        </Step>

        {connected && <GatewayKeyStep provider={provider} />}
        {connected && <FirstRequestStep />}

        <div className="flex flex-col items-start gap-3 border-t border-border pt-6 sm:flex-row sm:items-center sm:justify-between">
          <p className="text-sm text-muted-foreground-strong">
            No traffic of your own yet? Explore the demo tenant — a week of synthetic traffic, several teams, one blown budget, a
            handful of blocked requests.
          </p>
          <Button variant="outline" render={<Link to="/" />} className="shrink-0">
            <span className="rounded-sm border border-border-strong px-1 text-[11px] leading-4 font-semibold">Demo</span>
            Open the demo tenant
            <ArrowRight />
          </Button>
        </div>
      </div>
    </div>
  )
}

function readLang(): Lang {
  try {
    const v = localStorage.getItem(LANG_KEY)
    if (v === 'python' || v === 'typescript' || v === 'curl') return v
  } catch {
    /* storage unavailable */
  }
  return 'python'
}

function GatewayKeyStep({ provider }: { provider: (typeof providers)[number] }) {
  const [lang, setLang] = useState<Lang>(readLang)
  const all = useMemo(() => snippets(provider), [provider])

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
      <div className="flex flex-col gap-1.5">
        <div className="text-sm font-medium">Your gateway key</div>
        <div className="flex items-center gap-2 rounded-md border border-border bg-muted px-3 py-2">
          <span className="min-w-0 flex-1 truncate font-mono text-sm" title="Shown in full once. Store it as NEBARI_GATEWAY_KEY.">
            {GATEWAY_KEY.slice(0, 13)}
            <span className="text-muted-foreground-strong">{GATEWAY_KEY.slice(13)}</span>
          </span>
          <Button size="sm" variant="outline" onClick={() => copy(GATEWAY_KEY, 'Gateway key')}>
            <Copy /> Copy key
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          Shown in full once. Scoped to team <span className="font-mono">default</span>, expires in 90 days — change both under Keys.
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

function FirstRequestStep() {
  const { openReceipt } = useApp()
  const rows = useReceipts()
  const [receiptId, setReceiptId] = useState<string | null>(null)

  const land = () => {
    const first = receiptStream.getSnapshot().find((r) => !r.inFlight && r.verdict === 'allowed') ?? receiptStream.getSnapshot()[0]
    if (first) setReceiptId(first.id)
  }

  useEffect(() => {
    if (receiptId) return
    const t = window.setTimeout(land, 6000)
    return () => window.clearTimeout(t)
  }, [receiptId])

  const r = receiptId ? (rows.find((x) => x.id === receiptId) ?? receiptStream.byId.get(receiptId)) : undefined

  if (!r) {
    return (
      <Step n={3} title="Send your first request">
        <div role="status" className="flex flex-col items-start gap-3">
          <p className="flex items-center gap-2.5 text-sm">
            <span className="relative flex size-3 items-center justify-center" aria-hidden="true">
              <span className="absolute size-3 rounded-full border-2 border-dashed border-muted-foreground motion-safe:animate-spin motion-safe:[animation-duration:var(--duration-loading)]" />
            </span>
            Waiting for your first request…
          </p>
          <p className="text-sm text-muted-foreground">
            Run your app with the new base URL. This panel turns into that request's receipt the moment it lands.
          </p>
          <button
            type="button"
            onClick={land}
            className="text-xs text-muted-foreground underline underline-offset-4 hover:text-foreground"
          >
            Send a test request for me
          </button>
        </div>
      </Step>
    )
  }

  return (
    <Step n={3} title="Your first request" done>
      <div role="status" className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <VerdictBadge verdict={r.verdict} />
          <span className="font-mono text-sm">
            {r.requestedModel}
            {r.requestedModel !== r.resolvedModel && (
              <>
                {' '}
                → {r.resolvedModel}
              </>
            )}
          </span>
          <span className="text-sm text-muted-foreground">
            via <span className="font-mono text-foreground">{r.backend}</span>
          </span>
        </div>
        <dl className="grid grid-cols-3 gap-4 border-y border-border py-3">
          <div>
            <dt className="text-xs text-muted-foreground">Tokens</dt>
            <dd className="text-lg">
              <TokenCount value={r.inputTokens + r.outputTokens + r.reasoningTokens} unknown={r.inFlight} />
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">Cost</dt>
            <dd className="text-lg">
              <Money value={r.costUsd} precision="micro" unknown={r.inFlight} />
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">Latency</dt>
            <dd className="text-lg">
              <Duration ms={r.durationMs} unknown={r.inFlight} />
            </dd>
          </div>
        </dl>
        <DecisionTrace steps={r.trace} totalMs={r.durationMs} />
        <div className="flex flex-wrap items-center gap-2 border-t border-border pt-4">
          <Button onClick={() => openReceipt(r.id)}>
            Open full receipt <ArrowRight />
          </Button>
          <Button variant="ghost" render={<Link to="/traffic" />}>
            See it in live traffic
          </Button>
        </div>
      </div>
    </Step>
  )
}
